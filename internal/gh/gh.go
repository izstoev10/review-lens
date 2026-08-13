// Package gh is the single owner of GitHub CLI execution. Every question
// review-lens asks about a pull request — its diff, its head branch, its check
// rollup — and every action it takes (create, edit) goes through one Client,
// so availability, working-directory pinning, the stdout/stderr split, and
// JSON decoding are each decided exactly once.
package gh

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// PR is the subset of pull-request metadata review-lens reads and rewrites.
type PR struct {
	Number  int    `json:"number"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	HeadRef string `json:"headRefName"`
}

// CheckRun is one entry of GitHub's statusCheckRollup: either a check run
// (Status/Conclusion) or a legacy commit status (State).
type CheckRun struct {
	Name       string `json:"name"`
	Status     string `json:"status"`     // QUEUED | IN_PROGRESS | COMPLETED (checks) or "" (statuses)
	Conclusion string `json:"conclusion"` // SUCCESS | FAILURE | ... (checks)
	State      string `json:"state"`      // SUCCESS | FAILURE | PENDING (legacy commit statuses)
}

// Available reports whether the gh CLI can be used at all — the one canonical
// check and install hint, shared by every command that needs GitHub.
func Available() error {
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("the GitHub CLI (gh) is required; install it and run `gh auth login`")
	}
	return nil
}

// Client runs the GitHub CLI pinned to one directory.
//
// Dir decides which repo — and, for an empty PR number, which current branch —
// gh sees. The disposable worktree (detached HEAD) and the user's checkout
// give different answers, so callers pin it deliberately and pass explicit
// numbers or --head branches where the worktree cannot infer them.
type Client struct {
	Dir string
	// Exec, when non-nil, replaces the real gh binary and returns its stdout.
	// This is the seam tests and fake PR collaborators plug into.
	Exec func(args ...string) ([]byte, error)
}

// run executes gh, enforcing the stream discipline: stdout is the only data
// channel; stderr is diagnostics and surfaces only inside the error. This is
// what keeps a gh warning from ever leaking into a diff or a JSON payload.
func (c Client) run(args ...string) (string, error) {
	if c.Exec != nil {
		out, err := c.Exec(args...)
		return string(out), err
	}
	cmd := exec.Command("gh", args...)
	cmd.Dir = c.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("gh %s: %w\n%s",
			strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// view fetches PR fields via `gh pr view --json`. An empty number means the
// PR of Dir's current branch — that convention is implemented here, once.
func (c Client) view(number, fields string, into any) error {
	args := []string{"pr", "view"}
	if number != "" {
		args = append(args, number)
	}
	args = append(args, "--json", fields)
	out, err := c.run(args...)
	if err != nil {
		return fmt.Errorf("gh pr view failed (is there an open PR for this branch?): %w", err)
	}
	if err := json.Unmarshal([]byte(out), into); err != nil {
		return fmt.Errorf("parsing gh pr view: %w", err)
	}
	return nil
}

// Diff returns the PR's unified diff (empty number = current branch's PR).
func (c Client) Diff(number string) (string, error) {
	args := []string{"pr", "diff"}
	if number != "" {
		args = append(args, number)
	}
	out, err := c.run(args...)
	if err != nil {
		return "", fmt.Errorf("gh pr diff failed (is there an open PR for this branch?): %w", err)
	}
	return out, nil
}

// View returns the PR's metadata (empty number = current branch's PR).
func (c Client) View(number string) (PR, error) {
	var pr PR
	if err := c.view(number, "number,title,body,headRefName", &pr); err != nil {
		return PR{}, err
	}
	return pr, nil
}

// OpenFor returns the open PR whose head is branch. The branch is passed
// explicitly (--head) because callers often run from a detached worktree,
// where gh cannot infer a current branch.
func (c Client) OpenFor(branch string) (PR, error) {
	out, err := c.run("pr", "list", "--head", branch, "--state", "open", "--json", "number,body,title")
	if err != nil {
		return PR{}, fmt.Errorf("looking up PR: %w", err)
	}
	var prs []PR
	if err := json.Unmarshal([]byte(out), &prs); err != nil {
		return PR{}, fmt.Errorf("parsing gh pr list: %w", err)
	}
	if len(prs) == 0 {
		return PR{}, fmt.Errorf("no open PR found for branch %q", branch)
	}
	return prs[0], nil
}

// Create opens a PR for branch with gh's commit-derived title and body.
// created=false usually means a PR already exists, which callers treat as
// harmless; message is gh's own output either way (the PR URL on success).
func (c Client) Create(branch string) (created bool, message string) {
	out, err := c.run("pr", "create", "--fill", "--head", branch)
	if err != nil {
		return false, strings.TrimSpace(err.Error())
	}
	return true, strings.TrimSpace(out)
}

// Edit moves the PR's title and body to the given values, sending only the
// fields that actually differ. changed=false means there was nothing to send.
func (c Client) Edit(pr PR, title, body string) (changed bool, err error) {
	var flags []string
	if title != pr.Title {
		flags = append(flags, "--title", title)
	}
	if body != pr.Body {
		flags = append(flags, "--body", body)
	}
	if len(flags) == 0 {
		return false, nil
	}
	args := append([]string{"pr", "edit", strconv.Itoa(pr.Number)}, flags...)
	if _, err := c.run(args...); err != nil {
		return false, fmt.Errorf("updating PR #%d: %w", pr.Number, err)
	}
	return true, nil
}

// CheckRollup returns the PR's check runs (empty number = current branch's
// PR), for the caller to classify.
func (c Client) CheckRollup(number string) ([]CheckRun, error) {
	var payload struct {
		StatusCheckRollup []CheckRun `json:"statusCheckRollup"`
	}
	if err := c.view(number, "statusCheckRollup", &payload); err != nil {
		return nil, err
	}
	return payload.StatusCheckRollup, nil
}
