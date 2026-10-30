package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"sync"
)

// Stream sends one item of a streaming call to the client, as a "stream"
// notification. It fails once the client has detached. A handler calls
// it only from its own goroutine.
type Stream func(item any) error

// Handler serves one method. params is the request's params, or nil.
// The returned result is sent as the response; a streaming handler sends
// its items with stream first. ctx is cancelled when the client detaches
// or the server shuts down.
type Handler func(ctx context.Context, params json.RawMessage, stream Stream) (any, error)

// Server answers control calls with the handlers registered for their
// methods.
type Server struct {
	logger   *slog.Logger
	handlers map[string]Handler
}

// NewServer returns a server without handlers that logs to logger.
func NewServer(logger *slog.Logger) *Server {
	return &Server{logger: logger, handlers: map[string]Handler{}}
}

// Handle registers h for method. It must be called before Serve.
func (s *Server) Handle(method string, h Handler) {
	s.handlers[method] = h
}

// Serve accepts connections on ln until ctx is cancelled, then closes
// ln, cancels the calls in progress, waits for their handlers to return,
// and returns nil. It returns an error when accepting fails for another
// reason.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	var calls sync.WaitGroup
	defer func() {
		cancel()
		_ = ln.Close()
		calls.Wait()
	}()
	// Accept blocks until a client connects; closing the listener is
	// what makes it return when ctx is cancelled.
	stop := context.AfterFunc(ctx, func() { _ = ln.Close() })
	defer stop()
	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		calls.Go(func() { s.serve(ctx, conn) })
	}
}

// serve answers the one call of conn.
func (s *Server) serve(ctx context.Context, conn net.Conn) {
	defer func() { _ = conn.Close() }()
	r := bufio.NewReader(conn)
	w := bufio.NewWriter(conn)

	line, err := readLine(r)
	if err != nil {
		if !errors.Is(err, io.EOF) {
			s.reply(w, nil, nil, &Error{Code: CodeParseError, Message: err.Error()})
		}
		return
	}
	var req message
	if err := json.Unmarshal(line, &req); err != nil {
		s.reply(w, nil, nil, &Error{Code: CodeParseError, Message: "parse error: " + err.Error()})
		return
	}
	if req.JSONRPC != version || req.Method == "" || len(req.ID) == 0 || string(req.ID) == "null" {
		s.reply(w, req.ID, nil, &Error{Code: CodeInvalidRequest, Message: "invalid request: jsonrpc 2.0, a method and an id are required"})
		return
	}
	h, ok := s.handlers[req.Method]
	if !ok {
		s.reply(w, req.ID, nil, &Error{Code: CodeMethodNotFound, Message: "method not found: " + req.Method})
		return
	}

	// The client sends nothing after its request, so a read that returns
	// means it closed the connection: it detached.
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		_, _ = io.Copy(io.Discard, r)
		cancel()
	}()

	stream := func(item any) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		params, err := json.Marshal(item)
		if err != nil {
			return err
		}
		return writeMessage(w, message{Method: StreamMethod, Params: params})
	}
	result, err := h(ctx, req.Params, stream)
	if ctx.Err() != nil {
		// Detached or shutting down: nobody reads the response.
		return
	}
	if err != nil {
		var rpcErr *Error
		if !errors.As(err, &rpcErr) {
			rpcErr = &Error{Code: CodeInternalError, Message: err.Error()}
		}
		s.reply(w, req.ID, nil, rpcErr)
		return
	}
	data, err := json.Marshal(result)
	if err != nil {
		s.reply(w, req.ID, nil, &Error{Code: CodeInternalError, Message: "encoding the result: " + err.Error()})
		return
	}
	s.reply(w, req.ID, data, nil)
}

// reply writes the response to a call. A null id is written for a
// request whose id could not be read.
func (s *Server) reply(w *bufio.Writer, id, result json.RawMessage, rpcErr *Error) {
	if len(id) == 0 {
		id = json.RawMessage("null")
	}
	m := message{ID: id, Result: result, Error: rpcErr}
	if rpcErr == nil && len(result) == 0 {
		m.Result = json.RawMessage("null")
	}
	if err := writeMessage(w, m); err != nil {
		s.logger.Debug("control response not sent", "error", err)
	}
}
