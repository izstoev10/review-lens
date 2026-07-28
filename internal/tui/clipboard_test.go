package tui

import (
	"strings"
	"testing"

	"github.com/izstoev10/review-lens/internal/findings"
)

func TestCopyPrompt(t *testing.T) {
	f := findings.Finding{
		Severity: findings.Warning,
		File:     "internal/foo/bar.go",
		Line:     42,
		Title:    "returns an incomplete list",
		Detail:   "The diabetes branch is skipped when another comorbidity is present.",
	}
	got := copyPrompt(f)
	for _, want := range []string{"internal/foo/bar.go:42", "warning", "returns an incomplete list", "diabetes branch"} {
		if !strings.Contains(got, want) {
			t.Errorf("copyPrompt() missing %q in:\n%s", want, got)
		}
	}

	// A finding with no line number should reference the file without a ":line".
	noLine := copyPrompt(findings.Finding{Severity: findings.Info, File: "README.md", Title: "typo"})
	if !strings.Contains(noLine, "README.md") {
		t.Errorf("expected the file path, got:\n%s", noLine)
	}
	if strings.Contains(noLine, "README.md:0") {
		t.Errorf("expected no ':0' line suffix when line is 0, got:\n%s", noLine)
	}
}

func TestClipboardCommand(t *testing.T) {
	if name, _, ok := clipboardCommand("darwin"); !ok || name != "pbcopy" {
		t.Errorf("darwin: got (%q, %v), want pbcopy", name, ok)
	}
	if name, _, ok := clipboardCommand("windows"); !ok || name != "clip" {
		t.Errorf("windows: got (%q, %v), want clip", name, ok)
	}
}
