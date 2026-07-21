// Package agent invokes an AI coding CLI to attempt a fix or a review.
//
// The contract is intentionally dumb: we build a text prompt, run the
// configured agent command inside the worktree, and (for fixes) let the agent
// edit files on disk. Fix success is judged by re-running the checks afterwards,
// never by parsing agent output — which keeps the tool agnostic to which agent
// you use (claude, codex, opencode, ...).
package agent

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/izstoev10/review-lens/internal/config"
)

// maxInput caps how much check output / diff we paste into a prompt. Huge blobs
// (e.g. a whole monolith's lint log) make the agent slow and rarely help.
const maxInput = 60_000

// timeout bounds a single agent invocation so a wedged CLI can't hang forever.
const timeout = 10 * time.Minute

// truncate shortens s to at most max runes, noting how much was dropped.
func truncate(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + fmt.Sprintf("\n... [truncated %d more characters]", len(s)-max)
}

// Prompt builds the instruction sent to the agent for a failing check.
func Prompt(checkName, output string) string {
	return fmt.Sprintf(`A pre-push check failed. Fix the code in this repository so the check passes.

Check: %s

Output:
%s

Rules:
- Edit files directly to fix the root cause.
- Do not disable or skip the check.
- Make the smallest change that makes it pass.`, checkName, truncate(output, maxInput))
}

// ReviewPrompt builds the instruction for reviewing a diff. We ask for a strict
// JSON array so the output can be rendered as structured findings while keeping
// the reviewer's reasoning in the "detail" field.
func ReviewPrompt(diff string) string {
	return fmt.Sprintf(`Review the following code changes as a senior engineer.

Respond with ONLY a JSON array (no prose before or after, no markdown code
fences). Each element must be:
  {
    "severity": "error" | "warning" | "info",
    "file": "path/to/file",
    "line": <integer line number, or 0 if not applicable>,
    "title": "short one-line label",
    "detail": "1-3 sentences: the concrete failure mode and why it matters"
  }

Report only real, actionable findings in THESE changes: bugs, security issues,
risky logic, missing edge cases, or clear maintainability problems. Use "error"
for likely bugs or security issues, "warning" for real risks/judgment calls,
"info" for minor suggestions. Do NOT restate what the code does and do NOT
praise it. If there are no meaningful issues, respond with exactly: []

Diff:
%s`, truncate(diff, maxInput))
}

// run executes the agent command with prompt appended as the final argument,
// inside dir. Raw agent output is streamed to rawOut; a periodic "still working"
// heartbeat is written to status (so a silent agent doesn't look hung). It
// returns everything the agent printed.
func run(dir string, a *config.Agent, prompt string, rawOut, status io.Writer) (string, error) {
	if a == nil || len(a.Cmd) == 0 {
		return "", fmt.Errorf("no agent configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	args := append(append([]string{}, a.Cmd[1:]...), prompt)
	// #nosec G204 — agent command comes from the user's own config, by design.
	cmd := exec.CommandContext(ctx, a.Cmd[0], args...)
	cmd.Dir = dir

	var buf bytes.Buffer
	cmd.Stdout = io.MultiWriter(&buf, rawOut)
	cmd.Stderr = io.MultiWriter(&buf, rawOut)

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting agent %q: %w", a.Cmd[0], err)
	}

	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	start := time.Now()

	var err error
loop:
	for {
		select {
		case err = <-done:
			break loop
		case <-ticker.C:
			fmt.Fprintf(status, "review-lens: … agent still working (%ds elapsed)\n", int(time.Since(start).Seconds()))
		}
	}

	if ctx.Err() == context.DeadlineExceeded {
		return buf.String(), fmt.Errorf("agent %q timed out after %s", a.Cmd[0], timeout)
	}
	if err != nil {
		return buf.String(), fmt.Errorf("agent %q failed: %w", a.Cmd[0], err)
	}
	return strings.TrimSpace(buf.String()), nil
}

// Fix runs the agent to fix a failing check, streaming its work to progress.
func Fix(dir string, a *config.Agent, prompt string, progress io.Writer) error {
	_, err := run(dir, a, prompt, progress, progress)
	return err
}

// Review runs the agent read-only and returns its raw output. Only the
// heartbeat is written to status; the raw (JSON) output is captured for
// parsing, not streamed, so the terminal stays clean.
func Review(dir string, a *config.Agent, prompt string, status io.Writer) (string, error) {
	return run(dir, a, prompt, io.Discard, status)
}
