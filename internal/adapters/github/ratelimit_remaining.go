package github

import (
	"net/http"
	"strconv"
	"sync"

	"github.com/matteorosani/anthill/internal/ports"
)

// rateRemaining keeps the remaining rate limit of each GitHub resource,
// such as core or graphql, as the latest response gave it in
// X-RateLimit-Remaining. Its zero value is ready to use.
type rateRemaining struct {
	mu        sync.Mutex
	resources map[string]int64
}

// record takes the remaining count from a response, if it has one.
func (r *rateRemaining) record(resp *http.Response) {
	n, err := strconv.ParseInt(resp.Header.Get(headerRemaining), 10, 64)
	if err != nil {
		return
	}
	resource := resp.Header.Get(headerResource)
	if resource == "" {
		resource = "core"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.resources == nil {
		r.resources = map[string]int64{}
	}
	r.resources[resource] = n
}

// lowest is the smallest remaining count of any resource, the one that
// will stop polling first.
func (r *rateRemaining) lowest() (int64, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var low int64
	ok := false
	for _, n := range r.resources {
		if !ok || n < low {
			low, ok = n, true
		}
	}
	return low, ok
}

var _ ports.RateLimitReporter = (*Adapter)(nil)

// RateLimitRemaining is the lowest remaining rate limit of the GitHub
// resources the adapter has used, from X-RateLimit-Remaining.
func (a *Adapter) RateLimitRemaining() (int64, bool) {
	if t, ok := a.http.Transport.(*gateTransport); ok {
		return t.remaining.lowest()
	}
	return 0, false
}
