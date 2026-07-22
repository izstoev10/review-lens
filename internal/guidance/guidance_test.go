package guidance

import (
	"os"
	"path/filepath"
	"strings"
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

func TestStripFrontmatter(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"no frontmatter", "# Title\nbody", "# Title\nbody"},
		{"strips a block", "---\nname: code-review\ndescription: x\n---\n# Body\ntext", "# Body\ntext"},
		{"unterminated left intact", "---\nname: oops\n# Body", "---\nname: oops\n# Body"},
		{"mid-doc dashes are not frontmatter", "# Body\n\n---\n\nmore", "# Body\n\n---\n\nmore"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := strings.TrimSpace(stripFrontmatter(c.in)); got != strings.TrimSpace(c.want) {
				t.Errorf("stripFrontmatter(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

// Load must strip a SKILL.md's YAML frontmatter so it never reaches the prompt.
func TestLoadStripsSkillFrontmatter(t *testing.T) {
	dir := t.TempDir()
	rel := "skills/code-review/SKILL.md"
	if err := os.MkdirAll(filepath.Join(dir, "skills/code-review"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := "---\nname: code-review\ndescription: how to review\n---\n# Code review\n\nFlag real bugs only."
	if err := os.WriteFile(filepath.Join(dir, rel), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	got := Load(dir, rel)
	if strings.Contains(got, "name: code-review") {
		t.Errorf("frontmatter leaked into loaded guidance:\n%s", got)
	}
	if !strings.Contains(got, "Flag real bugs only.") {
		t.Errorf("expected body to survive, got:\n%s", got)
	}
}
