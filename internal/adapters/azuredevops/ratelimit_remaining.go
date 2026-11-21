package azuredevops

import (
	"net/http"
	"strconv"
	"sync"

	"github.com/matteorosani/anthill/internal/ports"
)

// headerRemaining is the remaining usage Azure DevOps sends only when a
// user nears or passes its limit (Azure DevOps docs, "Rate and usage
// limits").
const headerRemaining = "X-RateLimit-Remaining"

// rateRemaining keeps the remaining rate limit as the latest response
// that sent X-RateLimit-Remaining gave it. Its zero value is ready to
// use.
type rateRemaining struct {
	mu        sync.Mutex
	remaining int64
	ok        bool
}

// record takes the remaining count from a response, if it has one.
func (r *rateRemaining) record(resp *http.Response) {
	n, err := strconv.ParseInt(resp.Header.Get(headerRemaining), 10, 64)
	if err != nil {
		return
	}
	r.mu.Lock()
	r.remaining, r.ok = n, true
	r.mu.Unlock()
}

func (r *rateRemaining) get() (int64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.remaining, r.ok
}

var _ ports.RateLimitReporter = (*Adapter)(nil)

// RateLimitRemaining is the remaining rate limit from the latest response
// that sent X-RateLimit-Remaining. Azure DevOps sends it only near the
// limit, so ok is false until then.
func (a *Adapter) RateLimitRemaining() (int64, bool) {
	if t, ok := a.http.Transport.(*gateTransport); ok {
		return t.remaining.get()
	}
	return 0, false
}
