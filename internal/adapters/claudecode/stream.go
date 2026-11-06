package claudecode

import (
	"encoding/json"
)

// The event types of the streaming JSON output that the runner reads.
// Others, such as user messages, only go to the log.
const (
	eventSystem    = "system"
	eventAssistant = "assistant"
	eventResult    = "result"
)

// event is one line of the streaming JSON output: a system/init event
// carries the session id, an assistant event one message of the agent
// with the usage of its API response, and a result event ends a turn
// with its usage and the session's cost.
type event struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	IsError   bool   `json:"is_error"`
	// TotalCostUSD is the cost of the process so far, so the last one
	// is the run's cost.
	TotalCostUSD *float64 `json:"total_cost_usd"`
	// QueuedTurnCount, on a result, is the number of input messages
	// still waiting for a turn of their own. A message that arrives while
	// Claude Code is working can be folded into the running turn, and
	// then gets no result of its own (issue #146, smoke check C3). Older
	// versions do not report it.
	QueuedTurnCount *int `json:"queued_turn_count"`
	// Usage, on a result, is the turn's tokens, without subagents.
	Usage *usage `json:"usage"`
	// Message, on an assistant event, is the message with its API
	// response's id and usage.
	Message *struct {
		ID    string `json:"id"`
		Usage *usage `json:"usage"`
	} `json:"message"`
	// ParentToolUseID, on an assistant event, is set for a subagent's
	// message.
	ParentToolUseID *string `json:"parent_tool_use_id"`
}

// usage is the token counts of a turn or of one API response.
type usage struct {
	InputTokens              int64 `json:"input_tokens"`
	OutputTokens             int64 `json:"output_tokens"`
	CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
}

// total is all the tokens, cached ones included, as every runner counts
// them.
func (u usage) total() int64 {
	return u.InputTokens + u.OutputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens
}

// parseEvent decodes one output line. A line that is not a JSON object
// with a type is not an event.
func parseEvent(line []byte) (event, bool) {
	var ev event
	if err := json.Unmarshal(line, &ev); err != nil || ev.Type == "" {
		return event{}, false
	}
	return ev, true
}

// tokens are all the tokens a result's turn processed.
func (e event) tokens() (int64, bool) {
	if e.Usage == nil {
		return 0, false
	}
	return e.Usage.total(), true
}

// messageTokens are the tokens of an assistant event's message, with its
// id, when it is a message of the main loop that reports usage. The
// messages of one API response share the id and the usage, so a caller
// counts each id once. Its output tokens are the count at the start of
// the response, which only the result corrects.
func (e event) messageTokens() (id string, tokens int64, ok bool) {
	if e.Type != eventAssistant || e.ParentToolUseID != nil || e.Message == nil || e.Message.ID == "" || e.Message.Usage == nil {
		return "", 0, false
	}
	return e.Message.ID, e.Message.Usage.total(), true
}
