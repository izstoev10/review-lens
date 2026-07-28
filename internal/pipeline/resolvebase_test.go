package pipeline

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/izstoev10/review-lens/internal/config"
	"github.com/izstoev10/review-lens/internal/gitx"
)

// gitRepo builds a throwaway repo whose single branch is named `branch`, and
// returns a worktree pointing at it.
func gitRepo(t *testing.T, branch string) *gitx.Worktree {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run("init", "-q")
	run("config", "user.email", "t@t")
	run("config", "user.name", "t")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", "-A")
	run("commit", "-q", "-m", "init")
	run("branch", "-M", branch) // force the branch name deterministically
	return &gitx.Worktree{Path: dir}
}

func TestResolveBaseBranch(t *testing.T) {
	// Repo whose default branch is "master": a configured "main" (the default)
	// must fall back to master — the exact numan-api regression.
	master := gitRepo(t, "master")
	if got, err := resolveBaseBranch(master, config.Config{BaseBranch: "main", Remote: "origin"}); err != nil || got != "master" {
		t.Errorf("configured main → master fallback: got %q, err %v; want %q", got, err, "master")
	}
	// Unset base also resolves to master.
	if got, err := resolveBaseBranch(master, config.Config{Remote: "origin"}); err != nil || got != "master" {
		t.Errorf("unset base → master: got %q, err %v; want %q", got, err, "master")
	}

	// Repo whose default branch is "main".
	main := gitRepo(t, "main")
	if got, err := resolveBaseBranch(main, config.Config{Remote: "origin"}); err != nil || got != "main" {
		t.Errorf("default main: got %q, err %v; want %q", got, err, "main")
	}

	// A configured non-default base that exists is honoured.
	develop := gitRepo(t, "develop")
	if got, err := resolveBaseBranch(develop, config.Config{BaseBranch: "develop", Remote: "origin"}); err != nil || got != "develop" {
		t.Errorf("configured develop: got %q, err %v; want %q", got, err, "develop")
	}
	// But if neither the configured base nor main/master exist, it fails closed.
	if _, err := resolveBaseBranch(develop, config.Config{Remote: "origin"}); err == nil {
		t.Error("expected an error when no base branch resolves, got nil")
	}
}
