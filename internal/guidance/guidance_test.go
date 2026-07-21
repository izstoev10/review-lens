package guidance

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileFallsBackToDefault(t *testing.T) {
	// An empty dir has no guidance file, so Load must return the built-in default.
	if got := Load(t.TempDir(), ""); got != Default {
		t.Fatalf("missing file: got custom guidance, want built-in default")
	}
}

func TestLoadEmptyFileFallsBackToDefault(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, DefaultPath), []byte("   \n\t"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Load(dir, ""); got != Default {
		t.Fatalf("blank file: got %q, want built-in default", got)
	}
}

func TestLoadUsesFileWhenPresent(t *testing.T) {
	dir := t.TempDir()
	want := "# Custom\nFlag only security issues."
	if err := os.WriteFile(filepath.Join(dir, DefaultPath), []byte(want+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Load(dir, ""); got != want {
		t.Fatalf("got %q, want %q (trimmed file contents)", got, want)
	}
}

func TestLoadHonoursCustomRelPath(t *testing.T) {
	dir := t.TempDir()
	want := "custom-location guidance"
	if err := os.WriteFile(filepath.Join(dir, "docs-review.md"), []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Load(dir, "docs-review.md"); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}
