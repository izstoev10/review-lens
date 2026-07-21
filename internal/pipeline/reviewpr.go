package pipeline

import (
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/izstoev10/review-lens/internal/agent"
	"github.com/izstoev10/review-lens/internal/config"
)

// ReviewPR reviews an already-pushed pull request, read-only. It fetches the
// PR's diff with `gh pr diff` (current branch's PR if number is empty) and asks
// the agent to review it. Nothing is committed, pushed, or modified — this is
// purely a reviewer's lens over an open PR.
//
// dir is the repo directory; the agent runs there so it can read the code for
// context while reviewing.
func ReviewPR(dir, number string, cfg config.Config, log io.Writer) error {
	if cfg.Agent == nil {
		return fmt.Errorf("no agent configured (set \"agent\" in .review-lens.json)")
	}
	if _, err := exec.LookPath("gh"); err != nil {
		return fmt.Errorf("the GitHub CLI (gh) is required for PR review; install it and run `gh auth login`")
	}

	diff, err := ghPRDiff(dir, number)
	if err != nil {
		return err
	}
	if strings.TrimSpace(diff) == "" {
		fmt.Fprintln(log, "review-lens: PR has no diff to review")
		return nil
	}

	target := "the current branch's PR"
	if number != "" {
		target = "PR #" + number
	}
	fmt.Fprintf(log, "review-lens: reviewing %s...\n", target)
	raw, err := agent.Review(dir, cfg.Agent, agent.ReviewPrompt(diff), log)
	if err != nil {
		return err
	}
	fmt.Fprintln(log)
	showReview(raw, log)
	return nil
}

// ghPRDiff returns the unified diff of a PR via the GitHub CLI. An empty number
// means "the PR associated with the current branch".
func ghPRDiff(dir, number string) (string, error) {
	args := []string{"pr", "diff"}
	if number != "" {
		args = append(args, number)
	}
	cmd := exec.Command("gh", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("gh pr diff failed (is there an open PR for this branch?): %w\n%s", err, out)
	}
	return string(out), nil
}
