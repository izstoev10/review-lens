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

// Run gates the current branch of the repo containing startDir. When interactive
// (a real terminal), the review step opens the live findings TUI and any fixes
// applied there are re-gated and pushed; otherwise the review prints a plain
// report and the run proceeds unattended.
func Run(startDir string, cfg config.Config, interactive bool, log io.Writer) error {
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
		if err := reviewDiff(wt, cfg, branch, reviewGuidance, interactive, log); err != nil {
			fmt.Fprintf(log, "review-lens: review skipped: %v\n", err)
		}
	}

	// 4.5 An interactive review may have applied fixes in the worktree. Re-gate
	//     them — the whole point of `run` is to push only green code — then commit
	//     so they ride along in the push. A failed re-gate blocks the push.
	if interactive {
		if changed, err := wt.HasChanges(); err != nil {
			return err
		} else if changed {
			fmt.Fprintln(log, "review-lens: review applied fixes — re-running checks…")
			if _, err := checkAndFix(wt, cfg, log); err != nil {
				return err
			}
			if changed, err := wt.HasChanges(); err != nil {
				return err
			} else if changed {
				sha, err := wt.CommitAll("review-lens: apply review fixes")
				if err != nil {
					return fmt.Errorf("committing review fixes: %w", err)
				}
				fmt.Fprintf(log, "review-lens: committed review fixes (%s)\n", short(sha))
			}
		}
	}

	// 5. Push the (green) HEAD to the remote.
	fmt.Fprintf(log, "review-lens: pushing to %s/%s\n", cfg.Remote, branch)
	if err := wt.Push(cfg.Remote, branch); err != nil {
		return fmt.Errorf("push failed: %w", err)
	}

	// 6. Optionally open a PR via the gh CLI, building the body and stamping the
	//    gate signature.
	if cfg.OpenPR {
		if err := openPR(wt, cfg, branch, log); err != nil {
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
// agent to review it. Returns an error only if the review couldn't run (e.g.
// base branch missing) — a review that finds issues is not an error, since
// findings are advisory.
//
// When interactive (a real terminal + a streaming agent), it opens the same live
// TUI as `pr` — activity feed while reviewing, then a navigable findings viewer
// where fixes can be applied in the worktree. Otherwise it prints the plain
// colored report.
func reviewDiff(wt *gitx.Worktree, cfg config.Config, branch, reviewGuidance string, interactive bool, log io.Writer) error {
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
	prompt := agent.ReviewPrompt(reviewGuidance, diff)

	// Interactive: the live TUI, run in the worktree so any applied fixes stay
	// isolated and can be re-gated + pushed by the caller.
	if interactive && agent.CanStream(cfg.Agent) {
		fmt.Fprintf(log, "review-lens: reviewing changes vs %s...\n", base)
		return tui.RunReview(wt.Path, cfg.Agent, prompt, "Reviewing changes vs "+base)
	}

	fmt.Fprintf(log, "review-lens: reviewing changes vs %s...\n", base)
	raw, err := agent.Review(wt.Path, cfg.Agent, prompt, log)
	if err != nil {
		return err
	}
	fmt.Fprintln(log)
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

// openPR shells out to the GitHub CLI to open a PR for branch, then finalizes
// its title and body. It's best-effort: if gh isn't installed we bail; if a PR
// already exists `gh pr create` fails harmlessly and finalizePR still runs (so
// re-running review-lens back-fills the gate signature on an existing PR).
//
// The head branch is passed explicitly because the worktree runs with a detached
// HEAD, so gh can't infer "the current branch".
func openPR(wt *gitx.Worktree, cfg config.Config, branch string, log io.Writer) error {
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("gh not installed")
	}
	cmd := exec.Command("gh", "pr", "create", "--fill", "--head", branch)
	cmd.Dir = wt.Path
	out, err := cmd.CombinedOutput()
	created := err == nil
	if created {
		fmt.Fprintf(log, "review-lens: %s", out)
	} else {
		// Most commonly: a PR already exists for this branch. Not fatal — we still
		// finalize (ensure the signature) below.
		fmt.Fprintf(log, "review-lens: gh pr create: %s\n", strings.TrimSpace(string(out)))
	}
	return finalizePR(wt, cfg, branch, created, log)
}

// prInfo is the subset of a PR's metadata we read and rewrite.
type prInfo struct {
	Number int    `json:"number"`
	Body   string `json:"body"`
	Title  string `json:"title"`
}

// finalizePR sets the PR's title/body and always ensures the gate signature.
//
// On creation it builds the body from the repo's PR template (when present):
// the agent fills the template in from the branch diff, falling back to the raw
// template, then to gh's commit-derived body. If jiraBaseURL is set and a ticket
// key is parseable from the branch, it prefixes the title with "[KEY]" and adds
// a clickable "Jira:" link. On a re-run against an existing PR (created=false)
// it only back-fills the signature — never clobbering a body or title a human
// may have edited.
func finalizePR(wt *gitx.Worktree, cfg config.Config, branch string, created bool, log io.Writer) error {
	pr, err := prForBranch(wt.Path, branch)
	if err != nil {
		return err
	}
	newTitle, newBody := pr.Title, pr.Body

	if created {
		if tmpl := findPRTemplate(wt.Path); tmpl != "" {
			newBody = tmpl
			if cfg.Agent != nil {
				fmt.Fprintln(log, "review-lens: filling in the PR template from the diff…")
				if filled, err := fillPRTemplate(wt, cfg, tmpl, log); err != nil {
					fmt.Fprintf(log, "review-lens: could not fill template (%v); using it as-is\n", err)
				} else {
					newBody = filled
				}
			} else {
				fmt.Fprintln(log, "review-lens: using the repo PR template for the body")
			}
		}
		if key := jiraKeyFromBranch(branch); key != "" && cfg.JiraBaseURL != "" {
			newBody = withJiraRef(newBody, jiraURL(cfg.JiraBaseURL, key))
			newTitle = withJiraTitlePrefix(newTitle, key)
			fmt.Fprintf(log, "review-lens: linking Jira ticket %s\n", key)
		}
	}
	newBody, _ = signature.Ensure(newBody)

	args := []string{"pr", "edit", strconv.Itoa(pr.Number)}
	if newTitle != pr.Title {
		args = append(args, "--title", newTitle)
	}
	if newBody != pr.Body {
		args = append(args, "--body", newBody)
	}
	if len(args) == 3 { // nothing to change
		fmt.Fprintf(log, "review-lens: PR #%d already up to date\n", pr.Number)
		return nil
	}
	edit := exec.Command("gh", args...)
	edit.Dir = wt.Path
	if eout, err := edit.CombinedOutput(); err != nil {
		return fmt.Errorf("updating PR #%d: %w\n%s", pr.Number, err, eout)
	}
	fmt.Fprintf(log, "review-lens: finalized PR #%d\n", pr.Number)
	return nil
}

// fillPRTemplate asks the agent to populate the PR template from the branch's
// diff against the base branch, returning the filled markdown. Any error
// (missing base, empty diff, agent failure, empty output) lets the caller fall
// back to the raw template — filling is best-effort.
func fillPRTemplate(wt *gitx.Worktree, cfg config.Config, tmpl string, log io.Writer) (string, error) {
	base := cfg.BaseBranch
	if base == "" {
		base = "main"
	}
	if !wt.RefExists(base) {
		return "", fmt.Errorf("base branch %q not found", base)
	}
	diff, err := wt.DiffSince(base)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(diff) == "" {
		return "", fmt.Errorf("no diff vs %s", base)
	}
	raw, err := agent.Review(wt.Path, cfg.Agent, agent.PRBodyPrompt(tmpl, diff), log)
	if err != nil {
		return "", err
	}
	body := stripFences(raw)
	if body == "" {
		return "", fmt.Errorf("agent returned an empty body")
	}
	return body, nil
}

// prForBranch returns the open PR for branch via the gh CLI. Uses --head (not
// branch inference) so it works from the detached worktree.
func prForBranch(dir, branch string) (prInfo, error) {
	list := exec.Command("gh", "pr", "list", "--head", branch, "--state", "open", "--json", "number,body,title")
	list.Dir = dir
	out, err := list.Output()
	if err != nil {
		return prInfo{}, fmt.Errorf("looking up PR: %w", err)
	}
	var prs []prInfo
	if err := json.Unmarshal(out, &prs); err != nil {
		return prInfo{}, fmt.Errorf("parsing gh pr list: %w", err)
	}
	if len(prs) == 0 {
		return prInfo{}, fmt.Errorf("no open PR found for branch %q", branch)
	}
	return prs[0], nil
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
