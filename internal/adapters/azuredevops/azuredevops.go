// Package azuredevops is the Azure DevOps adapter. One Adapter implements
// ports.TrackerPort (work items), ports.CodeHostPort (Git pull requests)
// and ports.ClaimStore (claim comments, the claim tag and Assigned To) for
// one organization on dev.azure.com, because Azure DevOps plays both
// provider roles (design D1). Azure DevOps Server (on-premises) is not
// supported.
//
// It talks to the REST API, version 7.1 (7.1-preview.4 for work item
// comments, which are still in preview), with net/http and encoding/json.
// The microsoft/azure-devops-go-api library was rejected because it
// neither exposes the HTTP response, whose Date header the ports need, nor
// lets tests inject a transport (design D13).
//
// # Credentials
//
// The token comes from the credential source under
// domain.AzureDevOpsCredential(organization): the OS credential store,
// then the az session (app.Credentials). A token shaped like a JWT (three
// base64url parts, the first starting with eyJ) is a Microsoft Entra ID
// access token and is sent as a Bearer token; it is read again two
// minutes before the expiry it carries. Any other value is a personal
// access token (PAT), sent with Basic authentication and an empty user
// name. A PAT needs these scopes: Work Items (Read & write), Code (Read &
// write), Identity (Read) and Security (Manage); the last two only for
// the write-access check (CanWrite), which answers "no write access" when
// they are missing.
//
// # Polling
//
// Items finds changed work items with WIQL on System.ChangedDate and
// reads them in batches; PullRequests reads every active PR with its
// threads and returns those whose content changed. Both return a cursor,
// which the caller stores in ports.PollState and passes back (design D7).
//
// # Names and case
//
// Organization, project and repository names ignore case on Azure DevOps,
// but domain.Ref equality is exact. The adapter lowercases them in every
// Repository and Ref it builds or receives. Use Normalize for
// repositories that come from configuration. Tags are kept as Azure
// DevOps returns them; the domain compares labels in any case.
//
// # Rate limits
//
// Azure DevOps limits each user per organization, so the adapter keeps
// one pause for all its requests. When Azure DevOps throttles (429) or
// announces a delay with Retry-After, the adapter sends nothing before the
// reported time and returns a *ports.RateLimitError instead (spec
// work-item-sources, "Polling cadence"). It never sleeps and never
// retries a request.
package azuredevops

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/matteorosani/anthill/internal/adapters/internal/httpkit"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Default URLs of Azure DevOps Services.
const (
	DefaultBaseURL     = "https://dev.azure.com/"
	DefaultIdentityURL = "https://vssps.dev.azure.com/"
)

// DefaultTimeout bounds every HTTP request, so a hung connection cannot
// block a poll forever.
const DefaultTimeout = 30 * time.Second

// cacheTTL is how long write-access answers and repository lists are
// reused.
const cacheTTL = 10 * time.Minute

// RepoOptions are the per-repository settings the adapter needs.
type RepoOptions struct {
	// OptInLabel filters the first poll of a project. Default
	// domain.DefaultOptInLabel.
	OptInLabel string
	// BranchPrefix finds the item of a PR by its branch name, such as
	// ant/ado-12-fix-login. Default domain.DefaultBranchPrefix.
	BranchPrefix string
}

// Options configure an Adapter.
type Options struct {
	// Organization is the Azure DevOps organization, such as contoso.
	// Required. It also names the credential.
	Organization string
	// Credentials gives the token, such as app.Credentials over the OS
	// credential store and the az session. Required.
	Credentials ports.CredentialSource
	// Clock times rate-limit pauses, token refresh and the caches.
	// Required.
	Clock ports.Clock
	// Repo returns the settings of one project or repository. Nil means
	// the defaults.
	Repo func(domain.Repository) RepoOptions
	// Version goes into the User-Agent header, anthill/<Version>.
	// Default "dev".
	Version string
	// Logger receives one debug line per request: method, path and
	// status, never headers. Nil discards.
	Logger *slog.Logger
	// Redactor scrubs messages from Azure DevOps before they go into
	// errors. Nil means the built-in patterns only.
	Redactor *domain.Redactor
	// Timeout bounds each request. Default DefaultTimeout.
	Timeout time.Duration
	// BaseURL and IdentityURL replace DefaultBaseURL and
	// DefaultIdentityURL, with a trailing slash. Tests point them at an
	// httptest.Server.
	BaseURL     string
	IdentityURL string
	// Transport is the underlying HTTP transport. Nil means
	// http.DefaultTransport.
	Transport http.RoundTripper
}

// Adapter is the Azure DevOps adapter for one organization.
type Adapter struct {
	org         string
	baseURL     string
	identityURL string
	userAgent   string
	http        *http.Client
	clock       ports.Clock
	repoOpts    func(domain.Repository) RepoOptions
	redactor    *domain.Redactor
	logger      *slog.Logger
	auth        *authTransport
	gate        *httpkit.Gate

	mu sync.Mutex
	// login is the authenticated user's unique name, read once.
	login string
	// states caches the state categories of each work item type, by
	// project and type.
	states map[stateKey]map[string]string
	repos  map[string]reposEntry
	ids    map[string]string
	access map[accessKey]accessEntry
	// scopeWarned is set once a missing token scope was logged.
	scopeWarned bool
}

var (
	_ ports.TrackerPort  = (*Adapter)(nil)
	_ ports.CodeHostPort = (*Adapter)(nil)
	_ ports.ClaimStore   = (*Adapter)(nil)
)

// New returns an adapter. It sends no request until the first call.
func New(o Options) (*Adapter, error) {
	if o.Organization == "" || o.Credentials == nil || o.Clock == nil {
		return nil, fmt.Errorf("azure devops adapter needs an organization, credentials and a clock")
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
	if o.BaseURL == "" {
		o.BaseURL = DefaultBaseURL
	}
	if o.IdentityURL == "" {
		o.IdentityURL = DefaultIdentityURL
	}
	org := strings.ToLower(o.Organization)
	gate := httpkit.NewGate(o.Clock)
	auth := &authTransport{
		base: o.Transport,
		token: &httpkit.Token{
			Credentials:   o.Credentials,
			Key:           domain.AzureDevOpsCredential(org),
			Expiry:        jwtExpiry,
			RefreshBefore: refreshBefore,
			Clock:         o.Clock,
		},
	}
	return &Adapter{
		org:         org,
		baseURL:     withSlash(o.BaseURL),
		identityURL: withSlash(o.IdentityURL),
		userAgent:   "anthill/" + o.Version,
		http: &http.Client{
			Timeout:   o.Timeout,
			Transport: &gateTransport{base: auth, gate: gate, clock: o.Clock, logger: o.Logger},
		},
		clock:    o.Clock,
		repoOpts: o.Repo,
		redactor: o.Redactor,
		logger:   o.Logger,
		auth:     auth,
		gate:     gate,
		states:   map[stateKey]map[string]string{},
		repos:    map[string]reposEntry{},
		ids:      map[string]string{},
		access:   map[accessKey]accessEntry{},
	}, nil
}

func withSlash(s string) string {
	if !strings.HasSuffix(s, "/") {
		return s + "/"
	}
	return s
}

// Normalize returns repo with its organization, project and name in
// lowercase, the form in which the adapter builds every Azure DevOps
// Repository and Ref.
func Normalize(repo domain.Repository) domain.Repository {
	repo.Org = strings.ToLower(repo.Org)
	repo.Project = strings.ToLower(repo.Project)
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

// checkRepo refuses repositories of another provider or organization.
func (a *Adapter) checkRepo(repo domain.Repository) error {
	if repo.Provider != domain.AzureDevOps {
		return fmt.Errorf("azure devops adapter: repository %s is on provider %q", repo, repo.Provider)
	}
	if err := repo.Validate(); err != nil {
		return err
	}
	if repo.Org != a.org {
		return fmt.Errorf("azure devops adapter for %s: repository %s is in another organization", a.org, repo)
	}
	return nil
}

// checkProject refuses anything but a project (organization/project).
func (a *Adapter) checkProject(repo domain.Repository) error {
	if err := a.checkRepo(repo); err != nil {
		return err
	}
	if repo.Name != "" {
		return fmt.Errorf("azure devops adapter: work items belong to a project, not to repository %s", repo)
	}
	return nil
}

// checkCodeRepo refuses anything but a Git repository
// (organization/project/name).
func (a *Adapter) checkCodeRepo(repo domain.Repository) error {
	if err := a.checkRepo(repo); err != nil {
		return err
	}
	if repo.Name == "" {
		return fmt.Errorf("azure devops adapter: pull requests belong to a repository, not to project %s", repo)
	}
	return nil
}

// checkRef refuses a ref of the wrong kind of repository.
func (a *Adapter) checkRef(ref domain.Ref) error {
	if ref.Kind == domain.KindPR {
		return a.checkCodeRepo(ref.Repo)
	}
	return a.checkProject(ref.Repo)
}

// serverTime is the time in the Date header of a response, or the clock's
// time when the header is missing or invalid.
func (a *Adapter) serverTime(resp *http.Response) time.Time {
	if resp != nil {
		if t, err := http.ParseTime(resp.Header.Get("Date")); err == nil {
			return t
		}
	}
	return a.clock.Now()
}

// currentLogin is the authenticated user's unique name, lowercased, read
// once from the connection data.
func (a *Adapter) currentLogin(ctx context.Context) (string, error) {
	a.mu.Lock()
	login := a.login
	a.mu.Unlock()
	if login != "" {
		return login, nil
	}
	var data struct {
		AuthenticatedUser identity `json:"authenticatedUser"`
	}
	// connectionData takes no api-version.
	u := a.baseURL + url.PathEscape(a.org) + "/_apis/connectionData"
	if _, err := a.send(ctx, call{op: "read the authenticated user", method: http.MethodGet, url: u, out: &data}); err != nil {
		return "", err
	}
	login = data.AuthenticatedUser.login()
	if login == "" {
		return "", fmt.Errorf("azure devops: read the authenticated user: no account name in the answer")
	}
	a.mu.Lock()
	a.login = login
	a.mu.Unlock()
	return login, nil
}

// identity is an Azure DevOps identity as the connection data and the
// identities API return it.
type identity struct {
	ID         string `json:"id"`
	Descriptor string `json:"descriptor"`
	Properties struct {
		Account struct {
			Value string `json:"$value"`
		} `json:"Account"`
	} `json:"properties"`
}

func (i identity) login() string {
	return strings.ToLower(i.Properties.Account.Value)
}

// identityRef is a reference to a user in a work item field or a PR.
type identityRef struct {
	DisplayName string `json:"displayName"`
	UniqueName  string `json:"uniqueName"`
}

// login is the reference's unique name, lowercased.
func (r *identityRef) login() string {
	if r == nil {
		return ""
	}
	return strings.ToLower(r.UniqueName)
}
