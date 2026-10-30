package claudecode

import (
	"encoding/json"
)

// The event types of the streaming JSON output that the runner reads.
// Others, such as assistant and user messages, only go to the log.
const (
	eventSystem = "system"
	eventResult = "result"
)

// event is one line of the streaming JSON output: a system/init event
// carries the session id, and a result event ends a turn with its usage
// and the session's cost.
type event struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	SessionID string `json:"session_id"`
	IsError   bool   `json:"is_error"`
	// TotalCostUSD is the cost of the process so far, so the last one
	// is the run's cost.
	TotalCostUSD *float64 `json:"total_cost_usd"`
	// Usage is the turn's tokens.
	Usage *struct {
		InputTokens              int64 `json:"input_tokens"`
		OutputTokens             int64 `json:"output_tokens"`
		CacheCreationInputTokens int64 `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	} `json:"usage"`
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

// tokens are all the tokens a turn processed, cached ones included, as
// every runner counts them.
func (e event) tokens() (int64, bool) {
	if e.Usage == nil {
		return 0, false
	}
	u := e.Usage
	return u.InputTokens + u.OutputTokens + u.CacheCreationInputTokens + u.CacheReadInputTokens, true
}
