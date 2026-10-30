package github

import (
	"context"
	"strings"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
)

type accessKey struct {
	repo  domain.Repository
	login string
}

type accessEntry struct {
	write   bool
	expires time.Time
}

// CanWrite reports whether login has write access to repo: the admin,
// maintain or write role. Triage and read are not write access (spec
// change-reaction, "Who can trigger work"; spec agent-conversation,
// "Commands are authorized"). A login that is not a collaborator, or does
// not exist, has none.
//
// Answers, negative ones included, are reused for ten minutes measured
// on the clock, because every comment and claim marker asks. Errors are
// not cached.
func (a *Adapter) CanWrite(ctx context.Context, repo domain.Repository, login string) (bool, error) {
	repo = Normalize(repo)
	if err := checkRepo(repo); err != nil {
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

	write, err := a.lookupAccess(ctx, repo, login)
	if err != nil {
		return false, err
	}
	a.mu.Lock()
	a.access[key] = accessEntry{write: write, expires: now.Add(writeAccessTTL)}
	a.mu.Unlock()
	return write, nil
}

func (a *Adapter) lookupAccess(ctx context.Context, repo domain.Repository, login string) (bool, error) {
	level, resp, err := a.client.Repositories.GetPermissionLevel(ctx, repo.Org, repo.Name, login)
	if err != nil {
		err = a.apiError("read the permission of "+login+" on "+repo.String(), resp, err)
		if notFound(err) {
			return false, nil
		}
		return false, err
	}
	switch strings.ToLower(level.GetRoleName()) {
	case "admin", "maintain", "write":
		return true, nil
	case "triage", "read":
		return false, nil
	}
	// Older servers give no role name, and a custom role's name says
	// nothing: the base permission decides, admin, write, read or none.
	p := level.GetPermission()
	return p == "admin" || p == "write", nil
}

// Repositories returns the repository itself: on GitHub an issue's
// repository is also its code repository (spec work-item-sources,
// "Repository mapping").
func (a *Adapter) Repositories(_ context.Context, scope domain.Repository) ([]domain.Repository, error) {
	scope = Normalize(scope)
	if err := checkRepo(scope); err != nil {
		return nil, err
	}
	return []domain.Repository{scope}, nil
}
