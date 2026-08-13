package gh

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// recorder is a fake Exec that captures every argv and plays back canned
// stdout per command prefix.
type recorder struct {
	calls   [][]string
	replies map[string]string // matched against the joined argv prefix
	fail    map[string]string // prefix → error text
}

func (r *recorder) exec(args ...string) ([]byte, error) {
	r.calls = append(r.calls, args)
	joined := strings.Join(args, " ")
	for prefix, msg := range r.fail {
		if strings.HasPrefix(joined, prefix) {
			return nil, errors.New(msg)
		}
	}
	for prefix, out := range r.replies {
		if strings.HasPrefix(joined, prefix) {
			return []byte(out), nil
		}
	}
	return nil, nil
}

func (r *recorder) argv(i int) string { return strings.Join(r.calls[i], " ") }

// The empty-number convention — "" means the current branch's PR — is
// implemented once, in the client, for every question that accepts a number.
func TestEmptyNumberMeansCurrentBranch(t *testing.T) {
	r := &recorder{replies: map[string]string{"pr view": `{}`, "pr diff": ""}}
	c := Client{Exec: r.exec}

	_, _ = c.Diff("")
	_, _ = c.Diff("12")
	_, _ = c.View("")
	_, _ = c.CheckRollup("7")

	want := []string{
		"pr diff",
		"pr diff 12",
		"pr view --json number,title,body,headRefName",
		"pr view 7 --json statusCheckRollup",
	}
	for i, w := range want {
		if r.argv(i) != w {
			t.Errorf("call %d = %q, want %q", i, r.argv(i), w)
		}
	}
}

// Edit sends only the fields that differ, and nothing at all when neither
// does — the "already up to date" answer belongs to the client, not to a
// positional arg count at the call site.
func TestEditSendsOnlyChangedFields(t *testing.T) {
	pr := PR{Number: 7, Title: "old title", Body: "old body"}

	t.Run("nothing changed, nothing sent", func(t *testing.T) {
		r := &recorder{}
		changed, err := Client{Exec: r.exec}.Edit(pr, "old title", "old body")
		if err != nil || changed {
			t.Errorf("changed=%v err=%v, want a silent no-op", changed, err)
		}
		if len(r.calls) != 0 {
			t.Errorf("gh was invoked %d times for a no-op edit", len(r.calls))
		}
	})

	t.Run("only the body changed", func(t *testing.T) {
		r := &recorder{}
		changed, err := Client{Exec: r.exec}.Edit(pr, "old title", "new body")
		if err != nil || !changed {
			t.Fatalf("changed=%v err=%v, want a real edit", changed, err)
		}
		got := r.argv(0)
		if !strings.Contains(got, "--body new body") || strings.Contains(got, "--title") {
			t.Errorf("argv = %q, want --body only", got)
		}
		if !strings.HasPrefix(got, "pr edit 7") {
			t.Errorf("argv = %q, want it addressed to PR 7", got)
		}
	})
}

func TestOpenForReportsNoPR(t *testing.T) {
	r := &recorder{replies: map[string]string{"pr list": `[]`}}
	_, err := Client{Exec: r.exec}.OpenFor("feat/x")
	if err == nil || !strings.Contains(err.Error(), `"feat/x"`) {
		t.Errorf("err = %v, want a no-open-PR error naming the branch", err)
	}
	if got := r.argv(0); !strings.Contains(got, "--head feat/x") {
		t.Errorf("argv = %q, want an explicit --head (detached worktrees can't infer a branch)", got)
	}
}

// The real adapter's stream discipline: stdout is the result, stderr is
// diagnostics. A gh warning must never end up inside a diff — that diff goes
// verbatim into the review prompt.
func TestRealBinaryKeepsStderrOutOfResults(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		`echo "! warning: some gh notice" >&2` + "\n" +
		`echo "diff --git a/x b/x"` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	got, err := Client{Dir: dir}.Diff("")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "warning") {
		t.Errorf("diff = %q — gh's stderr leaked into the result", got)
	}
	if !strings.Contains(got, "diff --git") {
		t.Errorf("diff = %q, want the stdout payload", got)
	}
}

// On failure, stderr is exactly where the diagnostics belong: in the error.
func TestRealBinaryFailureCarriesStderr(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\necho 'no pull requests found' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	_, err := Client{Dir: dir}.Diff("")
	if err == nil || !strings.Contains(err.Error(), "no pull requests found") {
		t.Errorf("err = %v, want it to carry gh's stderr", err)
	}
}
