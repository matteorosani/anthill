package azuredevops

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
)

// gitNamespace is the security namespace of Git repositories, and
// genericContribute its Contribute bit: the permission to push, which is
// what "write access to the repository" means on Azure DevOps.
const (
	gitNamespace      = "2e9eb7ed-3c0a-47d4-87c1-0ffdd275fd87"
	genericContribute = 4
)

// missingScopes names the PAT scopes the write-access check needs, for
// the warning logged when Azure DevOps refuses it.
const missingScopes = "Identity (Read) and Security (Manage)"

type repoJSON struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Project struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"project"`
}

type reposEntry struct {
	repos   []repoJSON
	expires time.Time
}

type accessKey struct {
	repo  domain.Repository
	login string
}

type accessEntry struct {
	write   bool
	expires time.Time
}

// Repositories lists the Git repositories of a project
// (organization/project), lowercased, so that items can be mapped to
// their target repository (spec work-item-sources, "Repository
// mapping"). The list is reused for ten minutes on the clock.
func (a *Adapter) Repositories(ctx context.Context, scope domain.Repository) ([]domain.Repository, error) {
	scope = Normalize(scope)
	scope.Name = ""
	if err := a.checkProject(scope); err != nil {
		return nil, err
	}
	list, err := a.projectRepos(ctx, scope)
	if err != nil {
		return nil, err
	}
	out := make([]domain.Repository, len(list))
	for i, r := range list {
		out[i] = domain.Repository{Provider: domain.AzureDevOps, Org: scope.Org, Project: scope.Project, Name: strings.ToLower(r.Name)}
	}
	return out, nil
}

// projectRepos reads the repositories of a project, cached.
func (a *Adapter) projectRepos(ctx context.Context, project domain.Repository) ([]repoJSON, error) {
	now := a.clock.Now()
	a.mu.Lock()
	entry, ok := a.repos[project.Project]
	a.mu.Unlock()
	if ok && now.Before(entry.expires) {
		return entry.repos, nil
	}
	var list listOf[repoJSON]
	if _, err := a.send(ctx, call{
		op:     "list repositories of " + project.String(),
		method: http.MethodGet,
		url:    a.api(nil, project.Org, project.Project, "_apis", "git", "repositories"),
		out:    &list,
	}); err != nil {
		return nil, err
	}
	a.mu.Lock()
	a.repos[project.Project] = reposEntry{repos: list.Value, expires: now.Add(cacheTTL)}
	a.mu.Unlock()
	return list.Value, nil
}

// projectID reads the ID of a project, kept for the life of the adapter.
func (a *Adapter) projectID(ctx context.Context, project domain.Repository) (string, error) {
	a.mu.Lock()
	id, ok := a.ids[project.Project]
	a.mu.Unlock()
	if ok {
		return id, nil
	}
	var p struct {
		ID string `json:"id"`
	}
	if _, err := a.send(ctx, call{
		op:     "read project " + project.String(),
		method: http.MethodGet,
		url:    a.api(nil, project.Org, "_apis", "projects", project.Project),
		out:    &p,
	}); err != nil {
		return "", err
	}
	a.mu.Lock()
	a.ids[project.Project] = p.ID
	a.mu.Unlock()
	return p.ID, nil
}

// CanWrite reports whether login, a unique name, has write access: the
// Contribute permission on the Git repository (organization/project/name)
// or, for a project (organization/project), on all its repositories (spec
// change-reaction, "Who can trigger work"; spec agent-conversation,
// "Commands are authorized"). Group memberships count, custom groups
// included, because Azure DevOps evaluates the effective permission.
//
// Answers, negative ones included, are reused for ten minutes on the
// clock. Errors are not cached. When Azure DevOps refuses the check
// (401 or 403), usually because a PAT lacks the Identity or Security
// scope, the answer is "no write access", it is not cached, and one
// warning names the missing scopes.
func (a *Adapter) CanWrite(ctx context.Context, repo domain.Repository, login string) (bool, error) {
	repo = Normalize(repo)
	if err := a.checkRepo(repo); err != nil {
		return false, err
	}
	key := accessKey{repo: repo, login: strings.ToLower(login)}
	now := a.clock.Now()
	a.mu.Lock()
	entry, ok := a.access[key]
	a.mu.Unlock()
	if ok && now.Before(entry.expires) {
		return entry.write, nil
	}
	write, err := a.lookupAccess(ctx, repo, key.login)
	if status := statusOf(err); status == http.StatusUnauthorized || status == http.StatusForbidden {
		a.warnScopes(err)
		return false, nil
	}
	if err != nil {
		return false, err
	}
	a.mu.Lock()
	a.access[key] = accessEntry{write: write, expires: now.Add(cacheTTL)}
	a.mu.Unlock()
	return write, nil
}

// warnScopes logs, once, that the write-access check was refused.
func (a *Adapter) warnScopes(err error) {
	a.mu.Lock()
	warned := a.scopeWarned
	a.scopeWarned = true
	a.mu.Unlock()
	if !warned {
		a.logger.Warn("azure devops refused the write-access check; every user is treated as without write access. A PAT needs the "+missingScopes+" scopes",
			"organization", a.org, "err", err.Error())
	}
}

// lookupAccess finds the user's identity and reads its effective
// permissions on the repository's security token. A user Azure DevOps
// does not know has no access.
func (a *Adapter) lookupAccess(ctx context.Context, repo domain.Repository, login string) (bool, error) {
	project := domain.Repository{Provider: repo.Provider, Org: repo.Org, Project: repo.Project}
	projectID, err := a.projectID(ctx, project)
	if err != nil {
		return false, err
	}
	token := "repoV2/" + projectID
	if repo.Name != "" {
		repos, err := a.projectRepos(ctx, project)
		if err != nil {
			return false, err
		}
		found := false
		for _, r := range repos {
			if strings.EqualFold(r.Name, repo.Name) {
				token += "/" + r.ID
				found = true
			}
		}
		if !found {
			return false, nil
		}
	}

	var ids listOf[identity]
	if _, err := a.send(ctx, call{
		op:     "find the identity of " + login,
		method: http.MethodGet,
		url: a.identityAPI(url.Values{
			"searchFilter":    {"General"},
			"filterValue":     {login},
			"queryMembership": {"None"},
		}, a.org, "_apis", "identities"),
		out: &ids,
	}); err != nil {
		return false, err
	}
	descriptor := ""
	for _, id := range ids.Value {
		if id.login() == login {
			descriptor = id.Descriptor
		}
	}
	if descriptor == "" {
		return false, nil
	}

	var acls listOf[struct {
		Aces map[string]struct {
			ExtendedInfo struct {
				EffectiveAllow int `json:"effectiveAllow"`
				EffectiveDeny  int `json:"effectiveDeny"`
			} `json:"extendedInfo"`
		} `json:"acesDictionary"`
	}]
	if _, err := a.send(ctx, call{
		op:     "read the permissions of " + login + " on " + repo.String(),
		method: http.MethodGet,
		url: a.api(url.Values{
			"token":               {token},
			"descriptors":         {descriptor},
			"includeExtendedInfo": {"true"},
		}, a.org, "_apis", "accesscontrollists", gitNamespace),
		out: &acls,
	}); err != nil {
		return false, err
	}
	for _, acl := range acls.Value {
		for d, ace := range acl.Aces {
			if strings.EqualFold(d, descriptor) {
				info := ace.ExtendedInfo
				return info.EffectiveAllow&genericContribute != 0 && info.EffectiveDeny&genericContribute == 0, nil
			}
		}
	}
	return false, nil
}
