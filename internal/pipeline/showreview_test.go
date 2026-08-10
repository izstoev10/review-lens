package pipeline

import (
	"bytes"
	"strings"
	"testing"
)

// A parseable review renders the compact report; a malformed one renders a
// bounded excerpt. Neither path may dump the full response — on a broken run
// that can be an entire agent transcript.
func TestShowReviewNeverDumpsTheFullResponse(t *testing.T) {
	var out bytes.Buffer
	showReview(strings.Repeat("raw transcript line\n", 2000), &out)

	if out.Len() > 4000 {
		t.Errorf("output is %d bytes — malformed review output must be bounded", out.Len())
	}
	if !strings.Contains(out.String(), "raw transcript line") {
		t.Error("the excerpt should keep the start of the response for diagnosis")
	}
}

func TestShowReviewRendersParsedFindings(t *testing.T) {
	var out bytes.Buffer
	showReview(`[{"severity":"error","file":"a.go","line":1,"title":"boom","detail":"d","action":"ask-user"}]`, &out)
	if got := out.String(); !strings.Contains(got, "boom") || !strings.Contains(got, "a.go:1") {
		t.Errorf("report = %q, want the compact findings report", got)
	}
}
