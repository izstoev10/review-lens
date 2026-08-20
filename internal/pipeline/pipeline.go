// Package pipeline orchestrates a full gate run:
//
//	worktree -> checks -> (agent fix -> recheck)* -> commit fixes -> push -> PR
//
// It is the heart of the tool. Everything it does is logged to the provided
// writer so the CLI (and, later, a TUI) can show progress.
package pipeline

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/izstoev10/review-lens/internal/agent"
	"github.com/izstoev10/review-lens/internal/checks"
	"github.com/izstoev10/review-lens/internal/config"
	"github.com/izstoev10/review-lens/internal/findings"
	"github.com/izstoev10/review-lens/internal/gh"
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

	// 0. Running on the base branch is almost always a mistake (you forgot to
	//    branch), and the review would have nothing to diff — its skip used to
	//    bypass the #28 fail-closed guard and force-with-lease push local
	//    commits straight to the base (#37). Refuse before the worktree,
	//    checks, or agent spend any time.
	if onBaseBranch(branch, cfg) {
		return fmt.Errorf("on base branch %q — create a feature branch first (e.g. `git switch -c feat/…`) and rerun; review-lens does not push the base branch directly", branch)
	}

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

	// 1.5 Preflight the gate itself before anything runs or the agent is asked
	//     to fix anything: an empty gate must not appear green, and a check that
	//     can't start (missing dir or executable) is a configuration error, not
	//     a code defect.
	if err := preflight(wt.Path, cfg); err != nil {
		return err
	}

	// 1.6 Bootstrap: dependency/setup commands run once, before the check/fix
	//     loop. Their failures are environment problems and are reported
	//     directly — the agent is never asked to edit code to fix them.
	if err := runSetup(wt.Path, cfg, log); err != nil {
		return err
	}

	// 2. Check / fix loop.
	agentRan, err := checkAndFix(wt.Path, cfg, log)
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

	// 4. Review the committed changes just before pushing. Findings themselves are
	//    advisory (they never block the push), but the review must be able to RUN:
	//    if it's enabled and can't run (no base branch, agent error), we fail
	//    closed and refuse to push. Pushing + opening a PR with no review at all
	//    would defeat the point of the gate.
	var review *tui.Outcome
	if cfg.Review && cfg.Agent != nil {
		// Guidance is read from the real repo root (not the worktree) so edits
		// take effect immediately, without needing to be committed first.
		reviewGuidance := guidance.Load(root, cfg.ReviewGuidancePath)
		outcome, err := reviewDiff(wt, cfg, branch, reviewGuidance, interactive, log)
		if err != nil {
			return fmt.Errorf("review could not run — refusing to push unreviewed: %w", err)
		}
		review = outcome
		if review != nil {
			if err := reviewGate(*review); err != nil {
				return fmt.Errorf("refusing to push: %w", err)
			}
		}
	}

	// 4.5 An interactive review may have applied fixes in the worktree. Re-gate
	//     them — the whole point of `run` is to push only green code — then commit
	//     so they ride along in the push. A failed re-gate blocks the push.
	//     Gated on the session having actually run an apply, so stray artifacts
	//     left by the checks themselves never get committed as "review fixes".
	if review != nil && review.FixRan {
		if changed, err := wt.HasChanges(); err != nil {
			return err
		} else if changed {
			fmt.Fprintln(log, "review-lens: review applied fixes — re-running checks…")
			if _, err := checkAndFix(wt.Path, cfg, log); err != nil {
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

	// 5. Publish the (green) HEAD: push to the remote, then fast-forward the
	//    user's local branch to it when that is safe — otherwise any fix
	//    commits exist only on the remote once the worktree is deleted.
	fmt.Fprintf(log, "review-lens: pushing to %s/%s\n", cfg.Remote, branch)
	advanced, reason, err := wt.Publish(cfg.Remote, branch)
	if err != nil {
		return fmt.Errorf("push failed: %w", err)
	}
	reportPublish(log, branch, advanced, reason)

	// 6. Optionally open a PR via the gh CLI, building the body and stamping the
	//    gate signature.
	prNote := ""
	if cfg.OpenPR {
		if err := openPR(gh.Client{Dir: wt.Path}, wt, cfg, branch, log); err != nil {
			fmt.Fprintf(log, "review-lens: PR step skipped: %v\n", err)
			prNote = " (PR step skipped)"
		}
	}

	// The final line claims only what actually happened (#37).
	fmt.Fprintf(log, "review-lens: ✅ all checks green, pushed%s.\n", prNote)
	return nil
}

// onBaseBranch reports whether branch is the branch a review would diff
// against — the configured base, or the conventional defaults when none is
// configured. A pure name comparison: it must be answerable before any
// worktree exists, so resolveBaseBranch's ref resolution can't run yet; the
// resolved case is fail-closed separately in reviewDiff.
func onBaseBranch(branch string, cfg config.Config) bool {
	if cfg.BaseBranch != "" {
		return branch == cfg.BaseBranch
	}
	return branch == "main" || branch == "master"
}

// reportPublish tells the user where the pushed commit ended up locally —
// silence here is how a checkout falls behind without anyone noticing.
func reportPublish(log io.Writer, branch string, advanced bool, reason string) {
	switch {
	case advanced:
		fmt.Fprintf(log, "review-lens: local %s fast-forwarded to the pushed commit\n", branch)
	case reason != "":
		fmt.Fprintf(log, "review-lens: note: local %s was NOT advanced (%s) — run `git pull --ff-only` to catch up\n", branch, reason)
	}
}

// preflight validates the configured gate against the worktree before any
// check or agent runs: there must be meaningful checks, and every command must
// have an existing working directory and a resolvable executable. Failing here
// names the broken piece and points at the configurator, because no amount of
// agent-editing code can repair configuration.
func preflight(dir string, cfg config.Config) error {
	if !config.MeaningfulChecks(cfg) {
		return fmt.Errorf("no meaningful checks configured — refusing to push an unvalidated branch; run `review-lens configure` to set up the gate")
	}
	for _, c := range append(append([]config.Check{}, cfg.Setup...), cfg.Checks...) {
		if len(c.Cmd) == 0 {
			return fmt.Errorf("check %q has no command; run `review-lens configure` to repair the gate", c.Name)
		}
		if c.Dir != "" {
			if _, err := os.Stat(filepath.Join(dir, c.Dir)); err != nil {
				return fmt.Errorf("check %q: working directory %q does not exist in the repo — run `review-lens configure` to repair the gate", c.Name, c.Dir)
			}
		}
		if strings.ContainsRune(c.Cmd[0], os.PathSeparator) {
			if _, err := os.Stat(filepath.Join(dir, c.Dir, c.Cmd[0])); err != nil {
				return fmt.Errorf("check %q: command %q not found under %q — run `review-lens configure` to repair the gate", c.Name, c.Cmd[0], c.Dir)
			}
		} else if _, err := exec.LookPath(c.Cmd[0]); err != nil {
			return fmt.Errorf("check %q: executable %q not found on PATH — install it or run `review-lens configure`", c.Name, c.Cmd[0])
		}
	}
	return nil
}

// runSetup runs the bootstrap commands. A failure is reported as an
// environment/configuration error with the command's output attached; it is
// never routed to the fixing agent.
func runSetup(dir string, cfg config.Config, log io.Writer) error {
	for _, c := range cfg.Setup {
		fmt.Fprintf(log, "review-lens:   [setup] %s\n", c.Name)
		if r := checks.Run(dir, c); !r.Passed {
			return fmt.Errorf("setup command %q failed — this is an environment/configuration problem, not a code defect; fix it or run `review-lens configure`:\n%s",
				c.Name, strings.TrimSpace(r.Output))
		}
	}
	return nil
}

// checkAndFix runs all checks, and on failure asks the agent to fix and retries,
// up to cfg.MaxAgentAttempts. It returns agentRan=true if the agent was invoked
// at least once (so the caller knows whether to commit). It returns an error if
// checks are still failing when attempts run out (or if no agent is configured
// to fix them).
func checkAndFix(dir string, cfg config.Config, log io.Writer) (agentRan bool, err error) {
	attempts := cfg.MaxAgentAttempts
	for i := 0; ; i++ {
		results, ok := checks.RunAll(dir, cfg.Checks)
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

		// A failure the environment caused (missing executable, npm script,
		// working directory) is invalid configuration: asking the agent to edit
		// code for it would misdiagnose the problem and burn a fix attempt.
		if failed.ConfigProblem != "" {
			return agentRan, fmt.Errorf("check %q cannot run: %s — run `review-lens configure` to repair the gate\n%s",
				failed.Name, failed.ConfigProblem, strings.TrimSpace(failed.Output))
		}

		if cfg.Agent == nil {
			return agentRan, fmt.Errorf("check %q failed and no agent configured:\n%s", failed.Name, failed.Output)
		}
		if i >= attempts {
			return agentRan, fmt.Errorf("check %q still failing after %d fix attempt(s)", failed.Name, attempts)
		}

		fmt.Fprintf(log, "review-lens: attempt %d/%d — asking agent to fix %q (live output below)\n", i+1, attempts, failed.Name)
		agentRan = true
		prompt := agent.Prompt(failed.Name, failed.Output)
		if err := agent.Fix(dir, cfg.Agent, prompt, log); err != nil {
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
// resolveBaseBranch finds the branch to diff against. It tries the configured
// baseBranch (if any), then the common defaults main and master, in both local
// and remote-tracking (origin/…) forms — so a repo whose default branch is
// "master" just works, and a fresh clone with only remote-tracking refs does
// too. It returns an error if none resolve, letting callers fail closed rather
// than silently skip the review.
func resolveBaseBranch(wt *gitx.Worktree, cfg config.Config) (string, error) {
	remote := cfg.Remote
	if remote == "" {
		remote = "origin"
	}
	var tried []string
	seen := map[string]bool{}
	// Local refs first (configured, then the conventional defaults), then their
	// remote-tracking counterparts.
	for _, ref := range []string{
		cfg.BaseBranch, "main", "master",
		remote + "/" + cfg.BaseBranch, remote + "/main", remote + "/master",
	} {
		if ref == "" || ref == remote+"/" || seen[ref] {
			continue
		}
		seen[ref] = true
		tried = append(tried, ref)
		if wt.RefExists(ref) {
			return ref, nil
		}
	}
	return "", fmt.Errorf("no base branch found (tried %s) — set \"baseBranch\" in .review-lens.json",
		strings.Join(tried, ", "))
}

// reviewDiff returns the interactive session's outcome (nil on the plain path
// and the nothing-to-review early returns) so Run can judge push eligibility.
func reviewDiff(wt *gitx.Worktree, cfg config.Config, branch, reviewGuidance string, interactive bool, log io.Writer) (*tui.Outcome, error) {
	base, err := resolveBaseBranch(wt, cfg)
	if err != nil {
		return nil, err
	}
	if base == branch {
		// Run's early guard compares names only, so resolution can still land
		// here (e.g. configured base "main" in a repo whose real base is
		// "master"). A review skipped because there is nothing to diff must
		// not fall through to the push — the sibling of #28's fail-closed
		// rule (#37).
		return nil, fmt.Errorf("on base branch %q — nothing to review; create a feature branch and rerun", base)
	}
	diff, err := wt.DiffSince(base)
	if err != nil {
		return nil, err
	}
	if diff == "" {
		fmt.Fprintf(log, "review-lens: no changes vs %s to review\n", base)
		return nil, nil
	}
	prompt := agent.ReviewPrompt(reviewGuidance, diff)

	// Interactive: the live TUI, run in the worktree so any applied fixes stay
	// isolated and can be re-gated + pushed by the caller. Every agent gets the
	// TUI — one with no event stream shows a waiting state instead of a live
	// feed, then the same findings viewer.
	if interactive {
		fmt.Fprintf(log, "review-lens: reviewing changes vs %s...\n", base)
		outcome, err := tui.RunReview(wt.Path, cfg.Agent, prompt, "Reviewing changes vs "+base, tui.DestWorktree, log)
		if err != nil {
			return nil, err
		}
		return &outcome, nil
	}

	fmt.Fprintf(log, "review-lens: reviewing changes vs %s...\n", base)
	raw, err := agent.Review(wt.Path, cfg.Agent, prompt, log)
	if err != nil {
		return nil, err
	}
	fmt.Fprintln(log)
	list := showReview(raw, log)
	if list == nil {
		return nil, nil
	}
	// Unattended, nobody can decide — ask-user findings stay pending in the
	// outcome, and the same gate that guards an interactive run guards this one.
	outcome := tui.UnattendedOutcome(list)
	return &outcome, nil
}

// reviewGate decides whether a run may continue to push after a review.
// Auto-fix and no-op findings stay advisory — what blocks the push is a review
// that never completed (failed or stopped), an apply that ended in an unknown
// state, or an ask-user finding nobody decided: each would mean pushing
// unreviewed, half-edited, or unjudged code.
func reviewGate(o tui.Outcome) error {
	switch {
	case errors.Is(o.ReviewErr, agent.ErrCanceled):
		return fmt.Errorf("the review was stopped before it completed")
	case o.ReviewErr != nil:
		return fmt.Errorf("the review failed: %w", o.ReviewErr)
	case errors.Is(o.FixErr, agent.ErrCanceled):
		return fmt.Errorf("an apply was stopped mid-run — the worktree may hold partial edits")
	case o.FixErr != nil:
		return fmt.Errorf("the last apply failed — the worktree may hold partial edits: %w", o.FixErr)
	}
	if un := unresolvedAskUser(o); len(un) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "%s need a decision before this branch can be pushed:\n", plural(len(un), "ask-user finding"))
		for _, f := range un {
			loc := f.File
			if f.Line > 0 {
				loc = fmt.Sprintf("%s:%d", f.File, f.Line)
			}
			fmt.Fprintf(&b, "  - %s — %s\n", loc, f.Title)
		}
		b.WriteString("rerun `review-lens run` in a terminal and fix, approve, or skip each (a fix must also be applied)")
		return errors.New(b.String())
	}
	return nil
}

// unresolvedAskUser lists the ask-user findings the session left undecided.
// A fix that was marked but never applied is still unresolved — the intent
// was recorded, the judgement wasn't carried out. Auto-fix and no-op findings
// never appear here, whatever their decision state.
func unresolvedAskUser(o tui.Outcome) []findings.Finding {
	var un []findings.Finding
	for i, f := range o.Findings {
		if f.Action != findings.AskUser {
			continue
		}
		resolved := i < len(o.Decisions) &&
			(o.Decisions[i] == tui.DecisionApplied ||
				o.Decisions[i] == tui.DecisionApprove ||
				o.Decisions[i] == tui.DecisionSkip)
		if !resolved {
			un = append(un, f)
		}
	}
	return un
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// showReview prints an agent's raw review output as the compact colourised
// report and returns the parsed findings (nil when the output wasn't a
// findings array — then a bounded excerpt is printed instead, never the whole
// response, which on a broken run can be an entire transcript).
func showReview(raw string, log io.Writer) []findings.Finding {
	list, ok := findings.Parse(raw)
	if !ok {
		fmt.Fprintln(log, findings.Unparsable(raw))
		return nil
	}
	findings.Render(log, list, true)
	return list
}

// openPR opens a PR for branch via the PR client, then finalizes its title and
// body. It's best-effort: if gh isn't installed we bail; if a PR already
// exists Create fails harmlessly and finalizePR still runs (so re-running
// review-lens back-fills the gate signature on an existing PR).
//
// The head branch is passed explicitly because the worktree runs with a detached
// HEAD, so gh can't infer "the current branch".
func openPR(c gh.Client, wt *gitx.Worktree, cfg config.Config, branch string, log io.Writer) error {
	if c.Exec == nil {
		if err := gh.Available(); err != nil {
			return err
		}
	}
	created, message := c.Create(branch)
	if created {
		fmt.Fprintf(log, "review-lens: %s\n", message)
	} else {
		// Most commonly: a PR already exists for this branch. Not fatal — we still
		// finalize (ensure the signature) below.
		fmt.Fprintf(log, "review-lens: gh pr create: %s\n", message)
	}
	return finalizePR(c, wt, cfg, branch, created, log)
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
func finalizePR(c gh.Client, wt *gitx.Worktree, cfg config.Config, branch string, created bool, log io.Writer) error {
	pr, err := c.OpenFor(branch)
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

	changed, err := c.Edit(pr, newTitle, newBody)
	if err != nil {
		return err
	}
	if !changed {
		fmt.Fprintf(log, "review-lens: PR #%d already up to date\n", pr.Number)
		return nil
	}
	fmt.Fprintf(log, "review-lens: finalized PR #%d\n", pr.Number)
	return nil
}

// fillPRTemplate asks the agent to populate the PR template from the branch's
// diff against the base branch, returning the filled markdown. Any error
// (missing base, empty diff, agent failure, empty output) lets the caller fall
// back to the raw template — filling is best-effort.
func fillPRTemplate(wt *gitx.Worktree, cfg config.Config, tmpl string, log io.Writer) (string, error) {
	base, err := resolveBaseBranch(wt, cfg)
	if err != nil {
		return "", err
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

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}
