package pipeline

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// jiraKeyRe matches a Jira-style issue key anywhere in a branch name, e.g.
// "feat/oa-2576-add-limiter" → "oa-2576". Case-insensitive; the caller
// upper-cases the result.
var jiraKeyRe = regexp.MustCompile(`[A-Za-z]{2,}-\d+`)

// jiraKeyFromBranch extracts the upper-cased Jira key from a branch name, or ""
// if none is present.
func jiraKeyFromBranch(branch string) string {
	return strings.ToUpper(jiraKeyRe.FindString(branch))
}

// jiraURL joins a Jira browse base with a key into a full ticket URL, tolerating
// a trailing slash on the base (e.g. ".../browse/" or ".../browse").
func jiraURL(base, key string) string {
	return strings.TrimRight(base, "/") + "/" + key
}

// withJiraTitlePrefix prefixes "[KEY] " to a PR title, unless the title already
// references the key (idempotent).
func withJiraTitlePrefix(title, key string) string {
	if strings.Contains(title, key) {
		return title
	}
	return "[" + key + "] " + title
}

// withJiraRef prepends a "Jira: <url>" line to a PR body, unless the URL is
// already present (idempotent).
func withJiraRef(body, url string) string {
	if strings.Contains(body, url) {
		return body
	}
	line := "Jira: " + url
	if strings.TrimSpace(body) == "" {
		return line
	}
	return line + "\n\n" + body
}

// uninformativeTitle reports whether a commit-derived PR title carries no
// information worth keeping: a known throwaway subject (wip, fixup, tmp) or a
// bare single token. Multi-word subjects are always kept — the cost of
// wrongly replacing a real title is higher than letting a dull one through.
func uninformativeTitle(title string) bool {
	fields := strings.Fields(title)
	if len(fields) != 1 {
		return len(fields) == 0
	}
	switch strings.ToLower(strings.TrimRight(fields[0], "!:.")) {
	case "wip", "fixup", "fixup!", "tmp", "temp", "squash", "x", "asdf", "test", "changes", "stuff", "update", "updates", "fix", "fixes", "misc":
		return true
	}
	// A bare token with no word separators ("wip2", "asdfgh") says nothing; a
	// dotted or path-like one ("main.go", "v1.2.3") at least names a thing.
	return !strings.ContainsAny(fields[0], "./")
}

// titleFromBranch derives a readable PR title from a branch name:
// "feat/tui-apply-flow" → "feat: tui apply flow". A branch without a prefix
// just gets its separators spaced out.
func titleFromBranch(branch string) string {
	words := strings.NewReplacer("-", " ", "_", " ", "/", " ")
	prefix, rest, found := strings.Cut(branch, "/")
	if !found || strings.TrimSpace(words.Replace(rest)) == "" {
		return strings.TrimSpace(words.Replace(branch))
	}
	return prefix + ": " + strings.TrimSpace(words.Replace(rest))
}

// prTemplatePaths are the locations GitHub honours for a single PR template,
// spelled both lower- and upper-case (the two conventions in the wild).
var prTemplatePaths = []string{
	".github/pull_request_template.md",
	".github/PULL_REQUEST_TEMPLATE.md",
	"pull_request_template.md",
	"PULL_REQUEST_TEMPLATE.md",
	"docs/pull_request_template.md",
	"docs/PULL_REQUEST_TEMPLATE.md",
}

// findPRTemplate returns the contents of the repo's PR template under dir, or ""
// if there isn't one. The first matching path wins, mirroring GitHub's lookup.
func findPRTemplate(dir string) string {
	for _, p := range prTemplatePaths {
		if b, err := os.ReadFile(filepath.Join(dir, p)); err == nil {
			return string(b)
		}
	}
	return ""
}

// stripFences removes a single wrapping ``` code fence from s, if present — some
// agents wrap their whole answer in one despite being told not to. Content that
// isn't fenced is returned trimmed but otherwise untouched.
func stripFences(s string) string {
	s = strings.TrimSpace(s)
	if !strings.HasPrefix(s, "```") {
		return s
	}
	nl := strings.IndexByte(s, '\n')
	if nl < 0 {
		return "" // only an opening fence, no content
	}
	s = strings.TrimSpace(s[nl+1:])
	s = strings.TrimSuffix(s, "```")
	return strings.TrimSpace(s)
}
