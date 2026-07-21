// Package agent invokes an AI coding CLI to attempt a fix.
//
// The contract is intentionally dumb: we build a text prompt describing the
// failure, run the configured agent command inside the worktree, and let the
// agent edit files on disk. We do not parse the agent's stdout — success is
// judged by re-running the checks afterwards. That keeps gate agnostic to which
// agent you use (claude, codex, opencode, ...).
package agent

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/izstoev10/review-lens/internal/config"
)

// Prompt builds the instruction sent to the agent for a failing check.
func Prompt(checkName, output string) string {
	return fmt.Sprintf(`A pre-push check failed. Fix the code in this repository so the check passes.

Check: %s

Output:
%s

Rules:
- Edit files directly to fix the root cause.
- Do not disable or skip the check.
- Make the smallest change that makes it pass.`, checkName, strings.TrimSpace(output))
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
%s`, diff)
}

// Review runs the agent in read-only mode over a prompt and returns its output
// (the findings) as text. Unlike Fix, it keeps stdout because that IS the
// result the user wants to read.
func Review(dir string, a *config.Agent, prompt string) (string, error) {
	if a == nil || len(a.Cmd) == 0 {
		return "", fmt.Errorf("no agent configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	args := append(append([]string{}, a.Cmd[1:]...), prompt)
	// #nosec G204 — agent command is user-configured.
	cmd := exec.CommandContext(ctx, a.Cmd[0], args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("agent %q review failed: %w\n%s", a.Cmd[0], err, out)
	}
	return strings.TrimSpace(string(out)), nil
}

// Fix runs the configured agent inside dir with the given prompt. The prompt is
// appended as the final argument to the agent command. A generous timeout keeps
// a hung agent from wedging the whole run.
func Fix(dir string, a *config.Agent, prompt string) error {
	if a == nil || len(a.Cmd) == 0 {
		return fmt.Errorf("no agent configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	args := append(append([]string{}, a.Cmd[1:]...), prompt)
	// #nosec G204 — agent command is user-configured.
	cmd := exec.CommandContext(ctx, a.Cmd[0], args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("agent %q failed: %w\n%s", a.Cmd[0], err, out)
	}
	return nil
}
