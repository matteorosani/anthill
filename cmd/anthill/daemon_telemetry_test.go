package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"github.com/matteorosani/anthill/internal/adapters/telemetry"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/testkit"
)

// otlpReceiver is an in-process OTLP/HTTP receiver that records the path
// and Authorization header of each request.
type otlpReceiver struct {
	*httptest.Server
	mu   sync.Mutex
	auth map[string][]string
}

func newOTLPReceiver(t *testing.T) *otlpReceiver {
	t.Helper()
	r := &otlpReceiver{auth: map[string][]string{}}
	r.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		_, _ = io.Copy(io.Discard, req.Body)
		r.mu.Lock()
		r.auth[req.URL.Path] = append(r.auth[req.URL.Path], req.Header.Get("Authorization"))
		r.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(r.Close)
	return r
}

func (r *otlpReceiver) got(path string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.auth[path]...)
}

// Task 18.1, through the composition root: with otlp-endpoint set, the
// daemon exports its traces, metrics and logs with the headers from the
// credential store, and flushes them when it stops.
func TestDaemonExportsTelemetry(t *testing.T) {
	recv := newOTLPReceiver(t)
	creds := &testkit.CredentialStore{}
	creds.Set(domain.OTLPHeadersCredential(), "Authorization=Bearer%20k3y")
	saved := keyringCredentials
	keyringCredentials = creds
	t.Cleanup(func() { keyringCredentials = saved })

	e := newDaemonEnv(t)
	config := "otlp-endpoint: " + recv.URL + "\nrepositories:\n  - provider: github\n    remote: acme/shop\n    path: '" + e.dir + "'\n"
	if err := os.WriteFile(e.configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	stop := e.startDaemon(t)
	e.eventually("a poll", func() bool {
		statuses, _ := e.store.PollStatuses(context.Background())
		return len(statuses) > 0 && !statuses[0].LastSuccess.IsZero()
	})
	stop()

	for _, path := range []string{telemetry.PathTraces, telemetry.PathMetrics, telemetry.PathLogs} {
		got := recv.got(path)
		if len(got) == 0 {
			t.Errorf("nothing exported to %s", path)
		}
		for _, a := range got {
			if a != "Bearer k3y" {
				t.Errorf("%s: Authorization = %q, want the stored header", path, a)
			}
		}
	}
}

// Without otlp-endpoint the daemon reads no OTLP headers and exports
// nothing.
func TestDaemonTelemetryOffByDefault(t *testing.T) {
	recv := newOTLPReceiver(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", recv.URL)
	creds := &testkit.CredentialStore{}
	saved := keyringCredentials
	keyringCredentials = creds
	t.Cleanup(func() { keyringCredentials = saved })

	e := newDaemonEnv(t)
	stop := e.startDaemon(t)
	e.eventually("a poll", func() bool {
		statuses, _ := e.store.PollStatuses(context.Background())
		return len(statuses) > 0 && !statuses[0].LastSuccess.IsZero()
	})
	stop()
	for _, path := range []string{telemetry.PathTraces, telemetry.PathMetrics, telemetry.PathLogs} {
		if got := recv.got(path); len(got) != 0 {
			t.Errorf("%d exports to %s without an endpoint", len(got), path)
		}
	}
	if len(creds.Lookups) != 0 {
		t.Errorf("the credential store was read: %v", creds.Lookups)
	}
}
