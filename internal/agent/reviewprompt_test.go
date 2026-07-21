package agent

import (
	"strings"
	"testing"

	"github.com/izstoev10/review-lens/internal/guidance"
)

// The JSON output contract must appear in the prompt regardless of guidance, so
// tuning the criteria can never change the structured findings format.
func TestReviewPromptKeepsFixedJSONContract(t *testing.T) {
	for _, g := range []string{"", "Only flag SQL injection.", guidance.Default} {
		p := ReviewPrompt(g, "diff --git a b")
		for _, must := range []string{
			`"severity": "error" | "warning" | "info"`,
			`"detail": "1-3 sentences: the concrete failure mode and why it matters"`,
			`respond with exactly: []`,
			"Diff:",
		} {
			if !strings.Contains(p, must) {
				t.Errorf("guidance %q: prompt missing fixed contract fragment %q", g, must)
			}
		}
	}
}

func TestReviewPromptEmbedsCustomGuidance(t *testing.T) {
	p := ReviewPrompt("Only flag SQL injection.", "the diff")
	if !strings.Contains(p, "Only flag SQL injection.") {
		t.Error("custom guidance not embedded in prompt")
	}
	if !strings.Contains(p, "the diff") {
		t.Error("diff not embedded in prompt")
	}
}

func TestReviewPromptEmptyGuidanceUsesDefault(t *testing.T) {
	p := ReviewPrompt("  ", "d")
	if !strings.Contains(p, "Severity rubric") {
		t.Error("empty guidance should fall back to the built-in default")
	}
}
