// Package tui renders review findings in an interactive terminal UI built with
// bubbletea. Bubbletea follows the Elm architecture: a Model holds state,
// Update returns a new Model in response to messages (key presses, resize), and
// View renders the Model to a string. The runtime loops those for you.
package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/izstoev10/review-lens/internal/findings"
)

// Show runs the interactive findings viewer, blocking until the user quits.
func Show(items []findings.Finding) error {
	// WithAltScreen gives us a full-screen buffer that's restored on exit, so
	// the viewer doesn't scroll the user's scrollback away.
	p := tea.NewProgram(newModel(items), tea.WithAltScreen())
	_, err := p.Run()
	return err
}

type model struct {
	items         []findings.Finding
	cursor        int // index of the selected finding
	width, height int
}

func newModel(items []findings.Finding) model {
	return model{items: items, width: 80, height: 24}
}

func (m model) Init() tea.Cmd { return nil }

// Update handles messages. It returns the (possibly changed) model and an
// optional command; tea.Quit tells the runtime to exit.
func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
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
			m.cursor = len(m.items) - 1
		}
	}
	return m, nil
}

// --- styling -------------------------------------------------------------

var (
	titleStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("15"))
	dimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	cursorStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("13"))
	footerStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("244")).MarginTop(1)
	boxStyle    = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("240")).
			Padding(0, 1).
			MarginTop(1)
)

func sevStyle(s findings.Severity) lipgloss.Style {
	color := lipgloss.Color("36") // info: cyan
	switch s {
	case findings.Error:
		color = lipgloss.Color("9") // red
	case findings.Warning:
		color = lipgloss.Color("11") // yellow
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

// View renders the current state.
func (m model) View() string {
	if len(m.items) == 0 {
		return lipgloss.NewStyle().Foreground(lipgloss.Color("10")).Render("✓ No blocking findings.") + "\n"
	}

	var b strings.Builder

	// Header with severity summary.
	e, w, i := count(m.items)
	b.WriteString(titleStyle.Render("Findings") + dimStyle.Render("  "+summary(e, w, i)) + "\n\n")

	// List: one line per finding, selected one marked.
	for idx, f := range m.items {
		marker := "  "
		label := sevStyle(f.Severity).Render(sevLabel(f.Severity))
		title := f.Title
		if idx == m.cursor {
			marker = cursorStyle.Render("> ")
			title = titleStyle.Render(title)
		}
		b.WriteString(fmt.Sprintf("%s%s  %s\n", marker, label, title))
	}

	// Detail box for the selected finding.
	sel := m.items[m.cursor]
	loc := sel.File
	if sel.Line > 0 {
		loc = fmt.Sprintf("%s:%d", sel.File, sel.Line)
	}
	inner := m.width - 6 // account for border + padding
	if inner < 20 {
		inner = 20
	}
	detail := lipgloss.NewStyle().Width(inner).Render(strings.TrimSpace(sel.Detail))
	box := boxStyle.Width(inner + 2).Render(sevStyle(sel.Severity).Render(loc) + "\n" + detail)
	b.WriteString(box + "\n")

	b.WriteString(footerStyle.Render("j/k move · g/G top/bottom · q quit"))
	return b.String()
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
	return strings.Join(parts, ", ")
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}
