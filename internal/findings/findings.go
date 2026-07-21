// Package findings models the structured output of a review and renders it for
// the terminal. Keeping the model separate from how it's produced (the agent)
// and how it's shown means a future TUI can render the exact same Finding
// values without touching the agent code.
package findings

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
)

// Severity ranks a finding. Order matters: errors sort before warnings.
type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
	Info    Severity = "info"
)

func (s Severity) rank() int {
	switch s {
	case Error:
		return 0
	case Warning:
		return 1
	default:
		return 2
	}
}

// Action says what the review can safely do about a finding. It's the
// classification the auto-fix loop builds on: only auto-fix findings may be
// applied without a human in the loop.
type Action string

const (
	// AutoFix is objective and safe to fix automatically (e.g. an obvious bug
	// with one correct fix).
	AutoFix Action = "auto-fix"
	// AskUser is intent-sensitive and needs human judgement before any change.
	AskUser Action = "ask-user"
	// NoOp is informational; there is nothing to fix.
	NoOp Action = "no-op"
)

// normalize maps an action to a known value, failing closed to AskUser for
// missing or unrecognised values so nothing is auto-fixed unless the agent
// explicitly said it was safe.
func (a Action) normalize() Action {
	switch a {
	case AutoFix, AskUser, NoOp:
		return a
	default:
		return AskUser
	}
}

// Finding is a single review comment.
type Finding struct {
	Severity Severity `json:"severity"`
	File     string   `json:"file"`
	Line     int      `json:"line"`
	Title    string   `json:"title"`
	Detail   string   `json:"detail"`
	Action   Action   `json:"action"`
}

// Parse extracts findings from an agent's raw output. Agents sometimes wrap JSON
// in prose or ```json fences, so we locate the outermost JSON array rather than
// unmarshalling the whole blob. Returns ok=false if no array could be parsed —
// the caller should then fall back to showing the raw text.
func Parse(raw string) (list []Finding, ok bool) {
	start := strings.IndexByte(raw, '[')
	end := strings.LastIndexByte(raw, ']')
	if start < 0 || end <= start {
		return nil, false
	}
	if err := json.Unmarshal([]byte(raw[start:end+1]), &list); err != nil {
		return nil, false
	}
	// Fail closed: any missing or unrecognised action becomes ask-user, so the
	// auto-fix loop never touches a finding the agent didn't explicitly clear.
	for i := range list {
		list[i].Action = list[i].Action.normalize()
	}
	sort.SliceStable(list, func(i, j int) bool {
		return list[i].Severity.rank() < list[j].Severity.rank()
	})
	return list, true
}

// counts returns the number of errors, warnings and infos.
func counts(list []Finding) (e, w, i int) {
	for _, f := range list {
		switch f.Severity {
		case Error:
			e++
		case Warning:
			w++
		default:
			i++
		}
	}
	return
}

// ANSI colour helpers. color=false yields plain text (e.g. when piped).
const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	red    = "\033[31m"
	yellow = "\033[33m"
	cyan   = "\033[36m"
	green  = "\033[32m"
)

func paint(color bool, code, s string) string {
	if !color {
		return s
	}
	return code + s + reset
}

func (s Severity) color() string {
	switch s {
	case Error:
		return red
	case Warning:
		return yellow
	default:
		return cyan
	}
}

func (s Severity) label() string {
	switch s {
	case Error:
		return "ERROR"
	case Warning:
		return "WARN "
	default:
		return "INFO "
	}
}

func (a Action) color() string {
	switch a {
	case AutoFix:
		return green
	case NoOp:
		return dim
	default: // AskUser
		return yellow
	}
}

// Render writes findings to w as a compact, colourised report with a summary
// header, e.g.:
//
//	Findings — 1 error, 2 warnings
//
//	ERROR  patient_requirements/types.py:120
//	       outstanding() classifies NOT_TRACKED as outstanding
//	       A consumer building "what does the patient still need"...
func Render(w io.Writer, list []Finding, color bool) {
	if len(list) == 0 {
		fmt.Fprintln(w, paint(color, green, "✓ No blocking findings."))
		return
	}
	e, wn, i := counts(list)
	var parts []string
	if e > 0 {
		parts = append(parts, plural(e, "error"))
	}
	if wn > 0 {
		parts = append(parts, plural(wn, "warning"))
	}
	if i > 0 {
		parts = append(parts, plural(i, "suggestion"))
	}
	fmt.Fprintf(w, "%s %s\n\n", paint(color, bold, "Findings —"), strings.Join(parts, ", "))

	for _, f := range list {
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		sev := paint(color, bold+f.Severity.color(), f.Severity.label())
		act := paint(color, f.Action.color(), string(f.Action))
		fmt.Fprintf(w, "%s %s  %s\n", sev, paint(color, bold, f.Title), act)
		fmt.Fprintf(w, "      %s\n", paint(color, dim, loc))
		if d := strings.TrimSpace(f.Detail); d != "" {
			for _, line := range wrap(d, 92) {
				fmt.Fprintf(w, "      %s\n", line)
			}
		}
		fmt.Fprintln(w)
	}
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// wrap breaks s into lines no longer than width, on word boundaries.
func wrap(s string, width int) []string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	cur := words[0]
	for _, word := range words[1:] {
		if len(cur)+1+len(word) > width {
			lines = append(lines, cur)
			cur = word
			continue
		}
		cur += " " + word
	}
	return append(lines, cur)
}
