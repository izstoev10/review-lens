package agent

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/izstoev10/review-lens/internal/config"
)

func collectStream(t *testing.T, parse func(r io.Reader, activity onActivity) string, transcript string) (final string, acts []string) {
	t.Helper()
	final = parse(strings.NewReader(transcript), func(a string) { acts = append(acts, a) })
	return final, acts
}

// Claude's stream-json: activity from tool uses, the answer from the result
// event only — never from intermediate assistant text.
func TestParseStreamClaude(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"system","subtype":"init","model":"claude-sonnet-5"}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"main.go"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"Here are my findings, in the requested format."}]}}`,
		`{"type":"result","result":"[{\"severity\":\"info\"}]"}`,
	}, "\n")
	final, acts := collectStream(t, parseStream, transcript)

	if final != `[{"severity":"info"}]` {
		t.Errorf("final = %q, want the result event's payload", final)
	}
	want := []string{"connected · claude-sonnet-5", "read main.go", "Here are my findings, in the requested format."}
	if !slices.Equal(acts, want) {
		t.Errorf("activities = %q, want %q", acts, want)
	}
}

// The current Codex item envelope: commands announce themselves when they
// start, reasoning and the final message arrive complete.
func TestParseCodexStreamItemEnvelope(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"thread.started","thread_id":"t1"}`,
		`{"type":"item.started","item":{"type":"command_execution","command":"go test ./..."}}`,
		`{"type":"item.completed","item":{"type":"command_execution","command":"go test ./...","exit_code":0}}`,
		`{"type":"item.completed","item":{"type":"reasoning","text":"The diff only touches the parser."}}`,
		`{"type":"item.started","item":{"type":"file_change","changes":[{"path":"a.go"},{"path":"b.go"}]}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"[]"}}`,
		`{"type":"turn.completed","usage":{"input_tokens":10}}`,
	}, "\n")
	final, acts := collectStream(t, parseCodexStream, transcript)

	if final != "[]" {
		t.Errorf("final = %q, want the agent_message text only", final)
	}
	want := []string{"run go test ./...", "thinking · The diff only touches the parser.", "edit a.go, b.go"}
	if !slices.Equal(acts, want) {
		t.Errorf("activities = %q, want %q", acts, want)
	}
}

// Older Codex builds spell the item's type "item_type"; both must parse.
func TestParseCodexStreamItemTypeSpelling(t *testing.T) {
	final, _ := collectStream(t, parseCodexStream,
		`{"type":"item.completed","item":{"item_type":"agent_message","text":"done"}}`)
	if final != "done" {
		t.Errorf("final = %q, want %q", final, "done")
	}
}

// The legacy msg envelope: begin events drive activity, task_complete carries
// the authoritative final message (it repeats the last agent_message).
func TestParseCodexStreamLegacyMsgEnvelope(t *testing.T) {
	transcript := strings.Join([]string{
		`{"id":"0","msg":{"type":"session_configured","model":"gpt-5-codex"}}`,
		`{"id":"1","msg":{"type":"task_started"}}`,
		`{"id":"2","msg":{"type":"agent_reasoning","text":"Reading the diff first."}}`,
		`{"id":"3","msg":{"type":"exec_command_begin","command":["bash","-lc","ls"]}}`,
		`{"id":"4","msg":{"type":"exec_command_end","exit_code":0}}`,
		`{"id":"5","msg":{"type":"agent_message","message":"almost the answer"}}`,
		`{"id":"6","msg":{"type":"token_count","input_tokens":12}}`,
		`{"id":"7","msg":{"type":"task_complete","last_agent_message":"[]"}}`,
	}, "\n")
	final, acts := collectStream(t, parseCodexStream, transcript)

	if final != "[]" {
		t.Errorf("final = %q, want task_complete's last_agent_message", final)
	}
	want := []string{"connected · gpt-5-codex", "thinking · Reading the diff first.", "run bash -lc ls"}
	if !slices.Equal(acts, want) {
		t.Errorf("activities = %q, want %q", acts, want)
	}
}

// Raw protocol noise — malformed lines, unknown events — must be skipped, not
// surfaced or fatal.
func TestParseCodexStreamSkipsNoise(t *testing.T) {
	transcript := strings.Join([]string{
		`not json at all`,
		`{"type":"some.future.event","payload":{"x":1}}`,
		`{"type":"item.completed","item":{"type":"agent_message","text":"ok"}}`,
	}, "\n")
	final, acts := collectStream(t, parseCodexStream, transcript)
	if final != "ok" {
		t.Errorf("final = %q, want %q", final, "ok")
	}
	if len(acts) != 0 {
		t.Errorf("activities = %q, want none — noise must not leak into the feed", acts)
	}
}

func TestDetectFormat(t *testing.T) {
	tests := []struct {
		name string
		a    *config.Agent
		want streamFormat
	}{
		{"nil agent", nil, formatPlain},
		{"claude stream-json", config.ClaudeAgent(), formatClaude},
		{"codex with --json", config.CodexAgent(), formatCodex},
		{"codex without --json", &config.Agent{Cmd: []string{"codex", "exec"}}, formatPlain},
		{"--json on a non-codex binary", &config.Agent{Cmd: []string{"mytool", "--json"}}, formatPlain},
		{"codex by path", &config.Agent{Cmd: []string{"/usr/local/bin/codex", "exec", "--json"}}, formatCodex},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := detectFormat(tt.a); got != tt.want {
				t.Errorf("detectFormat = %v, want %v", got, tt.want)
			}
		})
	}
}

// An agent with no event stream still has a transport contract: stdout is the
// answer, stderr is diagnostics. Concatenating them (the old CombinedOutput
// behaviour) let log noise corrupt the findings JSON.
func TestPlainAgentKeepsDiagnosticsOutOfTheAnswer(t *testing.T) {
	a := &config.Agent{Cmd: []string{"sh", "-c", `echo "[warning] flux capacitor low" >&2; echo '[]'`}}
	var acts []string
	got, err := StreamReview(context.Background(), t.TempDir(), a, "prompt", func(s string) { acts = append(acts, s) })
	if err != nil {
		t.Fatal(err)
	}
	if got != "[]" {
		t.Errorf("result = %q, want stdout only", got)
	}
	// The one activity line is the waiting notice, so a live UI shows a blind
	// wait instead of an apparent stall.
	if len(acts) != 1 || !strings.Contains(acts[0], "waiting") {
		t.Errorf("activities = %q, want a single waiting notice", acts)
	}
}

// A failing plain agent surfaces its stderr in the error, where diagnostics
// belong.
func TestPlainAgentFailureCarriesDiagnostics(t *testing.T) {
	a := &config.Agent{Cmd: []string{"sh", "-c", `echo "auth expired" >&2; exit 1`}}
	_, err := StreamReview(context.Background(), t.TempDir(), a, "prompt", nil)
	if err == nil || !strings.Contains(err.Error(), "auth expired") {
		t.Errorf("err = %v, want it to carry the agent's stderr", err)
	}
}

// End to end through execAgent: a binary recognised as Codex has its JSONL
// parsed, returning the final message rather than the raw event transcript.
func TestCodexBinaryStreamsEndToEnd(t *testing.T) {
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		`echo '{"msg":{"type":"exec_command_begin","command":["ls"]}}'` + "\n" +
		`echo '{"msg":{"type":"agent_message","message":"[]"}}'` + "\n"
	bin := filepath.Join(dir, "codex")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}

	a := &config.Agent{Cmd: []string{bin, "exec", "--json"}}
	var acts []string
	got, err := StreamReview(context.Background(), t.TempDir(), a, "prompt", func(s string) { acts = append(acts, s) })
	if err != nil {
		t.Fatal(err)
	}
	if got != "[]" {
		t.Errorf("result = %q, want the agent message only, not the event transcript", got)
	}
	if want := []string{"run ls"}; !slices.Equal(acts, want) {
		t.Errorf("activities = %q, want %q", acts, want)
	}
}
