package signature

import (
	"strings"
	"testing"
)

func TestEnsure(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		wantChanged bool
		wantHas     bool // Marker present in the result
	}{
		{"empty body gets just the marker", "", true, true},
		{"plain body gets the marker appended", "Some PR description.", true, true},
		{"trailing newlines are collapsed before appending", "desc\n\n\n", true, true},
		{"already-signed body is untouched", "desc\n\n" + Marker, false, true},
		{"marker anywhere counts as present", Marker + "\nleading", false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, changed := Ensure(c.body)
			if changed != c.wantChanged {
				t.Errorf("changed = %v, want %v", changed, c.wantChanged)
			}
			if has := strings.Contains(got, Marker); has != c.wantHas {
				t.Errorf("result contains marker = %v, want %v (result=%q)", has, c.wantHas, got)
			}
		})
	}
}

// TestEnsureIdempotent verifies stamping twice yields the same body and no
// duplicate markers — the property the "update an existing PR" path relies on.
func TestEnsureIdempotent(t *testing.T) {
	once, _ := Ensure("original description")
	twice, changed := Ensure(once)
	if changed {
		t.Error("second Ensure reported a change; want idempotent no-op")
	}
	if twice != once {
		t.Errorf("second Ensure altered the body: %q != %q", twice, once)
	}
	if n := strings.Count(twice, Marker); n != 1 {
		t.Errorf("marker appears %d times, want exactly 1", n)
	}
}
