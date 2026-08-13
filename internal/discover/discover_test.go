package discover

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRepo fabricates a Repo without git: files are both "tracked" and
// readable, installed names resolve. This is the seam Discover was built
// around — layout and toolchain are plain data.
func fakeRepo(t *testing.T, files map[string]string, installed ...string) Repo {
	t.Helper()
	root := t.TempDir()
	var tracked []string
	for rel, content := range files {
		tracked = append(tracked, rel)
		full := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Repo{
		Root:    root,
		Tracked: tracked,
		LookPath: func(name string) (string, error) {
			for _, bin := range installed {
				if bin == name {
					return "/bin/" + name, nil
				}
			}
			return "", fmt.Errorf("%s not installed", name)
		},
	}
}

// A plain CI step that repeats an adapter's command must not shadow it: the
// deduped survivor keeps the adapter's confidence, or a non-interactive run
// would reject a gate it should have accepted.
func TestDedupePrefersHighConfidenceDuplicates(t *testing.T) {
	r := fakeRepo(t, map[string]string{
		"go.mod": "module m\n",
		".github/workflows/ci.yml": strings.Join([]string{
			"jobs:",
			"  test:",
			"    steps:",
			"      - run: go test ./...",
		}, "\n"),
	}, "go")

	proposals, _ := Discover(r)

	var testProposal *Proposal
	for i, p := range proposals {
		if strings.Join(p.Check.Cmd, " ") == "go test ./..." {
			if testProposal != nil {
				t.Fatal("the duplicated command survived twice")
			}
			testProposal = &proposals[i]
		}
	}
	if testProposal == nil {
		t.Fatal("go test ./... was not proposed at all")
	}
	if !testProposal.HighConfidence {
		t.Error("the CI step shadowed the adapter's high-confidence proposal")
	}
}

// A Makefile-only repo (no CI workflow to reveal the gate) still discovers its
// own gate target, preferring the canonical names in order.
func TestMakefileFallback(t *testing.T) {
	tests := []struct {
		name     string
		makefile string
		want     string
	}{
		{"ci wins over test", "ci: test\n\ttrue\ntest:\n\ttrue\n", "make ci"},
		{"check next", "check:\n\ttrue\ntest:\n\ttrue\n", "make check"},
		{"test as last resort", "build:\n\ttrue\ntest:\n\ttrue\n", "make test"},
		{"no gate target", "build:\n\ttrue\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := fakeRepo(t, map[string]string{"Makefile": tt.makefile}, "make")
			proposals, _ := Discover(r)

			var cmds []string
			for _, p := range proposals {
				cmds = append(cmds, strings.Join(p.Check.Cmd, " "))
			}
			got := strings.Join(cmds, ", ")
			if got != tt.want {
				t.Errorf("proposals = %q, want %q", got, tt.want)
			}
			if tt.want != "" && !proposals[0].HighConfidence {
				t.Error("a declared make target with make installed should be high confidence")
			}
		})
	}
}

// The plain-step filter decides what a workflow run line may become: one
// simple argv, or nothing.
func TestPlainStep(t *testing.T) {
	tests := []struct {
		cmd  string
		want bool
	}{
		{"go test ./...", true},
		{"make ci", true},
		{"", false},
		{"|", false}, // block scalar marker
		{"npm test -- --coverage=${{ x }}", false}, // templating
		{"cd app && npm test", false},              // shell operators
		{"echo $HOME", false},
		{"go test | tee log", false},
	}
	for _, tt := range tests {
		if got := plainStep(tt.cmd); got != tt.want {
			t.Errorf("plainStep(%q) = %v, want %v", tt.cmd, got, tt.want)
		}
	}
}

// repoOwnedGate recognises commands the repository itself provides — and only
// those.
func TestRepoOwnedGate(t *testing.T) {
	r := fakeRepo(t, map[string]string{
		"Makefile":      "ci:\n\ttrue\n",
		"scripts/check": "#!/bin/sh\n",
	}, "make")

	tests := []struct {
		cmd   string
		owned bool
	}{
		{"make ci", true},
		{"make deploy", false}, // target not declared
		{"./scripts/check", true},
		{"scripts/check", true},
		{"bash scripts/check", true},
		{"scripts/missing", false},
		{"npm test", false}, // a tool, not a repo-owned command
	}
	for _, tt := range tests {
		if _, got := repoOwnedGate(r, tt.cmd, "wf.yml"); got != tt.owned {
			t.Errorf("repoOwnedGate(%q) = %v, want %v", tt.cmd, got, tt.owned)
		}
	}
}

// Sanity: the composed walk still targets the right project directories.
func TestDiscoverKeepsProjectDirectories(t *testing.T) {
	r := fakeRepo(t, map[string]string{"server/mix.exs": "x"}, "mix")
	proposals, _ := Discover(r)
	if len(proposals) == 0 {
		t.Fatal("expected proposals for the nested project")
	}
	for _, p := range proposals {
		if p.Check.Dir != "server" {
			t.Errorf("proposal %q has dir %q, want %q", p.Check.Name, p.Check.Dir, "server")
		}
	}
}
