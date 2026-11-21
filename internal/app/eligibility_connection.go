package app

import (
	"context"
	"errors"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// ConnectionCandidates is TrackerCandidates limited to the repository
// entries of one provider connection (domain.ConnectionName), so that the
// daemon re-evaluates after a poll only what that poll could have
// changed.
func ConnectionCandidates(config *Configuration, connection string, tracker func(ctx context.Context, repo domain.Repository) (ports.TrackerPort, error)) CandidateSource {
	return func(ctx context.Context) ([]Candidate, error) {
		snap := config.Current()
		if snap == nil {
			return nil, errors.New("the configuration is not loaded")
		}
		only := *snap
		only.Repositories = nil
		for _, rc := range snap.Repositories {
			if domain.ConnectionName(rc.Repository) == connection {
				only.Repositories = append(only.Repositories, rc)
			}
		}
		return TrackerCandidates(&Configuration{current: &only}, tracker)(ctx)
	}
}
