package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// ErrNotRunning is returned when no daemon listens at the address.
var ErrNotRunning = errors.New("daemon not running")

// Client calls the daemon over the control interface.
type Client struct {
	address string
}

// NewClient returns a client for the daemon listening at address.
func NewClient(address string) *Client {
	return &Client{address: address}
}

// Call calls method with params and decodes the result into result,
// which may be nil to ignore it. A JSON-RPC error is returned as *Error.
func (c *Client) Call(ctx context.Context, method string, params, result any) error {
	return c.Stream(ctx, method, params, nil, result)
}

// Stream calls a streaming method: it passes each streamed item to item
// as it arrives, then decodes the final result into result. When item
// returns an error or ctx is cancelled, the client detaches by closing
// the connection, and Stream returns that error.
func (c *Client) Stream(ctx context.Context, method string, params any, item func(json.RawMessage) error, result any) error {
	conn, err := dial(ctx, c.address)
	if err != nil {
		if notRunning(err) {
			return ErrNotRunning
		}
		return fmt.Errorf("connecting to the daemon at %s: %w", c.address, err)
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()

	req := message{ID: json.RawMessage("1"), Method: method}
	if params != nil {
		if req.Params, err = json.Marshal(params); err != nil {
			return fmt.Errorf("encoding the params of %s: %w", method, err)
		}
	}
	if err := writeMessage(bufio.NewWriter(conn), req); err != nil {
		return c.failed(ctx, "sending the request to", err)
	}
	r := bufio.NewReader(conn)
	for {
		line, err := readLine(r)
		if err != nil {
			return c.failed(ctx, "reading the answer from", err)
		}
		var m message
		if err := json.Unmarshal(line, &m); err != nil {
			return fmt.Errorf("reading the answer to %s: %w", method, err)
		}
		if m.Method == StreamMethod && len(m.ID) == 0 {
			if item != nil {
				if err := item(m.Params); err != nil {
					return err
				}
			}
			continue
		}
		if m.Error != nil {
			return m.Error
		}
		if result == nil {
			return nil
		}
		if err := json.Unmarshal(m.Result, result); err != nil {
			return fmt.Errorf("decoding the result of %s: %w", method, err)
		}
		return nil
	}
}

// failed explains a broken connection: ctx's error when the caller
// detached, else what went wrong.
func (c *Client) failed(ctx context.Context, doing string, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, io.EOF) {
		return fmt.Errorf("%s the daemon: connection closed", doing)
	}
	return fmt.Errorf("%s the daemon: %w", doing, err)
}
