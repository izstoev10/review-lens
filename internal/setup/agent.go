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

// SelectAgent discovers supported agent CLIs and chooses the config written by
// review-lens init. Interactive callers can choose among installed agents or
// disable agent features. Non-interactive callers use the first installed
// supported agent, preserving Claude as the preferred streaming experience.
func SelectAgent(
	in io.Reader,
	out io.Writer,
	interactive bool,
	lookPath func(string) (string, error),
) (*config.Agent, error) {
	available := make([]agentOption, 0, len(agentOptions))
	for _, option := range agentOptions {
		if _, err := lookPath(option.binary); err == nil {
			available = append(available, option)
		}
	}

	if len(available) == 0 {
		if interactive {
			fmt.Fprintln(out, "No supported agent CLI found; continuing without AI fixes or reviews.")
		}
		return nil, nil
	}
	if !interactive {
		return available[0].newAgent(), nil
	}

	fmt.Fprintln(out, "Choose an installed agent for AI fixes and reviews:")
	for i, option := range available {
		fmt.Fprintf(out, "  %d) %s\n", i+1, option.name)
	}
	noneChoice := len(available) + 1
	fmt.Fprintf(out, "  %d) None\n", noneChoice)

	scanner := bufio.NewScanner(in)
	for {
		fmt.Fprint(out, "Agent [1]: ")
		if !scanner.Scan() {
			if err := scanner.Err(); err != nil {
				return nil, fmt.Errorf("reading agent choice: %w", err)
			}
			return available[0].newAgent(), nil
		}
		answer := strings.TrimSpace(scanner.Text())
		if answer == "" {
			return available[0].newAgent(), nil
		}
		choice, err := strconv.Atoi(answer)
		switch {
		case err != nil, choice < 1, choice > noneChoice:
			fmt.Fprintf(out, "Enter a number from 1 to %d.\n", noneChoice)
		case choice == noneChoice:
			return nil, nil
		default:
			return available[choice-1].newAgent(), nil
		}
	}
}
