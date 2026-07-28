package tui

import (
	"fmt"
	"os/exec"
	"runtime"
	"strings"

	"github.com/izstoev10/review-lens/internal/findings"
)

// copyPrompt turns a finding into a paste-ready prompt for another agent/tab, so
// the reviewer can dig into it with wider context instead of only choosing
// fix-or-not here.
func copyPrompt(f findings.Finding) string {
	loc := f.File
	if f.Line > 0 {
		loc = fmt.Sprintf("%s:%d", f.File, f.Line)
	}
	return fmt.Sprintf(`A code review flagged the following in %s:

[%s] %s
%s

Look into this in the context of the wider codebase and tell me whether it's a real problem and, if so, the best way to address it.`, loc, f.Severity, f.Title, strings.TrimSpace(f.Detail))
}

// clipboardCopy writes text to the system clipboard by piping it to the
// platform's clipboard tool. Consistent with the rest of the tool, it shells out
// to a real binary rather than pulling in a clipboard dependency.
func clipboardCopy(text string) error {
	name, args, ok := clipboardCommand(runtime.GOOS)
	if !ok {
		return fmt.Errorf("no clipboard tool found (install wl-clipboard, xclip, or xsel)")
	}
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(text)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s: %w", name, err)
	}
	return nil
}

// clipboardCommand returns the clipboard-write command for goos. macOS and
// Windows are fixed; Linux/BSD probe for an available tool (Wayland first, then
// X11). Split out from clipboardCopy so the per-OS choice is unit-testable.
func clipboardCommand(goos string) (name string, args []string, ok bool) {
	switch goos {
	case "darwin":
		return "pbcopy", nil, true
	case "windows":
		return "clip", nil, true
	default:
		for _, c := range []struct {
			name string
			args []string
		}{
			{"wl-copy", nil},
			{"xclip", []string{"-selection", "clipboard"}},
			{"xsel", []string{"--clipboard", "--input"}},
		} {
			if _, err := exec.LookPath(c.name); err == nil {
				return c.name, c.args, true
			}
		}
		return "", nil, false
	}
}
