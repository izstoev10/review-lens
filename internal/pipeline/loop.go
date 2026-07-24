package pipeline

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
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
// ignored.
//
// Like `run`, everything runs inside a throwaway git worktree, so the user's
// active checkout is never touched and a loop on one branch can churn while the
// user (or another loop) works a different branch in another terminal. The PR
// number and its head branch are resolved once, up front, from the user's real
// checkout — so the loop stays pinned to that PR even if the user later switches
// branches, and the `gh`/CI calls don't depend on the (detached) worktree's HEAD.
func AutoFixLoop(dir, prNumber string, cfg config.Config, log io.Writer) error {
	if cfg.Agent == nil {
		return fmt.Errorf("no agent configured (set \"agent\" in .review-lens.json)")
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("the GitHub CLI (gh) is required for the auto-fix loop")
	}
	root, err := gitx.RepoRoot(dir)
	if err != nil {
		return fmt.Errorf("not a git repo: %w", err)
	}

	// Pin the target PR + branch from the user's real checkout before we isolate.
	// This resolves an empty prNumber to a concrete number and gives us the head
	// branch to base the worktree on and push back to.
	number, branch, err := resolvePR(dir, prNumber)
	if err != nil {
		return err
	}

	// Base the worktree on the branch's latest remote head — that's the commit
	// the PR (and `gh pr diff`) actually reflects, and it lets the loop operate on
	// a PR whose branch isn't checked out locally (e.g. `loop <num>`). Fall back
	// to the local branch if the remote is unreachable.
	base := branch
	if err := gitx.Fetch(root, cfg.Remote, branch); err != nil {
		fmt.Fprintf(log, "review-lens: could not fetch %s/%s (%v); basing on the local branch\n", cfg.Remote, branch, err)
	} else {
		base = cfg.Remote + "/" + branch
	}

	// Isolate: the fix/commit/push cycle runs in a disposable worktree, never the
	// user's working directory. Cleaned up on return (success, error, or panic).
	wt, err := gitx.AddWorktree(root, base)
	if err != nil {
		return fmt.Errorf("creating worktree: %w", err)
	}
	defer func() {
		if rmErr := wt.Remove(); rmErr != nil {
			fmt.Fprintf(log, "review-lens: warning: worktree cleanup failed: %v\n", rmErr)
		}
	}()
	fmt.Fprintf(log, "review-lens: PR #%s (branch %s) — isolated worktree at %s\n", number, branch, wt.Path)

	// Guidance is read from the real repo root (not the worktree) so edits take
	// effect immediately, without needing to be committed first — as `run` does.
	reviewGuidance := guidance.Load(root, cfg.ReviewGuidancePath)

	maxIter := cfg.MaxLoopIterations
	if maxIter <= 0 {
		maxIter = 3
	}

	for attempt := 1; attempt <= maxIter; attempt++ {
		fmt.Fprintf(log, "\nreview-lens: ═══ iteration %d/%d ═══\n", attempt, maxIter)

		// 1. Review the PR diff.
		diff, err := ghPRDiff(wt.Path, number)
		if err != nil {
			return err
		}
		raw, err := agent.Review(wt.Path, cfg.Agent, agent.ReviewPrompt(reviewGuidance, diff), log)
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
			s, failing, err := ci.Poll(wt.Path, number, ciPollInterval, ciPollTimeout,
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
			if err := agent.Fix(wt.Path, cfg.Agent, autoFixPrompt(autoFix), log); err != nil {
				return err
			}
			if progressed, err := commitPush(wt, cfg.Remote, branch, fmt.Sprintf("review-lens: auto-fix (iteration %d)", attempt), log); err != nil {
				return err
			} else if !progressed {
				fmt.Fprintln(log, "review-lens: agent made no changes — stopping to avoid a no-progress loop.")
				return nil
			}
			time.Sleep(ciSettleDelay)

		case actFixCI:
			fmt.Fprintln(log, "review-lens: CI is red with no review findings — asking the agent to fix the build…")
			if err := agent.Fix(wt.Path, cfg.Agent, agent.Prompt("CI", "The pushed branch is failing GitHub CI. Investigate and fix the failing checks."), log); err != nil {
				return err
			}
			if progressed, err := commitPush(wt, cfg.Remote, branch, fmt.Sprintf("review-lens: fix CI (iteration %d)", attempt), log); err != nil {
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

// resolvePR resolves the target PR's number and head branch via the gh CLI,
// running in the user's real checkout. An empty prNumber means "the PR of the
// current branch"; a non-empty one is looked up directly. Resolving both up
// front lets the loop pin itself to one PR/branch and pass an explicit number to
// every later gh/CI call (which otherwise infer the PR from the current branch —
// impossible from the detached worktree the loop runs in).
func resolvePR(dir, prNumber string) (number, branch string, err error) {
	args := []string{"pr", "view"}
	if prNumber != "" {
		args = append(args, prNumber)
	}
	args = append(args, "--json", "number,headRefName")
	cmd := exec.Command("gh", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("gh pr view failed (is there an open PR for this branch?): %w", err)
	}
	var p struct {
		Number      int    `json:"number"`
		HeadRefName string `json:"headRefName"`
	}
	if err := json.Unmarshal(out, &p); err != nil {
		return "", "", fmt.Errorf("parsing gh pr view: %w", err)
	}
	if p.HeadRefName == "" {
		return "", "", fmt.Errorf("could not determine the PR's head branch")
	}
	return strconv.Itoa(p.Number), p.HeadRefName, nil
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

// commitPush commits any changes the agent made in the worktree and pushes them
// to remote as branch. Returns progressed=false when the agent left the tree
// unchanged (so the loop can stop rather than spin). The commit and push both
// run from the worktree, never the user's checkout.
func commitPush(wt *gitx.Worktree, remote, branch, msg string, log io.Writer) (progressed bool, err error) {
	changed, err := wt.HasChanges()
	if err != nil {
		return false, err
	}
	if !changed {
		return false, nil
	}
	if _, err := wt.CommitAll(msg); err != nil {
		return false, err
	}
	fmt.Fprintf(log, "review-lens: committed + pushing (%s)\n", msg)
	if err := wt.Push(remote, branch); err != nil {
		return false, fmt.Errorf("push failed: %w", err)
	}
	return true, nil
}
