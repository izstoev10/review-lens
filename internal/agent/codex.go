package agent

import (
	"bufio"
	"encoding/json"
	"io"
	"sort"
	"strings"
)

// Codex's `exec --json` prints one JSON event per line. Two generations of the
// protocol exist in the wild and this parser accepts both:
//
//   - the item envelope: {"type":"item.completed","item":{"type":"agent_message",
//     "text":"..."}} with item types like reasoning, command_execution,
//     file_change, web_search and mcp_tool_call (older builds spell the item's
//     type "item_type");
//   - the legacy msg envelope: {"msg":{"type":"agent_message","message":"..."}}
//     with begin/end pairs such as exec_command_begin and a final task_complete
//     carrying last_agent_message.
//
// Unknown events are skipped, so a Codex upgrade degrades to fewer activity
// lines rather than a parse failure.

// codexItem is one unit of work in the item envelope.
type codexItem struct {
	Type     string `json:"type"`
	ItemType string `json:"item_type"` // older spelling of Type
	Text     string `json:"text"`      // agent_message, reasoning
	Command  string `json:"command"`   // command_execution
	Query    string `json:"query"`     // web_search
	Server   string `json:"server"`    // mcp_tool_call
	Tool     string `json:"tool"`
	Changes  []struct {
		Path string `json:"path"`
	} `json:"changes"` // file_change
}

func (it codexItem) kind() string {
	if it.Type != "" {
		return it.Type
	}
	return it.ItemType
}

// codexMsg is the payload of the legacy msg envelope.
type codexMsg struct {
	Type             string   `json:"type"`
	Message          string   `json:"message"`            // agent_message, error
	Text             string   `json:"text"`               // agent_reasoning
	Model            string   `json:"model"`              // session_configured
	Command          []string `json:"command"`            // exec_command_begin
	Query            string   `json:"query"`              // web_search_begin
	LastAgentMessage string   `json:"last_agent_message"` // task_complete
	Invocation       *struct {
		Server string `json:"server"`
		Tool   string `json:"tool"`
	} `json:"invocation"` // mcp_tool_call_begin
	Changes map[string]json.RawMessage `json:"changes"` // patch_apply_begin
}

// codexEvent is the union of both envelopes; at most one of Item/Msg is set
// per line.
type codexEvent struct {
	Type    string     `json:"type"`
	Message string     `json:"message"` // top-level error events
	Item    *codexItem `json:"item"`
	Msg     *codexMsg  `json:"msg"`
}

// parseCodexStream reads Codex JSONL lines, emits activity for the work items,
// and returns the agent's final message — never the surrounding transport
// events, so diagnostics can't leak into the findings parser.
func parseCodexStream(r io.Reader, activity onActivity) string {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024) // large lines (command output)
	emit := func(s string) {
		if activity != nil && s != "" {
			activity(s)
		}
	}

	var final string
	for sc.Scan() {
		var ev codexEvent
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue // ignore non-JSON / partial lines
		}
		var text, act string
		switch {
		case ev.Item != nil:
			text, act = codexItemEvent(ev.Type, *ev.Item)
		case ev.Msg != nil:
			text, act = codexMsgEvent(*ev.Msg)
		case ev.Type == "error":
			act = "error · " + snippet(ev.Message)
		}
		emit(act)
		if text != "" {
			final = text
		}
	}
	return strings.TrimSpace(final)
}

// codexItemEvent translates one item-envelope event into the final text and/or
// an activity line. Action items announce themselves when they start; text
// items (reasoning, the answer) only exist complete.
func codexItemEvent(evType string, it codexItem) (final, act string) {
	switch kind := it.kind(); {
	case evType == "item.completed" && kind == "agent_message":
		return it.Text, ""
	case evType == "item.completed" && kind == "reasoning":
		return "", "thinking · " + snippet(it.Text)
	case evType != "item.started":
		return "", ""
	case kind == "command_execution":
		return "", "run " + firstLine(truncate(it.Command, 60))
	case kind == "file_change":
		paths := make([]string, 0, len(it.Changes))
		for _, c := range it.Changes {
			paths = append(paths, c.Path)
		}
		return "", "edit " + strings.Join(paths, ", ")
	case kind == "web_search":
		return "", "search " + snippet(it.Query)
	case kind == "mcp_tool_call":
		return "", "tool " + it.Server + "." + it.Tool
	}
	return "", ""
}

// codexMsgEvent does the same for the legacy msg envelope. agent_message and
// task_complete's last_agent_message both carry the answer; Codex emits
// task_complete last, so it wins in parseCodexStream.
func codexMsgEvent(msg codexMsg) (final, act string) {
	switch msg.Type {
	case "session_configured":
		// Immediate feedback so the feed isn't empty during first-token latency.
		return "", "connected · " + msg.Model
	case "agent_reasoning":
		return "", "thinking · " + snippet(msg.Text)
	case "exec_command_begin":
		return "", "run " + firstLine(truncate(strings.Join(msg.Command, " "), 60))
	case "patch_apply_begin":
		paths := make([]string, 0, len(msg.Changes))
		for p := range msg.Changes {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		return "", "edit " + strings.Join(paths, ", ")
	case "web_search_begin":
		return "", "search " + snippet(msg.Query)
	case "mcp_tool_call_begin":
		if msg.Invocation == nil {
			return "", ""
		}
		return "", "tool " + msg.Invocation.Server + "." + msg.Invocation.Tool
	case "agent_message":
		return msg.Message, ""
	case "task_complete":
		return msg.LastAgentMessage, ""
	case "error":
		return "", "error · " + snippet(msg.Message)
	}
	return "", ""
}
