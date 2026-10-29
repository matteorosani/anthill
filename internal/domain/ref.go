package domain

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Provider names a tracker and code host. Its value is the short prefix
// used in references and branch names.
type Provider string

// The supported providers.
const (
	GitHub      Provider = "gh"
	AzureDevOps Provider = "ado"
)

// Valid reports whether p is one of the supported providers.
func (p Provider) Valid() bool {
	return p == GitHub || p == AzureDevOps
}

// RefKind tells whether a reference points to an item or a pull request.
type RefKind string

// The kinds of reference.
const (
	KindItem RefKind = "item"
	KindPR   RefKind = "pr"
)

// Repository locates where an item or PR lives on its provider.
//
// GitHub uses Org (the owner) and Name: matteorosani/anthill.
// Azure DevOps uses Org, Project and Name. Work items belong to a project,
// so their repository has an empty Name (contoso/shop). Code repositories
// and their PRs have all three parts (contoso/shop/web).
type Repository struct {
	Provider Provider
	Org      string
	Project  string
	Name     string
}

// String prints the repository path, such as matteorosani/anthill or
// contoso/shop/web.
func (r Repository) String() string {
	parts := []string{r.Org, r.Project, r.Name}
	var nonEmpty []string
	for _, p := range parts {
		if p != "" {
			nonEmpty = append(nonEmpty, p)
		}
	}
	return strings.Join(nonEmpty, "/")
}

// Validate checks that the repository has the parts its provider needs.
func (r Repository) Validate() error {
	switch r.Provider {
	case GitHub:
		if r.Org == "" || r.Name == "" || r.Project != "" {
			return fmt.Errorf("github repository %q must be owner/name", r.String())
		}
	case AzureDevOps:
		if r.Org == "" || r.Project == "" {
			return fmt.Errorf("azure devops repository %q must be org/project or org/project/repo", r.String())
		}
	default:
		return fmt.Errorf("unknown provider %q", r.Provider)
	}
	return nil
}

// ErrInvalidRef is returned, wrapped, for every reference that cannot be
// parsed. Check for it with errors.Is.
var ErrInvalidRef = errors.New("invalid reference")

// Ref identifies one item or pull request on a provider. Two refs are
// equal, with ==, only when every field is equal.
type Ref struct {
	Repo   Repository
	Kind   RefKind
	Number int
}

// String prints the short form, such as gh-12 or ado-pr-34. The short form
// leaves out the repository, which comes from the context: branch names,
// commit trailers, markers, messages and telemetry.
func (r Ref) String() string {
	if r.Kind == KindPR {
		return fmt.Sprintf("%s-pr-%d", r.Repo.Provider, r.Number)
	}
	return fmt.Sprintf("%s-%d", r.Repo.Provider, r.Number)
}

// Full prints the reference with its repository, for logs:
// matteorosani/anthill#12 for an item and matteorosani/anthill#pr-34 for a
// pull request.
func (r Ref) Full() string {
	if r.Kind == KindPR {
		return fmt.Sprintf("%s#pr-%d", r.Repo, r.Number)
	}
	return fmt.Sprintf("%s#%d", r.Repo, r.Number)
}

// ParseRef parses a short form such as gh-12 or ado-pr-34 found in the
// given repository, and returns the full reference. The provider in the
// short form must match the repository's provider. An Azure DevOps work
// item belongs to the project, so its reference keeps only org/project
// even when repo names a code repository.
func ParseRef(short string, repo Repository) (Ref, error) {
	provider, kind, number, err := parseShortRef(short)
	if err != nil {
		return Ref{}, err
	}
	if err := repo.Validate(); err != nil {
		return Ref{}, fmt.Errorf("%w %q: %w", ErrInvalidRef, short, err)
	}
	if provider != repo.Provider {
		return Ref{}, fmt.Errorf("%w %q: repository %s is on provider %q", ErrInvalidRef, short, repo, repo.Provider)
	}
	if provider == AzureDevOps && kind == KindItem {
		repo.Name = ""
	}
	if provider == AzureDevOps && kind == KindPR && repo.Name == "" {
		return Ref{}, fmt.Errorf("%w %q: pull requests need a code repository, not project %s", ErrInvalidRef, short, repo)
	}
	return Ref{Repo: repo, Kind: kind, Number: number}, nil
}

// parseShortRef splits a short form into its parts without needing a
// repository. Numbers are positive and have no leading zeros, and only
// lowercase is accepted.
func parseShortRef(short string) (Provider, RefKind, int, error) {
	invalid := func(reason string) (Provider, RefKind, int, error) {
		return "", "", 0, fmt.Errorf("%w %q: %s", ErrInvalidRef, short, reason)
	}
	prefix, rest, ok := strings.Cut(short, "-")
	if !ok {
		return invalid("expected <provider>-<number> or <provider>-pr-<number>")
	}
	provider := Provider(prefix)
	if !provider.Valid() {
		return invalid("unknown provider " + strconv.Quote(prefix))
	}
	kind := KindItem
	if after, found := strings.CutPrefix(rest, "pr-"); found {
		kind = KindPR
		rest = after
	}
	if rest == "" || rest[0] == '0' {
		return invalid("number must be positive with no leading zeros")
	}
	for _, c := range rest {
		if c < '0' || c > '9' {
			return invalid("number must contain only digits")
		}
	}
	number, err := strconv.Atoi(rest)
	if err != nil {
		return invalid("number is too large")
	}
	return provider, kind, number, nil
}
