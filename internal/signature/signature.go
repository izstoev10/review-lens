// Package signature defines the deterministic "gate signature" review-lens
// stamps into the body of every PR it opens. A companion GitHub Actions workflow
// fails PRs to the base branch whose body lacks the marker, so every human PR is
// provably routed through the gate.
//
// The marker is an HTML comment: invisible in the rendered PR body but trivially
// greppable. It is a fixed literal (deterministic), so the CLI and the workflow
// agree on it without coordination — keep the Marker constant here in sync with
// the value hard-coded in .github/workflows/require-review-lens.yml.
package signature

import "strings"

// Marker is the gate signature. Bump the version suffix only if the format has
// to change, and update the workflow to match.
const Marker = "<!-- review-lens-gate:v1 -->"

// Ensure appends Marker to body if it isn't already present, returning the
// (possibly) updated body and whether it changed. It is idempotent: stamping an
// already-signed body is a no-op, so re-running review-lens on an existing PR
// never duplicates the marker.
func Ensure(body string) (newBody string, changed bool) {
	if strings.Contains(body, Marker) {
		return body, false
	}
	trimmed := strings.TrimRight(body, "\n")
	if trimmed == "" {
		return Marker, true
	}
	return trimmed + "\n\n" + Marker, true
}
