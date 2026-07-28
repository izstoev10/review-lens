package pipeline

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJiraKeyFromBranch(t *testing.T) {
	cases := map[string]string{
		"feat/oa-2576-add-rate-limiter": "OA-2576",
		"OA-2576":                       "OA-2576",
		"bugfix/PROJ-1":                 "PROJ-1",
		"main":                          "",
		"feat/no-ticket-here":           "",         // no digits → not a key
		"feat/issue-15-require-gate":    "ISSUE-15", // structural match; gated by jiraBaseURL at the call site
	}
	for branch, want := range cases {
		if got := jiraKeyFromBranch(branch); got != want {
			t.Errorf("jiraKeyFromBranch(%q) = %q, want %q", branch, got, want)
		}
	}
}

func TestJiraURL(t *testing.T) {
	want := "https://acme.atlassian.net/browse/OA-2576"
	for _, base := range []string{"https://acme.atlassian.net/browse/", "https://acme.atlassian.net/browse"} {
		if got := jiraURL(base, "OA-2576"); got != want {
			t.Errorf("jiraURL(%q) = %q, want %q", base, got, want)
		}
	}
}

func TestWithJiraTitlePrefix(t *testing.T) {
	if got := withJiraTitlePrefix("feat: add limiter", "OA-2576"); got != "[OA-2576] feat: add limiter" {
		t.Errorf("got %q", got)
	}
	// Idempotent: don't double-prefix when the key is already present.
	already := "[OA-2576] feat: add limiter"
	if got := withJiraTitlePrefix(already, "OA-2576"); got != already {
		t.Errorf("expected unchanged, got %q", got)
	}
}

func TestWithJiraRef(t *testing.T) {
	url := "https://acme.atlassian.net/browse/OA-2576"

	if got := withJiraRef("", url); got != "Jira: "+url {
		t.Errorf("empty body: got %q", got)
	}
	if got := withJiraRef("## Summary\n\ndetails", url); got != "Jira: "+url+"\n\n## Summary\n\ndetails" {
		t.Errorf("plain body: got %q", got)
	}
	// Idempotent: don't add a second reference to the same URL.
	body := "Jira: " + url + "\n\nbody"
	if got := withJiraRef(body, url); got != body {
		t.Errorf("expected unchanged, got %q", got)
	}
}

func TestStripFences(t *testing.T) {
	cases := map[string]string{
		"## Title\n\nbody":                 "## Title\n\nbody", // unfenced, unchanged
		"```\n## Title\n\nbody\n```":       "## Title\n\nbody", // bare fence
		"```markdown\n## Title\nbody\n```": "## Title\nbody",   // language-tagged fence
		"  ```md\n## T\n```  ":             "## T",             // surrounding whitespace
		"no fence here":                    "no fence here",    // plain
	}
	for in, want := range cases {
		if got := stripFences(in); got != want {
			t.Errorf("stripFences(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFindPRTemplate(t *testing.T) {
	dir := t.TempDir()
	if got := findPRTemplate(dir); got != "" {
		t.Errorf("empty repo: got %q, want \"\"", got)
	}

	tmplDir := filepath.Join(dir, ".github")
	if err := os.MkdirAll(tmplDir, 0o755); err != nil {
		t.Fatal(err)
	}
	want := "## Description\n\n- [ ] tests\n"
	if err := os.WriteFile(filepath.Join(tmplDir, "pull_request_template.md"), []byte(want), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := findPRTemplate(dir); got != want {
		t.Errorf("with template: got %q, want %q", got, want)
	}
}
