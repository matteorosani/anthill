package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// Connection is one provider connection: one provider adapter, which is
// GitHub or one Azure DevOps organization, playing tracker and code host.
// Name is domain.ConnectionName of its repositories.
type Connection struct {
	Name     string
	Tracker  ports.TrackerPort
	CodeHost ports.CodeHostPort
}

// PollReport is what one poll of a connection did.
type PollReport struct {
	Connection string
	// Polled counts the sources (the items or the PRs of one repository)
	// read successfully.
	Polled int
	// Failed names each source that failed, with its error.
	Failed []string
	// RateLimitedUntil is set when the provider reported its rate limit
	// exhausted; the sources not read yet were skipped.
	RateLimitedUntil time.Time
	// Events counts the change events written to the outbox.
	Events int
}

// Ingestion polls a provider connection, turns what changed into change
// events, and writes them to the outbox with the new hashes and cursors
// in one transaction (design D7; spec work-item-sources, "Change
// detection").
type Ingestion struct {
	state  ports.PollState
	config *Configuration
	clock  ports.Clock
}

// NewIngestion returns an ingestion service over the poll state, reading
// the repositories to poll from config at every poll.
func NewIngestion(state ports.PollState, config *Configuration, clock ports.Clock) *Ingestion {
	return &Ingestion{state: state, config: config, clock: clock}
}

type sourceKind int

const (
	itemSource sourceKind = iota
	prSource
)

// source is one poll target of a connection: the items of a tracker
// repository or the PRs of a code repository. Its key names its cursor
// within the connection's cursor.
type source struct {
	key  string
	kind sourceKind
	repo domain.Repository
	// optIn holds the opt-in labels of the item source: those of every
	// enabled entry of the repository, since an Azure DevOps project can
	// back several entries.
	optIn []string
}

// sources lists what a connection polls: the items of each tracker
// repository, then the PRs of each code repository, of the enabled
// repository entries, in entry order.
func sources(snap *ConfigSnapshot, connection string) []source {
	var items, prs []source
	index := map[string]int{}
	for _, rc := range snap.Repositories {
		if !rc.Enabled() || domain.ConnectionName(rc.Repository) != connection {
			continue
		}
		tracker := domain.TrackerRepository(rc.Repository)
		key := "items:" + strings.ToLower(tracker.String())
		if i, ok := index[key]; ok {
			items[i].optIn = append(items[i].optIn, rc.Settings.OptInLabel)
		} else {
			index[key] = len(items)
			items = append(items, source{key: key, kind: itemSource, repo: tracker, optIn: []string{rc.Settings.OptInLabel}})
		}
		key = "prs:" + strings.ToLower(rc.Repository.String())
		if _, ok := index[key]; !ok {
			index[key] = len(prs)
			prs = append(prs, source{key: key, kind: prSource, repo: rc.Repository})
		}
	}
	return append(items, prs...)
}

// Poll polls every source of a connection once.
//
// Each source fails on its own: a failed source keeps its old cursor and
// is named in the recorded failure, while the others are stored (spec
// work-item-sources, "Supported providers"). Only when every source
// fails is nothing stored, so the last successful poll time stays. When
// the provider reports its rate limit exhausted, no further request is
// sent in this poll, and the reset time is recorded (spec
// work-item-sources, "Polling cadence").
//
// The error is for the store or a cancelled context only; provider
// failures are in the report.
func (in *Ingestion) Poll(ctx context.Context, conn Connection) (PollReport, error) {
	report := PollReport{Connection: conn.Name}
	snap := in.config.Current()
	if snap == nil {
		return report, errors.New("poll " + conn.Name + ": the configuration is not loaded")
	}
	status, err := in.state.PollStatus(ctx, conn.Name)
	if err != nil {
		return report, fmt.Errorf("read the poll state of %s: %w", conn.Name, err)
	}
	old := decodeCursors(status.Cursor)
	list := sources(snap, conn.Name)
	result := ports.PollResult{Connection: conn.Name}
	cursors := map[string]string{}
	for _, src := range list {
		if c, ok := old[src.key]; ok {
			cursors[src.key] = c
		}
	}
	for _, src := range list {
		cursor, err := in.pollSource(ctx, conn, src, old[src.key], &result)
		if ctx.Err() != nil {
			return report, ctx.Err()
		}
		var stored *storeError
		if errors.As(err, &stored) {
			return report, stored.err
		}
		var limited *ports.RateLimitError
		if errors.As(err, &limited) {
			report.RateLimitedUntil = limited.Reset
			break
		}
		if err != nil {
			report.Failed = append(report.Failed, src.describe()+": "+err.Error())
			continue
		}
		cursors[src.key] = cursor
		report.Polled++
	}

	now := in.clock.Now()
	if report.Polled > 0 || len(list) == 0 {
		result.Cursor, result.At = encodeCursors(cursors), now
		if err := in.state.RecordPoll(ctx, result); err != nil {
			return report, fmt.Errorf("record the poll of %s: %w", conn.Name, err)
		}
		report.Events = len(result.Events)
	}
	if message := report.failure(); message != "" {
		if err := in.state.RecordPollFailure(ctx, conn.Name, now, message, report.RateLimitedUntil); err != nil {
			return report, fmt.Errorf("record the failed poll of %s: %w", conn.Name, err)
		}
	}
	return report, nil
}

// failure is the message recorded for a poll that did not read every
// source, or "".
func (r PollReport) failure() string {
	parts := r.Failed
	if !r.RateLimitedUntil.IsZero() {
		parts = append([]string{fmt.Sprintf("%s until %s", ports.ErrRateLimited, r.RateLimitedUntil.UTC().Format(time.RFC3339))}, parts...)
	}
	return strings.Join(parts, "; ")
}

func (s source) describe() string {
	if s.kind == itemSource {
		return "items of " + s.repo.String()
	}
	return "pull requests of " + s.repo.String()
}

// pollSource reads one source from its cursor and adds what changed to
// result. It returns the source's next cursor.
func (in *Ingestion) pollSource(ctx context.Context, conn Connection, src source, cursor string, result *ports.PollResult) (string, error) {
	if src.kind == itemSource {
		page, err := conn.Tracker.Items(ctx, src.repo, cursor)
		if err != nil {
			return "", err
		}
		for _, item := range page.Items {
			prev, err := in.stored(ctx, item.Ref)
			if err != nil {
				return "", err
			}
			add(result, item.Ref, domain.IngestItem(prev, item, src.optIn))
		}
		return page.Cursor, nil
	}
	page, err := conn.CodeHost.PullRequests(ctx, src.repo, cursor)
	if err != nil {
		return "", err
	}
	for _, pr := range page.PullRequests {
		prev, err := in.stored(ctx, pr.Ref)
		if err != nil {
			return "", err
		}
		add(result, pr.Ref, domain.IngestPullRequest(prev, pr))
	}
	return page.Cursor, nil
}

// storeError is a failure of the local store while polling, which ends
// the poll instead of counting as a failure of the source.
type storeError struct{ err error }

func (e *storeError) Error() string { return e.err.Error() }

// stored reads what the previous polls kept of an item or PR.
func (in *Ingestion) stored(ctx context.Context, ref domain.Ref) (domain.StoredContent, error) {
	hashes, err := in.state.Hashes(ctx, ref)
	if err != nil {
		return domain.StoredContent{}, &storeError{fmt.Errorf("read the hashes of %s: %w", ref.Full(), err)}
	}
	texts := map[string]string{}
	if len(hashes) > 0 {
		if texts, err = in.state.Texts(ctx, ref); err != nil {
			return domain.StoredContent{}, &storeError{fmt.Errorf("read the texts of %s: %w", ref.Full(), err)}
		}
	}
	return domain.StoredContent{Hashes: hashes, Texts: texts}, nil
}

func add(result *ports.PollResult, ref domain.Ref, in domain.Ingested) {
	for _, h := range in.Hashes {
		result.Hashes = append(result.Hashes, ports.Hash{Ref: ref, Group: h.Group, Value: h.Value, Text: h.Text})
	}
	result.Events = append(result.Events, in.Events...)
}

// decodeCursors reads a connection's cursor: a JSON object from source
// key to the adapter's cursor of that source. A cursor that cannot be
// read counts as empty, so every source starts again from a full read;
// the stored hashes keep that from emitting changes twice.
func decodeCursors(s string) map[string]string {
	cursors := map[string]string{}
	if s != "" && json.Unmarshal([]byte(s), &cursors) != nil {
		return map[string]string{}
	}
	return cursors
}

func encodeCursors(cursors map[string]string) string {
	data, _ := json.Marshal(cursors) // A map of strings always marshals, with sorted keys.
	return string(data)
}
