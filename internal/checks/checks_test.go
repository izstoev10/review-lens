package checks

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/izstoev10/review-lens/internal/config"
)

// A check with a working directory must run inside it — that's what lets a
// monorepo project validate itself with commands that only work from its root.
func TestRunHonoursWorkingDirectory(t *testing.T) {
	base := t.TempDir()
	if err := os.Mkdir(filepath.Join(base, "app"), 0o755); err != nil {
		t.Fatal(err)
	}

	r := Run(base, config.Check{Name: "where", Cmd: []string{"pwd"}, Dir: "app"})

	if !r.Passed {
		t.Fatalf("pwd failed: %s", r.Output)
	}
	if got := strings.TrimSpace(r.Output); filepath.Base(got) != "app" {
		t.Errorf("check ran in %q, want the app subdirectory", got)
	}
}

// Environment failures are configuration problems, not code defects — they
// carry a ConfigProblem so the pipeline shows them instead of dispatching the
// fixing agent.
func TestRunClassifiesEnvironmentFailures(t *testing.T) {
	base := t.TempDir()
	tests := []struct {
		name  string
		check config.Check
		want  string
	}{
		{
			// The invariant #59 pinned on this package: Run is total, so an
			// empty argv — however a caller built it — reports as
			// configuration, never a panic.
			name:  "empty command",
			check: config.Check{Name: "x"},
			want:  "no command",
		},
		{
			name:  "missing working directory",
			check: config.Check{Name: "x", Cmd: []string{"true"}, Dir: "gone"},
			want:  "working directory",
		},
		{
			name:  "missing executable",
			check: config.Check{Name: "x", Cmd: []string{"review-lens-no-such-binary"}},
			want:  "not found",
		},
		{
			name:  "npm missing script",
			check: config.Check{Name: "x", Cmd: []string{"sh", "-c", `echo 'npm error Missing script: "lint"'; exit 1`}},
			want:  "npm script",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := Run(base, tt.check)
			if r.Passed {
				t.Fatal("check unexpectedly passed")
			}
			if !strings.Contains(r.ConfigProblem, tt.want) {
				t.Errorf("ConfigProblem = %q, want it to mention %q", r.ConfigProblem, tt.want)
			}
		})
	}
}

// An ordinary failing command is a code problem: no ConfigProblem, so the
// agent loop stays in charge.
func TestRunLeavesCodeFailuresToTheAgent(t *testing.T) {
	r := Run(t.TempDir(), config.Check{Name: "x", Cmd: []string{"sh", "-c", "echo test exploded; exit 1"}})
	if r.Passed || r.ConfigProblem != "" {
		t.Errorf("got Passed=%v ConfigProblem=%q, want a plain failure", r.Passed, r.ConfigProblem)
	}
}
