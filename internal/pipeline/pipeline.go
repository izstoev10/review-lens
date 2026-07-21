// Package pipeline orchestrates a full gate run:
//
//	worktree -> checks -> (agent fix -> recheck)* -> commit fixes -> push -> PR
//
// It is the heart of the tool. Everything it does is logged to the provided
// writer so the CLI (and, later, a TUI) can show progress.
package pipeline

import (
	"fmt"
	"io"
	"os/exec"

	"github.com/izstoev10/review-lens/internal/agent"
	"github.com/izstoev10/review-lens/internal/checks"
	"github.com/izstoev10/review-lens/internal/config"
	"github.com/izstoev10/review-lens/internal/gitx"
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
		if err := reviewDiff(wt, cfg, branch, log); err != nil {
			fmt.Fprintf(log, "review-lens: review skipped: %v\n", err)
		}
	}

	// 5. Push the (green) HEAD to the remote.
	fmt.Fprintf(log, "review-lens: pushing to %s/%s\n", cfg.Remote, branch)
	if err := wt.Push(cfg.Remote, branch); err != nil {
		return fmt.Errorf("push failed: %w", err)
	}

	// 6. Optionally open a PR via the gh CLI.
	if cfg.OpenPR {
		if err := openPR(wt.Path, log); err != nil {
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
func reviewDiff(wt *gitx.Worktree, cfg config.Config, branch string, log io.Writer) error {
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
	fmt.Fprintf(log, "review-lens: reviewing changes vs %s (live output below)...\n\n", base)
	if _, err := agent.Review(wt.Path, cfg.Agent, agent.ReviewPrompt(diff), log); err != nil {
		return err
	}
	fmt.Fprintln(log)
	return nil
}

// openPR shells out to the GitHub CLI. It's best-effort: if gh isn't installed
// or a PR already exists, we just log and move on.
func openPR(dir string, log io.Writer) error {
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("gh not installed")
	}
	cmd := exec.Command("gh", "pr", "create", "--fill")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, out)
	}
	fmt.Fprintf(log, "review-lens: %s", out)
	return nil
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
