package github

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/matteorosani/anthill/internal/ports"
)

// graphqlRequest is the body of a GraphQL call.
type graphqlRequest struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables,omitempty"`
}

// graphqlResponse is the envelope of a GraphQL answer. Data is decoded
// later into the shape the query asked for.
type graphqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []graphqlError  `json:"errors"`
}

type graphqlError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// graphql runs one query or mutation and decodes its data into out. It
// goes through the same HTTP client as REST, so it carries the token and
// respects the rate-limit gate.
func (a *Adapter) graphql(ctx context.Context, op, query string, vars map[string]any, out any) error {
	body, err := json.Marshal(graphqlRequest{Query: query, Variables: vars})
	if err != nil {
		return fmt.Errorf("github: %s: %w", op, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.graphqlURL, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("github: %s: %w", op, err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", a.client.UserAgent())
	resp, err := a.http.Do(req)
	if err != nil {
		if rl, ok := errors.AsType[*ports.RateLimitError](err); ok {
			return fmt.Errorf("github: %s: %w", op, rl)
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("github: %s: %w", op, ctxErr)
		}
		return &scrubbedError{msg: fmt.Sprintf("github: %s: %s", op, a.scrub(err.Error())), err: err}
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return fmt.Errorf("github: %s: read answer: %w", op, err)
	}
	var envelope graphqlResponse
	if resp.StatusCode != http.StatusOK {
		var message struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &message)
		return a.statusError(op, resp.StatusCode, message.Message)
	}
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("github: %s: decode answer: %w", op, err)
	}
	if len(envelope.Errors) > 0 {
		first := envelope.Errors[0]
		text := fmt.Sprintf("github: %s: %s", op, a.scrub(first.Message))
		switch first.Type {
		case "NOT_FOUND":
			return fmt.Errorf("%s: %w", text, ports.ErrNotFound)
		case "RATE_LIMITED":
			// GraphQL answers 200 with this error; the gate has paused
			// the graphql limit from the headers of the same answer.
			if err := a.gate.check("graphql"); err != nil {
				return fmt.Errorf("github: %s: %w", op, err)
			}
			return fmt.Errorf("github: %s: %w", op, &ports.RateLimitError{Reset: a.clock.Now().Add(secondaryWait)})
		}
		return errors.New(text)
	}
	if err := json.Unmarshal(envelope.Data, out); err != nil {
		return fmt.Errorf("github: %s: decode data: %w", op, err)
	}
	return nil
}
