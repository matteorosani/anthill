package codex

import "encoding/json"

// The event types of codex exec --json that the runner reads. Others,
// such as items and failed turns, only go to the log.
const (
	eventThreadStarted = "thread.started"
	eventTurnCompleted = "turn.completed"
)

// event is one line of codex exec --json: thread.started carries the
// session id, and turn.completed the turn's tokens.
type event struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Usage    *struct {
		// InputTokens include the cached ones.
		InputTokens       int64 `json:"input_tokens"`
		CachedInputTokens int64 `json:"cached_input_tokens"`
		OutputTokens      int64 `json:"output_tokens"`
	} `json:"usage"`
}

func parseEvent(line []byte) (event, bool) {
	var ev event
	if err := json.Unmarshal(line, &ev); err != nil || ev.Type == "" {
		return event{}, false
	}
	return ev, true
}

// tokens are all the tokens of a turn, cached ones included, as every
// runner counts them: Codex's input tokens already include the cached
// ones.
func (e event) tokens() (int64, bool) {
	if e.Usage == nil {
		return 0, false
	}
	return e.Usage.InputTokens + e.Usage.OutputTokens, true
}
