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

// publishFixture builds a repo on main with a bare origin, plus a worktree
// holding one commit main doesn't have — the exact shape of a `run` that
// committed fixes and is about to push.
func publishFixture(t *testing.T) (root, remote string, wt *Worktree) {
	t.Helper()
	root, remote = t.TempDir(), t.TempDir()
	git := func(dir string, args ...string) string {
		t.Helper()
		out, err := run(dir, args...)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	git(remote, "init", "--bare", "-q")
	git(root, "init", "-q")
	git(root, "config", "user.email", "t@t")
	git(root, "config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(root, "add", "-A")
	git(root, "commit", "-q", "-m", "base")
	git(root, "branch", "-M", "main")
	git(root, "remote", "add", "origin", remote)

	wt, err := AddWorktree(root, "main")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = wt.Remove() })
	if err := os.WriteFile(filepath.Join(wt.Path, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := wt.CommitAll("fixes"); err != nil {
		t.Fatal(err)
	}
	return root, remote, wt
}

func branchSHA(t *testing.T, dir, branch string) string {
	t.Helper()
	sha, err := run(dir, "rev-parse", "--verify", "refs/heads/"+branch)
	if err != nil {
		t.Fatal(err)
	}
	return sha
}

// The defect behind issue #42: fix commits used to be reachable only from the
// remote once the worktree died. Publish must land them in the user's clean
// checkout — ref AND working tree.
func TestPublishFastForwardsACleanCheckout(t *testing.T) {
	root, remote, wt := publishFixture(t)

	advanced, reason, err := wt.Publish("origin", "main")
	if err != nil {
		t.Fatal(err)
	}
	if !advanced || reason != "" {
		t.Fatalf("advanced=%v reason=%q, want a clean fast-forward", advanced, reason)
	}
	pushed, err := run(wt.Path, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if branchSHA(t, root, "main") != pushed {
		t.Error("local main does not point at the pushed commit")
	}
	if branchSHA(t, remote, "main") != pushed {
		t.Error("the remote never got the commit")
	}
	data, err := os.ReadFile(filepath.Join(root, "a.txt"))
	if err != nil || string(data) != "two\n" {
		t.Errorf("working tree = %q, want the fixed content", data)
	}
}

// Local edits that overlap the pushed change must survive untouched: git
// refuses the fast-forward, the push still lands, and the reason is reported.
func TestPublishLeavesADirtyOverlappingCheckoutAlone(t *testing.T) {
	root, remote, wt := publishFixture(t)
	old := branchSHA(t, root, "main")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("precious local edit\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	advanced, reason, err := wt.Publish("origin", "main")
	if err != nil {
		t.Fatal(err)
	}
	if advanced || reason == "" {
		t.Fatalf("advanced=%v reason=%q, want a refusal with a reason", advanced, reason)
	}
	if branchSHA(t, root, "main") != old {
		t.Error("local main moved despite the dirty checkout")
	}
	if data, _ := os.ReadFile(filepath.Join(root, "a.txt")); string(data) != "precious local edit\n" {
		t.Errorf("the user's edit was clobbered: %q", data)
	}
	if branchSHA(t, remote, "main") == old {
		t.Error("the push itself should still have happened")
	}
}

// A branch the user has switched away from advances by ref only — their
// current checkout stays exactly as it is.
func TestPublishAdvancesAnUncheckedOutBranch(t *testing.T) {
	root, _, wt := publishFixture(t)
	if _, err := run(root, "switch", "-q", "-c", "elsewhere"); err != nil {
		t.Fatal(err)
	}

	advanced, reason, err := wt.Publish("origin", "main")
	if err != nil {
		t.Fatal(err)
	}
	if !advanced || reason != "" {
		t.Fatalf("advanced=%v reason=%q, want the ref updated", advanced, reason)
	}
	pushed, err := run(wt.Path, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if branchSHA(t, root, "main") != pushed {
		t.Error("main's ref was not advanced")
	}
	if cur, _ := run(root, "symbolic-ref", "-q", "HEAD"); cur != "refs/heads/elsewhere" {
		t.Errorf("the user's checkout moved to %q", cur)
	}
}

// A local branch that gained its own commit after the worktree was created has
// diverged: never rewind or sidestep the user's work.
func TestPublishRefusesADivergedBranch(t *testing.T) {
	root, _, wt := publishFixture(t)
	if err := os.WriteFile(filepath.Join(root, "b.txt"), []byte("their work\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := run(root, "add", "-A"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(root, "commit", "-q", "-m", "their commit"); err != nil {
		t.Fatal(err)
	}
	theirs := branchSHA(t, root, "main")

	advanced, reason, err := wt.Publish("origin", "main")
	if err != nil {
		t.Fatal(err)
	}
	if advanced || !strings.Contains(reason, "diverged") {
		t.Fatalf("advanced=%v reason=%q, want a diverged refusal", advanced, reason)
	}
	if branchSHA(t, root, "main") != theirs {
		t.Error("the user's commit was rewound")
	}
}

// A worktree with nothing new reports neither an advance nor a warning.
func TestPublishIsQuietWhenAlreadyCurrent(t *testing.T) {
	root, _, _ := publishFixture(t)
	fresh, err := AddWorktree(root, "main") // no extra commit this time
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = fresh.Remove() })

	advanced, reason, err := fresh.Publish("origin", "main")
	if err != nil {
		t.Fatal(err)
	}
	if advanced || reason != "" {
		t.Errorf("advanced=%v reason=%q, want a silent no-op", advanced, reason)
	}
}
