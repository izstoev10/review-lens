// Package agent invokes an AI coding CLI to attempt a fix or a review.
//
// The contract is intentionally dumb: we build a text prompt, run the
// configured agent command inside the worktree, and (for fixes) let the agent
// edit files on disk. Fix success is judged by re-running the checks afterwards,
// never by parsing agent output — which keeps the tool agnostic to which agent
// you use (claude, codex, opencode, ...).
//
// The agent's stdout/stderr is streamed live to the caller's writer so a long
// run shows progress instead of looking hung.
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
// (e.g. a whole monolith's lint log) make the agent slow and rarely help — the
// first chunk is almost always enough to identify the fix.
const maxInput = 12_000

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

// ReviewPrompt builds the instruction for reviewing a diff. We ask for concise,
// actionable findings and nothing else, so the output is readable in a terminal.
func ReviewPrompt(diff string) string {
	return fmt.Sprintf(`Review the following code changes as a senior engineer.

Report only real, actionable findings: bugs, security issues, risky logic,
missing edge cases, or clear maintainability problems. For each, give the file
and a one-line explanation. Do NOT restate what the code does, do NOT praise it,
and do NOT edit any files — this is a read-only review. If there are no
meaningful issues, say exactly "No blocking findings."

Diff:
%s`, truncate(diff, maxInput))
}

// run executes the agent command with prompt appended as the final argument,
// inside dir, streaming combined output to progress. It returns everything the
// agent printed (also captured) so callers that want the text (Review) can use
// it.
func run(dir string, a *config.Agent, prompt string, progress io.Writer) (string, error) {
	if a == nil || len(a.Cmd) == 0 {
		return "", fmt.Errorf("no agent configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	args := append(append([]string{}, a.Cmd[1:]...), prompt)
	// #nosec G204 — agent command comes from the user's own config, by design.
	cmd := exec.CommandContext(ctx, a.Cmd[0], args...)
	cmd.Dir = dir

	// Tee output to both the live progress writer and a buffer we return.
	var buf bytes.Buffer
	w := io.MultiWriter(&buf, progress)
	cmd.Stdout = w
	cmd.Stderr = w

	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting agent %q: %w", a.Cmd[0], err)
	}

	// Heartbeat: some agents (notably `claude -p`) print nothing until they
	// finish, so a long run looks hung. Emit an elapsed-time tick until the
	// process exits, so the user can see it's still alive.
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
			fmt.Fprintf(progress, "review-lens: … agent still working (%ds elapsed)\n", int(time.Since(start).Seconds()))
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

// Fix runs the agent to fix a failing check, streaming progress to progress.
func Fix(dir string, a *config.Agent, prompt string, progress io.Writer) error {
	_, err := run(dir, a, prompt, progress)
	return err
}

// Review runs the agent read-only over a prompt, streaming progress, and returns
// its findings text.
func Review(dir string, a *config.Agent, prompt string, progress io.Writer) (string, error) {
	return run(dir, a, prompt, progress)
}
