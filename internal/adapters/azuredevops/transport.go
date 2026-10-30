package azuredevops

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/internal/httpkit"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// refreshBefore is how long before its expiry an Entra ID token is read
// again from the credential source.
const refreshBefore = 2 * time.Minute

// authTransport adds the token to every request: a Bearer token for an
// Entra ID access token, Basic authentication with an empty user name for
// a PAT. A 401 drops the token, so that the next request reads a fresh
// one. Every request also asks Azure DevOps to answer 401 instead of
// redirecting to a sign-in page (X-TFS-FedAuthRedirect).
type authTransport struct {
	base  http.RoundTripper
	token *httpkit.Token

	mu sync.Mutex
	// basic is the last Basic credential sent, base64-encoded, so that
	// text can be scrubbed of it too.
	basic string
}

func (t *authTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	token, err := t.token.Get(req.Context())
	if err != nil {
		// Credential sources never put a value in their errors.
		return nil, fmt.Errorf("azure devops token: %w", err)
	}
	// A RoundTripper must not change the caller's request, so the
	// headers go on a copy.
	req = req.Clone(req.Context())
	req.Header.Set("Authorization", t.header(token))
	req.Header.Set("X-TFS-FedAuthRedirect", "Suppress")
	resp, err := t.base.RoundTrip(req)
	if err == nil && resp.StatusCode == http.StatusUnauthorized {
		t.token.Drop()
	}
	return resp, err
}

// header is the Authorization header for token.
func (t *authTransport) header(token domain.Secret) string {
	if looksLikeJWT(token.Reveal()) {
		return "Bearer " + token.Reveal()
	}
	basic := base64.StdEncoding.EncodeToString([]byte(":" + token.Reveal()))
	t.mu.Lock()
	t.basic = basic
	t.mu.Unlock()
	return "Basic " + basic
}

// scrub replaces the last token used, raw or Basic-encoded, in text.
func (t *authTransport) scrub(text string) string {
	text = t.token.Scrub(text)
	t.mu.Lock()
	basic := t.basic
	t.mu.Unlock()
	if basic != "" {
		text = strings.ReplaceAll(text, basic, domain.Redacted)
	}
	return text
}

// looksLikeJWT reports whether s has the shape of a JSON Web Token: three
// dot-separated parts, the first a base64url JSON object ({" is eyJ).
// Entra ID access tokens have it; PATs never do.
func looksLikeJWT(s string) bool {
	return strings.HasPrefix(s, "eyJ") && strings.Count(s, ".") == 2
}

// jwtExpiry reads the exp claim of an Entra ID token without checking its
// signature: the time only decides when to read the token again. It
// returns the zero time for a PAT or an unreadable token.
func jwtExpiry(token domain.Secret) time.Time {
	s := token.Reveal()
	if !looksLikeJWT(s) {
		return time.Time{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(strings.Split(s, ".")[1])
	if err != nil {
		return time.Time{}
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if json.Unmarshal(payload, &claims) != nil || claims.Exp == 0 {
		return time.Time{}
	}
	return time.Unix(claims.Exp, 0)
}

// Throttling headers (Azure DevOps docs, "Rate and usage limits").
const (
	headerRetry = "Retry-After"
	headerReset = "X-RateLimit-Reset"
)

// gateKey is the one pause of the adapter: Azure DevOps limits each user
// per organization, whatever the endpoint.
const gateKey = "organization"

// throttleWait is how long to wait after a 429 that says nothing about
// when to come back.
const throttleWait = time.Minute

// gateTransport sends a request only when the organization is not paused,
// and pauses it when a response says so. A refused or throttled request
// fails with a *ports.RateLimitError.
type gateTransport struct {
	base   http.RoundTripper
	gate   *httpkit.Gate
	clock  ports.Clock
	logger *slog.Logger
}

func (t *gateTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if err := t.gate.Check(gateKey); err != nil {
		t.logger.Debug("azure devops request held for rate limit", "method", req.Method, "path", req.URL.Path)
		return nil, err
	}
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	t.logger.Debug("azure devops request", "method", req.Method, "path", req.URL.Path, "status", resp.StatusCode)
	until, limited := t.limit(resp)
	if until.IsZero() {
		return resp, nil
	}
	t.gate.Pause(gateKey, until)
	if !limited {
		// Azure DevOps answered, but asks to wait before the next
		// request; the next one is held instead.
		return resp, nil
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
	t.logger.Debug("azure devops rate limit", "until", until)
	return nil, &ports.RateLimitError{Reset: until}
}

// limit reads a response's throttling state. until is when requests may
// be sent again, zero when there is no limit; limited tells whether this
// response itself was refused.
func (t *gateTransport) limit(resp *http.Response) (until time.Time, limited bool) {
	now := t.clock.Now()
	retry, hasRetry := time.Duration(0), false
	if secs, err := strconv.Atoi(resp.Header.Get(headerRetry)); err == nil && secs >= 0 {
		retry, hasRetry = time.Duration(secs)*time.Second, true
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		if hasRetry && retry > 0 && resp.StatusCode < 300 {
			return now.Add(retry), false
		}
		return time.Time{}, false
	}
	if hasRetry {
		return now.Add(retry), true
	}
	if secs, err := strconv.ParseInt(resp.Header.Get(headerReset), 10, 64); err == nil {
		if reset := time.Unix(secs, 0); reset.After(now) {
			return reset, true
		}
	}
	return now.Add(throttleWait), true
}
