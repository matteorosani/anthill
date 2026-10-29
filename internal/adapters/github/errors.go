package github

import (
	"errors"
	"fmt"
	"net/http"

	gh "github.com/google/go-github/v93/github"

	"github.com/matteorosani/anthill/internal/ports"
)

// apiError turns an error from a GitHub call into an adapter error. The
// message is rebuilt from the operation, the status and GitHub's message
// only, so that no request header, and so no token, can end up in it,
// and the redactor scrubs GitHub's message. The shared port errors are
// wrapped, so callers check them with errors.Is and errors.As.
func (a *Adapter) apiError(op string, resp *gh.Response, err error) error {
	if rl, ok := errors.AsType[*ports.RateLimitError](err); ok {
		return fmt.Errorf("github: %s: %w", op, rl)
	}
	status := 0
	if resp != nil && resp.Response != nil {
		status = resp.StatusCode
	}
	var message string
	if er, ok := errors.AsType[*gh.ErrorResponse](err); ok {
		message = er.Message
		if er.Response != nil {
			status = er.Response.StatusCode
		}
		for _, detail := range er.Errors {
			if detail.Message != "" {
				message += "; " + detail.Message
			}
		}
	}
	if status == 0 {
		// A transport error: the connection, a timeout or the
		// credential lookup. It stays wrapped for errors.Is.
		return &scrubbedError{msg: fmt.Sprintf("github: %s: %s", op, a.scrub(err.Error())), err: err}
	}
	return a.statusError(op, status, message)
}

// statusError is the error for an answer with an error status.
func (a *Adapter) statusError(op string, status int, message string) error {
	text := fmt.Sprintf("github: %s: %d %s", op, status, http.StatusText(status))
	if message != "" {
		text += ": " + a.scrub(message)
	}
	switch status {
	case http.StatusNotFound, http.StatusGone:
		return fmt.Errorf("%s: %w", text, ports.ErrNotFound)
	case http.StatusConflict:
		return fmt.Errorf("%s: %w", text, ports.ErrConflict)
	}
	return errors.New(text)
}

// scrub removes secrets from text that came from GitHub or the network:
// the token in use, and whatever the redactor's patterns match.
func (a *Adapter) scrub(text string) string {
	return a.redactor.Redact(a.auth.scrub(text))
}

// scrubbedError shows a scrubbed message but still wraps the original
// error, so that errors.Is finds ports.ErrNoCredential or a context error.
type scrubbedError struct {
	msg string
	err error
}

func (e *scrubbedError) Error() string { return e.msg }
func (e *scrubbedError) Unwrap() error { return e.err }

// notFound reports whether err is a GitHub 404.
func notFound(err error) bool {
	return errors.Is(err, ports.ErrNotFound)
}
