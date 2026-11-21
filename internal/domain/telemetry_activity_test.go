package domain_test

import (
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

// Task 18.3, spec observability, "Captured agent log line with code":
// only the event type and tool name are taken from a line.
func TestActivityEvent(t *testing.T) {
	for _, c := range []struct {
		name, line, event, tool string
	}{
		{"claude tool use", `{"type":"assistant","message":{"content":[{"type":"text","text":"Now I write it."},{"type":"tool_use","name":"Write","input":{"content":"package main"}}]}}`, "assistant", "Write"},
		{"claude init", `{"type":"system","subtype":"init","cwd":"/home/ann/w","tools":["Bash"]}`, "system:init", ""},
		{"claude result", `{"type":"result","subtype":"success","result":"Done: the fix is in."}`, "result:success", ""},
		{"codex mcp call", `{"type":"item.started","item":{"id":"item_1","type":"mcp_tool_call","server":"anthill","tool":"get_task"}}`, "item.started:mcp_tool_call", "get_task"},
		{"codex command", `{"type":"item.completed","item":{"type":"command_execution","command":"cat .env","aggregated_output":"TOKEN=x"}}`, "item.completed:command_execution", ""},
		{"opencode tool", `{"type":"tool_use","part":{"type":"tool","tool":"anthill_push","state":{"output":"pushed"}}}`, "tool_use", "anthill_push"},
		{"stderr", "panic: secret value in /home/ann/main.go", domain.ActivityEventStderr, ""},
		{"not an object", `["type","x"]`, domain.ActivityEventStderr, ""},
		{"content as type", `{"type":"rm -rf / && echo done"}`, domain.ActivityEventStderr, ""},
		{"content as tool name", `{"type":"assistant","message":{"content":[{"type":"tool_use","name":"func main() { leak() }"}]}}`, "assistant", ""},
	} {
		t.Run(c.name, func(t *testing.T) {
			event, tool := domain.ActivityEvent(c.line)
			if event != c.event || tool != c.tool {
				t.Errorf("ActivityEvent = %q, %q; want %q, %q", event, tool, c.event, c.tool)
			}
		})
	}
	long := `{"type":"` + strings.Repeat("a", 65) + `"}`
	if event, _ := domain.ActivityEvent(long); event != domain.ActivityEventStderr {
		t.Errorf("a 65-character type passed: %q", event)
	}
}
