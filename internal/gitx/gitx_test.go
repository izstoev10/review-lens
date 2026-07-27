package gitx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestDiffSinceAmbiguousBasePath guards the regression where a base ref that
// also names a path (e.g. a "base" branch next to a "base/" directory) made
// `git diff --merge-base base HEAD` bail with "ambiguous argument". The trailing
// "--" in DiffSince/ChangedFiles fixes it.
func TestDiffSinceAmbiguousBasePath(t *testing.T) {
	dir := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		if _, err := run(dir, args...); err != nil {
			t.Fatalf("git %s: %v", strings.Join(args, " "), err)
		}
	}
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	git("init", "-q")
	git("config", "user.email", "t@t")
	git("config", "user.name", "t")
	write("a.txt", "one\n")
	git("add", "-A")
	git("commit", "-q", "-m", "init")
	git("branch", "base") // base points at the initial commit

	// The collision: a path named exactly like the base ref, plus a real change.
	write("base/keep.txt", "x\n")
	write("a.txt", "two\n")
	git("add", "-A")
	git("commit", "-q", "-m", "feature")

	w := &Worktree{Path: dir}

	diff, err := w.DiffSince("base")
	if err != nil {
		t.Fatalf("DiffSince returned error (ambiguity not handled): %v", err)
	}
	if !strings.Contains(diff, "a.txt") {
		t.Errorf("diff should mention the changed file; got:\n%s", diff)
	}

	files, err := w.ChangedFiles("base")
	if err != nil {
		t.Fatalf("ChangedFiles returned error: %v", err)
	}
	var sawA bool
	for _, f := range files {
		if f == "a.txt" {
			sawA = true
		}
	}
	if !sawA {
		t.Errorf("ChangedFiles should include a.txt; got %v", files)
	}
}
