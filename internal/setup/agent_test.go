package setup

import (
	"bytes"
	"errors"
	"slices"
	"strings"
	"testing"
)

var wantCodexCommand = []string{
	"codex", "--ask-for-approval", "never",
	"exec", "--sandbox", "workspace-write",
}

func requireCodexAgent(t *testing.T, agentCommand []string) {
	t.Helper()
	if !slices.Equal(agentCommand, wantCodexCommand) {
		t.Fatalf("got command %q, want %q", agentCommand, wantCodexCommand)
	}
}

func pathLookup(installed ...string) func(string) (string, error) {
	return func(name string) (string, error) {
		for _, candidate := range installed {
			if name == candidate {
				return "/bin/" + name, nil
			}
		}
		return "", errors.New("not found")
	}
}

func agentNames(options []agentOption) []string {
	names := make([]string, 0, len(options))
	for _, option := range options {
		names = append(names, option.name)
	}
	return names
}

func TestInstalledAgentsKeepsPreferenceOrder(t *testing.T) {
	// Installed in the opposite order to agentOptions: preference, not PATH
	// order, decides the menu.
	got := agentNames(installedAgents(pathLookup("codex", "claude")))

	want := []string{"Claude Code", "Codex"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestInstalledAgentsSkipsMissingBinaries(t *testing.T) {
	got := agentNames(installedAgents(pathLookup("codex")))

	want := []string{"Codex"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestInstalledAgentsWithEmptyPath(t *testing.T) {
	if got := installedAgents(pathLookup()); len(got) != 0 {
		t.Fatalf("got %q, want no agents", agentNames(got))
	}
}

func TestParseAgentChoice(t *testing.T) {
	// noneChoice of 3 models two installed agents plus None.
	const noneChoice = 3

	tests := []struct {
		name   string
		answer string
		want   int
		wantOK bool
	}{
		{name: "blank takes the default", answer: "", want: defaultChoice, wantOK: true},
		{name: "whitespace takes the default", answer: "  \t ", want: defaultChoice, wantOK: true},
		{name: "first agent", answer: "1", want: 1, wantOK: true},
		{name: "second agent", answer: "2", want: 2, wantOK: true},
		{name: "none", answer: "3", want: noneChoice, wantOK: true},
		{name: "surrounding whitespace is trimmed", answer: " 2 ", want: 2, wantOK: true},
		{name: "zero is below range", answer: "0"},
		{name: "negative is below range", answer: "-1"},
		{name: "past none is above range", answer: "4"},
		{name: "not a number", answer: "codex"},
		{name: "trailing garbage", answer: "2x"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := parseAgentChoice(test.answer, noneChoice)
			if ok != test.wantOK {
				t.Fatalf("parseAgentChoice(%q) ok = %t, want %t", test.answer, ok, test.wantOK)
			}
			if got != test.want {
				t.Fatalf("parseAgentChoice(%q) = %d, want %d", test.answer, got, test.want)
			}
		})
	}
}

func TestAgentForChoiceResolvesInstalledAgent(t *testing.T) {
	installed := installedAgents(pathLookup("claude", "codex"))

	agent := agentForChoice(installed, 2)
	if agent == nil {
		t.Fatal("got nil agent, want Codex")
	}
	requireCodexAgent(t, agent.Cmd)
}

func TestAgentForChoiceNoneYieldsNoAgent(t *testing.T) {
	installed := installedAgents(pathLookup("claude", "codex"))

	if agent := agentForChoice(installed, len(installed)+1); agent != nil {
		t.Fatalf("got agent %#v, want nil", agent)
	}
}

func TestSelectAgentInteractiveDefaultsToOnlyInstalledAgent(t *testing.T) {
	var out bytes.Buffer

	agent, err := SelectAgent(strings.NewReader("\n"), &out, true, pathLookup("codex"))
	if err != nil {
		t.Fatal(err)
	}
	if agent == nil {
		t.Fatal("got nil agent, want Codex")
	}
	requireCodexAgent(t, agent.Cmd)
	if !strings.Contains(out.String(), "1) Codex") {
		t.Fatalf("prompt did not list detected Codex agent:\n%s", out.String())
	}
}

func TestSelectAgentInteractiveUsesExplicitChoice(t *testing.T) {
	var out bytes.Buffer

	agent, err := SelectAgent(strings.NewReader("2\n"), &out, true, pathLookup("claude", "codex"))
	if err != nil {
		t.Fatal(err)
	}
	if agent == nil {
		t.Fatal("got nil agent, want Codex")
	}
	requireCodexAgent(t, agent.Cmd)
}

func TestSelectAgentInteractiveCanDisableAgent(t *testing.T) {
	var out bytes.Buffer

	agent, err := SelectAgent(strings.NewReader("3\n"), &out, true, pathLookup("claude", "codex"))
	if err != nil {
		t.Fatal(err)
	}
	if agent != nil {
		t.Fatalf("got agent %#v, want nil", agent)
	}
}

func TestSelectAgentNonInteractiveDetectsInstalledAgent(t *testing.T) {
	agent, err := SelectAgent(strings.NewReader(""), &bytes.Buffer{}, false, pathLookup("codex"))
	if err != nil {
		t.Fatal(err)
	}
	if agent == nil {
		t.Fatal("got nil agent, want Codex")
	}
	requireCodexAgent(t, agent.Cmd)
}

func TestSelectAgentWithNoInstalledAgentDisablesAgent(t *testing.T) {
	var out bytes.Buffer

	agent, err := SelectAgent(strings.NewReader(""), &out, true, pathLookup())
	if err != nil {
		t.Fatal(err)
	}
	if agent != nil {
		t.Fatalf("got agent %#v, want nil", agent)
	}
	if !strings.Contains(out.String(), "No supported agent CLI found") {
		t.Fatalf("missing no-agent explanation:\n%s", out.String())
	}
}
