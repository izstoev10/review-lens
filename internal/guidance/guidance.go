// Package guidance loads the editable review criteria — what to flag, the
// severity rubric, and house-style expectations — that shape the review prompt.
//
// The criteria live in a markdown file at the repo root so they can be tuned
// without recompiling review-lens. When that file is absent (or empty) we fall
// back to the built-in Default below, so the tool always has sensible criteria.
//
// Only the *criteria* are editable. The structured JSON output format is a
// contract with the findings parser and stays hardcoded in the agent package,
// so tuning the guidance can never break how findings are rendered.
package guidance

import (
	"os"
	"path/filepath"
	"strings"
)

// DefaultPath is where review-lens looks for the guidance file, relative to the
// repo root. It's a companion to the .review-lens.json config.
const DefaultPath = ".review-lens.guidance.md"

// Default is the built-in review guidance, used when no guidance file is found.
// `review-lens init` writes this to DefaultPath so it's easy to edit.
const Default = `# Review guidance

Review the code changes as a senior engineer. Focus on the diff itself, not the
surrounding code that didn't change.

## What to flag

Report only real, actionable problems introduced or exposed by THESE changes:

- Bugs and incorrect logic
- Security issues
- Risky logic and unhandled error paths
- Missing edge cases
- Clear maintainability problems

## Severity rubric

- **error** — likely bugs or security issues; something is probably wrong.
- **warning** — real risks or judgment calls worth a second look.
- **info** — minor suggestions and nits.

## House style

- Do NOT restate what the code does.
- Do NOT praise the code.
- Be concrete: name the failure mode and why it matters.
- If there are no meaningful issues, report none.`

// Load returns the review guidance for the repo rooted at root. It reads the
// file at relPath (resolved under root); if relPath is empty it uses
// DefaultPath. A missing or empty file falls back to Default, so callers always
// get usable guidance.
func Load(root, relPath string) string {
	if strings.TrimSpace(relPath) == "" {
		relPath = DefaultPath
	}
	data, err := os.ReadFile(filepath.Join(root, relPath))
	if err != nil {
		return Default
	}
	if s := strings.TrimSpace(string(data)); s != "" {
		return s
	}
	return Default
}
