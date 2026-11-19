package opencode

import (
	"encoding/json"
	"strings"
)

// eventStepFinish ends one step of the agent with its tokens and cost,
// and eventText is a text part of the agent's message. Other events,
// such as tool use, only go to the log.
const (
	eventStepFinish = "step_finish"
	eventText       = "text"
)

// event is one line of opencode run --format json. Every event carries
// the session id; step_finish carries the step's tokens and cost, and
// text a text part of the agent's message.
type event struct {
	Type      string `json:"type"`
	SessionID string `json:"sessionID"`
	Part      struct {
		// Text is the text of a text part.
		Text   string `json:"text"`
		Tokens *struct {
			Input     int64 `json:"input"`
			Output    int64 `json:"output"`
			Reasoning int64 `json:"reasoning"`
			Cache     struct {
				Read  int64 `json:"read"`
				Write int64 `json:"write"`
			} `json:"cache"`
		} `json:"tokens"`
		// Cost is the step's cost in USD.
		Cost *float64 `json:"cost"`
	} `json:"part"`
}

func parseEvent(line []byte) (event, bool) {
	var ev event
	if err := json.Unmarshal(line, &ev); err != nil || ev.Type == "" {
		return event{}, false
	}
	return ev, true
}

// tokens are all the tokens of a step, cached ones included, as every
// runner counts them.
func (e event) tokens() (int64, bool) {
	t := e.Part.Tokens
	if t == nil {
		return 0, false
	}
	return t.Input + t.Output + t.Reasoning + t.Cache.Read + t.Cache.Write, true
}

// message is the text of a text part of the agent's message.
func (e event) message() (string, bool) {
	if e.Type != eventText || strings.TrimSpace(e.Part.Text) == "" {
		return "", false
	}
	return e.Part.Text, true
}
