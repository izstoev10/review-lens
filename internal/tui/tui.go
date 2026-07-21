// Package tui renders a review in an interactive terminal UI built with
// bubbletea (the Elm architecture: Model state, Update on messages, View to a
// string). It has two phases: a live "running" view that streams the agent's
// activity while it works, then a findings viewer once results arrive.
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

// --- messages sent into the program --------------------------------------

type activityMsg string
type doneMsg struct {
	result string
	err    error
}
type tickMsg time.Time

func tick() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// RunReview streams a review live: it starts the agent in a goroutine, feeds
// each activity and the final result into the TUI, and shows findings when done.
func RunReview(dir string, a *config.Agent, prompt, title string) error {
	m := newModel(title)
	p := tea.NewProgram(m, tea.WithAltScreen())
	go func() {
		result, err := agent.StreamReview(dir, a, prompt, func(act string) {
			p.Send(activityMsg(act))
		})
		p.Send(doneMsg{result: result, err: err})
	}()
	_, err := p.Run()
	return err
}

// Show displays already-computed findings (no live phase). Used when the agent
// doesn't stream but we're still interactive.
func Show(items []findings.Finding) error {
	m := newModel("")
	m.phase = phaseDone
	m.items = items
	p := tea.NewProgram(m, tea.WithAltScreen())
	_, err := p.Run()
	return err
}

// --- model ---------------------------------------------------------------

type phase int

const (
	phaseRunning phase = iota
	phaseDone
)

type model struct {
	title   string
	phase   phase
	spinner spinner.Model

	// running phase
	activities []string
	start      time.Time
	elapsed    time.Duration

	// done phase
	items    []findings.Finding
	rawText  string // shown when the result wasn't parseable findings
	err      error
	cursor   int
	width    int
	height   int
}

func newModel(title string) model {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = lipgloss.NewStyle().Foreground(lipgloss.Color("13"))
	return model{
		title:   title,
		phase:   phaseRunning,
		spinner: s,
		start:   time.Now(),
		width:   80,
		height:  24,
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, tick())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height

	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "j", "down":
			if m.phase == phaseDone && m.cursor < len(m.items)-1 {
				m.cursor++
			}
		case "k", "up":
			if m.phase == phaseDone && m.cursor > 0 {
				m.cursor--
			}
		case "g", "home":
			m.cursor = 0
		case "G", "end":
			if len(m.items) > 0 {
				m.cursor = len(m.items) - 1
			}
		}

	case activityMsg:
		m.activities = append(m.activities, string(msg))

	case doneMsg:
		m.phase = phaseDone
		m.err = msg.err
		if list, ok := findings.Parse(msg.result); ok {
			m.items = list
		} else {
			m.rawText = strings.TrimSpace(msg.result)
		}
		return m, nil

	case tickMsg:
		if m.phase == phaseRunning {
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

// --- styling -------------------------------------------------------------

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	cursorStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))
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
	if m.phase == phaseRunning {
		return m.runningView()
	}
	return m.doneView()
}

func (m model) runningView() string {
	var b strings.Builder
	head := m.title
	if head == "" {
		head = "Reviewing changes"
	}
	fmt.Fprintf(&b, "%s %s  %s\n\n",
		m.spinner.View(),
		titleStyle.Render(head),
		dimStyle.Render(fmt.Sprintf("(%ds)", int(m.elapsed.Seconds()))),
	)

	// Show the tail of the activity feed that fits the screen.
	max := m.height - 6
	if max < 3 {
		max = 3
	}
	acts := m.activities
	if len(acts) > max {
		acts = acts[len(acts)-max:]
		b.WriteString(dimStyle.Render(fmt.Sprintf("  … %d earlier\n", len(m.activities)-max)))
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

func (m model) doneView() string {
	if m.err != nil {
		return errStyle.Render("Review failed") + "\n\n" +
			dimStyle.Render(clip(m.err.Error(), m.width)) + "\n\n" +
			footerStyle.Render("q quit")
	}
	if len(m.items) == 0 {
		if m.rawText != "" {
			return titleStyle.Render("Review (unstructured)") + "\n\n" +
				m.rawText + "\n\n" + footerStyle.Render("q quit")
		}
		return okStyle.Render("✓ No blocking findings.") + "\n\n" + footerStyle.Render("q quit")
	}

	var b strings.Builder
	e, w, i := count(m.items)
	b.WriteString(titleStyle.Render("Findings") + dimStyle.Render("  "+summary(e, w, i)) + "\n\n")

	for idx, f := range m.items {
		marker := "  "
		title := f.Title
		if idx == m.cursor {
			marker = cursorStyle.Render("> ")
			title = titleStyle.Render(title)
		}
		fmt.Fprintf(&b, "%s%s  %s\n", marker, sevStyle(f.Severity).Render(sevLabel(f.Severity)), clip(title, m.width-12))
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
	b.WriteString(footerStyle.Render("j/k move · g/G top/bottom · q quit"))
	return b.String()
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
