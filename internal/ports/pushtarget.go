package ports

import "github.com/matteorosani/anthill/internal/domain"

// PushTarget is where SourceControl.PushTo pushes: a branch of the
// clone's remote, or of a fork (spec pr-shepherd, "Anthill verifies,
// pushes and replies").
type PushTarget struct {
	// Branch is the branch on the target, such as the source branch of a
	// human PR.
	Branch string
	// Expect is the commit the branch must be at before the push, such as
	// the head commit a fix pass started from. When the branch is
	// elsewhere or gone, nothing is pushed and PushTo returns ErrConflict.
	// Empty means no check.
	Expect string
	// Repo and URL name a fork the branch is in: the provider's
	// repository and clone URL. PushTo pushes there only when the URL
	// points to Repo (domain.Repository.MatchesRemoteURL). An empty URL
	// means the clone's own checked URL (Clone.URL).
	Repo domain.Repository
	URL  string
}
