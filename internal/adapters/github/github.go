// Package github is the GitHub adapter. One Adapter implements
// ports.TrackerPort (issues), ports.CodeHostPort (pull requests) and
// ports.ClaimStore (claim comments, the claim label and the assignee) for
// one GitHub host, because GitHub plays both provider roles (design D1).
//
// It talks REST through google/go-github (design D13) and GraphQL, for
// what REST does not offer, through a small client in graphql.go: review
// threads with their resolution, replies to and resolution of threads,
// and switching a PR between draft and ready.
//
// # Polling
//
// Items and PullRequests return a cursor, which the caller stores in
// ports.PollState and passes back to the next poll. It holds the latest
// updated_at seen and the ETag of the last answer, so an unchanged
// repository costs one conditional request answered with 304, which does
// not count against the rate limit (design D7).
//
// The first poll of a repository (empty cursor) reads the issues with
// the opt-in label. Later polls read every issue changed since the
// cursor, with or without the label, so that the removal of the opt-in
// label or the closing of an item is seen. Incremental polls therefore
// return issues that are not opted in, and the ingestion service filters
// them out.
//
// # Names and case
//
// GitHub owner and repository names are case-insensitive, but
// domain.Ref equality is exact. The adapter lowercases the owner and the
// name of every Repository and Ref it builds or receives, so
// MatteoRosani/Anthill and matteorosani/anthill are the same repository.
// Use Normalize for repositories that come from configuration.
//
// # Rate limits
//
// When GitHub reports that a rate limit is exhausted, the adapter sends
// no request for that limit before the reported reset and returns a
// *ports.RateLimitError instead (spec work-item-sources, "Polling
// cadence"). It never sleeps: the caller decides when to poll again.
package github

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	gh "github.com/google/go-github/v93/github"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// DefaultHost is the host of github.com.
const DefaultHost = "github.com"

// DefaultTimeout bounds every HTTP request, so a hung connection cannot
// block a poll forever.
const DefaultTimeout = 30 * time.Second

// writeAccessTTL is how long a write-access answer is reused.
const writeAccessTTL = 10 * time.Minute

// RepoOptions are the per-repository settings the adapter needs.
type RepoOptions struct {
	// OptInLabel filters the first poll of a repository. Default
	// domain.DefaultOptInLabel.
	OptInLabel string
	// BranchPrefix finds the item of a PR by its branch name, such as
	// ant/gh-12-fix-login. Default domain.DefaultBranchPrefix.
	BranchPrefix string
}

// Options configure an Adapter.
type Options struct {
	// Host is the GitHub host, such as github.com or a GitHub Enterprise
	// Server host. Default DefaultHost. It also names the credential:
	// domain.GitHubCredential(Host).
	Host string
	// Credentials gives the token, such as app.Credentials over the OS
	// credential store and the gh session. Required.
	Credentials ports.CredentialSource
	// Clock times rate-limit pauses and the write-access cache. Required.
	Clock ports.Clock
	// Repo returns the settings of one repository. Nil means the defaults
	// for every repository.
	Repo func(domain.Repository) RepoOptions
	// Version goes into the User-Agent header, anthill/<Version>. Default
	// "dev".
	Version string
	// Logger receives one debug line per request: method, path and
	// status, never headers. Nil discards.
	Logger *slog.Logger
	// Redactor scrubs messages from GitHub before they go into errors.
	// Nil means the built-in patterns only.
	Redactor *domain.Redactor
	// Timeout bounds each request. Default DefaultTimeout.
	Timeout time.Duration
	// BaseURL replaces the REST API URL, with a trailing slash; GraphQL
	// is then sent to BaseURL + "graphql". Tests point it at an
	// httptest.Server. Empty means the URL of Host.
	BaseURL string
	// Transport is the underlying HTTP transport. Nil means
	// http.DefaultTransport.
	Transport http.RoundTripper
}

// Adapter is the GitHub adapter for one host.
type Adapter struct {
	client     *gh.Client
	http       *http.Client
	graphqlURL string
	host       string
	clock      ports.Clock
	repoOpts   func(domain.Repository) RepoOptions
	redactor   *domain.Redactor
	auth       *authTransport
	gate       *gate

	mu sync.Mutex
	// login is the authenticated user, read once with GET /user.
	login  string
	access map[accessKey]accessEntry
}

var (
	_ ports.TrackerPort  = (*Adapter)(nil)
	_ ports.CodeHostPort = (*Adapter)(nil)
	_ ports.ClaimStore   = (*Adapter)(nil)
)

// New returns an adapter. It sends no request until the first call.
func New(o Options) (*Adapter, error) {
	if o.Credentials == nil || o.Clock == nil {
		return nil, fmt.Errorf("github adapter needs credentials and a clock")
	}
	if o.Host == "" {
		o.Host = DefaultHost
	}
	if o.Version == "" {
		o.Version = "dev"
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	if o.Redactor == nil {
		o.Redactor, _ = domain.NewRedactor(nil)
	}
	if o.Timeout == 0 {
		o.Timeout = DefaultTimeout
	}
	if o.Transport == nil {
		o.Transport = http.DefaultTransport
	}
	if o.Repo == nil {
		o.Repo = func(domain.Repository) RepoOptions { return RepoOptions{} }
	}

	restURL, graphqlURL := o.BaseURL, ""
	switch {
	case restURL != "":
		if !strings.HasSuffix(restURL, "/") {
			restURL += "/"
		}
		graphqlURL = restURL + "graphql"
	case o.Host == DefaultHost:
		restURL, graphqlURL = "https://api.github.com/", "https://api.github.com/graphql"
	default:
		restURL, graphqlURL = "https://"+o.Host+"/api/v3/", "https://"+o.Host+"/api/graphql"
	}

	g := &gate{clock: o.Clock, until: map[string]time.Time{}}
	auth := &authTransport{
		base:        o.Transport,
		credentials: o.Credentials,
		key:         domain.GitHubCredential(o.Host),
	}
	httpClient := &http.Client{
		Timeout:   o.Timeout,
		Transport: &gateTransport{base: auth, gate: g, logger: o.Logger},
	}
	client, err := gh.NewClient(
		gh.WithHTTPClient(httpClient),
		gh.WithURLs(&restURL, &restURL),
		gh.WithUserAgent("anthill/"+o.Version),
		// The gate times rate limits on the ports.Clock; go-github's own
		// check would use the machine's clock.
		gh.WithDisableRateLimitCheck(),
	)
	if err != nil {
		return nil, fmt.Errorf("github client: %w", err)
	}
	return &Adapter{
		client:     client,
		http:       httpClient,
		graphqlURL: graphqlURL,
		host:       o.Host,
		clock:      o.Clock,
		repoOpts:   o.Repo,
		redactor:   o.Redactor,
		auth:       auth,
		gate:       g,
		access:     map[accessKey]accessEntry{},
	}, nil
}

// Normalize returns repo with its owner and name in lowercase, the form
// in which the adapter builds every GitHub Repository and Ref.
func Normalize(repo domain.Repository) domain.Repository {
	repo.Org = strings.ToLower(repo.Org)
	repo.Name = strings.ToLower(repo.Name)
	return repo
}

func normalizeRef(ref domain.Ref) domain.Ref {
	ref.Repo = Normalize(ref.Repo)
	return ref
}

// options returns the settings of repo with defaults filled in.
func (a *Adapter) options(repo domain.Repository) RepoOptions {
	o := a.repoOpts(repo)
	if o.OptInLabel == "" {
		o.OptInLabel = domain.DefaultOptInLabel
	}
	if o.BranchPrefix == "" {
		o.BranchPrefix = domain.DefaultBranchPrefix
	}
	return o
}

// checkRepo refuses repositories of another provider.
func checkRepo(repo domain.Repository) error {
	if repo.Provider != domain.GitHub {
		return fmt.Errorf("github adapter: repository %s is on provider %q", repo, repo.Provider)
	}
	return repo.Validate()
}

// serverTime is the time in the Date header of a response, or the clock's
// time when the header is missing or invalid.
func (a *Adapter) serverTime(resp *gh.Response) time.Time {
	if resp != nil && resp.Response != nil {
		if t, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
			return t
		}
	}
	return a.clock.Now()
}

// currentLogin is the authenticated user's login, read once.
func (a *Adapter) currentLogin(ctx context.Context) (string, error) {
	a.mu.Lock()
	login := a.login
	a.mu.Unlock()
	if login != "" {
		return login, nil
	}
	user, resp, err := a.client.Users.Get(ctx, "")
	if err != nil {
		return "", a.apiError("read the authenticated user", resp, err)
	}
	a.mu.Lock()
	a.login = user.GetLogin()
	a.mu.Unlock()
	return user.GetLogin(), nil
}
