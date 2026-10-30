package domain

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"slices"
	"strings"
)

// The field groups an item or PR is hashed by (design D7). A change in a
// group's hash becomes change events; an unchanged group emits none.
const (
	GroupTitle              = "title"
	GroupBody               = "body"
	GroupAcceptanceCriteria = "acceptance-criteria"
	GroupState              = "state"
	GroupLabels             = "labels"
	GroupAssignees          = "assignees"
	// GroupLinks covers dependency, parent and child links.
	GroupLinks = "links"
	// GroupOptIn records whether the item was opted in.
	GroupOptIn = "opt-in"
	GroupDraft = "draft"
	GroupHead  = "head"
)

// CommentGroup is the field group of one comment, keyed by its ID.
func CommentGroup(id string) string {
	return "comment/" + id
}

// ThreadCommentGroup is the field group of one comment in a PR review
// thread. The thread is part of the key because a provider may number
// comments per thread.
func ThreadCommentGroup(thread, id string) string {
	return "thread/" + thread + "/comment/" + id
}

// GroupHash is the hash of one field group. Text is the group's text,
// kept for the groups whose old value a change event carries: titles,
// bodies and acceptance criteria (for the diff of an edit), and labels
// (to tell which label was added or removed). It is empty for the others.
type GroupHash struct {
	Group string
	Value string
	Text  string
}

// StoredContent is what the local store keeps of an item or PR from the
// previous poll: the hash and the text of each group. Both maps are
// empty for an item or PR never seen.
type StoredContent struct {
	Hashes map[string]string
	Texts  map[string]string
}

// Ingested is what a poll found for one item or PR: its change events,
// in a fixed order, and the hashes to store, those of the groups that
// changed.
type Ingested struct {
	Events []ChangeEvent
	Hashes []GroupHash
}

// hashOf is the SHA-256 of a value's JSON encoding, in hex. JSON encodes
// strings, slices and struct fields in a fixed order, so equal content
// always gives the same hash; callers sort lists whose order does not
// matter first.
func hashOf(v any) string {
	data, err := json.Marshal(v)
	if err != nil {
		// Only strings, booleans and slices of strings are hashed, which
		// always marshal.
		panic("hash field group: " + err.Error())
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func textHash(group, text string) GroupHash {
	return GroupHash{Group: group, Value: hashOf(text), Text: text}
}

// labelsHash hashes labels without regard to order or case, as HasLabel
// compares them. Text keeps them as written, one per line.
func labelsHash(labels []string) GroupHash {
	sorted := slices.Clone(labels)
	slices.SortFunc(sorted, func(a, b string) int { return cmp.Compare(strings.ToLower(a), strings.ToLower(b)) })
	lower := make([]string, len(sorted))
	for i, l := range sorted {
		lower[i] = strings.ToLower(l)
	}
	return GroupHash{Group: GroupLabels, Value: hashOf(lower), Text: strings.Join(sorted, "\n")}
}

func setHash(group string, values []string) GroupHash {
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	return GroupHash{Group: group, Value: hashOf(sorted)}
}

func itemHashes(item WorkItem, optedIn bool) []GroupHash {
	var links []string
	for _, d := range item.Dependencies {
		links = append(links, "dependency "+d.Full())
	}
	if item.Parent != nil {
		links = append(links, "parent "+item.Parent.Full())
	}
	for _, c := range item.Children {
		links = append(links, "child "+c.Full())
	}
	assignees := make([]string, len(item.Assignees))
	for i, a := range item.Assignees {
		assignees[i] = strings.ToLower(a)
	}
	hashes := []GroupHash{
		textHash(GroupTitle, item.Title),
		textHash(GroupBody, item.Body),
		textHash(GroupAcceptanceCriteria, item.AcceptanceCriteria),
		{Group: GroupState, Value: hashOf(string(item.State))},
		labelsHash(item.Labels),
		setHash(GroupAssignees, assignees),
		setHash(GroupLinks, links),
		{Group: GroupOptIn, Value: hashOf(optedIn)},
	}
	for _, c := range item.Comments {
		hashes = append(hashes, GroupHash{Group: CommentGroup(c.ID), Value: hashOf(c.Body)})
	}
	return hashes
}

func pullRequestHashes(pr PullRequest) []GroupHash {
	hashes := []GroupHash{
		textHash(GroupTitle, pr.Title),
		textHash(GroupBody, pr.Body),
		{Group: GroupDraft, Value: hashOf(pr.Draft)},
		labelsHash(pr.Labels),
		{Group: GroupHead, Value: hashOf(pr.HeadCommit)},
	}
	for _, c := range pr.Comments {
		hashes = append(hashes, GroupHash{Group: CommentGroup(c.ID), Value: hashOf(c.Body)})
	}
	for _, th := range pr.Threads {
		for _, c := range th.Comments {
			hashes = append(hashes, GroupHash{Group: ThreadCommentGroup(th.ID, c.ID), Value: hashOf(c.Body)})
		}
	}
	return hashes
}

// changed returns the hashes that differ from the stored ones.
func changed(prev StoredContent, current []GroupHash) []GroupHash {
	var out []GroupHash
	for _, h := range current {
		if old, ok := prev.Hashes[h.Group]; !ok || old != h.Value {
			out = append(out, h)
		}
	}
	return out
}

// OptedIn reports whether an item carries one of the opt-in labels, in
// any case (spec work-item-sources, "Opt-in marking of tickets").
func OptedIn(item WorkItem, optInLabels []string) bool {
	return slices.ContainsFunc(optInLabels, func(l string) bool { return HasLabel(item.Labels, l) })
}

// IngestItem compares an item with what the previous poll stored and
// returns its change events and new hashes (spec work-item-sources,
// "Change detection" and "Opt-in marking of tickets"):
//
//   - an item never seen and not opted in is ignored: nothing is stored;
//   - an item that becomes opted in, for the first time or again, emits
//     one ChangeItemIngested, and its content becomes the baseline
//     without further events;
//   - an opted-in item that loses the opt-in label emits ChangeLabelRemoved
//     for each opt-in label it lost and nothing else;
//   - an item that stays without the opt-in label emits nothing, but its
//     hashes are kept current;
//   - an opted-in item emits one event per change.
//
// A comment's origin comes from its marker only, never from its author
// (spec work-item-sources, "Self-authored changes are distinguishable").
// Changes without a marker, such as labels or a state, are human
// (glossary, "Human").
func IngestItem(prev StoredContent, item WorkItem, optInLabels []string) Ingested {
	optedIn := OptedIn(item, optInLabels)
	known := len(prev.Hashes) > 0
	if !known && !optedIn {
		return Ingested{}
	}
	current := itemHashes(item, optedIn)
	diff := changed(prev, current)
	if len(diff) == 0 {
		return Ingested{}
	}
	result := Ingested{Hashes: diff}
	wasOptedIn := prev.Hashes[GroupOptIn] == hashOf(true)
	base := ChangeEvent{Ref: item.Ref, At: item.UpdatedAt, Origin: OriginHuman}
	switch {
	case optedIn && !wasOptedIn:
		e := base
		e.Kind = ChangeItemIngested
		result.Events = []ChangeEvent{e}
	case !optedIn && wasOptedIn:
		for _, e := range labelEvents(base, prev, current) {
			if e.Kind == ChangeLabelRemoved && slices.ContainsFunc(optInLabels, func(l string) bool { return strings.EqualFold(l, e.Label) }) {
				result.Events = append(result.Events, e)
			}
		}
	case optedIn:
		result.Events = itemEvents(base, prev, item, diff)
	}
	return result
}

func itemEvents(base ChangeEvent, prev StoredContent, item WorkItem, diff []GroupHash) []ChangeEvent {
	var events []ChangeEvent
	comments := map[string]Comment{}
	for _, c := range item.Comments {
		comments[CommentGroup(c.ID)] = c
	}
	for _, h := range diff {
		e := base
		switch h.Group {
		case GroupTitle, GroupBody, GroupAcceptanceCriteria:
			events = append(events, editEvent(base, prev, h))
			continue
		case GroupState:
			e.Kind, e.State = ChangeStateChanged, string(item.State)
		case GroupLabels:
			events = append(events, labelEvents(base, prev, []GroupHash{h})...)
			continue
		case GroupAssignees:
			e.Kind = ChangeAssigneesChanged
		case GroupLinks:
			e.Kind = ChangeLinksChanged
		default:
			c, ok := comments[h.Group]
			if !ok {
				continue
			}
			events = append(events, commentEvent(base, prev, h.Group, c, ""))
			continue
		}
		events = append(events, e)
	}
	return events
}

// IngestPullRequest compares a PR with what the previous poll stored and
// returns its change events and new hashes. A PR never seen emits one
// ChangePRIngested, and its content becomes the baseline. Origins follow
// the same rule as for items.
func IngestPullRequest(prev StoredContent, pr PullRequest) Ingested {
	current := pullRequestHashes(pr)
	diff := changed(prev, current)
	if len(diff) == 0 {
		return Ingested{}
	}
	result := Ingested{Hashes: diff}
	base := ChangeEvent{Ref: pr.Ref, At: pr.UpdatedAt, Origin: OriginHuman}
	if len(prev.Hashes) == 0 {
		e := base
		e.Kind = ChangePRIngested
		result.Events = []ChangeEvent{e}
		return result
	}
	type threadComment struct {
		thread  string
		comment Comment
	}
	comments := map[string]threadComment{}
	for _, c := range pr.Comments {
		comments[CommentGroup(c.ID)] = threadComment{comment: c}
	}
	for _, th := range pr.Threads {
		for _, c := range th.Comments {
			comments[ThreadCommentGroup(th.ID, c.ID)] = threadComment{thread: th.ID, comment: c}
		}
	}
	for _, h := range diff {
		e := base
		switch h.Group {
		case GroupTitle, GroupBody:
			result.Events = append(result.Events, editEvent(base, prev, h))
			continue
		case GroupDraft:
			e.Kind, e.State = ChangeDraftChanged, PRReady
			if pr.Draft {
				e.State = PRDraft
			}
		case GroupLabels:
			result.Events = append(result.Events, labelEvents(base, prev, []GroupHash{h})...)
			continue
		case GroupHead:
			e.Kind, e.Head = ChangeCommitsPushed, pr.HeadCommit
		default:
			tc, ok := comments[h.Group]
			if !ok {
				continue
			}
			result.Events = append(result.Events, commentEvent(base, prev, h.Group, tc.comment, tc.thread))
			continue
		}
		result.Events = append(result.Events, e)
	}
	return result
}

var groupFields = map[string]ItemField{
	GroupTitle:              FieldTitle,
	GroupBody:               FieldBody,
	GroupAcceptanceCriteria: FieldAcceptanceCriteria,
}

// editEvent is the ChangeItemEdited of a title, body or acceptance
// criteria, with the old and new text. Its origin is the marker of the
// new text, so that a PR body Anthill wrote counts as Anthill's.
func editEvent(base ChangeEvent, prev StoredContent, h GroupHash) ChangeEvent {
	e := base
	e.Kind, e.Field = ChangeItemEdited, groupFields[h.Group]
	e.Old, e.Body = prev.Texts[h.Group], h.Text
	e.Origin = OriginOf(h.Text)
	return e
}

// labelEvents compares the label lists, in any case, and returns one
// event per label added or removed, in the order of the lists.
func labelEvents(base ChangeEvent, prev StoredContent, current []GroupHash) []ChangeEvent {
	var now []string
	for _, h := range current {
		if h.Group == GroupLabels {
			now = splitLines(h.Text)
		}
	}
	before := splitLines(prev.Texts[GroupLabels])
	var events []ChangeEvent
	for _, l := range before {
		if !HasLabel(now, l) {
			e := base
			e.Kind, e.Label = ChangeLabelRemoved, l
			events = append(events, e)
		}
	}
	for _, l := range now {
		if !HasLabel(before, l) {
			e := base
			e.Kind, e.Label = ChangeLabelAdded, l
			events = append(events, e)
		}
	}
	return events
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, "\n")
}

// commentEvent is a ChangeCommentAdded for a comment the store has no
// hash of, or a ChangeCommentEdited. Only its marker decides its origin.
func commentEvent(base ChangeEvent, prev StoredContent, group string, c Comment, thread string) ChangeEvent {
	e := base
	e.Kind, e.At = ChangeCommentAdded, c.CreatedAt
	if _, ok := prev.Hashes[group]; ok {
		e.Kind, e.At = ChangeCommentEdited, c.UpdatedAt
	}
	e.Actor, e.CommentID, e.ThreadID, e.Body = c.Author, c.ID, thread, c.Body
	e.Origin = c.Origin()
	return e
}

// The provider connection names: one connection per provider adapter,
// which is GitHub (github.com) or one Azure DevOps organization.
const (
	ConnectionGitHub            = "github"
	connectionAzureDevOpsPrefix = "azure-devops/"
)

// ConnectionName names the provider connection that polls a repository:
// "github", or "azure-devops/<organization>" in lowercase. Each
// connection is polled on its own (spec work-item-sources, "Supported
// providers").
func ConnectionName(r Repository) string {
	if r.Provider == AzureDevOps {
		return connectionAzureDevOpsPrefix + strings.ToLower(r.Org)
	}
	return ConnectionGitHub
}

// TrackerRepository is the repository whose items belong with a code
// repository: the repository itself on GitHub, and its project
// (organization/project, in lowercase) on Azure DevOps.
func TrackerRepository(r Repository) Repository {
	if r.Provider == AzureDevOps {
		return Repository{Provider: AzureDevOps, Org: strings.ToLower(r.Org), Project: strings.ToLower(r.Project)}
	}
	return r
}
