// Package gitx wraps the git commands gate needs.
//
// Design choice: we shell out to the real `git` binary via os/exec instead of
// using a pure-Go git library. For a workflow tool this is the right call —
// it behaves exactly like your terminal git (same config, hooks, credentials)
// and there's nothing to keep in sync with git's own behaviour.
package gitx

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// run executes git in dir and returns trimmed stdout. Stderr is folded into the
// error so failures are legible.
func run(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %w\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// RepoRoot returns the top-level directory of the repo containing dir.
func RepoRoot(dir string) (string, error) {
	return run(dir, "rev-parse", "--show-toplevel")
}

// CurrentBranch returns the checked-out branch name in dir.
func CurrentBranch(dir string) (string, error) {
	return run(dir, "rev-parse", "--abbrev-ref", "HEAD")
}

// Worktree is a disposable git worktree. Gate runs all checks inside one so the
// developer's real working directory is never touched — this isolation is the
// core idea of the tool.
type Worktree struct {
	Path   string // filesystem path of the worktree
	Branch string // the source branch it was created from
	repo   string // path of the originating repo (for cleanup)
}

// AddWorktree creates a throwaway worktree at a temp path, checked out to the
// same commit as `branch`. It detaches HEAD so the worktree doesn't "claim" the
// branch (git forbids the same branch being checked out in two worktrees).
func AddWorktree(repoRoot, branch string) (*Worktree, error) {
	dir, err := os.MkdirTemp("", "gate-worktree-*")
	if err != nil {
		return nil, err
	}
	// --detach: check out the commit, not the branch ref.
	// --force: tolerate the temp dir already existing.
	if _, err := run(repoRoot, "worktree", "add", "--detach", "--force", dir, branch); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	return &Worktree{Path: dir, Branch: branch, repo: repoRoot}, nil
}

// Remove tears the worktree down: git forgets it, then the temp dir is deleted.
// Safe to defer immediately after AddWorktree.
func (w *Worktree) Remove() error {
	if w == nil {
		return nil
	}
	_, gitErr := run(w.repo, "worktree", "remove", "--force", w.Path)
	fsErr := os.RemoveAll(w.Path)
	if gitErr != nil {
		return gitErr
	}
	return fsErr
}

// HasChanges reports whether the worktree has any uncommitted changes — i.e.
// whether an agent's fix actually modified files.
func (w *Worktree) HasChanges() (bool, error) {
	out, err := run(w.Path, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	return out != "", nil
}

// Diff returns the unstaged+staged diff in the worktree. Useful as context for
// the agent.
func (w *Worktree) Diff() (string, error) {
	return run(w.Path, "diff", "HEAD")
}

// RefExists reports whether ref (e.g. "main" or "origin/main") resolves in the
// worktree. Used to decide whether a review diff is possible.
func (w *Worktree) RefExists(ref string) bool {
	_, err := run(w.Path, "rev-parse", "--verify", "--quiet", ref)
	return err == nil
}

// DiffSince returns the diff of this branch's HEAD against its merge-base with
// base — i.e. exactly the changes this branch introduces on top of base. This
// is what a reviewer wants to look at, not unrelated commits already on base.
func (w *Worktree) DiffSince(base string) (string, error) {
	return run(w.Path, "diff", "--merge-base", base, "HEAD")
}

// ChangedFiles returns the paths this branch changed versus its merge-base with
// base, restricted to files that still exist in the worktree (so deleted files
// aren't handed to a linter). Used to scope checks/fixes to the diff instead of
// the whole repo — essential on large monorepos.
func (w *Worktree) ChangedFiles(base string) ([]string, error) {
	out, err := run(w.Path, "diff", "--name-only", "--merge-base", base, "HEAD")
	if err != nil {
		return nil, err
	}
	var files []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, statErr := os.Stat(filepath.Join(w.Path, line)); statErr == nil {
			files = append(files, line)
		}
	}
	return files, nil
}

// CommitAll stages everything and commits with msg. Returns the new commit SHA.
func (w *Worktree) CommitAll(msg string) (string, error) {
	if _, err := run(w.Path, "add", "-A"); err != nil {
		return "", err
	}
	if _, err := run(w.Path, "commit", "-m", msg); err != nil {
		return "", err
	}
	return run(w.Path, "rev-parse", "HEAD")
}

// Push pushes the worktree's current HEAD to remote as `branch`, setting it as
// the upstream. Runs from the worktree so it pushes the (possibly fixed) commit.
// The destination is fully qualified (refs/heads/…) so it works even when the
// branch doesn't yet exist on the remote.
func (w *Worktree) Push(remote, branch string) error {
	_, err := run(w.Path, "push", "--force-with-lease", "-u", remote, "HEAD:refs/heads/"+branch)
	return err
}
