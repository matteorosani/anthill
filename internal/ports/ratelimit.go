package ports

// RateLimitReporter is implemented by provider adapters that learn their
// remaining rate limit from the provider's responses, for the remaining
// rate limit metric (spec observability, "Metrics"). It is optional: a
// port that does not implement it has no such metric.
type RateLimitReporter interface {
	// RateLimitRemaining is the number of requests the provider said are
	// left in the current window, in its latest response that said so.
	// ok is false until a response has said so.
	RateLimitRemaining() (remaining int64, ok bool)
}
