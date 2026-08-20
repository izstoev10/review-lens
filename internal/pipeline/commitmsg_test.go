package pipeline

import (
	"strings"
	"testing"

	"github.com/izstoev10/review-lens/internal/findings"
	"github.com/izstoev10/review-lens/internal/tui"
)

// Commit messages must say WHAT was fixed — a branch history of identical
// "apply review fixes" lines tells a reviewer or a bisect nothing (#39).
func TestCheckFixMessage(t *testing.T) {
	if got := checkFixMessage([]string{"test"}); got != `review-lens: fix failing "test" check` {
		t.Errorf("one check: got %q", got)
	}
	got := checkFixMessage([]string{"vet", "test"})
	if !strings.Contains(got, "vet") || !strings.Contains(got, "test") {
		t.Errorf("two checks: got %q, want both names", got)
	}
}

func TestReviewFixMessage(t *testing.T) {
	o := tui.Outcome{
		Findings: []findings.Finding{
			{File: "internal/tui/tui.go", Line: 330, Title: "Quitting mid-apply exits silently"},
			{File: "internal/agent/agent.go", Title: "no line number on this one"},
			{File: "a.txt", Line: 1, Title: "skipped, must not appear"},
		},
		Decisions: []tui.Decision{tui.DecisionApplied, tui.DecisionApplied, tui.DecisionSkip},
	}

	got := reviewFixMessage(o, []string{"test"})

	if !strings.HasPrefix(got, "review-lens: apply 2 review fixes\n\n") {
		t.Errorf("subject/body split wrong:\n%s", got)
	}
	for _, want := range []string{
		"internal/tui/tui.go:330 — Quitting mid-apply exits silently",
		"internal/agent/agent.go — no line number on this one",
		`failing "test" check`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("message missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "must not appear") {
		t.Errorf("a skipped finding leaked into the message:\n%s", got)
	}
}

// No recorded applied findings (decisions lost or empty) still yields a sane
// constant subject rather than "apply 0 review fixes".
func TestReviewFixMessageFallsBackWithoutDecisions(t *testing.T) {
	got := reviewFixMessage(tui.Outcome{}, nil)
	if got != "review-lens: apply review fixes" {
		t.Errorf("got %q", got)
	}
}
