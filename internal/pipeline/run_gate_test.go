package pipeline

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/izstoev10/review-lens/internal/config"
)

// pushableRepo builds a repo on branch feat/x (one commit ahead of main) whose
// origin is a local bare repo — so a run can genuinely push, and the test can
// ask the remote whether it did.
func pushableRepo(t *testing.T) (root, remote string) {
	t.Helper()
	root, remote = t.TempDir(), t.TempDir()
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
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
	git(root, "switch", "-q", "-c", "feat/x")
	if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	git(root, "add", "-A")
	git(root, "commit", "-q", "-m", "change")
	return root, remote
}

// remoteHasBranch asks the bare remote whether the branch ever arrived.
func remoteHasBranch(t *testing.T, remote, branch string) bool {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--verify", "-q", "refs/heads/"+branch)
	cmd.Dir = remote
	return cmd.Run() == nil
}

// reviewingConfig gates with one real (trivially green) check and a fake
// review agent that prints the given findings JSON on stdout.
func reviewingConfig(findingsJSON string) config.Config {
	cfg := config.Default()
	cfg.Checks = []config.Check{{Name: "ok", Cmd: []string{"true"}}}
	cfg.Agent = &config.Agent{Cmd: []string{"sh", "-c", "echo '" + findingsJSON + "'"}}
	cfg.Review = true
	cfg.OpenPR = false
	cfg.BaseBranch = "main"
	return cfg
}

// The run decision boundary, end to end: a review that raises an ask-user
// finding must stop an unattended run before the push — judgement calls can't
// be resolved without a human — while a review with none preserves the
// advisory flow and pushes.
func TestRunStopsBeforePushOnUnresolvedAskUser(t *testing.T) {
	root, remote := pushableRepo(t)
	cfg := reviewingConfig(`[{"severity":"warning","file":"a.txt","line":1,"title":"needs judgement","detail":"d","action":"ask-user"}]`)

	var log bytes.Buffer
	err := Run(root, cfg, false, &log)

	if err == nil {
		t.Fatal("run pushed despite an unresolved ask-user finding")
	}
	for _, want := range []string{"needs judgement", "review-lens run"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to contain %q", err, want)
		}
	}
	if remoteHasBranch(t, remote, "feat/x") {
		t.Error("the branch reached the remote — the gate ran after the push")
	}
	// The compact findings summary must still have been printed.
	if !strings.Contains(log.String(), "needs judgement") {
		t.Errorf("log should carry the findings summary; got:\n%s", log.String())
	}
}

// The #37 gate bypass: a run on the base branch used to skip the review,
// fall through to a force-with-lease push of the base, and report success.
// It must refuse up front — before the worktree and checks — and nothing may
// reach the remote.
func TestRunRefusesTheBaseBranch(t *testing.T) {
	root, remote := pushableRepo(t)
	git := exec.Command("git", "switch", "-q", "main")
	git.Dir = root
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git switch: %v\n%s", err, out)
	}
	cfg := reviewingConfig(`[]`)

	var log bytes.Buffer
	err := Run(root, cfg, false, &log)

	if err == nil || !strings.Contains(err.Error(), "base branch") {
		t.Fatalf("err = %v, want a base-branch refusal", err)
	}
	if !strings.Contains(err.Error(), "feature branch") {
		t.Errorf("err = %v, want it to tell the user to create a branch", err)
	}
	if strings.Contains(log.String(), "isolated worktree") {
		t.Error("a worktree was created before the base-branch refusal")
	}
	if remoteHasBranch(t, remote, "main") {
		t.Error("the base branch reached the remote despite the refusal")
	}
}

// The name guard can miss (it compares names, not refs): with a configured
// base that doesn't exist, resolution can still land on the current branch.
// That skipped review must fail closed instead of falling through to a push.
func TestRunFailsClosedWhenResolutionLandsOnTheCurrentBranch(t *testing.T) {
	root, remote := pushableRepo(t)
	git := exec.Command("git", "switch", "-q", "main")
	git.Dir = root
	if out, err := git.CombinedOutput(); err != nil {
		t.Fatalf("git switch: %v\n%s", err, out)
	}
	cfg := reviewingConfig(`[]`)
	cfg.BaseBranch = "develop" // doesn't exist; resolution falls back to main

	var log bytes.Buffer
	err := Run(root, cfg, false, &log)

	if err == nil || !strings.Contains(err.Error(), "nothing to review") {
		t.Fatalf("err = %v, want the fail-closed skipped-review error", err)
	}
	if remoteHasBranch(t, remote, "main") {
		t.Error("the base branch reached the remote despite the skipped review")
	}
}

func TestOnBaseBranch(t *testing.T) {
	tests := []struct {
		name, branch, configured string
		want                     bool
	}{
		{"configured base matches", "main", "main", true},
		{"feature branch passes", "feat/x", "main", false},
		{"only the configured name counts when set", "master", "develop", false},
		{"unset base falls back to main", "main", "", true},
		{"unset base falls back to master", "master", "", true},
		{"unset base still passes features", "feat/x", "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := config.Config{BaseBranch: tt.configured}
			if got := onBaseBranch(tt.branch, cfg); got != tt.want {
				t.Errorf("onBaseBranch(%q, base=%q) = %v, want %v", tt.branch, tt.configured, got, tt.want)
			}
		})
	}
}

func TestRunPushesWhenNoAskUserFindings(t *testing.T) {
	root, remote := pushableRepo(t)
	cfg := reviewingConfig(`[{"severity":"info","file":"a.txt","line":1,"title":"fyi","detail":"d","action":"no-op"}]`)

	var log bytes.Buffer
	if err := Run(root, cfg, false, &log); err != nil {
		t.Fatalf("run failed: %v\n%s", err, log.String())
	}
	if !remoteHasBranch(t, remote, "feat/x") {
		t.Error("a review with no ask-user findings should have pushed")
	}
}
