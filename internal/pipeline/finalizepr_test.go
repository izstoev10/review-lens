package pipeline

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/izstoev10/review-lens/internal/config"
	"github.com/izstoev10/review-lens/internal/gh"
	"github.com/izstoev10/review-lens/internal/gitx"
	"github.com/izstoev10/review-lens/internal/signature"
)

// fakePR wires a gh.Client whose only PR is pr, recording any edit.
func fakePR(t *testing.T, pr gh.PR) (gh.Client, *[][]string) {
	t.Helper()
	listing, err := json.Marshal([]gh.PR{pr})
	if err != nil {
		t.Fatal(err)
	}
	var edits [][]string
	c := gh.Client{Exec: func(args ...string) ([]byte, error) {
		switch args[1] {
		case "list":
			return listing, nil
		case "edit":
			edits = append(edits, args)
			return nil, nil
		}
		t.Fatalf("unexpected gh call: %v", args)
		return nil, nil
	}}
	return c, &edits
}

// Re-running against an existing PR back-fills the gate signature and nothing
// else — a human's edited title and body must survive.
func TestFinalizePRBackfillsOnlyTheSignature(t *testing.T) {
	c, edits := fakePR(t, gh.PR{Number: 7, Title: "human title", Body: "human body"})
	wt := &gitx.Worktree{Path: t.TempDir()}

	var log bytes.Buffer
	if err := finalizePR(c, wt, config.Config{}, "feat/x", false, &log); err != nil {
		t.Fatal(err)
	}

	if len(*edits) != 1 {
		t.Fatalf("gh pr edit invoked %d times, want 1", len(*edits))
	}
	argv := strings.Join((*edits)[0], " ")
	if strings.Contains(argv, "--title") {
		t.Errorf("edit = %q — a human's title must not be rewritten on a re-run", argv)
	}
	if !strings.Contains(argv, "human body") || !strings.Contains(argv, signature.Marker) {
		t.Errorf("edit = %q, want the original body with the signature appended", argv)
	}
}

// An already-signed PR needs no edit at all.
func TestFinalizePRAlreadySignedIsANoOp(t *testing.T) {
	body, _ := signature.Ensure("done")
	c, edits := fakePR(t, gh.PR{Number: 7, Title: "t", Body: body})
	wt := &gitx.Worktree{Path: t.TempDir()}

	var log bytes.Buffer
	if err := finalizePR(c, wt, config.Config{}, "feat/x", false, &log); err != nil {
		t.Fatal(err)
	}
	if len(*edits) != 0 {
		t.Errorf("gh pr edit invoked for an up-to-date PR: %v", *edits)
	}
	if !strings.Contains(log.String(), "already up to date") {
		t.Errorf("log = %q, want the up-to-date notice", log.String())
	}
}
