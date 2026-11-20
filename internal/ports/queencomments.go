package ports

import (
	"context"

	"github.com/matteorosani/anthill/internal/domain"
)

// QueenCommentPort posts the queen's own words on a ticket or PR, such as
// an answer given in the queen console (spec queen-console, "Answer from
// the console"). Unlike the comments of TrackerPort and CodeHostPort, the
// text carries no 🐜 prefix and no marker: Anthill posts it as the
// developer (design D14), so it is a human comment for every instance.
type QueenCommentPort interface {
	// PostAsQueen posts body on ref: as a reply in the thread threadID
	// when it is set, and as a new comment otherwise. It returns the
	// comment posted.
	PostAsQueen(ctx context.Context, ref domain.Ref, threadID, body string) (domain.Comment, error)
}
