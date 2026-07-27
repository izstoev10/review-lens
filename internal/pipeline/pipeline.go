// Package pipeline orchestrates a full gate run:
//
//	worktree -> checks -> (agent fix -> recheck)* -> commit fixes -> push -> PR
//
// It is the heart of the tool. Everything it does is logged to the provided
// writer so the CLI (and, later, a TUI) can show progress.
package pipeline

import (
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"

	"github.com/izstoev10/review-lens/internal/agent"
	"github.com/izstoev10/review-lens/internal/checks"
	"github.com/izstoev10/review-lens/internal/config"
	"github.com/izstoev10/review-lens/internal/findings"
	"github.com/izstoev10/review-lens/internal/gitx"
	"github.com/izstoev10/review-lens/internal/guidance"
	"github.com/izstoev10/review-lens/internal/signature"
	"github.com/izstoev10/review-lens/internal/tui"
)

// Run gates the current branch of the repo containing startDir.
func Run(startDir string, cfg config.Config, log io.Writer) error {
	root, err := gitx.RepoRoot(startDir)
	if err != nil {
		return fmt.Errorf("not a git repo: %w", err)
	}
	branch, err := gitx.CurrentBranch(root)
	if err != nil {
		return err
	}
	fmt.Fprintf(log, "review-lens: repo=%s branch=%s\n", root, branch)

	// 1. Isolate: everything below runs in a throwaway worktree, so the user's
	//    working directory is never modified even while the agent edits files.
	wt, err := gitx.AddWorktree(root, branch)
	if err != nil {
		return fmt.Errorf("creating worktree: %w", err)
	}
	defer func() {
		if rmErr := wt.Remove(); rmErr != nil {
			fmt.Fprintf(log, "review-lens: warning: worktree cleanup failed: %v\n", rmErr)
		}
	}()
	fmt.Fprintf(log, "review-lens: isolated worktree at %s\n", wt.Path)

	// 2. Check / fix loop.
	agentRan, err := checkAndFix(wt, cfg, log)
	if err != nil {
		return err
	}

	// 3. Commit only if the agent actually applied a fix. We gate on agentRan
	//    (not merely "the worktree is dirty") so stray build artifacts left by
	//    the checks themselves never get committed or pushed.
	if agentRan {
		changed, err := wt.HasChanges()
		if err != nil {
			return err
		}
		if changed {
			sha, err := wt.CommitAll("review-lens: apply automated fixes")
			if err != nil {
				return fmt.Errorf("committing fixes: %w", err)
			}
			fmt.Fprintf(log, "review-lens: committed fixes (%s)\n", short(sha))
		}
	}

	// 4. Review the committed changes just before pushing. Advisory only —
	//    findings are printed for the human; they do not block the push.
	if cfg.Review && cfg.Agent != nil {
		// Guidance is read from the real repo root (not the worktree) so edits
		// take effect immediately, without needing to be committed first.
		reviewGuidance := guidance.Load(root, cfg.ReviewGuidancePath)
		if err := reviewDiff(wt, cfg, branch, reviewGuidance, log); err != nil {
			fmt.Fprintf(log, "review-lens: review skipped: %v\n", err)
		}
	}

	// 5. Push the (green) HEAD to the remote.
	fmt.Fprintf(log, "review-lens: pushing to %s/%s\n", cfg.Remote, branch)
	if err := wt.Push(cfg.Remote, branch); err != nil {
		return fmt.Errorf("push failed: %w", err)
	}

	// 6. Optionally open a PR via the gh CLI, stamping the gate signature.
	if cfg.OpenPR {
		if err := openPR(wt.Path, branch, log); err != nil {
			fmt.Fprintf(log, "review-lens: PR step skipped: %v\n", err)
		}
	}

	fmt.Fprintln(log, "review-lens: ✅ all checks green, pushed.")
	return nil
}

// checkAndFix runs all checks, and on failure asks the agent to fix and retries,
// up to cfg.MaxAgentAttempts. It returns agentRan=true if the agent was invoked
// at least once (so the caller knows whether to commit). It returns an error if
// checks are still failing when attempts run out (or if no agent is configured
// to fix them).
func checkAndFix(wt *gitx.Worktree, cfg config.Config, log io.Writer) (agentRan bool, err error) {
	attempts := cfg.MaxAgentAttempts
	for i := 0; ; i++ {
		results, ok := checks.RunAll(wt.Path, cfg.Checks)
		for _, r := range results {
			status := "ok"
			if !r.Passed {
				status = "FAIL"
			}
			fmt.Fprintf(log, "review-lens:   [%s] %s\n", status, r.Name)
		}
		if ok {
			return agentRan, nil
		}

		failed := results[len(results)-1] // fail-fast: last result is the failure

		if cfg.Agent == nil {
			return agentRan, fmt.Errorf("check %q failed and no agent configured:\n%s", failed.Name, failed.Output)
		}
		if i >= attempts {
			return agentRan, fmt.Errorf("check %q still failing after %d fix attempt(s)", failed.Name, attempts)
		}

		fmt.Fprintf(log, "review-lens: attempt %d/%d — asking agent to fix %q (live output below)\n", i+1, attempts, failed.Name)
		agentRan = true
		prompt := agent.Prompt(failed.Name, failed.Output)
		if err := agent.Fix(wt.Path, cfg.Agent, prompt, log); err != nil {
			return agentRan, fmt.Errorf("agent fix failed: %w", err)
		}
		fmt.Fprintln(log, "\nreview-lens: agent finished, re-running checks...")
	}
}

// reviewDiff computes the branch's diff against the base branch and asks the
// agent to review it, printing findings. Returns an error only if the review
// couldn't run (e.g. base branch missing) — a review that finds issues is not
// an error, since findings are advisory.
func reviewDiff(wt *gitx.Worktree, cfg config.Config, branch, reviewGuidance string, log io.Writer) error {
	base := cfg.BaseBranch
	if base == "" {
		base = "main"
	}
	// Nothing sensible to diff against if base is missing (e.g. first push of a
	// brand-new repo, or reviewing the base branch itself).
	if !wt.RefExists(base) {
		return fmt.Errorf("base branch %q not found", base)
	}
	if base == branch {
		return fmt.Errorf("on base branch %q; nothing to review", base)
	}
	diff, err := wt.DiffSince(base)
	if err != nil {
		return err
	}
	if diff == "" {
		fmt.Fprintf(log, "review-lens: no changes vs %s to review\n", base)
		return nil
	}
	fmt.Fprintf(log, "review-lens: reviewing changes vs %s...\n", base)
	raw, err := agent.Review(wt.Path, cfg.Agent, agent.ReviewPrompt(reviewGuidance, diff), log)
	if err != nil {
		return err
	}
	fmt.Fprintln(log)
	// Inside `run`: plain render, no full-screen TUI mid-pipeline (dir/agent
	// unused because interactive is false).
	showReview(raw, log, false, "", nil)
	return nil
}

// showReview presents an agent's raw review output. When interactive, it opens
// the bubbletea TUI (dir + agent enable its fix action); otherwise (piped, or
// mid-`run`) it prints the colourised report. Falls back to raw text if the
// output isn't parseable JSON, and to the plain report if the TUI can't start.
func showReview(raw string, log io.Writer, interactive bool, dir string, a *config.Agent) {
	list, ok := findings.Parse(raw)
	if !ok {
		fmt.Fprintln(log, strings.TrimSpace(raw))
		return
	}
	if interactive && len(list) > 0 {
		if err := tui.Show(list, dir, a); err == nil {
			return
		}
		// TUI failed to start (e.g. not a real terminal) — fall through to plain.
	}
	findings.Render(log, list, true)
}

// openPR shells out to the GitHub CLI to open a PR for branch, then stamps the
// gate signature into its body. It's best-effort: if gh isn't installed we bail;
// if a PR already exists `gh pr create` fails harmlessly and we still ensure the
// signature is present (so re-running review-lens back-fills an existing PR).
//
// The head branch is passed explicitly because the worktree runs with a detached
// HEAD, so gh can't infer "the current branch".
func openPR(dir, branch string, log io.Writer) error {
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("gh not installed")
	}
	cmd := exec.Command("gh", "pr", "create", "--fill", "--head", branch)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Most commonly: a PR already exists for this branch. Not fatal — we still
		// stamp the signature below.
		fmt.Fprintf(log, "review-lens: gh pr create: %s", strings.TrimSpace(string(out)))
	} else {
		fmt.Fprintf(log, "review-lens: %s", out)
	}
	return stampSignature(dir, branch, log)
}

// stampSignature ensures the open PR for branch carries the gate signature,
// appending it via `gh pr edit` when missing. Idempotent, so it's safe to run on
// every `review-lens run`. Uses `gh pr list --head` (not branch inference) so it
// works from the detached worktree.
func stampSignature(dir, branch string, log io.Writer) error {
	list := exec.Command("gh", "pr", "list", "--head", branch, "--state", "open", "--json", "number,body")
	list.Dir = dir
	out, err := list.Output()
	if err != nil {
		return fmt.Errorf("looking up PR to stamp: %w", err)
	}
	var prs []struct {
		Number int    `json:"number"`
		Body   string `json:"body"`
	}
	if err := json.Unmarshal(out, &prs); err != nil {
		return fmt.Errorf("parsing gh pr list: %w", err)
	}
	if len(prs) == 0 {
		return fmt.Errorf("no open PR found for branch %q", branch)
	}
	pr := prs[0]
	newBody, changed := signature.Ensure(pr.Body)
	if !changed {
		fmt.Fprintf(log, "review-lens: gate signature already present on PR #%d\n", pr.Number)
		return nil
	}
	edit := exec.Command("gh", "pr", "edit", strconv.Itoa(pr.Number), "--body", newBody)
	edit.Dir = dir
	if eout, err := edit.CombinedOutput(); err != nil {
		return fmt.Errorf("stamping signature: %w\n%s", err, eout)
	}
	fmt.Fprintf(log, "review-lens: stamped gate signature into PR #%d\n", pr.Number)
	return nil
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
