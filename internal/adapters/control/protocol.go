// Package control is the control interface between the daemon and the
// queen console (spec queen-console, "Local control interface"; design
// D10). It speaks JSON-RPC 2.0 over a Unix domain socket or a Windows
// named pipe, so it is local-only by construction: neither has a network
// address another machine could reach.
//
// Each connection carries one call. The client writes one request; the
// server answers with zero or more "stream" notifications and then one
// response with the request's id, which ends the call. Every message is
// one JSON object on one line. A client detaches by closing the
// connection, which cancels the handler's context.
//
// The protocol code is shared; only listening and dialling differ by OS
// (transport_unix.go and transport_windows.go).
package control

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
)

// StreamMethod is the method of the notifications a streaming call sends
// before its response.
const StreamMethod = "stream"

// maxMessage is the longest message line either side accepts, in bytes.
const maxMessage = 1 << 20

// version is the JSON-RPC version every message carries.
const version = "2.0"

// The JSON-RPC 2.0 error codes the server uses.
const (
	CodeParseError     = -32700
	CodeInvalidRequest = -32600
	CodeMethodNotFound = -32601
	CodeInvalidParams  = -32602
	CodeInternalError  = -32603
)

// Error is a JSON-RPC error object. A handler returns one to choose the
// code; any other error is sent as an internal error with its message.
type Error struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	return e.Message
}

// message is any JSON-RPC message: a request, a response or a
// notification. Which fields are set tells them apart.
type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *Error          `json:"error,omitempty"`
}

// errTooLong is returned for a message line longer than maxMessage.
var errTooLong = fmt.Errorf("message longer than %d bytes", maxMessage)

// readLine reads one message line, without its newline.
func readLine(r *bufio.Reader) ([]byte, error) {
	var line []byte
	for {
		chunk, err := r.ReadSlice('\n')
		line = append(line, chunk...)
		if len(line) > maxMessage+1 {
			return nil, errTooLong
		}
		switch {
		case err == nil:
			return line[:len(line)-1], nil
		case !errors.Is(err, bufio.ErrBufferFull):
			return nil, err
		}
	}
}

// writeMessage writes m as one line.
func writeMessage(w *bufio.Writer, m message) error {
	m.JSONRPC = version
	data, err := json.Marshal(m)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := w.Write(data); err != nil {
		return err
	}
	return w.Flush()
}
