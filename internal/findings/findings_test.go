package findings

import "testing"

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
		"explicit auto-fix":    AutoFix,
		"explicit no-op":       NoOp,
		"unrecognised action":  AskUser, // fail closed
		"missing action":       AskUser, // fail closed
	}
	for _, f := range list {
		if got := want[f.Title]; f.Action != got {
			t.Errorf("finding %q: got action %q, want %q", f.Title, f.Action, got)
		}
	}
}
