// Package agent invokes an AI coding CLI to attempt a fix or a review.
//
// When the configured command emits Claude's streaming JSON (--output-format
// stream-json), we parse that stream: each line is an event, and we surface a
// human-readable activity ("read handler.go", "grep TODO") as it happens, then
// return the agent's final text. Fix success is judged by re-running the checks
// afterwards, never by parsing agent output.
package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	"github.com/izstoev10/review-lens/internal/config"
)

const (
	maxInput = 60_000
	timeout  = 10 * time.Minute
)

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

// ReviewPrompt builds the instruction for reviewing a diff, requesting a strict
// JSON array so the output renders as structured findings.
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

// CanStream reports whether the configured command emits Claude's stream-json.
func CanStream(a *config.Agent) bool {
	if a == nil {
		return false
	}
	for _, s := range a.Cmd {
		if s == "stream-json" {
			return true
		}
	}
	return false
}

// onActivity is called with a short human-readable description of each agent
// action as it happens. May be nil.
type onActivity func(string)

// exec runs the agent with prompt appended, inside dir. If the command streams
// JSON it is parsed for activity + final text; otherwise the raw combined
// output is returned as the text. activity (if non-nil) is called per event.
func execAgent(dir string, a *config.Agent, prompt string, activity onActivity) (string, error) {
	if a == nil || len(a.Cmd) == 0 {
		return "", fmt.Errorf("no agent configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	args := append(append([]string{}, a.Cmd[1:]...), prompt)
	// #nosec G204 — agent command comes from the user's own config, by design.
	cmd := exec.CommandContext(ctx, a.Cmd[0], args...)
	cmd.Dir = dir

	if !CanStream(a) {
		out, err := cmd.CombinedOutput()
		if err != nil {
			return string(out), fmt.Errorf("agent %q failed: %w", a.Cmd[0], err)
		}
		return strings.TrimSpace(string(out)), nil
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return "", fmt.Errorf("starting agent %q: %w", a.Cmd[0], err)
	}

	result := parseStream(stdout, activity)

	waitErr := cmd.Wait()
	if ctx.Err() == context.DeadlineExceeded {
		return result, fmt.Errorf("agent %q timed out after %s", a.Cmd[0], timeout)
	}
	if waitErr != nil {
		return result, fmt.Errorf("agent %q failed: %w\n%s", a.Cmd[0], waitErr, strings.TrimSpace(stderr.String()))
	}
	return result, nil
}

// streamEvent is the subset of Claude's stream-json we care about.
type streamEvent struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Message *struct {
		Content []struct {
			Type  string          `json:"type"`
			Text  string          `json:"text"`
			Name  string          `json:"name"`
			Input json.RawMessage `json:"input"`
		} `json:"content"`
	} `json:"message"`
	Result string `json:"result"` // present on the final {"type":"result"} event
}

// parseStream reads stream-json lines, emits activity for tool uses, and returns
// the agent's final result text.
func parseStream(r io.Reader, activity onActivity) string {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // large lines (tool results)
	var result string
	for sc.Scan() {
		var ev streamEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue // ignore non-JSON / partial lines
		}
		switch ev.Type {
		case "assistant":
			if ev.Message == nil || activity == nil {
				continue
			}
			for _, c := range ev.Message.Content {
				if c.Type == "tool_use" {
					activity(describeTool(c.Name, c.Input))
				}
			}
		case "result":
			result = strings.TrimSpace(ev.Result)
		}
	}
	return result
}

// describeTool turns a tool call into a short activity line.
func describeTool(name string, input json.RawMessage) string {
	var m map[string]any
	_ = json.Unmarshal(input, &m)
	get := func(k string) string {
		if v, ok := m[k].(string); ok {
			return v
		}
		return ""
	}
	switch name {
	case "Read":
		return "read " + get("file_path")
	case "Edit", "MultiEdit":
		return "edit " + get("file_path")
	case "Write":
		return "write " + get("file_path")
	case "Grep":
		return "grep " + get("pattern")
	case "Glob":
		return "glob " + get("pattern")
	case "Bash":
		return "run " + firstLine(truncate(get("command"), 60))
	case "Task":
		return "subagent " + truncate(get("description"), 40)
	case "":
		return "working"
	default:
		return strings.ToLower(name)
	}
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// --- public entry points -------------------------------------------------

// Fix runs the agent to fix a failing check, streaming activity to progress.
func Fix(dir string, a *config.Agent, prompt string, progress io.Writer) error {
	_, err := execAgent(dir, a, prompt, func(act string) {
		fmt.Fprintf(progress, "review-lens:   → %s\n", act)
	})
	return err
}

// Review runs the agent read-only and returns its final text. Activity (if any)
// is written to status so a long run shows progress.
func Review(dir string, a *config.Agent, prompt string, status io.Writer) (string, error) {
	return execAgent(dir, a, prompt, func(act string) {
		fmt.Fprintf(status, "review-lens:   → %s\n", act)
	})
}

// StreamReview runs the agent read-only, calling activity for each action as it
// happens (for a live UI), and returns the final text. Used by the TUI.
func StreamReview(dir string, a *config.Agent, prompt string, activity func(string)) (string, error) {
	return execAgent(dir, a, prompt, activity)
}

// StreamFix runs the agent to apply fixes, streaming activity to the callback.
// Same mechanism as StreamReview; the difference is only the prompt (which asks
// the agent to edit files) and that the caller doesn't parse the result.
func StreamFix(dir string, a *config.Agent, prompt string, activity func(string)) (string, error) {
	return execAgent(dir, a, prompt, activity)
}
