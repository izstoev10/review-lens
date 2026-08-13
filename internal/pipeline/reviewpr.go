package pipeline

import (
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/izstoev10/review-lens/internal/agent"
	"github.com/izstoev10/review-lens/internal/config"
	"github.com/izstoev10/review-lens/internal/gh"
	"github.com/izstoev10/review-lens/internal/gitx"
	"github.com/izstoev10/review-lens/internal/guidance"
	"github.com/izstoev10/review-lens/internal/tui"
)

// ReviewPR reviews an already-pushed pull request, read-only. It fetches the
// PR's diff with `gh pr diff` (current branch's PR if number is empty) and asks
// the agent to review it. Nothing is committed, pushed, or modified — this is
// purely a reviewer's lens over an open PR.
//
// dir is the repo directory; the agent runs there so it can read the code for
// context while reviewing.
func ReviewPR(dir, number string, cfg config.Config, log io.Writer, interactive bool) error {
	if cfg.Agent == nil {
		return fmt.Errorf("no agent configured (set \"agent\" in .review-lens.json)")
	}
	if err := gh.Available(); err != nil {
		return err
	}

	diff, err := gh.Client{Dir: dir}.Diff(number)
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
	// Load review guidance from the repo root (falls back to the built-in
	// default if there's no guidance file, or if dir isn't inside a repo).
	root := dir
	if r, err := gitx.RepoRoot(dir); err == nil {
		root = r
	}
	prompt := agent.ReviewPrompt(guidance.Load(root, cfg.ReviewGuidancePath), diff)

	// An interactive terminal gets the live TUI (activity while the agent works
	// — or a waiting state if it emits no events — then findings). Piped output
	// gets the plain report. Same rules as `run`.
	if interactive {
		outcome, err := tui.RunReview(dir, cfg.Agent, prompt, "Reviewing "+target, tui.DestWorkingTree, log)
		if err != nil {
			return err
		}
		// `pr` is read-only, so a stopped review is the user's call and not an
		// error — but a review that *failed* must exit non-zero, not vanish with
		// the alt screen.
		switch {
		case errors.Is(outcome.ReviewErr, agent.ErrCanceled):
			fmt.Fprintln(log, "review-lens: review stopped")
			return nil
		case outcome.ReviewErr != nil:
			return fmt.Errorf("review failed: %w", outcome.ReviewErr)
		}
		return nil
	}

	fmt.Fprintf(log, "review-lens: reviewing %s...\n", target)
	raw, err := agent.Review(dir, cfg.Agent, prompt, log)
	if err != nil {
		return err
	}
	fmt.Fprintln(log)
	showReview(raw, log)
	return nil
}
