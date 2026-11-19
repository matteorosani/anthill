package codex

import (
	"encoding/json"
	"strings"
)

// The event types of codex exec --json that the runner reads. Others,
// such as items and failed turns, only go to the log.
const (
	eventThreadStarted = "thread.started"
	eventTurnCompleted = "turn.completed"
	eventItemCompleted = "item.completed"
)

// itemAgentMessage is the item of a message of the agent.
const itemAgentMessage = "agent_message"

// event is one line of codex exec --json: thread.started carries the
// session id, turn.completed the turn's tokens, and item.completed with
// an agent_message item a message of the agent.
type event struct {
	Type     string `json:"type"`
	ThreadID string `json:"thread_id"`
	Item     *struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"item"`
	Usage *struct {
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

// message is the text of a completed message of the agent.
func (e event) message() (string, bool) {
	if e.Type != eventItemCompleted || e.Item == nil || e.Item.Type != itemAgentMessage || strings.TrimSpace(e.Item.Text) == "" {
		return "", false
	}
	return e.Item.Text, true
}
