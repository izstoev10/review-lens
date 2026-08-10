package findings

import (
	"strings"
	"testing"
)

// TestParseActionDefaults verifies the action classification is parsed when
// present and fails closed to ask-user when missing or unrecognised.
func TestParseActionDefaults(t *testing.T) {
	raw := `[
	  {"severity":"error","file":"a.go","line":1,"title":"explicit auto-fix","detail":"d","action":"auto-fix"},
	  {"severity":"error","file":"b.go","line":2,"title":"explicit no-op","detail":"d","action":"no-op"},
	  {"severity":"warning","file":"c.go","line":3,"title":"unrecognised action","detail":"d","action":"delete-everything"},
	  {"severity":"info","file":"d.go","line":0,"title":"missing action","detail":"d"}
	]`

	list, ok := Parse(raw)
	if !ok {
		t.Fatal("Parse returned ok=false for valid JSON array")
	}
	if len(list) != 4 {
		t.Fatalf("got %d findings, want 4", len(list))
	}

	// Findings sort by severity (errors first); within a severity the input
	// order is stable, so index by title to stay robust.
	want := map[string]Action{
		"explicit auto-fix":   AutoFix,
		"explicit no-op":      NoOp,
		"unrecognised action": AskUser, // fail closed
		"missing action":      AskUser, // fail closed
	}
	for _, f := range list {
		if got := want[f.Title]; f.Action != got {
			t.Errorf("finding %q: got action %q, want %q", f.Title, f.Action, got)
		}
	}
}

// A malformed review response must come back bounded — a broken run can emit
// an entire transcript, and dumping it defeats the compact report.
func TestUnparsableBoundsLongOutput(t *testing.T) {
	raw := strings.TrimSpace(strings.Repeat("transport diagnostic line\n", 500))
	got := Unparsable(raw)

	if len(got) > 4000 {
		t.Errorf("excerpt is %d bytes — not bounded", len(got))
	}
	if !strings.Contains(got, "transport diagnostic line") {
		t.Error("excerpt should preserve the start of the output for diagnosis")
	}
	if !strings.Contains(got, "more lines omitted") {
		t.Error("excerpt should say that (and how much) output was cut")
	}
}

// A single line larger than the whole budget (typical for broken JSON) must
// still be clipped rather than passed through.
func TestUnparsableClipsOneGiantLine(t *testing.T) {
	got := Unparsable(strings.Repeat("x", 100_000))
	if len(got) > 4000 {
		t.Errorf("excerpt is %d bytes — not bounded", len(got))
	}
}

// Silence is its own failure mode and deserves a clear message, not an empty
// excerpt.
func TestUnparsableEmptyOutput(t *testing.T) {
	if got := Unparsable("  \n "); !strings.Contains(got, "no output") {
		t.Errorf("Unparsable(blank) = %q, want it to say the agent returned nothing", got)
	}
}

// Short prose passes through whole — the reader should see everything the
// agent said when it fits.
func TestUnparsableKeepsShortOutputWhole(t *testing.T) {
	raw := "I could not review this diff.\nThe repository failed to load."
	got := Unparsable(raw)
	for _, line := range strings.Split(raw, "\n") {
		if !strings.Contains(got, line) {
			t.Errorf("excerpt lost line %q", line)
		}
	}
	if strings.Contains(got, "omitted") {
		t.Error("nothing was cut, so nothing should be reported as omitted")
	}
}
