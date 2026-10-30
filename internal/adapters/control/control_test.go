package control

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// testAddress returns a fresh control address for one test: a socket in a
// short temporary directory on Unix, because macOS limits socket paths
// to 103 bytes and t.TempDir() can be longer, or a uniquely named pipe
// on Windows.
func testAddress(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`\\.\pipe\anthill-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	}
	dir, err := os.MkdirTemp("", "ah")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return filepath.Join(dir, "home", "control.sock")
}

// startServer listens at a fresh address and serves until the test ends.
func startServer(t *testing.T, register func(*Server)) (string, net.Listener) {
	t.Helper()
	address := testAddress(t)
	ln, err := Listen(address)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(slog.New(slog.DiscardHandler))
	register(srv)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Serve: %v", err)
		}
	})
	return address, ln
}

type echoParams struct {
	Text string `json:"text"`
}

// A call goes to the daemon and its result comes back, on every OS.
func TestClientRoundTrip(t *testing.T) {
	address, _ := startServer(t, func(s *Server) {
		s.Handle("echo", func(_ context.Context, params json.RawMessage, _ Stream) (any, error) {
			var p echoParams
			if err := json.Unmarshal(params, &p); err != nil {
				return nil, &Error{Code: CodeInvalidParams, Message: err.Error()}
			}
			return echoParams{Text: strings.ToUpper(p.Text)}, nil
		})
	})
	var got echoParams
	err := NewClient(address).Call(context.Background(), "echo", echoParams{Text: "hello"}, &got)
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != "HELLO" {
		t.Errorf("result = %q, want HELLO", got.Text)
	}
}

// A streaming call sends its items, then its result.
func TestStreamingCall(t *testing.T) {
	address, _ := startServer(t, func(s *Server) {
		s.Handle("count", func(_ context.Context, _ json.RawMessage, stream Stream) (any, error) {
			for i := 1; i <= 3; i++ {
				if err := stream(i); err != nil {
					return nil, err
				}
			}
			return "done", nil
		})
	})
	var items []string
	var result string
	err := NewClient(address).Stream(context.Background(), "count", nil, func(item json.RawMessage) error {
		items = append(items, string(item))
		return nil
	}, &result)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(items, ",") != "1,2,3" || result != "done" {
		t.Errorf("items %v, result %q", items, result)
	}
}

// A client that detaches by closing the connection cancels the handler,
// and the server goes on serving other calls.
func TestDetachCancelsTheHandler(t *testing.T) {
	cancelled := make(chan struct{})
	address, _ := startServer(t, func(s *Server) {
		s.Handle("follow", func(ctx context.Context, _ json.RawMessage, stream Stream) (any, error) {
			// Stream until the client detaches.
			for i := 0; ctx.Err() == nil; i++ {
				if err := stream(i); err != nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			<-ctx.Done()
			close(cancelled)
			return nil, ctx.Err()
		})
		s.Handle("ping", func(context.Context, json.RawMessage, Stream) (any, error) { return "pong", nil })
	})
	client := NewClient(address)
	errDetach := errors.New("detach")
	err := client.Stream(context.Background(), "follow", nil, func(json.RawMessage) error { return errDetach }, nil)
	if !errors.Is(err, errDetach) {
		t.Fatalf("err = %v, want %v", err, errDetach)
	}
	select {
	case <-cancelled:
	case <-time.After(10 * time.Second):
		t.Fatal("the handler was not cancelled")
	}
	var pong string
	if err := client.Call(context.Background(), "ping", nil, &pong); err != nil || pong != "pong" {
		t.Errorf("ping after detach: %q, %v", pong, err)
	}
}

// Cancelling the caller's context detaches too.
func TestCallerContextDetaches(t *testing.T) {
	address, _ := startServer(t, func(s *Server) {
		s.Handle("block", func(ctx context.Context, _ json.RawMessage, _ Stream) (any, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		})
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	err := NewClient(address).Call(ctx, "block", nil, nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the deadline", err)
	}
}

func TestErrors(t *testing.T) {
	address, _ := startServer(t, func(s *Server) {
		s.Handle("bad-params", func(context.Context, json.RawMessage, Stream) (any, error) {
			return nil, &Error{Code: CodeInvalidParams, Message: "item is required"}
		})
		s.Handle("broken", func(context.Context, json.RawMessage, Stream) (any, error) {
			return nil, errors.New("store unavailable")
		})
	})
	client := NewClient(address)
	tests := []struct {
		method   string
		wantCode int
		wantMsg  string
	}{
		{"missing", CodeMethodNotFound, "method not found: missing"},
		{"bad-params", CodeInvalidParams, "item is required"},
		{"broken", CodeInternalError, "store unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.method, func(t *testing.T) {
			err := client.Call(context.Background(), tt.method, nil, nil)
			var rpcErr *Error
			if !errors.As(err, &rpcErr) || rpcErr.Code != tt.wantCode || rpcErr.Message != tt.wantMsg {
				t.Errorf("err = %#v, want code %d %q", err, tt.wantCode, tt.wantMsg)
			}
		})
	}
}

// rawCall sends one line and returns the server's answer as a map.
func rawCall(t *testing.T, address, line string) map[string]any {
	t.Helper()
	conn, err := dial(context.Background(), address)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := io.WriteString(conn, line); err != nil {
		t.Fatal(err)
	}
	answer, err := readLine(bufio.NewReader(conn))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(answer, &m); err != nil {
		t.Fatalf("answer %q: %v", answer, err)
	}
	return m
}

// The wire format is JSON-RPC 2.0, one message per line.
func TestWireFormat(t *testing.T) {
	address, _ := startServer(t, func(s *Server) {
		s.Handle("nothing", func(context.Context, json.RawMessage, Stream) (any, error) { return nil, nil })
	})
	tests := []struct {
		name, line string
		want       string
	}{
		{"success with null result", `{"jsonrpc":"2.0","id":"a","method":"nothing"}` + "\n", `{"id":"a","jsonrpc":"2.0","result":null}`},
		{"parse error", "not json\n", `{"error":{"code":-32700,"message":"parse error: invalid character 'o' in literal null (expecting 'u')"},"id":null,"jsonrpc":"2.0"}`},
		{"no id", `{"jsonrpc":"2.0","method":"nothing"}` + "\n", `{"error":{"code":-32600,"message":"invalid request: jsonrpc 2.0, a method and an id are required"},"id":null,"jsonrpc":"2.0"}`},
		{"wrong version", `{"jsonrpc":"1.0","id":7,"method":"nothing"}` + "\n", `{"error":{"code":-32600,"message":"invalid request: jsonrpc 2.0, a method and an id are required"},"id":7,"jsonrpc":"2.0"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(rawCall(t, address, tt.line))
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("answer = %s\nwant     %s", got, tt.want)
			}
		})
	}
}

func TestTooLongRequest(t *testing.T) {
	address, _ := startServer(t, func(*Server) {})
	m := rawCall(t, address, strings.Repeat("x", maxMessage+10)+"\n")
	if e, ok := m["error"].(map[string]any); !ok || e["code"] != float64(CodeParseError) {
		t.Errorf("answer = %v", m)
	}
}

func TestNoDaemonRunning(t *testing.T) {
	err := NewClient(testAddress(t)).Call(context.Background(), "status", nil, nil)
	if !errors.Is(err, ErrNotRunning) {
		t.Errorf("err = %v, want ErrNotRunning", err)
	}
}

// After shutdown, the server has stopped listening and clients see no
// daemon.
func TestServeStopsWhenCancelled(t *testing.T) {
	address := testAddress(t)
	ln, err := Listen(address)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- NewServer(slog.New(slog.DiscardHandler)).Serve(ctx, ln) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Serve did not return")
	}
	if err := NewClient(address).Call(context.Background(), "status", nil, nil); !errors.Is(err, ErrNotRunning) {
		t.Errorf("err = %v, want ErrNotRunning", err)
	}
}

// Scenario "Remote connection attempt": the control interface listens on
// a Unix socket or a local named pipe, never on a network address, so
// another host has nothing to connect to.
func TestRemoteConnectionAttempt(t *testing.T) {
	_, ln := startServer(t, func(*Server) {})
	network := ln.Addr().Network()
	switch runtime.GOOS {
	case "windows":
		if network != "pipe" || !strings.HasPrefix(ln.Addr().String(), `\\.\pipe\`) {
			t.Errorf("listening on %s %s, want a local pipe", network, ln.Addr())
		}
	default:
		if network != "unix" || !filepath.IsAbs(ln.Addr().String()) {
			t.Errorf("listening on %s %s, want a Unix socket", network, ln.Addr())
		}
	}
	if _, isTCP := ln.Addr().(*net.TCPAddr); isTCP {
		t.Error("listening on TCP")
	}
}
