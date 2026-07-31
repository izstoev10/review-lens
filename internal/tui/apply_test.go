package tui

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/izstoev10/review-lens/internal/config"
	"github.com/izstoev10/review-lens/internal/findings"
)

// fixingModel is a model paused mid-apply, with finding 0 in flight and
// finding 1 left untouched.
func fixingModel(t *testing.T) model {
	t.Helper()
	m := newModel("", t.TempDir(), &config.Agent{Cmd: []string{"true"}}, make(chan tea.Msg, 8))
	m.phase = phaseFixing
	m.items = []findings.Finding{{Title: "first"}, {Title: "second"}}
	m.decisions = map[int]decision{0: decFix, 1: decPending}
	m.applying = []int{0}
	return m
}

func press(t *testing.T, m model, key string) model {
	t.Helper()
	next, _ := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)})
	return next.(model)
}

// Applying used to be the end of the session — every key but `q` was ignored
// afterwards, so findings left unmarked could never be handled. A finished
// apply must land back in the viewer, still navigable.
func TestApplyReturnsToAWorkingViewer(t *testing.T) {
	next, _ := fixingModel(t).Update(fixDoneMsg{})
	m := next.(model)

	if m.phase != phaseDone {
		t.Fatalf("phase = %v, want phaseDone (the viewer)", m.phase)
	}
	if !m.applied[0] {
		t.Error("the applied finding should be marked as fixed")
	}
	if got := len(m.pending()); got != 0 {
		t.Errorf("pending = %d, want 0 — a fixed finding must not be re-sent", got)
	}
	if m.fixSummary == "" {
		t.Error("expected an outcome banner after applying")
	}

	// The viewer is genuinely live again: navigate, mark the second finding,
	// and it becomes the next thing to apply.
	m = press(t, m, "j")
	if m.cursor != 1 {
		t.Fatalf("cursor = %d, want 1 — keys are dead after applying", m.cursor)
	}
	m = press(t, m, "f")
	if got := m.pending(); len(got) != 1 || got[0] != 1 {
		t.Errorf("pending = %v, want [1] — cannot queue a second pass", got)
	}
}

// A failed apply must leave the marks intact so the user can just hit enter
// again, rather than re-marking everything from scratch.
func TestFailedApplyKeepsTheWorkQueued(t *testing.T) {
	next, _ := fixingModel(t).Update(fixDoneMsg{err: errors.New("boom")})
	m := next.(model)

	if m.phase != phaseDone {
		t.Fatalf("phase = %v, want phaseDone", m.phase)
	}
	if m.applied[0] {
		t.Error("a failed apply must not mark the finding as fixed")
	}
	if !m.fixFailed {
		t.Error("expected the outcome banner to be styled as a failure")
	}
	if got := m.pending(); len(got) != 1 || got[0] != 0 {
		t.Errorf("pending = %v, want [0] — a retry should need no re-marking", got)
	}
}

// Marking an applied finding again clears the fixed flag, so a fix that didn't
// take can be sent back to the agent.
func TestReMarkingAnAppliedFindingQueuesItAgain(t *testing.T) {
	next, _ := fixingModel(t).Update(fixDoneMsg{})
	m := next.(model)

	m = press(t, m, "f") // cursor is still on finding 0
	if m.applied[0] {
		t.Error("re-marking should clear the applied flag")
	}
	if got := m.pending(); len(got) != 1 || got[0] != 0 {
		t.Errorf("pending = %v, want [0]", got)
	}
}

// Quitting mid-apply must not return control to the caller while the agent is
// still editing files: `run` re-checks and commits that tree the moment the
// TUI exits. The quit is held until the agent reports it has stopped.
func TestQuitDuringApplyWaitsForTheAgent(t *testing.T) {
	m := fixingModel(t)
	canceled := false
	m.cancelAgent = func() { canceled = true }

	next, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	m = next.(model)

	if !canceled {
		t.Error("quitting should cancel the running agent")
	}
	if !m.quitting {
		t.Error("expected the model to record that a quit is pending")
	}
	if cmd != nil {
		t.Fatal("must not quit while the agent may still be writing files")
	}

	// Once the agent reports back, the program is free to exit.
	next, cmd = m.Update(fixDoneMsg{})
	if cmd == nil {
		t.Fatal("expected to quit once the agent had stopped")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("expected a quit command after the agent stopped")
	}
	if !next.(model).applied[0] {
		t.Error("edits the agent completed before stopping should still be recorded")
	}
}

// Nothing is running in the viewer, so quitting there is immediate.
func TestQuitFromTheViewerIsImmediate(t *testing.T) {
	m := fixingModel(t)
	m.phase = phaseDone

	_, cmd := m.handleKey(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if cmd == nil {
		t.Fatal("expected an immediate quit from the viewer")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("expected a quit command")
	}
}

// Two apply passes must not stack up duplicate checkpoints in the pipeline panel.
func TestRepeatedAppliesReuseTheStage(t *testing.T) {
	next, _ := fixingModel(t).Update(fixDoneMsg{})
	m := next.(model)
	next, _ = m.Update(fixDoneMsg{})
	m = next.(model)

	n := 0
	for _, s := range m.stages {
		if s.name == "Apply fixes" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("found %d \"Apply fixes\" stages, want 1", n)
	}
}
