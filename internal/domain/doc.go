// Package domain holds Anthill's core model and rules: items, pull requests,
// claims, ants, budgets and the other concepts of the glossary.
//
// The domain does no I/O. It never talks to a provider, git, an agent, the
// file system or the clock directly; application services pass it what it
// needs (design D1).
package domain
