package pipeline

import (
	"errors"
	"strings"
	"testing"

	"github.com/izstoev10/review-lens/internal/agent"
	"github.com/izstoev10/review-lens/internal/findings"
	"github.com/izstoev10/review-lens/internal/tui"
)

// The gate on pushing after an interactive review: findings stay advisory,
// but a review that never completed — or an apply that ended in an unknown
// state — must block the push. This is the fail-closed contract of issue #50.
func TestReviewGate(t *testing.T) {
	tests := []struct {
		name    string
		outcome tui.Outcome
		block   string // "" = push allowed; otherwise the error must contain it
	}{
		{
			name:    "clean review with findings pushes — findings are advisory",
			outcome: tui.Outcome{Findings: make([]findings.Finding, 3), Decisions: make([]tui.Decision, 3)},
		},
		{
			name:    "clean review with applied fixes pushes",
			outcome: tui.Outcome{FixRan: true},
		},
		{
			name:    "quitting mid-review blocks — the branch was never reviewed",
			outcome: tui.Outcome{ReviewErr: agent.ErrCanceled},
			block:   "stopped",
		},
		{
			name:    "a failed review blocks",
			outcome: tui.Outcome{ReviewErr: errors.New("agent exploded")},
			block:   "review failed",
		},
		{
			name:    "an apply stopped mid-run blocks — the tree may be half-edited",
			outcome: tui.Outcome{FixRan: true, FixErr: agent.ErrCanceled},
			block:   "partial edits",
		},
		{
			name:    "a failed apply blocks for the same reason",
			outcome: tui.Outcome{FixRan: true, FixErr: errors.New("agent died")},
			block:   "partial edits",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := reviewGate(tt.outcome)
			if tt.block == "" {
				if err != nil {
					t.Fatalf("gate blocked a clean session: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatal("gate allowed a push it must refuse")
			}
			if !strings.Contains(err.Error(), tt.block) {
				t.Errorf("err = %v, want it to mention %q", err, tt.block)
			}
		})
	}
}
