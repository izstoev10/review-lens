// Package tui renders a review in an interactive terminal UI built with
// bubbletea (the Elm architecture: Model state, Update on messages, View to a
// string).
//
// Long-running work (the agent) runs in a goroutine that writes messages to a
// channel; a "waitFor" command reads the next message and re-subscribes, so the
// UI updates live without the model ever holding the tea.Program. Phases:
//
//	running → done (findings, selectable) → fixing → fixed
package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/izstoev10/review-lens/internal/agent"
	"github.com/izstoev10/review-lens/internal/config"
	"github.com/izstoev10/review-lens/internal/findings"
)

// --- messages ------------------------------------------------------------

type activityMsg string
type doneMsg struct {
	result string
	err    error
}
type fixDoneMsg struct{ err error }
type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// waitFor reads the next message from the event channel. Re-issued after each
// message so exactly one reader is always outstanding.
func waitFor(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

// --- entry points --------------------------------------------------------

// RunReview streams a review live, then shows selectable findings.
func RunReview(dir string, a *config.Agent, prompt, title string) error {
	ch := make(chan tea.Msg, 128)
	m := newModel(title, dir, a, ch)
	p := tea.NewProgram(m, tea.WithAltScreen())
	go func() {
		result, err := agent.StreamReview(dir, a, prompt, func(act string) { ch <- activityMsg(act) })
		ch <- doneMsg{result: result, err: err}
	}()
	_, err := p.Run()
	return err
}

// Show displays already-computed findings (no live review phase). dir and a
// enable the fix action; pass a nil agent to make it read-only.
func Show(items []findings.Finding, dir string, a *config.Agent) error {
	ch := make(chan tea.Msg, 128)
	m := newModel("", dir, a, ch)
	m.phase = phaseDone
	m.items = items
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// --- model ---------------------------------------------------------------

type phase int

const (
	phaseRunning phase = iota // initial review streaming
	phaseDone                 // findings shown, selectable
	phaseFixing               // agent applying selected fixes
	phaseFixed                // fixes applied (or failed)
)

type model struct {
	title    string
	dir      string
	agentCfg *config.Agent
	events   chan tea.Msg

	phase   phase
	spinner spinner.Model
	start   time.Time
	elapsed time.Duration

	activities []string

	items    []findings.Finding
	selected map[int]bool
	cursor   int
	rawText  string
	err      error

	fixErr     error
	fixedCount int

	width, height int
}

func newModel(title, dir string, a *config.Agent, ch chan tea.Msg) model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
	return model{
		title:    title,
		dir:      dir,
		agentCfg: a,
		events:   ch,
		phase:    phaseRunning,
		spinner:  s,
		selected: map[int]bool{},
		start:    time.Now(),
		width:    80,
		height:   24,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, tick(), waitFor(m.events))
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case tea.KeyMsg:
		return m.handleKey(msg)

	case activityMsg:
		m.activities = append(m.activities, string(msg))
		return m, waitFor(m.events)

	case doneMsg:
		m.phase = phaseDone
		m.err = msg.err
		if list, ok := findings.Parse(msg.result); ok {
			m.items = list
		} else {
			m.rawText = strings.TrimSpace(msg.result)
		}
		return m, waitFor(m.events)

	case fixDoneMsg:
		m.phase = phaseFixed
		m.fixErr = msg.err
		return m, waitFor(m.events)

	case tickMsg:
		if m.phase == phaseRunning || m.phase == phaseFixing {
			m.elapsed = time.Since(m.start)
			return m, tick()
		}

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "q", "esc", "ctrl+c":
		return m, tea.Quit
	}
	if m.phase != phaseDone {
		return m, nil
	}
	switch msg.String() {
	case "j", "down":
		if m.cursor < len(m.items)-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		if len(m.items) > 0 {
			m.cursor = len(m.items) - 1
		}
	case " ":
		m.selected[m.cursor] = !m.selected[m.cursor]
	case "A":
		for i := range m.items {
			m.selected[i] = true
		}
	case "N":
		m.selected = map[int]bool{}
	case "f":
		return m.startFix()
	}
	return m, nil
}

// startFix kicks off the agent to fix the selected findings, if any and if an
// agent is configured. It transitions to the fixing phase and restarts the
// elapsed timer.
func (m model) startFix() (tea.Model, tea.Cmd) {
	if m.agentCfg == nil || m.selectedCount() == 0 {
		return m, nil
	}
	m.fixedCount = m.selectedCount()
	prompt := fixPrompt(m.items, m.selected)
	dir, a, ch := m.dir, m.agentCfg, m.events
	go func() {
		_, err := agent.StreamFix(dir, a, prompt, func(act string) { ch <- activityMsg(act) })
		ch <- fixDoneMsg{err: err}
	}()
	m.phase = phaseFixing
	m.activities = nil
	m.start = time.Now()
	return m, tea.Batch(m.spinner.Tick, tick())
}

func (m model) selectedCount() int {
	n := 0
	for _, v := range m.selected {
		if v {
			n++
		}
	}
	return n
}

func fixPrompt(items []findings.Finding, sel map[int]bool) string {
	var b strings.Builder
	b.WriteString("Apply fixes for the following code review findings. Edit files directly to fix the root cause, make the smallest change that resolves each, and do not disable or suppress checks.\n\n")
	for i, f := range items {
		if !sel[i] {
			continue
		}
		loc := f.File
		if f.Line > 0 {
			loc = fmt.Sprintf("%s:%d", f.File, f.Line)
		}
		fmt.Fprintf(&b, "- [%s] %s — %s\n", loc, f.Title, f.Detail)
	}
	return b.String()
}

// --- styling -------------------------------------------------------------

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	cursorStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))
	selStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("10"))
	footerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).MarginTop(1)
	errStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("9"))
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	boxStyle    = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 1).
			MarginTop(1)
)

func sevStyle(s findings.Severity) lipgloss.Style {
	color := lipgloss.Color("36")
	switch s {
	case findings.Error:
		color = lipgloss.Color("9")
	case findings.Warning:
		color = lipgloss.Color("11")
	}
	return lipgloss.NewStyle().Bold(true).Foreground(color)
}

func sevLabel(s findings.Severity) string {
	switch s {
	case findings.Error:
		return "ERROR"
	case findings.Warning:
		return "WARN "
	default:
		return "INFO "
	}
}

// --- view ----------------------------------------------------------------

func (m model) View() string {
	switch m.phase {
	case phaseRunning:
		return m.workingView(orDefault(m.title, "Reviewing changes"))
	case phaseFixing:
		return m.workingView(fmt.Sprintf("Fixing %d finding(s)", m.fixedCount))
	case phaseFixed:
		return m.fixedView()
	default:
		return m.doneView()
	}
}

// workingView is the live spinner + activity feed, shared by review and fix.
func (m model) workingView(head string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s  %s\n\n",
		m.spinner.View(),
		titleStyle.Render(head),
		dimStyle.Render(fmt.Sprintf("(%ds)", int(m.elapsed.Seconds()))),
	)
	max := m.height - 6
	if max < 3 {
		max = 3
	}
	acts := m.activities
	if len(acts) > max {
		b.WriteString(dimStyle.Render(fmt.Sprintf("  … %d earlier\n", len(acts)-max)))
		acts = acts[len(acts)-max:]
	}
	for _, a := range acts {
		fmt.Fprintf(&b, "  %s %s\n", dimStyle.Render("→"), clip(a, m.width-4))
	}
	if len(m.activities) == 0 {
		b.WriteString(dimStyle.Render("  starting…\n"))
	}
	b.WriteString(footerStyle.Render("q abort"))
	return b.String()
}

func (m model) fixedView() string {
	if m.fixErr != nil {
		return errStyle.Render("Fix failed") + "\n\n" +
			dimStyle.Render(clip(m.fixErr.Error(), m.width)) + "\n\n" +
			footerStyle.Render("q quit")
	}
	return okStyle.Render(fmt.Sprintf("✓ Applied fixes for %d finding(s).", m.fixedCount)) + "\n\n" +
		dimStyle.Render("Files were edited in your working tree — review with `git diff`, then commit.") + "\n\n" +
		footerStyle.Render("q quit")
}

func (m model) doneView() string {
	if m.err != nil {
		return errStyle.Render("Review failed") + "\n\n" +
			dimStyle.Render(clip(m.err.Error(), m.width)) + "\n\n" +
			footerStyle.Render("q quit")
	}
	if len(m.items) == 0 {
		if m.rawText != "" {
			return titleStyle.Render("Review (unstructured)") + "\n\n" + m.rawText + "\n\n" + footerStyle.Render("q quit")
		}
		return okStyle.Render("✓ No blocking findings.") + "\n\n" + footerStyle.Render("q quit")
	}

	var b strings.Builder
	e, w, i := count(m.items)
	head := titleStyle.Render("Findings") + dimStyle.Render("  "+summary(e, w, i))
	if n := m.selectedCount(); n > 0 {
		head += selStyle.Render(fmt.Sprintf("   %d selected", n))
	}
	b.WriteString(head + "\n\n")

	for idx, f := range m.items {
		marker := "  "
		if idx == m.cursor {
			marker = cursorStyle.Render("> ")
		}
		box := "[ ]"
		if m.selected[idx] {
			box = selStyle.Render("[x]")
		}
		title := f.Title
		if idx == m.cursor {
			title = titleStyle.Render(title)
		}
		fmt.Fprintf(&b, "%s%s %s  %s\n", marker, box, sevStyle(f.Severity).Render(sevLabel(f.Severity)), clip(title, m.width-16))
	}

	sel := m.items[m.cursor]
	loc := sel.File
	if sel.Line > 0 {
		loc = fmt.Sprintf("%s:%d", sel.File, sel.Line)
	}
	inner := m.width - 6
	if inner < 20 {
		inner = 20
	}
	detail := lipgloss.NewStyle().Width(inner).Render(strings.TrimSpace(sel.Detail))
	b.WriteString(boxStyle.Width(inner+2).Render(sevStyle(sel.Severity).Render(loc)+"\n"+detail) + "\n")

	keys := "j/k move · space select · A all · N none · q quit"
	if m.agentCfg != nil {
		keys = "j/k move · space select · A all · N none · f fix selected (edits files) · q quit"
	}
	b.WriteString(footerStyle.Render(keys))
	return b.String()
}

// --- helpers -------------------------------------------------------------

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func clip(s string, width int) string {
	if width < 4 {
		width = 4
	}
	if len(s) <= width {
		return s
	}
	return s[:width-1] + "…"
}

func count(list []findings.Finding) (e, w, i int) {
	for _, f := range list {
		switch f.Severity {
		case findings.Error:
			e++
		case findings.Warning:
			w++
		default:
			i++
		}
	}
	return
}

func summary(e, w, i int) string {
	var parts []string
	if e > 0 {
		parts = append(parts, plural(e, "error"))
	}
	if w > 0 {
		parts = append(parts, plural(w, "warning"))
	}
	if i > 0 {
		parts = append(parts, plural(i, "suggestion"))
	}
	if len(parts) == 0 {
		return "no findings"
	}
	return strings.Join(parts, ", ")
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
