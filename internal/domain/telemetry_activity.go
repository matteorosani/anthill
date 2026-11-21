package domain

import (
	"encoding/json"
	"strings"
)

// ActivityEventStderr is the event of an activity line that is not a
// JSON event. The runners write their events to standard output as
// JSON, so such a line is one of the agent's standard error; it is
// exported without its text.
const ActivityEventStderr = "stderr"

// maxActivityName bounds an event or tool name taken from a line.
const maxActivityName = 64

// ActivityEvent reads the event type and the tool name, if any, of one
// line of an agent's activity, for telemetry (spec observability,
// "Content is never exported"). It takes only those: never text, code,
// commands, arguments or results. It understands the JSON events of the
// runners Anthill supports:
//
//   - Claude Code: "type" and "subtype", and the name of a tool_use block
//     of "message.content";
//   - Codex: "type" and "item.type", and "item.tool";
//   - OpenCode: "type", and "part.tool".
//
// A line that is not a JSON object gives ActivityEventStderr and no tool.
// A name that is not a short identifier is dropped, so a field holding
// content in an unexpected format never passes.
func ActivityEvent(line string) (event, tool string) {
	var ev struct {
		Type    string `json:"type"`
		Subtype string `json:"subtype"`
		Message *struct {
			Content []struct {
				Type string `json:"type"`
				Name string `json:"name"`
			} `json:"content"`
		} `json:"message"`
		Item *struct {
			Type string `json:"type"`
			Tool string `json:"tool"`
		} `json:"item"`
		Part *struct {
			Tool string `json:"tool"`
		} `json:"part"`
	}
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "{") || json.Unmarshal([]byte(line), &ev) != nil {
		return ActivityEventStderr, ""
	}
	event = activityName(ev.Type)
	if event == "" {
		return ActivityEventStderr, ""
	}
	switch {
	case ev.Item != nil && activityName(ev.Item.Type) != "":
		event += ":" + activityName(ev.Item.Type)
	case activityName(ev.Subtype) != "":
		event += ":" + activityName(ev.Subtype)
	}
	switch {
	case ev.Message != nil:
		for _, c := range ev.Message.Content {
			if c.Type == "tool_use" {
				tool = activityName(c.Name)
				break
			}
		}
	case ev.Item != nil:
		tool = activityName(ev.Item.Tool)
	case ev.Part != nil:
		tool = activityName(ev.Part.Tool)
	}
	return event, tool
}

// activityName returns s when it is a short identifier, such as
// item.started, tool_use or mcp__anthill__push, and "" otherwise.
func activityName(s string) string {
	if s == "" || len(s) > maxActivityName {
		return ""
	}
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case r == '_', r == '-', r == '.', r == ':', r == '/':
		default:
			return ""
		}
	}
	return s
}
