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
