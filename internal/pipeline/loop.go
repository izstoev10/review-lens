package pipeline

import (
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/izstoev10/review-lens/internal/agent"
	"github.com/izstoev10/review-lens/internal/ci"
	"github.com/izstoev10/review-lens/internal/config"
	"github.com/izstoev10/review-lens/internal/findings"
	"github.com/izstoev10/review-lens/internal/gitx"
	"github.com/izstoev10/review-lens/internal/guidance"
)

// CI polling cadence. Kept modest so a stuck loop can't hang forever.
const (
	ciPollInterval = 20 * time.Second
	ciPollTimeout  = 20 * time.Minute
	ciSettleDelay  = 10 * time.Second // let a new run register after a push
)

// loopAction is what the loop should do next given the current findings + CI.
type loopAction int

const (
	actApplyFixes loopAction = iota // auto-fixable findings exist — fix them
	actEscalate                     // ask-user findings block; needs a human
	actFixCI                        // no findings, but CI is red — fix the build
	actDone                         // no blocking findings and CI green — success
	actWaitCI                       // no findings, CI still pending — keep waiting
)

// decideNext is the pure decision at the heart of the loop, unit-tested. Auto-fix
// always takes priority (we can make progress); otherwise ask-user escalates;
// otherwise the CI status decides.
func decideNext(numAutoFix, numAskUser int, status ci.Status) loopAction {
	switch {
	case numAutoFix > 0:
		return actApplyFixes
	case numAskUser > 0:
		return actEscalate
	case status == ci.Failure:
		return actFixCI
	case status == ci.Success:
		return actDone
	default:
		return actWaitCI
	}
}

// partition splits findings by their action classification.
func partition(items []findings.Finding) (autoFix, askUser, noOp []findings.Finding) {
	for _, f := range items {
		switch f.Action {
		case findings.AutoFix:
			autoFix = append(autoFix, f)
		case findings.NoOp:
			noOp = append(noOp, f)
		default: // AskUser / unknown → treated as needing a human
			askUser = append(askUser, f)
		}
	}
	return
}

// AutoFixLoop repeatedly reviews an open PR, applies the auto-fixable findings,
// pushes, waits for GitHub CI, and re-reviews — until the PR is clean and green,
// an ask-user finding needs a human, no progress can be made, or the iteration
// limit is hit. ask-user findings are never auto-applied; no-op findings are
// ignored. Everything happens on the real branch (this edits + pushes it).
func AutoFixLoop(dir, prNumber string, cfg config.Config, log io.Writer) error {
	if cfg.Agent == nil {
		return fmt.Errorf("no agent configured (set \"agent\" in .review-lens.json)")
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("the GitHub CLI (gh) is required for the auto-fix loop")
	}
	root := dir
	if r, err := gitx.RepoRoot(dir); err == nil {
		root = r
	}
	reviewGuidance := guidance.Load(root, cfg.ReviewGuidancePath)

	maxIter := cfg.MaxLoopIterations
	if maxIter <= 0 {
		maxIter = 3
	}

	for attempt := 1; attempt <= maxIter; attempt++ {
		fmt.Fprintf(log, "\nreview-lens: ═══ iteration %d/%d ═══\n", attempt, maxIter)

		// 1. Review the PR diff.
		diff, err := ghPRDiff(dir, prNumber)
		if err != nil {
			return err
		}
		raw, err := agent.Review(dir, cfg.Agent, agent.ReviewPrompt(reviewGuidance, diff), log)
		if err != nil {
			return err
		}
		items, _ := findings.Parse(raw)
		autoFix, askUser, noOp := partition(items)
		fmt.Fprintf(log, "review-lens: findings — %d auto-fix, %d ask-user, %d no-op\n",
			len(autoFix), len(askUser), len(noOp))

		// 2. Only spend time polling CI when there are no auto-fixes to apply
		//    (otherwise we're about to push a new commit anyway).
		status := ci.Pending
		if len(autoFix) == 0 {
			fmt.Fprintln(log, "review-lens: checking CI…")
			s, failing, err := ci.Poll(dir, prNumber, ciPollInterval, ciPollTimeout,
				func(msg string) { fmt.Fprintf(log, "review-lens: %s\n", msg) })
			if err != nil {
				fmt.Fprintf(log, "review-lens: CI: %v\n", err)
			}
			status = s
			fmt.Fprintf(log, "review-lens: CI %s%s\n", status, failingSuffix(failing))
		}

		// 3. Decide and act.
		switch decideNext(len(autoFix), len(askUser), status) {
		case actDone:
			fmt.Fprintln(log, "review-lens: ✅ clean review and green CI — done.")
			return nil

		case actEscalate:
			fmt.Fprintln(log, "review-lens: ⚠ findings need human judgement (ask-user) — stopping:")
			for _, f := range askUser {
				fmt.Fprintf(log, "  • [%s] %s — %s\n", loc(f), f.Title, f.Detail)
			}
			return nil

		case actApplyFixes:
			fmt.Fprintf(log, "review-lens: applying %d auto-fix finding(s)…\n", len(autoFix))
			if err := agent.Fix(dir, cfg.Agent, autoFixPrompt(autoFix), log); err != nil {
				return err
			}
			if progressed, err := commitPush(dir, fmt.Sprintf("review-lens: auto-fix (iteration %d)", attempt), log); err != nil {
				return err
			} else if !progressed {
				fmt.Fprintln(log, "review-lens: agent made no changes — stopping to avoid a no-progress loop.")
				return nil
			}
			time.Sleep(ciSettleDelay)

		case actFixCI:
			fmt.Fprintln(log, "review-lens: CI is red with no review findings — asking the agent to fix the build…")
			if err := agent.Fix(dir, cfg.Agent, agent.Prompt("CI", "The pushed branch is failing GitHub CI. Investigate and fix the failing checks."), log); err != nil {
				return err
			}
			if progressed, err := commitPush(dir, fmt.Sprintf("review-lens: fix CI (iteration %d)", attempt), log); err != nil {
				return err
			} else if !progressed {
				fmt.Fprintln(log, "review-lens: agent made no changes — stopping to avoid a no-progress loop.")
				return nil
			}
			time.Sleep(ciSettleDelay)

		case actWaitCI:
			fmt.Fprintln(log, "review-lens: CI still pending after timeout — will re-check next iteration.")
		}
	}

	fmt.Fprintf(log, "review-lens: reached the iteration limit (%d) — stopping for human review.\n", maxIter)
	return nil
}

// autoFixPrompt asks the agent to fix a set of auto-fixable findings.
func autoFixPrompt(items []findings.Finding) string {
	var b strings.Builder
	b.WriteString("Apply fixes for the following code review findings. Edit files directly to fix the root cause, make the smallest change that resolves each, match the surrounding code style, and do not disable or suppress checks.\n\n")
	for _, f := range items {
		fmt.Fprintf(&b, "- [%s] %s — %s\n", loc(f), f.Title, f.Detail)
	}
	return b.String()
}

func loc(f findings.Finding) string {
	if f.Line > 0 {
		return fmt.Sprintf("%s:%d", f.File, f.Line)
	}
	return f.File
}

func failingSuffix(failing []string) string {
	if len(failing) == 0 {
		return ""
	}
	return " (failing: " + strings.Join(failing, ", ") + ")"
}

// commitPush stages everything, commits if there are changes, and pushes.
// Returns progressed=false when the agent left the tree unchanged.
func commitPush(dir, msg string, log io.Writer) (progressed bool, err error) {
	if out, _ := gitOut(dir, "status", "--porcelain"); strings.TrimSpace(out) == "" {
		return false, nil
	}
	if _, err := gitOut(dir, "add", "-A"); err != nil {
		return false, err
	}
	if _, err := gitOut(dir, "commit", "-m", msg); err != nil {
		return false, err
	}
	fmt.Fprintf(log, "review-lens: committed + pushing (%s)\n", msg)
	if _, err := gitOut(dir, "push"); err != nil {
		return false, fmt.Errorf("push failed: %w", err)
	}
	return true, nil
}

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return string(out), nil
}
