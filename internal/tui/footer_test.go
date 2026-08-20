package tui

import (
	"strings"
	"testing"
)

// The footer is the session's steering wheel: it must offer only actions that
// currently apply, and once every marked fix has landed it must state the one
// next action instead of leaving the user to guess what quitting does.
func TestViewerFooter(t *testing.T) {
	tests := []struct {
		name                        string
		pending, undecided, applied int
		dest                        Dest
		hasAgent                    bool
		contains                    []string
		absent                      []string
	}{
		{
			name:    "marked fixes offer enter",
			pending: 2, dest: DestWorktree, hasAgent: true,
			contains: []string{"enter apply marked", "q quit"},
		},
		{
			name: "nothing applicable hides enter",
			dest: DestWorktree, hasAgent: true,
			contains: []string{"f mark fix"},
			absent:   []string{"enter apply marked"},
		},
		{
			name:    "all fixes applied on the run path: quitting is continuing",
			applied: 1, dest: DestWorktree, hasAgent: true,
			contains: []string{"✓ 1 fix applied", "press q to continue", "commit and push", "q continue"},
			absent:   []string{"q quit", "enter apply marked"},
		},
		{
			name:    "all fixes applied on the pr path: git diff, then commit",
			applied: 2, dest: DestWorkingTree, hasAgent: true,
			contains: []string{"✓ 2 fixes applied", "git diff", "q quit"},
			absent:   []string{"commit and push"},
		},
		{
			name:    "undecided ask-user findings outrank the completion note",
			applied: 1, undecided: 1, dest: DestWorktree, hasAgent: true,
			contains: []string{"decide 1 ask-user finding"},
			absent:   []string{"press q to continue"},
		},
		{
			name:     "no agent: navigation only",
			dest:     DestWorkingTree,
			contains: []string{"j/k move"},
			absent:   []string{"f mark fix", "enter"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := viewerFooter(tt.pending, tt.undecided, tt.applied, tt.dest, tt.hasAgent)
			for _, want := range tt.contains {
				if !strings.Contains(got, want) {
					t.Errorf("footer %q should contain %q", got, want)
				}
			}
			for _, unwanted := range tt.absent {
				if strings.Contains(got, unwanted) {
					t.Errorf("footer %q should not contain %q", got, unwanted)
				}
			}
		})
	}
}
