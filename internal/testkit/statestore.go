package testkit

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"sync"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// StateStore is an in-memory ports.StateStore. It behaves as the SQLite
// store does; the contract tests in package storetest run against both.
type StateStore struct {
	mu         sync.Mutex
	ants       []ports.AntRecord
	runs       []ports.RunRecord
	rounds     []ports.RoundRecord
	polls      map[string]ports.PollStatus
	hashes     map[domain.Ref]map[string]string
	texts      map[domain.Ref]map[string]string
	outbox     []outboxEntry
	changes    []changeEntry
	questions  map[string]ports.QuestionRecord
	extensions []ports.ExtensionRecord
	hidden     map[domain.Ref]ports.HiddenProposal
	claims     map[claimKey]ports.HeldClaim
}

type claimKey struct {
	ref  domain.Ref
	kind domain.ClaimKind
}

type outboxEntry struct {
	event ports.OutboxEvent
	done  bool
}

type changeEntry struct {
	change   ports.StoredChange
	consumed bool
}

var _ ports.StateStore = (*StateStore)(nil)

// NewStateStore returns an empty store.
func NewStateStore() *StateStore {
	return &StateStore{
		polls:     map[string]ports.PollStatus{},
		hashes:    map[domain.Ref]map[string]string{},
		texts:     map[domain.Ref]map[string]string{},
		questions: map[string]ports.QuestionRecord{},
		hidden:    map[domain.Ref]ports.HiddenProposal{},
		claims:    map[claimKey]ports.HeldClaim{},
	}
}

func cloneAnt(r ports.AntRecord) ports.AntRecord {
	r.History = slices.Clone(r.History)
	if r.Budget.Open != nil {
		open := *r.Budget.Open
		r.Budget.Open = &open
	}
	if r.PR != nil {
		pr := *r.PR
		r.PR = &pr
	}
	return r
}

func cloneUsage(u ports.Usage) ports.Usage {
	if u.Tokens != nil {
		t := *u.Tokens
		u.Tokens = &t
	}
	if u.CostUSD != nil {
		c := *u.CostUSD
		u.CostUSD = &c
	}
	return u
}

// CreateAnt stores a new ant.
func (s *StateStore) CreateAnt(_ context.Context, r ports.AntRecord) (ports.AntID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r = cloneAnt(r)
	r.ID = ports.AntID(len(s.ants) + 1)
	s.ants = append(s.ants, r)
	return r.ID, nil
}

// SaveAnt updates an ant. Its history must extend the stored one.
func (s *StateStore) SaveAnt(_ context.Context, r ports.AntRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := int(r.ID) - 1
	if i < 0 || i >= len(s.ants) {
		return fmt.Errorf("ant %d: %w", r.ID, ports.ErrNotFound)
	}
	if len(r.History) < len(s.ants[i].History) {
		return fmt.Errorf("ant %d: history cannot shrink from %d to %d transitions", r.ID, len(s.ants[i].History), len(r.History))
	}
	s.ants[i] = cloneAnt(r)
	return nil
}

// Ant returns an ant by ID.
func (s *StateStore) Ant(_ context.Context, id ports.AntID) (ports.AntRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := int(id) - 1
	if i < 0 || i >= len(s.ants) {
		return ports.AntRecord{}, fmt.Errorf("ant %d: %w", id, ports.ErrNotFound)
	}
	return cloneAnt(s.ants[i]), nil
}

// CurrentAnt returns the latest ant of an item.
func (s *StateStore) CurrentAnt(_ context.Context, item domain.Ref) (ports.AntRecord, error) {
	return s.latest(func(r ports.AntRecord) bool { return r.Item == item }, "item "+item.Full())
}

// AntByBranch returns the latest ant on a branch.
func (s *StateStore) AntByBranch(_ context.Context, branch string) (ports.AntRecord, error) {
	return s.latest(func(r ports.AntRecord) bool { return r.Branch == branch }, "branch "+branch)
}

func (s *StateStore) latest(match func(ports.AntRecord) bool, what string) (ports.AntRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.ants) - 1; i >= 0; i-- {
		if match(s.ants[i]) {
			return cloneAnt(s.ants[i]), nil
		}
	}
	return ports.AntRecord{}, fmt.Errorf("ant for %s: %w", what, ports.ErrNotFound)
}

// Ants lists the current ant of every item in one of states.
func (s *StateStore) Ants(_ context.Context, states ...domain.AntState) ([]ports.AntRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := map[domain.Ref]ports.AntRecord{}
	for _, r := range s.ants {
		current[r.Item] = r
	}
	var out []ports.AntRecord
	for _, r := range current {
		if len(states) == 0 || slices.Contains(states, r.State()) {
			out = append(out, cloneAnt(r))
		}
	}
	slices.SortFunc(out, func(a, b ports.AntRecord) int { return cmp.Compare(a.ID, b.ID) })
	return out, nil
}

// StartRun stores an active run.
func (s *StateStore) StartRun(_ context.Context, r ports.RunRecord) (ports.RunID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r.ID = ports.RunID(len(s.runs) + 1)
	r.Usage = cloneUsage(r.Usage)
	s.runs = append(s.runs, r)
	return r.ID, nil
}

// EndRun stores the end of a run.
func (s *StateStore) EndRun(_ context.Context, r ports.RunRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := int(r.ID) - 1
	if i < 0 || i >= len(s.runs) {
		return fmt.Errorf("run %d: %w", r.ID, ports.ErrNotFound)
	}
	run := &s.runs[i]
	run.EndedAt, run.Outcome, run.Reason, run.Usage = r.EndedAt, r.Outcome, r.Reason, cloneUsage(r.Usage)
	if r.SessionID != "" {
		run.SessionID = r.SessionID
	}
	return nil
}

// Runs lists an ant's runs.
func (s *StateStore) Runs(_ context.Context, ant ports.AntID) ([]ports.RunRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ports.RunRecord
	for _, r := range s.runs {
		if r.Ant == ant {
			r.Usage = cloneUsage(r.Usage)
			out = append(out, r)
		}
	}
	return out, nil
}

// StartRound stores an active round.
func (s *StateStore) StartRound(_ context.Context, r ports.RoundRecord) (ports.RoundID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r.ID = ports.RoundID(len(s.rounds) + 1)
	r.Usage = cloneUsage(r.Usage)
	s.rounds = append(s.rounds, r)
	return r.ID, nil
}

// EndRound stores the end of a round.
func (s *StateStore) EndRound(_ context.Context, r ports.RoundRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := int(r.ID) - 1
	if i < 0 || i >= len(s.rounds) {
		return fmt.Errorf("round %d: %w", r.ID, ports.ErrNotFound)
	}
	s.rounds[i].EndedAt, s.rounds[i].Usage = r.EndedAt, cloneUsage(r.Usage)
	return nil
}

// Rounds lists a PR's rounds.
func (s *StateStore) Rounds(_ context.Context, pr domain.Ref) ([]ports.RoundRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ports.RoundRecord
	for _, r := range s.rounds {
		if r.PR == pr {
			r.Usage = cloneUsage(r.Usage)
			out = append(out, r)
		}
	}
	return out, nil
}

// Usage lists the runs and rounds started in [from, to).
func (s *StateStore) Usage(_ context.Context, from, to time.Time) ([]ports.RunRecord, []ports.RoundRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	in := func(t time.Time) bool { return !t.Before(from) && t.Before(to) }
	var runs []ports.RunRecord
	for _, r := range s.runs {
		if in(r.StartedAt) {
			r.Usage = cloneUsage(r.Usage)
			runs = append(runs, r)
		}
	}
	var rounds []ports.RoundRecord
	for _, r := range s.rounds {
		if in(r.StartedAt) {
			r.Usage = cloneUsage(r.Usage)
			rounds = append(rounds, r)
		}
	}
	return runs, rounds, nil
}

// RecordPoll stores a poll's cursor, hashes and events at once.
func (s *StateStore) RecordPoll(_ context.Context, p ports.PollResult) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.polls[p.Connection]
	st.Connection, st.Cursor, st.LastSuccess = p.Connection, p.Cursor, p.At
	st.LastError, st.LastErrorAt, st.RateLimitedUntil = "", time.Time{}, time.Time{}
	s.polls[p.Connection] = st
	for _, h := range p.Hashes {
		if s.hashes[h.Ref] == nil {
			s.hashes[h.Ref] = map[string]string{}
		}
		s.hashes[h.Ref][h.Group] = h.Value
		if s.texts[h.Ref] == nil {
			s.texts[h.Ref] = map[string]string{}
		}
		if h.Text == "" {
			delete(s.texts[h.Ref], h.Group)
		} else {
			s.texts[h.Ref][h.Group] = h.Text
		}
	}
	for _, e := range p.Events {
		id := ports.OutboxID(len(s.outbox) + 1)
		s.outbox = append(s.outbox, outboxEntry{event: ports.OutboxEvent{ID: id, Event: e, DetectedAt: p.At}})
	}
	return nil
}

// RecordPollFailure stores a failed poll.
func (s *StateStore) RecordPollFailure(_ context.Context, connection string, at time.Time, message string, rateLimitedUntil time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.polls[connection]
	st.Connection, st.LastError, st.LastErrorAt, st.RateLimitedUntil = connection, message, at, rateLimitedUntil
	s.polls[connection] = st
	return nil
}

// PollStatus returns a connection's state.
func (s *StateStore) PollStatus(_ context.Context, connection string) (ports.PollStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st, ok := s.polls[connection]; ok {
		return st, nil
	}
	return ports.PollStatus{Connection: connection}, nil
}

// PollStatuses lists every connection's state by name.
func (s *StateStore) PollStatuses(_ context.Context) ([]ports.PollStatus, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Collect(maps.Values(s.polls))
	slices.SortFunc(out, func(a, b ports.PollStatus) int { return cmp.Compare(a.Connection, b.Connection) })
	return out, nil
}

// Hashes returns the stored hashes of an item or PR.
func (s *StateStore) Hashes(_ context.Context, ref domain.Ref) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return maps.Clone(s.hashes[ref]), nil
}

// Texts returns the stored texts of an item or PR.
func (s *StateStore) Texts(_ context.Context, ref domain.Ref) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := maps.Clone(s.texts[ref])
	if out == nil {
		out = map[string]string{}
	}
	return out, nil
}

// Pending lists the events not yet done.
func (s *StateStore) Pending(_ context.Context) ([]ports.OutboxEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ports.OutboxEvent
	for _, e := range s.outbox {
		if !e.done {
			out = append(out, e.event)
		}
	}
	return out, nil
}

// Done marks an event handled. Marking it again does nothing.
func (s *StateStore) Done(_ context.Context, id ports.OutboxID, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	i := int(id) - 1
	if i < 0 || i >= len(s.outbox) {
		return fmt.Errorf("outbox event %d: %w", id, ports.ErrNotFound)
	}
	s.outbox[i].done = true
	return nil
}

// StoreChange stores a change unless its Source is already stored.
func (s *StateStore) StoreChange(_ context.Context, c ports.StoredChange) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c.Source != 0 && slices.ContainsFunc(s.changes, func(e changeEntry) bool { return e.change.Source == c.Source }) {
		return nil
	}
	c.ID = int64(len(s.changes) + 1)
	s.changes = append(s.changes, changeEntry{change: c})
	return nil
}

// StoredChanges lists an ant's changes not yet consumed.
func (s *StateStore) StoredChanges(_ context.Context, ant ports.AntID) ([]ports.StoredChange, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ports.StoredChange
	for _, e := range s.changes {
		if e.change.Ant == ant && !e.consumed {
			out = append(out, e.change)
		}
	}
	return out, nil
}

// ConsumeChanges marks changes as given to a run.
func (s *StateStore) ConsumeChanges(_ context.Context, ids []int64, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, id := range ids {
		if id < 1 || int(id) > len(s.changes) {
			return fmt.Errorf("stored change %d: %w", id, ports.ErrNotFound)
		}
	}
	for _, id := range ids {
		s.changes[id-1].consumed = true
	}
	return nil
}

// SaveQuestion stores or updates a question.
func (s *StateStore) SaveQuestion(_ context.Context, q ports.QuestionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.questions[q.CommentID] = q
	return nil
}

// PendingQuestions lists the questions without an answer.
func (s *StateStore) PendingQuestions(_ context.Context) ([]ports.QuestionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ports.QuestionRecord
	for _, q := range s.questions {
		if q.AnsweredAt.IsZero() {
			out = append(out, q)
		}
	}
	slices.SortFunc(out, func(a, b ports.QuestionRecord) int {
		return cmp.Or(a.AskedAt.Compare(b.AskedAt), cmp.Compare(a.CommentID, b.CommentID))
	})
	return out, nil
}

// SaveExtension stores or updates an extension request.
func (s *StateStore) SaveExtension(_ context.Context, r ports.ExtensionRecord) (ports.ExtensionID, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.ID == 0 {
		r.ID = ports.ExtensionID(len(s.extensions) + 1)
		s.extensions = append(s.extensions, r)
		return r.ID, nil
	}
	i := int(r.ID) - 1
	if i < 0 || i >= len(s.extensions) {
		return 0, fmt.Errorf("extension request %d: %w", r.ID, ports.ErrNotFound)
	}
	s.extensions[i] = r
	return r.ID, nil
}

// PendingExtensions lists the requests waiting for the queen.
func (s *StateStore) PendingExtensions(_ context.Context) ([]ports.ExtensionRecord, error) {
	return s.filterExtensions(func(r ports.ExtensionRecord) bool { return r.Request.Status == domain.ExtensionPending })
}

// Extensions lists an ant's requests.
func (s *StateStore) Extensions(_ context.Context, ant ports.AntID) ([]ports.ExtensionRecord, error) {
	return s.filterExtensions(func(r ports.ExtensionRecord) bool { return r.Ant == ant })
}

func (s *StateStore) filterExtensions(match func(ports.ExtensionRecord) bool) ([]ports.ExtensionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []ports.ExtensionRecord
	for _, r := range s.extensions {
		if match(r) {
			out = append(out, r)
		}
	}
	return out, nil
}

// Hide stores or replaces an item's hidden entry.
func (s *StateStore) Hide(_ context.Context, h ports.HiddenProposal) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.hidden[h.Item] = h
	return nil
}

// Unhide removes an item's hidden entry.
func (s *StateStore) Unhide(_ context.Context, item domain.Ref) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.hidden, item)
	return nil
}

// Hidden lists the hidden entries, oldest first.
func (s *StateStore) Hidden(_ context.Context) ([]ports.HiddenProposal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Collect(maps.Values(s.hidden))
	slices.SortFunc(out, func(a, b ports.HiddenProposal) int {
		return cmp.Or(a.HiddenAt.Compare(b.HiddenAt), cmp.Compare(refKey(a.Item), refKey(b.Item)))
	})
	return out, nil
}

// SaveClaim stores or replaces a held claim.
func (s *StateStore) SaveClaim(_ context.Context, c ports.HeldClaim) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.claims[claimKey{c.Ref, c.Kind}] = c
	return nil
}

// DeleteClaim removes a held claim.
func (s *StateStore) DeleteClaim(_ context.Context, ref domain.Ref, kind domain.ClaimKind) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.claims, claimKey{ref, kind})
	return nil
}

// HeldClaims lists the held claims by ref and kind.
func (s *StateStore) HeldClaims(_ context.Context) ([]ports.HeldClaim, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := slices.Collect(maps.Values(s.claims))
	slices.SortFunc(out, func(a, b ports.HeldClaim) int {
		return cmp.Or(cmp.Compare(refKey(a.Ref), refKey(b.Ref)), cmp.Compare(a.Kind, b.Kind))
	})
	return out, nil
}

// refKey is the ref's text key, the same as the SQLite store's.
func refKey(r domain.Ref) string {
	return string(r.Repo.Provider) + ":" + r.Full()
}
