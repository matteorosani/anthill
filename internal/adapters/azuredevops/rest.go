package azuredevops

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/matteorosani/anthill/internal/adapters/internal/httpkit"
	"github.com/matteorosani/anthill/internal/ports"
)

// API versions. Work item comments are still a preview API in 7.1.
const (
	apiVersion         = "7.1"
	commentsAPIVersion = "7.1-preview.4"
)

// Content types of request bodies.
const (
	contentJSON      = "application/json"
	contentJSONPatch = "application/json-patch+json"
)

// maxAnswer bounds how much of an answer is read.
const maxAnswer = 32 << 20

// call is one REST request.
type call struct {
	// op names the operation in errors, such as "read work item 12".
	op     string
	method string
	url    string
	// body is encoded as JSON when not nil.
	body any
	// contentType defaults to application/json.
	contentType string
	// out receives the decoded answer when not nil.
	out any
}

// send runs one request and decodes its answer into c.out. It returns the
// response, with its body already read and closed, for its headers. An
// error status becomes an adapter error (see statusError).
func (a *Adapter) send(ctx context.Context, c call) (*http.Response, error) {
	var body io.Reader
	if c.body != nil {
		// Comment bodies hold markers (<!-- … -->): keep < and > as they
		// are instead of \u003c and \u003e.
		var buf bytes.Buffer
		enc := json.NewEncoder(&buf)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(c.body); err != nil {
			return nil, fmt.Errorf("azure devops: %s: %w", c.op, err)
		}
		body = &buf
	}
	req, err := http.NewRequestWithContext(ctx, c.method, c.url, body)
	if err != nil {
		return nil, fmt.Errorf("azure devops: %s: %w", c.op, err)
	}
	req.Header.Set("Accept", contentJSON)
	req.Header.Set("User-Agent", a.userAgent)
	if c.body != nil {
		ct := c.contentType
		if ct == "" {
			ct = contentJSON
		}
		req.Header.Set("Content-Type", ct)
	}
	resp, err := a.http.Do(req)
	if err != nil {
		return nil, a.transportError(ctx, c.op, err)
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxAnswer))
	if err != nil {
		return resp, fmt.Errorf("azure devops: %s: read answer: %w", c.op, err)
	}
	// 203 is the sign-in page Azure DevOps serves for a bad PAT when it
	// redirects instead of answering 401.
	if resp.StatusCode >= 300 || resp.StatusCode == http.StatusNonAuthoritativeInfo {
		return resp, a.statusError(c.op, resp.StatusCode, errorMessage(data))
	}
	if c.out != nil && len(data) > 0 {
		if err := json.Unmarshal(data, c.out); err != nil {
			return resp, fmt.Errorf("azure devops: %s: decode answer: %w", c.op, err)
		}
	}
	return resp, nil
}

// transportError is the error for a request that got no answer: a rate
// limit, the credential lookup, a timeout or the connection. It keeps the
// cause wrapped for errors.Is and errors.As, behind a scrubbed message.
func (a *Adapter) transportError(ctx context.Context, op string, err error) error {
	if rl, ok := errors.AsType[*ports.RateLimitError](err); ok {
		return fmt.Errorf("azure devops: %s: %w", op, rl)
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return fmt.Errorf("azure devops: %s: %w", op, ctxErr)
	}
	return &httpkit.ScrubbedError{Msg: fmt.Sprintf("azure devops: %s: %s", op, a.scrub(err.Error())), Err: err}
}

// errorMessage reads the message of an Azure DevOps error answer, such as
// {"message": "TF401019: ...", "typeKey": "..."}.
func errorMessage(data []byte) string {
	var e struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &e) != nil {
		return ""
	}
	return e.Message
}

// statusError is the error for an answer with an error status. The
// message is rebuilt from the operation, the status and Azure DevOps'
// message only, so that no request header, and so no token, can end up in
// it, and the message is scrubbed. 409 and 412 are a lost revision
// precondition (spec work-claiming, "Azure DevOps optimistic
// concurrency").
func (a *Adapter) statusError(op string, status int, message string) error {
	text := fmt.Sprintf("azure devops: %s: %d %s", op, status, http.StatusText(status))
	if message != "" {
		text += ": " + a.scrub(message)
	}
	switch status {
	case http.StatusNotFound, http.StatusGone:
		return fmt.Errorf("%s: %w", text, ports.ErrNotFound)
	case http.StatusConflict, http.StatusPreconditionFailed:
		return fmt.Errorf("%s: %w", text, ports.ErrConflict)
	}
	return &statusErr{msg: text, status: status}
}

// statusErr is an error status without a port error.
type statusErr struct {
	msg    string
	status int
}

func (e *statusErr) Error() string { return e.msg }

// statusOf returns the HTTP status behind err, or 0.
func statusOf(err error) int {
	if se, ok := errors.AsType[*statusErr](err); ok {
		return se.status
	}
	return 0
}

// scrub removes secrets from text that came from Azure DevOps or the
// network: the token in use, and whatever the redactor's patterns match.
func (a *Adapter) scrub(text string) string {
	return a.redactor.Redact(a.auth.scrub(text))
}

// api returns the URL of an organization API: base, then the escaped
// path segments, then query with api-version added when it has none.
func (a *Adapter) api(query url.Values, segments ...string) string {
	return buildURL(a.baseURL, query, segments...)
}

// identityAPI is api on the identity host (vssps).
func (a *Adapter) identityAPI(query url.Values, segments ...string) string {
	return buildURL(a.identityURL, query, segments...)
}

func buildURL(base string, query url.Values, segments ...string) string {
	escaped := make([]string, len(segments))
	for i, s := range segments {
		escaped[i] = url.PathEscape(s)
	}
	if query == nil {
		query = url.Values{}
	}
	if query.Get("api-version") == "" {
		query.Set("api-version", apiVersion)
	}
	return base + strings.Join(escaped, "/") + "?" + query.Encode()
}

// webURL is the address of a page in the web interface.
func (a *Adapter) webURL(segments ...string) string {
	escaped := make([]string, len(segments))
	for i, s := range segments {
		escaped[i] = url.PathEscape(s)
	}
	return a.baseURL + strings.Join(escaped, "/")
}

// patchOp is one operation of a JSON Patch document (RFC 6902), the body
// Azure DevOps takes to change a work item, such as
// {"op": "test", "path": "/rev", "value": 7}.
type patchOp struct {
	Op    string
	Path  string
	Value any
}

// MarshalJSON writes the value for every operation but remove, which
// has none. An empty value, such as cleared tags, is still sent.
func (p patchOp) MarshalJSON() ([]byte, error) {
	if p.Op == "remove" {
		return json.Marshal(struct {
			Op   string `json:"op"`
			Path string `json:"path"`
		}{p.Op, p.Path})
	}
	return json.Marshal(struct {
		Op    string `json:"op"`
		Path  string `json:"path"`
		Value any    `json:"value"`
	}{p.Op, p.Path, p.Value})
}

// listOf is the envelope of most Azure DevOps lists.
type listOf[T any] struct {
	Count int `json:"count"`
	Value []T `json:"value"`
}
