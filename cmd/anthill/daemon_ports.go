package main

import (
	"context"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// provider is what the daemon needs of one provider adapter: tracker,
// code host and claim store, and posting the queen's answers.
type provider interface {
	ports.TrackerPort
	ports.CodeHostPort
	ports.ClaimStore
	ports.QueenCommentPort
}

// providerConnection is one provider connection: GitHub, or one Azure
// DevOps organization. Name is domain.ConnectionName of its repositories.
type providerConnection struct {
	Name     string
	Provider provider
	// Login reads the developer's login on the provider, the first part
	// of the instance id.
	Login func(ctx context.Context) (string, error)
}

// daemonPorts are the adapters the composition root hands to the
// application services. realPorts builds them for this machine; tests
// build them over fakes (internal/testkit).
type daemonPorts struct {
	Store       ports.StateStore
	Connections []providerConnection
	Git         ports.SourceControl
	Files       ports.WorktreeFiles
	Executor    ports.Executor
	Runners     map[domain.Runner]ports.AgentRunner
	Verifier    ports.Verifier
	// Machine is this machine's id, the second part of the instance id.
	Machine string
	// Close releases what the adapters hold, such as the database. Nil
	// means nothing to release.
	Close func() error
}
