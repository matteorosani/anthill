// Package testkit holds fakes for every interface in package ports, so that
// tests never need the network, a provider or a real agent (design D13).
// Every fake is deterministic and safe for concurrent use.
//
//   - Clock: time moves only on Advance or Set.
//   - Provider: one in-memory provider playing tracker, code host and
//     claim store; each instance talks to it through Provider.Client.
//   - AgentRunner: runs follow scripts instead of starting processes.
//   - SourceControl: clones, branches and worktrees in memory.
//   - Notifier: records notifications.
//   - StateStore: the local store in memory; package storetest checks it
//     against the same contract as the SQLite store.
//   - ConfigSource and CredentialStore: configuration and credentials.
//
// # Fake provider options
//
// Provider gives each comment a numeric ID and a CreatedAt from one
// sequence under one lock, so "provider creation order" is always
// defined, and its server time is the Clock passed to NewProvider. Tests
// change its behaviour with:
//
//   - Barrier(op, n): the next n calls of op wait, after the provider
//     has carried them out, until all n have arrived. With
//     Barrier(OpClaims, 2) and Barrier(OpPost, 2), two instances both
//     check, then both post, before either re-reads, so they race for a
//     claim and the order of their posts decides the winner. A call that
//     fails does not count; a waiting call returns when its context ends.
//   - ReadLag(d): reads hide comments created less than d ago, like a
//     provider with read-after-write lag.
//   - RateLimit(until): every call fails with *ports.RateLimitError until
//     the provider's time reaches until. Requests logs every call with
//     its time, refused ones included, so a test can check that nothing
//     was sent before the reset.
//   - Fail(op, err) fails every call of op until Fail(op, nil);
//     FailNext(op, err) fails only the next one, and several queue.
//
// Tests act as humans with PutItem, PutPullRequest, EditItem,
// EditPullRequest and Comment, and inspect state with Item, PullRequest
// and Requests.
package testkit
