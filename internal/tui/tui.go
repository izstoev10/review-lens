// Package tui renders a review in an interactive terminal UI built with
// bubbletea (the Elm architecture: Model state, Update on messages, View to a
// string).
//
// Long-running work (the agent) runs in a goroutine that writes messages to a
// channel; a "waitFor" command reads the next message and re-subscribes, so the
// UI updates live without the model ever holding the tea.Program.
//
// The UI has a checkpoint pipeline (stages ticked off as they complete) above a
// body that changes with the phase: a live activity feed while the agent works,
// then a navigable findings viewer, then a fix summary.
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

func waitFor(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-ch }
}

// --- entry points --------------------------------------------------------

// RunReview streams a review live, then shows selectable findings.
func RunReview(dir string, a *config.Agent, prompt, title string) error {
	ch := make(chan tea.Msg, 128)
	m := newModel(title, dir, a, ch)
	m.stages = []stage{{"Fetch diff", stageDone}, {"Review", stageRunning}, {"Findings", stagePending}}
	p := tea.NewProgram(m, tea.WithAltScreen())
	go func() {
		result, err := agent.StreamReview(dir, a, prompt, func(act string) { ch <- activityMsg(act) })
		ch <- doneMsg{result: result, err: err}
	}()
	_, err := p.Run()
	return err
}

// Show displays already-computed findings (no live review phase).
func Show(items []findings.Finding, dir string, a *config.Agent) error {
	ch := make(chan tea.Msg, 128)
	m := newModel("", dir, a, ch)
	m.phase = phaseDone
	m.items = items
	m.stages = []stage{{"Review", stageDone}, {"Findings", stageDone}}
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// --- model ---------------------------------------------------------------

type phase int

const (
	phaseRunning phase = iota
	phaseDone
	phaseFixing
	phaseFixed
)

type stageStatus int

const (
	stagePending stageStatus = iota
	stageRunning
	stageDone
	stageFailed
)

type stage struct {
	name   string
	status stageStatus
}

type model struct {
	title    string
	dir      string
	agentCfg *config.Agent
	events   chan tea.Msg

	phase   phase
	stages  []stage
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

func (m *model) setStage(name string, st stageStatus) {
	for i := range m.stages {
		if m.stages[i].name == name {
			m.stages[i].status = st
			return
		}
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
		if msg.err != nil {
			m.setStage("Review", stageFailed)
		} else {
			m.setStage("Review", stageDone)
			m.setStage("Findings", stageDone)
		}
		if list, ok := findings.Parse(msg.result); ok {
			m.items = list
		} else {
			m.rawText = strings.TrimSpace(msg.result)
		}
		return m, waitFor(m.events)

	case fixDoneMsg:
		m.phase = phaseFixed
		m.fixErr = msg.err
		if msg.err != nil {
			m.setStage("Apply fixes", stageFailed)
		} else {
			m.setStage("Apply fixes", stageDone)
		}
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
	m.stages = append(m.stages, stage{"Apply fixes", stageRunning})
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
	doneIcon    = lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render("✓")
	pendIcon    = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).Render("○")
	failIcon    = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Render("✗")
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

func actionStyle(a findings.Action) lipgloss.Style {
	color := lipgloss.Color("11") // ask-user (yellow)
	switch a {
	case findings.AutoFix:
		color = lipgloss.Color("10") // green
	case findings.NoOp:
		color = lipgloss.Color("244") // dim
	}
	return lipgloss.NewStyle().Foreground(color)
}

// --- panel drawing -------------------------------------------------------

var borderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))

// padVisible right-pads s with spaces to a visible width of w (ANSI-aware).
func padVisible(s string, w int) string {
	if d := w - lipgloss.Width(s); d > 0 {
		return s + strings.Repeat(" ", d)
	}
	return s
}

// panel draws a rounded box with the title embedded in the top border and an
// optional right-aligned status, wrapping the given body lines. Callers are
// responsible for keeping each body line within the inner width.
func panel(title, right string, bodyLines []string, width int) string {
	if width < 24 {
		width = 24
	}
	inner := width - 4 // space between "│ " and " │"

	left := borderStyle.Render("╭─ ") + titleStyle.Render(title) + " "
	var rightSeg string
	if right != "" {
		rightSeg = " " + right + borderStyle.Render(" ─╮")
	} else {
		rightSeg = borderStyle.Render("╮")
	}
	dashes := width - lipgloss.Width(left) - lipgloss.Width(rightSeg)
	if dashes < 0 {
		dashes = 0
	}
	var b strings.Builder
	b.WriteString(left + borderStyle.Render(strings.Repeat("─", dashes)) + rightSeg + "\n")
	for _, ln := range bodyLines {
		b.WriteString(borderStyle.Render("│") + " " + padVisible(ln, inner) + " " + borderStyle.Render("│") + "\n")
	}
	b.WriteString(borderStyle.Render("╰" + strings.Repeat("─", width-2) + "╯"))
	return b.String()
}

// --- view ----------------------------------------------------------------

func (m model) View() string {
	switch m.phase {
	case phaseRunning, phaseFixing:
		return m.pipelinePanel() + "\n" + m.logPanel() + "\n" + footerStyle.Render(m.footer())
	case phaseFixed:
		return m.pipelinePanel() + "\n\n" + m.fixedBody() + "\n" + footerStyle.Render(m.footer())
	default:
		return m.pipelinePanel() + "\n\n" + m.findingsBody() + "\n" + footerStyle.Render(m.footer())
	}
}

// pipelinePanel renders the checkpoint panel: each stage with its status icon,
// a live elapsed timer next to the running one, and an overall status.
func (m model) pipelinePanel() string {
	status, statusColor := "running", lipgloss.Color("13")
	if m.phase == phaseDone {
		status, statusColor = "done", lipgloss.Color("10")
	}
	if m.err != nil || m.fixErr != nil {
		status, statusColor = "failed", lipgloss.Color("9")
	}
	right := lipgloss.NewStyle().Bold(true).Foreground(statusColor).Render(status)

	var rows []string
	if m.title != "" {
		rows = append(rows, dimStyle.Render(strings.TrimPrefix(m.title, "Reviewing ")), "")
	}
	for _, s := range m.stages {
		var icon string
		switch s.status {
		case stageDone:
			icon = doneIcon
		case stageFailed:
			icon = failIcon
		case stageRunning:
			icon = m.spinner.View()
		default:
			icon = pendIcon
		}
		row := fmt.Sprintf("%s %s", icon, s.name)
		if s.status == stageRunning {
			row += dimStyle.Render(fmt.Sprintf("   %ds", int(m.elapsed.Seconds())))
		}
		rows = append(rows, row)
	}
	return panel("Pipeline", right, rows, m.width)
}

// logPanel is the live feed of the agent's actions in its own bordered box.
func (m model) logPanel() string {
	inner := m.width - 4
	max := m.height - len(m.stages) - 9
	if max < 3 {
		max = 3
	}
	acts := m.activities
	var rows []string
	if len(acts) > max {
		rows = append(rows, dimStyle.Render(fmt.Sprintf("… %d earlier", len(acts)-max)))
		acts = acts[len(acts)-max:]
	}
	for _, a := range acts {
		rows = append(rows, dimStyle.Render("→ ")+clip(a, inner-2))
	}
	if len(m.activities) == 0 {
		rows = append(rows, dimStyle.Render("waiting for the model…"))
	}
	return panel("Log", "", rows, m.width)
}

func (m model) fixedBody() string {
	if m.fixErr != nil {
		return errStyle.Render("Fix failed") + "\n\n" + dimStyle.Render(clip(m.fixErr.Error(), m.width))
	}
	return okStyle.Render(fmt.Sprintf("✓ Applied fixes for %d finding(s).", m.fixedCount)) + "\n\n" +
		dimStyle.Render("Files were edited in your working tree — review with `git diff`, then commit.")
}

func (m model) findingsBody() string {
	if m.err != nil {
		return errStyle.Render("Review failed") + "\n\n" + dimStyle.Render(clip(m.err.Error(), m.width))
	}
	if len(m.items) == 0 {
		if m.rawText != "" {
			return titleStyle.Render("Review (unstructured)") + "\n\n" + m.rawText
		}
		return okStyle.Render("✓ No blocking findings.")
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
		act := actionStyle(f.Action).Render(padVisible(string(f.Action), 8))
		fmt.Fprintf(&b, "%s%s %s %s  %s\n", marker, box, sevStyle(f.Severity).Render(sevLabel(f.Severity)), act, clip(title, m.width-25))
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
	header := sevStyle(sel.Severity).Render(loc) + dimStyle.Render("  ·  ") + actionStyle(sel.Action).Render(string(sel.Action))
	b.WriteString(boxStyle.Width(inner+2).Render(header+"\n"+detail))
	return b.String()
}

func (m model) footer() string {
	switch m.phase {
	case phaseRunning, phaseFixing:
		return "q abort"
	case phaseDone:
		if len(m.items) == 0 {
			return "q quit"
		}
		if m.agentCfg != nil {
			return "j/k move · space select · A all · N none · f fix selected (edits files) · q quit"
		}
		return "j/k move · space select · q quit"
	default:
		return "q quit"
	}
}

// --- helpers -------------------------------------------------------------

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
