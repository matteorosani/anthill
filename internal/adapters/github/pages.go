package github

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	gh "github.com/google/go-github/v93/github"
)

// collect follows GitHub's page links: it calls fetch for page 1, then
// for each next page the response names, and returns every element with
// the first page's response. T is the element type, such as
// *gh.IssueComment.
func collect[T any](fetch func(gh.ListOptions) ([]T, *gh.Response, error)) ([]T, *gh.Response, error) {
	var all []T
	var first *gh.Response
	for page := 1; page > 0; {
		batch, resp, err := fetch(gh.ListOptions{Page: page, PerPage: perPage})
		if err != nil {
			return nil, resp, err
		}
		if first == nil {
			first = resp
		}
		all = append(all, batch...)
		page = resp.NextPage
	}
	return all, first, nil
}

// listPages is collect for a GET that go-github offers no option for: a
// conditional request. It sends the first page with If-None-Match when
// etag is set and returns that page's response, whose status is 304,
// with no elements, when nothing changed.
func listPages[T any](ctx context.Context, a *Adapter, op, path string, query url.Values, etag string) ([]T, *gh.Response, error) {
	var all []T
	var first *gh.Response
	for page := 1; page > 0; {
		query.Set("page", strconv.Itoa(page))
		req, err := a.client.NewRequest(ctx, http.MethodGet, path+"?"+query.Encode(), nil)
		if err != nil {
			return nil, nil, fmt.Errorf("github: %s: %w", op, err)
		}
		if page == 1 && etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		var batch []T
		resp, err := a.client.Do(req, &batch)
		if page == 1 && resp != nil && resp.StatusCode == http.StatusNotModified {
			return nil, resp, nil
		}
		if err != nil {
			return nil, nil, a.apiError(op, resp, err)
		}
		if first == nil {
			first = resp
		}
		all = append(all, batch...)
		page = resp.NextPage
	}
	return all, first, nil
}
