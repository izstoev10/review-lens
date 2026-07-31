// Package setup handles the interactive choices made by review-lens init.
package setup

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/izstoev10/review-lens/internal/config"
)

type agentOption struct {
	name     string
	binary   string
	newAgent func() *config.Agent
}

var agentOptions = []agentOption{
	{name: "Claude Code", binary: "claude", newAgent: config.ClaudeAgent},
	{name: "Codex", binary: "codex", newAgent: config.CodexAgent},
}

// defaultChoice is the menu entry taken when the answer is blank or input ends,
// keeping Claude the preferred streaming experience when it is installed.
const defaultChoice = 1

// SelectAgent discovers supported agent CLIs and chooses the config written by
// review-lens init. Interactive callers can choose among installed agents or
// disable agent features. Non-interactive callers use the first installed
// supported agent.
func SelectAgent(
	in io.Reader,
	out io.Writer,
	interactive bool,
	lookPath func(string) (string, error),
) (*config.Agent, error) {
	installed := installedAgents(lookPath)
	switch {
	case len(installed) == 0:
		if interactive {
			fmt.Fprintln(out, "No supported agent CLI found; continuing without AI fixes or reviews.")
		}
		return nil, nil
	case !interactive:
		return agentForChoice(installed, defaultChoice), nil
	default:
		return promptForAgent(in, out, installed)
	}
}

// installedAgents returns the supported agents whose CLI is on PATH, keeping
// the preference order of agentOptions.
func installedAgents(lookPath func(string) (string, error)) []agentOption {
	installed := make([]agentOption, 0, len(agentOptions))
	for _, option := range agentOptions {
		if _, err := lookPath(option.binary); err == nil {
			installed = append(installed, option)
		}
	}
	return installed
}

// promptForAgent asks until the answer names an installed agent or None. Input
// that ends without an answer selects the default rather than failing.
func promptForAgent(in io.Reader, out io.Writer, installed []agentOption) (*config.Agent, error) {
	noneChoice := printAgentMenu(out, installed)

	scanner := bufio.NewScanner(in)
	for {
		fmt.Fprint(out, "Agent [1]: ")
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return nil, fmt.Errorf("reading agent choice: %w", err)
			}
			return agentForChoice(installed, defaultChoice), nil
		}
		choice, ok := parseAgentChoice(scanner.Text(), noneChoice)
		if !ok {
			fmt.Fprintf(out, "Enter a number from 1 to %d.\n", noneChoice)
			continue
		}
		return agentForChoice(installed, choice), nil
	}
}

// printAgentMenu lists the installed agents followed by a None entry, and
// returns the choice that disables agent features.
func printAgentMenu(out io.Writer, installed []agentOption) int {
	fmt.Fprintln(out, "Choose an installed agent for AI fixes and reviews:")
	for i, option := range installed {
		fmt.Fprintf(out, "  %d) %s\n", i+1, option.name)
	}
	noneChoice := len(installed) + 1
	fmt.Fprintf(out, "  %d) None\n", noneChoice)
	return noneChoice
}

// parseAgentChoice maps a menu answer onto a 1-based choice, treating a blank
// answer as the default. It reports whether the answer was a choice on offer.
func parseAgentChoice(answer string, noneChoice int) (int, bool) {
	answer = strings.TrimSpace(answer)
	if answer == "" {
		return defaultChoice, true
	}
	choice, err := strconv.Atoi(answer)
	if err != nil || choice < 1 || choice > noneChoice {
		return 0, false
	}
	return choice, true
}

// agentForChoice resolves a choice already validated by parseAgentChoice. The
// entry past the last installed agent is None, which yields no agent.
func agentForChoice(installed []agentOption, choice int) *config.Agent {
	if choice > len(installed) {
		return nil
	}
	return installed[choice-1].newAgent()
}
