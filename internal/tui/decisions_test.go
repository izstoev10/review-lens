package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/izstoev10/review-lens/internal/agent"
	"github.com/izstoev10/review-lens/internal/findings"
)

func TestDefaultDecisions(t *testing.T) {
	items := []findings.Finding{
		{Action: findings.AutoFix},
		{Action: findings.AskUser},
		{Action: findings.NoOp},
		{Action: ""}, // unknown -> treated as pending
	}
	got := defaultDecisions(items)
	want := map[int]decision{0: decFix, 1: decPending, 2: decSkip, 3: decPending}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("finding %d: got decision %d, want %d", i, got[i], w)
		}
	}
}

func TestFixPromptIncludesOnlyFixMarkedAndConventions(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("Use tabs. Prefer table-driven tests."), 0o644); err != nil {
		t.Fatal(err)
	}
	items := []findings.Finding{
		{File: "a.go", Line: 10, Title: "fix me", Detail: "bug", Action: findings.AutoFix},
		{File: "b.go", Line: 20, Title: "leave me", Detail: "style", Action: findings.AskUser},
	}
	decisions := map[int]decision{0: decFix, 1: decApprove}

	prompt := fixPrompt(dir, items, decisions, nil)

	if !strings.Contains(prompt, "table-driven tests") {
		t.Error("expected repo conventions (AGENTS.md) to be included in the fix prompt")
	}
	if !strings.Contains(prompt, "a.go:10") {
		t.Error("expected the fix-marked finding to be included")
	}
	if strings.Contains(prompt, "b.go:20") {
		t.Error("approved finding should NOT be in the fix prompt")
	}
}

// A second apply pass must not re-send what the agent already fixed, or the
// agent gets asked to fix code it has just rewritten.
func TestFixPromptSkipsAlreadyApplied(t *testing.T) {
	items := []findings.Finding{
		{File: "a.go", Line: 10, Title: "already done", Detail: "bug"},
		{File: "b.go", Line: 20, Title: "still open", Detail: "bug"},
	}
	decisions := map[int]decision{0: decFix, 1: decFix}
	applied := map[int]bool{0: true}

	prompt := fixPrompt(t.TempDir(), items, decisions, applied)

	if strings.Contains(prompt, "a.go:10") {
		t.Error("an already-applied finding should not be sent to the agent again")
	}
	if !strings.Contains(prompt, "b.go:20") {
		t.Error("expected the outstanding finding to still be sent")
	}
}

func TestPendingFixes(t *testing.T) {
	items := make([]findings.Finding, 5)
	tests := []struct {
		name      string
		decisions map[int]decision
		applied   map[int]bool
		want      []int
	}{
		{
			name:      "only fix-marked findings",
			decisions: map[int]decision{0: decFix, 1: decApprove, 2: decSkip, 3: decPending, 4: decFix},
			want:      []int{0, 4},
		},
		{
			name:      "applied findings drop out",
			decisions: map[int]decision{0: decFix, 1: decFix, 2: decFix},
			applied:   map[int]bool{0: true, 2: true},
			want:      []int{1},
		},
		{
			name:      "nothing marked",
			decisions: map[int]decision{0: decSkip},
			want:      nil,
		},
		{
			name:      "everything already applied",
			decisions: map[int]decision{0: decFix, 1: decFix},
			applied:   map[int]bool{0: true, 1: true},
			want:      nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := pendingFixes(items, tt.decisions, tt.applied)
			if len(got) != len(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("got %v, want %v", got, tt.want)
				}
			}
		})
	}
}

// The advice after an apply depends on where the files actually changed: in a
// `run` they're in a throwaway worktree the pipeline commits, so pointing the
// user at `git diff` in their own tree would show them nothing.
func TestFixOutcome(t *testing.T) {
	tests := []struct {
		name       string
		n          int
		dest       Dest
		err        error
		wantFailed bool
		contains   []string
		absent     []string
	}{
		{
			name:     "worktree success sends nobody to git diff",
			n:        2,
			dest:     DestWorktree,
			contains: []string{"2 findings", "worktree", "push"},
			absent:   []string{"git diff"},
		},
		{
			name:     "working tree success asks for a commit",
			n:        1,
			dest:     DestWorkingTree,
			contains: []string{"1 finding", "git diff", "commit"},
			absent:   []string{"worktree"},
		},
		{
			name:       "cancel is reported as a stop, not a failure to fix",
			n:          1,
			err:        agent.ErrCanceled,
			wantFailed: true,
			contains:   []string{"canceled"},
		},
		{
			name:       "a real error surfaces its message",
			n:          1,
			err:        errors.New("agent exploded"),
			wantFailed: true,
			contains:   []string{"agent exploded"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, failed := fixOutcome(tt.n, tt.dest, tt.err)
			if failed != tt.wantFailed {
				t.Errorf("failed = %v, want %v", failed, tt.wantFailed)
			}
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("outcome %q should contain %q", got, want)
				}
			}
			for _, unwanted := range tt.absent {
				if strings.Contains(got, unwanted) {
					t.Errorf("outcome %q should not contain %q", got, unwanted)
				}
			}
		})
	}
}
