package github

import (
	"bytes"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/internal/httpkit"
	"github.com/matteorosani/anthill/internal/ports"
)

// authTransport adds the token to every request. The token is read from
// the credential source on first use and kept until GitHub answers 401,
// so that a token refreshed in the gh session or the credential store is
// picked up by the next request.
type authTransport struct {
	base  http.RoundTripper
	token *httpkit.Token
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := t.token.Get(req.Context())
	if err != nil {
		// Credential sources never put a value in their errors.
		return nil, fmt.Errorf("github token: %w", err)
	}
	// A RoundTripper must not change the caller's request, so the header
	// goes on a copy.
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", "Bearer "+token.Reveal())
	resp, err := t.base.RoundTrip(req)
	if err == nil && resp.StatusCode == http.StatusUnauthorized {
		t.token.Drop()
	}
	return resp, err
}

// Rate-limit headers (GitHub docs, "Rate limits for the REST API").
const (
	headerRemaining = "X-RateLimit-Remaining"
	headerReset     = "X-RateLimit-Reset"
	headerResource  = "X-RateLimit-Resource"
	headerRetry     = "Retry-After"
)

// secondaryWait is how long to wait after a secondary rate limit that
// gives no Retry-After: "wait for at least one minute" (GitHub docs).
const secondaryWait = time.Minute

// gateTransport sends a request only when its rate limit is not paused,
// and pauses the limit when a response says it is exhausted. A refused
// or rate-limited request fails with a *ports.RateLimitError.
type gateTransport struct {
	base   http.RoundTripper
	gate   *httpkit.Gate
	clock  ports.Clock
	logger *slog.Logger
}

func (t *gateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resource := resourceOf(req)
	if err := t.gate.Check(resource); err != nil {
		t.logger.Debug("github request held for rate limit", "method", req.Method, "path", req.URL.Path)
		return nil, err
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	t.logger.Debug("github request", "method", req.Method, "path", req.URL.Path, "status", resp.StatusCode)
	if r := resp.Header.Get(headerResource); r != "" {
		resource = r
	}
	until, limited := t.limit(resp)
	if until.IsZero() {
		return resp, nil
	}
	t.gate.Pause(resource, until)
	if !limited {
		// The last request of the window succeeded; the next one would
		// fail, so it is held instead.
		return resp, nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
	t.logger.Debug("github rate limit", "resource", resource, "until", until)
	return nil, &ports.RateLimitError{Reset: until}
}

// limit reads a response's rate-limit state. until is when requests may
// be sent again, zero when the limit is not exhausted; limited tells
// whether this response itself was refused.
func (t *gateTransport) limit(resp *http.Response) (until time.Time, limited bool) {
	now := t.clock.Now()
	reset := func() time.Time {
		secs, err := strconv.ParseInt(resp.Header.Get(headerReset), 10, 64)
		if err != nil {
			return now.Add(secondaryWait)
		}
		return time.Unix(secs, 0)
	}
	exhausted := resp.Header.Get(headerRemaining) == "0"
	if resp.StatusCode != http.StatusForbidden && resp.StatusCode != http.StatusTooManyRequests {
		if exhausted && resp.StatusCode < 300 {
			return reset(), false
		}
		return time.Time{}, false
	}
	// Secondary limits: Retry-After in seconds.
	if secs, err := strconv.Atoi(resp.Header.Get(headerRetry)); err == nil {
		return now.Add(time.Duration(secs) * time.Second), true
	}
	// Primary limits: no requests left until the reset.
	if exhausted {
		return reset(), true
	}
	// A secondary limit without Retry-After says so in its message.
	if secondaryMessage(resp) {
		return now.Add(secondaryWait), true
	}
	return time.Time{}, false
}

// secondaryMessage reports whether a 403 or 429 body mentions a
// secondary rate limit. The body is put back for the caller.
func secondaryMessage(resp *http.Response) bool {
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(data))
	if err != nil {
		return false
	}
	text := strings.ToLower(string(data))
	return resp.StatusCode == http.StatusTooManyRequests || strings.Contains(text, "secondary rate limit")
}

// resourceOf names the rate limit a request counts against before its
// response says so: GraphQL has its own.
func resourceOf(req *http.Request) string {
	if strings.HasSuffix(req.URL.Path, "/graphql") {
		return "graphql"
	}
	return "core"
}
