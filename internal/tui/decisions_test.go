package tui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/izstoev10/review-lens/internal/findings"
)

func TestDefaultDecisions(t *testing.T) {
	items := []findings.Finding{
		{Action: findings.AutoFix},
		{Action: findings.AskUser},
		{Action: findings.NoOp},
		{Action: ""}, // unknown -> treated as pending
	}
	got := defaultDecisions(items)
	want := map[int]decision{0: decFix, 1: decPending, 2: decSkip, 3: decPending}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("finding %d: got decision %d, want %d", i, got[i], w)
		}
	}
}

func TestFixPromptIncludesOnlyFixMarkedAndConventions(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte("Use tabs. Prefer table-driven tests."), 0o644); err != nil {
		t.Fatal(err)
	}
	items := []findings.Finding{
		{File: "a.go", Line: 10, Title: "fix me", Detail: "bug", Action: findings.AutoFix},
		{File: "b.go", Line: 20, Title: "leave me", Detail: "style", Action: findings.AskUser},
	}
	decisions := map[int]decision{0: decFix, 1: decApprove}

	prompt := fixPrompt(dir, items, decisions)

	if !strings.Contains(prompt, "table-driven tests") {
		t.Error("expected repo conventions (AGENTS.md) to be included in the fix prompt")
	}
	if !strings.Contains(prompt, "a.go:10") {
		t.Error("expected the fix-marked finding to be included")
	}
	if strings.Contains(prompt, "b.go:20") {
		t.Error("approved finding should NOT be in the fix prompt")
	}
}
