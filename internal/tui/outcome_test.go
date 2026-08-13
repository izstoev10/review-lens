package tui

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/izstoev10/review-lens/internal/agent"
	"github.com/izstoev10/review-lens/internal/config"
	"github.com/izstoev10/review-lens/internal/findings"
)

// Quitting mid-review cancels the agent; the cancellation must survive into
// the outcome, or the caller pushes a branch whose review never happened —
// the exact fail-closed violation of issue #50.
func TestQuitMidReviewSurfacesTheCancellation(t *testing.T) {
	m := newModel("t", t.TempDir(), &config.Agent{Cmd: []string{"true"}}, make(chan tea.Msg, 8))
	m.quitting = true // requestQuit has cancelled the agent

	next, _ := m.Update(doneMsg{err: agent.ErrCanceled})
	o := next.(model).outcome()

	if !errors.Is(o.ReviewErr, agent.ErrCanceled) {
		t.Errorf("ReviewErr = %v, want agent.ErrCanceled — a stopped review must be visible to the caller", o.ReviewErr)
	}
}

// A review that failed outright must surface its error the same way.
func TestFailedReviewSurfacesItsError(t *testing.T) {
	m := newModel("t", t.TempDir(), nil, make(chan tea.Msg, 8))
	boom := errors.New("agent exploded")

	next, _ := m.Update(doneMsg{err: boom})

	if got := next.(model).outcome().ReviewErr; !errors.Is(got, boom) {
		t.Errorf("ReviewErr = %v, want the agent's error", got)
	}
}

// The user's per-finding choices must cross the seam in the exported
// vocabulary, with an applied fix outranking the decision that requested it.
func TestOutcomeExportsDecisions(t *testing.T) {
	m := newModel("t", t.TempDir(), nil, make(chan tea.Msg, 8))
	m.items = make([]findings.Finding, 5)
	m.decisions = map[int]decision{0: decFix, 1: decFix, 2: decApprove, 3: decSkip, 4: decPending}
	m.applied = map[int]bool{0: true}

	got := m.outcome().Decisions

	want := []Decision{DecisionApplied, DecisionFix, DecisionApprove, DecisionSkip, DecisionPending}
	if len(got) != len(want) {
		t.Fatalf("got %d decisions, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("decision %d = %v, want %v", i, got[i], want[i])
		}
	}
}

// An apply the user stopped mid-run may have half-edited the tree; the
// outcome must say so, so the caller can refuse to commit and push it.
func TestCancelledApplySurvivesInOutcome(t *testing.T) {
	m := fixingModel(t)
	m.fixRan = true

	next, _ := m.Update(fixDoneMsg{err: agent.ErrCanceled})
	o := next.(model).outcome()

	if !o.FixRan {
		t.Error("FixRan = false, want true — an apply started")
	}
	if !errors.Is(o.FixErr, agent.ErrCanceled) {
		t.Errorf("FixErr = %v, want agent.ErrCanceled", o.FixErr)
	}
}

// A clean session — review completed, fixes (if any) applied — reports no
// errors, so the caller is free to push.
func TestCleanSessionHasCleanOutcome(t *testing.T) {
	m := fixingModel(t)
	m.fixRan = true

	next, _ := m.Update(fixDoneMsg{})
	o := next.(model).outcome()

	if o.ReviewErr != nil || o.FixErr != nil {
		t.Errorf("outcome = %+v, want no errors after a clean session", o)
	}
	if len(o.Findings) == 0 {
		t.Error("the session's findings should cross the seam")
	}
}

// A non-interactive review has nobody to decide: ask-user findings must reach
// the caller's gate as pending, while auto-fix and no-op take their defaults.
func TestUnattendedOutcomeKeepsAskUserPending(t *testing.T) {
	o := UnattendedOutcome([]findings.Finding{
		{Action: findings.AskUser},
		{Action: findings.AutoFix},
		{Action: findings.NoOp},
	})

	want := []Decision{DecisionPending, DecisionFix, DecisionSkip}
	for i, w := range want {
		if o.Decisions[i] != w {
			t.Errorf("decision %d = %v, want %v", i, o.Decisions[i], w)
		}
	}
	if o.ReviewErr != nil || o.FixRan {
		t.Errorf("outcome = %+v, want a plain completed-review shape", o)
	}
}
