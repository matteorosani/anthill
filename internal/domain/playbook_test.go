package domain_test

import (
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
)

// maxDecisions is the limit of decisions per trigger the tests allow, as
// the service does in case of a fault (design D22).
const maxDecisions = 10

// applyEffect returns the snapshot after the service has carried out d
// and recorded its effect, as each decision's doc comment describes.
func applyEffect(t *testing.T, s domain.AntSnapshot, d domain.AntDecision) domain.AntSnapshot {
	t.Helper()
	switch d := d.(type) {
	case domain.StartRun:
		s.State, s.QueuedFrom = domain.AntStarting, ""
		s.Runs++
		s.LastRun = d.Kind
		if d.Kind == domain.RoleFollowUpRun && d.NewBudget > 0 {
			s.FollowUpRuns++
		}
		s.Stored = without(s.Stored, d.Changes)
		s.Answer, s.Finish, s.Proposal = nil, nil, domain.ProposalNone
	case domain.DeliverChange:
		s.Unrouted = without(s.Unrouted, []domain.AntChange{d.Change})
	case domain.StoreChange:
		s.Unrouted = without(s.Unrouted, []domain.AntChange{d.Change})
		s.Stored = append(slices.Clone(s.Stored), d.Change)
		if s.Proposal == domain.ProposalDeclined {
			s.Proposal = domain.ProposalNone
		}
	case domain.ProposeFollowUp:
		s.Proposal = domain.ProposalPending
	case domain.OpenPR:
		s.PR = &domain.AntPR{Ref: prRef, Draft: d.Draft, DraftForced: d.Draft}
	case domain.FinishWithNoChange:
		s.State, s.Finish = domain.AntDone, nil
	case domain.CompleteFinish:
		s.State, s.Finish = domain.AntSucceeded, nil
		if s.PR == nil {
			t.Fatalf("%#v without a PR", d)
		}
		pr := *s.PR
		switch {
		case d.MarkDraft:
			pr.Draft, pr.DraftForced = true, true
		case d.MarkReady:
			pr.Draft, pr.DraftForced = false, false
		case d.Status == domain.FinishComplete:
			pr.DraftForced = false
		}
		s.PR = &pr
	default:
		t.Fatalf("no effect for decision %#v", d)
	}
	return s
}

// without returns changes minus those in used, matched by ID.
func without(changes, used []domain.AntChange) []domain.AntChange {
	var kept []domain.AntChange
	for _, c := range changes {
		if !slices.ContainsFunc(used, func(u domain.AntChange) bool { return u.ID == c.ID }) {
			kept = append(kept, c)
		}
	}
	return kept
}

// decideChain calls the playbook, applies each decision's effect and calls
// it again, as the service does, until it returns Nothing. It fails when
// a decision comes back after its effect was recorded, or when the chain
// does not end within maxDecisions. It returns every decision but the
// final Nothing.
func decideChain(t *testing.T, s domain.AntSnapshot) []domain.AntDecision {
	t.Helper()
	var chain []domain.AntDecision
	for range maxDecisions {
		d := domain.DecideAnt(s)
		if _, ok := d.(domain.Nothing); ok {
			return chain
		}
		for _, earlier := range chain {
			if reflect.DeepEqual(d, earlier) {
				t.Fatalf("decision %#v returned again after its effect was recorded; chain so far: %#v", d, chain)
			}
		}
		chain = append(chain, d)
		s = applyEffect(t, s, d)
	}
	t.Fatalf("no Nothing within %d decisions: %#v", maxDecisions, chain)
	return nil
}

var (
	prRef = domain.Ref{Repo: domain.Repository{Provider: domain.GitHub, Org: "acme", Name: "web"}, Kind: domain.KindPR, Number: 40}

	descriptionEdited = domain.AntChange{ID: 1, Event: domain.ChangeEvent{
		Kind: domain.ChangeItemEdited, Field: domain.FieldBody, Old: "Fix login.", Body: "Fix login.\n\nAlso the empty password.",
	}}
	prComment = domain.AntChange{ID: 2, Event: domain.ChangeEvent{
		Kind: domain.ChangeCommentAdded, CommentID: "c2", Body: "please also handle the empty-password case",
	}}
	secondComment = domain.AntChange{ID: 3, Event: domain.ChangeEvent{
		Kind: domain.ChangeCommentAdded, CommentID: "c3", Body: "and add a test for it",
	}}
	blockingFinding = domain.AntChange{ID: 4, Event: domain.ChangeEvent{Kind: domain.ChangeCommentAdded, CommentID: "r1"}, Findings: []domain.Finding{
		{Location: domain.Location{File: "auth.go", StartLine: 12}, Severity: domain.SeverityBlocking, Classification: domain.NeedsDeveloper, Message: "empty password accepted"},
	}}
	suggestionAndNit = domain.AntChange{ID: 5, Event: domain.ChangeEvent{Kind: domain.ChangeCommentAdded, CommentID: "r2"}, Findings: []domain.Finding{
		{Location: domain.Location{File: "auth.go", StartLine: 20}, Severity: domain.SeveritySuggestion, Classification: domain.NeedsDeveloper, Message: "extract the check"},
		{Location: domain.Location{File: "auth.go", StartLine: 3}, Severity: domain.SeverityNit, Classification: domain.AutoFixable, Message: "unused import"},
	}}
	answer = &domain.ChangeEvent{Kind: domain.ChangeCommentAdded, CommentID: "a1", ThreadID: "q1", Body: "60 seconds"}
)

// antWith returns a snapshot of an ant with default settings, an hour's
// base budget, one task run behind it and a resumable session, changed by
// edit.
func antWith(state domain.AntState, edit func(*domain.AntSnapshot)) domain.AntSnapshot {
	s := domain.AntSnapshot{
		State:           state,
		Runs:            1,
		LastRun:         domain.RoleTaskRun,
		Resumable:       true,
		Base:            time.Hour,
		AutoFollowUp:    domain.DefaultAutoFollowUp,
		FollowUpLimit:   domain.DefaultFollowUpLimit,
		FollowUpPercent: domain.DefaultFollowUpPercent,
	}
	if edit != nil {
		edit(&s)
	}
	return s
}

// succeeded returns a snapshot of an ant whose task run opened its PR,
// changed by edit.
func succeeded(edit func(*domain.AntSnapshot)) domain.AntSnapshot {
	return antWith(domain.AntSucceeded, func(s *domain.AntSnapshot) {
		s.PR = &domain.AntPR{Ref: prRef}
		if edit != nil {
			edit(s)
		}
	})
}

// playbookCase is one scenario: the snapshot, and the decisions the
// playbook returns for it, one after another, until Nothing.
type playbookCase struct {
	name     string
	snapshot domain.AntSnapshot
	want     []domain.AntDecision
}

func runPlaybookCases(t *testing.T, cases []playbookCase) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := decideChain(t, c.snapshot)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("decisions:\n got %s\nwant %s", show(got), show(c.want))
			}
		})
	}
}

func show(ds []domain.AntDecision) string {
	if len(ds) == 0 {
		return "[] (Nothing at once)"
	}
	return fmt.Sprintf("%#v", ds)
}

// TestPlaybookFinish has one case per finish scenario of spec
// ant-execution that the playbook decides. Exit without finishing, stop,
// abandon and restart are the services'.
func TestPlaybookFinish(t *testing.T) {
	login := "Login fixed."
	twoPoints := []string{"no test", "no docs"}
	runPlaybookCases(t, []playbookCase{
		{
			name: "successful finish",
			snapshot: antWith(domain.AntFinishing, func(s *domain.AntSnapshot) {
				s.PR = &domain.AntPR{Ref: prRef}
				s.Finish = &domain.AntFinish{Status: domain.FinishComplete, Summary: login, NewCommits: 2}
			}),
			want: []domain.AntDecision{domain.CompleteFinish{Status: domain.FinishComplete}},
		},
		{
			name: "finish without opening a PR",
			snapshot: antWith(domain.AntFinishing, func(s *domain.AntSnapshot) {
				s.Finish = &domain.AntFinish{Status: domain.FinishComplete, Summary: login, NewCommits: 2}
			}),
			want: []domain.AntDecision{
				domain.OpenPR{Summary: login},
				domain.CompleteFinish{Status: domain.FinishComplete},
			},
		},
		{
			// The agent's draft choice is honoured after complete.
			name: "complete without local verification",
			snapshot: antWith(domain.AntFinishing, func(s *domain.AntSnapshot) {
				s.PR = &domain.AntPR{Ref: prRef, Draft: true}
				s.Finish = &domain.AntFinish{Status: domain.FinishComplete, Summary: login, NewCommits: 2}
			}),
			want: []domain.AntDecision{domain.CompleteFinish{Status: domain.FinishComplete}},
		},
		{
			name: "bug already fixed",
			snapshot: antWith(domain.AntFinishing, func(s *domain.AntSnapshot) {
				s.Finish = &domain.AntFinish{Status: domain.FinishNoChange, Summary: "Fixed on main by #9."}
			}),
			want: []domain.AntDecision{domain.FinishWithNoChange{Explanation: "Fixed on main by #9."}},
		},
		{
			name: "follow-up needing no change",
			snapshot: antWith(domain.AntFinishing, func(s *domain.AntSnapshot) {
				s.LastRun, s.FollowUpRuns = domain.RoleFollowUpRun, 1
				s.PR = &domain.AntPR{Ref: prRef}
				s.Finish = &domain.AntFinish{Status: domain.FinishNoChange, Summary: "Replied on the thread."}
			}),
			want: []domain.AntDecision{domain.CompleteFinish{Status: domain.FinishNoChange}},
		},
		{
			name: "partial completion",
			snapshot: antWith(domain.AntFinishing, func(s *domain.AntSnapshot) {
				s.Finish = &domain.AntFinish{Status: domain.FinishPartial, Summary: login, OpenPoints: twoPoints, NewCommits: 2}
			}),
			want: []domain.AntDecision{
				domain.OpenPR{Draft: true, Summary: login, OpenPoints: twoPoints},
				domain.CompleteFinish{Status: domain.FinishPartial, MarkDraft: true, OpenPoints: twoPoints},
			},
		},
		{
			name: "partial completion after the agent opened a ready PR",
			snapshot: antWith(domain.AntFinishing, func(s *domain.AntSnapshot) {
				s.PR = &domain.AntPR{Ref: prRef}
				s.Finish = &domain.AntFinish{Status: domain.FinishPartial, Summary: login, OpenPoints: twoPoints, NewCommits: 2}
			}),
			want: []domain.AntDecision{
				domain.CompleteFinish{Status: domain.FinishPartial, MarkDraft: true, OpenPoints: twoPoints},
			},
		},
		{
			name: "draft completed by a later run",
			snapshot: antWith(domain.AntFinishing, func(s *domain.AntSnapshot) {
				s.Runs, s.LastRun, s.FollowUpRuns = 2, domain.RoleFollowUpRun, 1
				s.PR = &domain.AntPR{Ref: prRef, Draft: true, DraftForced: true}
				s.Finish = &domain.AntFinish{Status: domain.FinishComplete, Summary: login, NewCommits: 3}
			}),
			want: []domain.AntDecision{domain.CompleteFinish{Status: domain.FinishComplete, MarkReady: true}},
		},
		{
			// Design D22: "open the PR, then start the follow-up for
			// stored changes" comes out as successive decisions.
			name: "comment arrives while finishing without a PR",
			snapshot: antWith(domain.AntFinishing, func(s *domain.AntSnapshot) {
				s.Unrouted = []domain.AntChange{descriptionEdited}
				s.Finish = &domain.AntFinish{Status: domain.FinishComplete, Summary: login, NewCommits: 2}
			}),
			want: []domain.AntDecision{
				domain.StoreChange{Change: descriptionEdited},
				domain.OpenPR{Summary: login},
				domain.CompleteFinish{Status: domain.FinishComplete},
				domain.StartRun{Kind: domain.RoleFollowUpRun, Changes: []domain.AntChange{descriptionEdited}, NewBudget: 30 * time.Minute},
			},
		},
	})
}

// TestPlaybookRouting has one case per routing scenario of spec
// change-reaction, and the answer scenarios of specs agent-conversation and
// ant-execution.
func TestPlaybookRouting(t *testing.T) {
	runPlaybookCases(t, []playbookCase{
		{
			name: "ticket edited during a run",
			snapshot: antWith(domain.AntRunning, func(s *domain.AntSnapshot) {
				s.Unrouted = []domain.AntChange{descriptionEdited}
			}),
			want: []domain.AntDecision{domain.DeliverChange{Change: descriptionEdited}},
		},
		{
			// How the change reaches the run is the service's: the
			// playbook decides the same delivery.
			name: "runner without live input or hooks",
			snapshot: antWith(domain.AntRunning, func(s *domain.AntSnapshot) {
				s.Unrouted = []domain.AntChange{descriptionEdited}
			}),
			want: []domain.AntDecision{domain.DeliverChange{Change: descriptionEdited}},
		},
		{
			name: "change while starting",
			snapshot: antWith(domain.AntStarting, func(s *domain.AntSnapshot) {
				s.Unrouted = []domain.AntChange{descriptionEdited}
			}),
			want: []domain.AntDecision{domain.DeliverChange{Change: descriptionEdited}},
		},
		{
			name: "change while awaiting an extension",
			snapshot: antWith(domain.AntAwaitingExtension, func(s *domain.AntSnapshot) {
				s.Unrouted = []domain.AntChange{descriptionEdited}
			}),
			want: []domain.AntDecision{domain.DeliverChange{Change: descriptionEdited}},
		},
		{
			// "A change detected after the run called finish SHALL be
			// handled as for an ant without an active run."
			name: "change detected after finish",
			snapshot: antWith(domain.AntFinishing, func(s *domain.AntSnapshot) {
				s.PR = &domain.AntPR{Ref: prRef}
				s.LastRun = domain.RoleFollowUpRun
				s.Unrouted = []domain.AntChange{prComment}
			}),
			want: []domain.AntDecision{domain.StoreChange{Change: prComment}},
		},
		{
			name: "comment on a timed-out ant's draft PR",
			snapshot: antWith(domain.AntTimedOut, func(s *domain.AntSnapshot) {
				s.PR = &domain.AntPR{Ref: prRef, Draft: true, DraftForced: true}
				s.Unrouted = []domain.AntChange{prComment}
			}),
			want: []domain.AntDecision{domain.StoreChange{Change: prComment}},
		},
		{
			name: "change on a stopped ant",
			snapshot: antWith(domain.AntStopped, func(s *domain.AntSnapshot) {
				s.Unrouted = []domain.AntChange{descriptionEdited}
			}),
			want: []domain.AntDecision{domain.StoreChange{Change: descriptionEdited}},
		},
		{
			name: "change on a failed ant",
			snapshot: antWith(domain.AntFailed, func(s *domain.AntSnapshot) {
				s.Unrouted = []domain.AntChange{descriptionEdited}
			}),
			want: []domain.AntDecision{domain.StoreChange{Change: descriptionEdited}},
		},
		{
			name: "change on a queued ant",
			snapshot: antWith(domain.AntQueued, func(s *domain.AntSnapshot) {
				s.QueuedFrom = domain.AntFailed
				s.Unrouted = []domain.AntChange{descriptionEdited}
			}),
			want: []domain.AntDecision{domain.StoreChange{Change: descriptionEdited}},
		},
		{
			name: "change on a done ant",
			snapshot: antWith(domain.AntDone, func(s *domain.AntSnapshot) {
				s.Unrouted = []domain.AntChange{prComment}
			}),
			want: nil,
		},
		{
			name: "ticket edited while waiting for an answer",
			snapshot: antWith(domain.AntAwaitingInput, func(s *domain.AntSnapshot) {
				s.Unrouted = []domain.AntChange{descriptionEdited}
				s.Answer = answer
			}),
			want: []domain.AntDecision{
				domain.StoreChange{Change: descriptionEdited},
				domain.StartRun{Kind: domain.RoleTaskRun, Answer: answer, Changes: []domain.AntChange{descriptionEdited}},
			},
		},
		{
			name: "developer replies in thread",
			snapshot: antWith(domain.AntAwaitingInput, func(s *domain.AntSnapshot) {
				s.PR = &domain.AntPR{Ref: prRef}
				s.LastRun = domain.RoleFollowUpRun
				s.Answer = answer
			}),
			want: []domain.AntDecision{domain.StartRun{Kind: domain.RoleFollowUpRun, Answer: answer}},
		},
		{
			name: "unrelated comment",
			snapshot: antWith(domain.AntAwaitingInput, func(s *domain.AntSnapshot) {
				s.Unrouted = []domain.AntChange{prComment}
			}),
			want: []domain.AntDecision{domain.StoreChange{Change: prComment}},
		},
		{
			name: "runner without resume support",
			snapshot: antWith(domain.AntAwaitingInput, func(s *domain.AntSnapshot) {
				s.Resumable = false
				s.Answer = answer
			}),
			want: []domain.AntDecision{domain.StartRun{Kind: domain.RoleTaskRun, Brief: domain.BriefSummary, Answer: answer}},
		},
		{
			name: "answer arrives while capacity is full",
			snapshot: antWith(domain.AntQueued, func(s *domain.AntSnapshot) {
				s.QueuedFrom = domain.AntAwaitingInput
				s.Answer = answer
			}),
			want: []domain.AntDecision{domain.StartRun{Kind: domain.RoleTaskRun, Answer: answer}},
		},
		{
			name: "queued for capacity",
			snapshot: antWith(domain.AntQueued, func(s *domain.AntSnapshot) {
				s.Runs, s.LastRun, s.Resumable = 0, "", false
			}),
			want: []domain.AntDecision{domain.StartRun{Kind: domain.RoleTaskRun, Brief: domain.BriefTask}},
		},
		{
			name: "queued at acceptance with a stored change",
			snapshot: antWith(domain.AntQueued, func(s *domain.AntSnapshot) {
				s.Runs, s.LastRun, s.Resumable = 0, "", false
				s.Unrouted = []domain.AntChange{descriptionEdited}
			}),
			want: []domain.AntDecision{
				domain.StoreChange{Change: descriptionEdited},
				domain.StartRun{Kind: domain.RoleTaskRun, Brief: domain.BriefTask, Changes: []domain.AntChange{descriptionEdited}},
			},
		},
		{
			name: "reviewer comments on an ant PR",
			snapshot: succeeded(func(s *domain.AntSnapshot) {
				s.Unrouted = []domain.AntChange{prComment}
			}),
			want: []domain.AntDecision{
				domain.StoreChange{Change: prComment},
				domain.StartRun{Kind: domain.RoleFollowUpRun, Changes: []domain.AntChange{prComment}, NewBudget: 30 * time.Minute},
			},
		},
		{
			name: "requirement changed while the PR is open",
			snapshot: succeeded(func(s *domain.AntSnapshot) {
				s.Unrouted = []domain.AntChange{descriptionEdited}
			}),
			want: []domain.AntDecision{
				domain.StoreChange{Change: descriptionEdited},
				domain.StartRun{Kind: domain.RoleFollowUpRun, Changes: []domain.AntChange{descriptionEdited}, NewBudget: 30 * time.Minute},
			},
		},
		{
			name: "two comments in one poll",
			snapshot: succeeded(func(s *domain.AntSnapshot) {
				s.Unrouted = []domain.AntChange{prComment, secondComment}
			}),
			want: []domain.AntDecision{
				domain.StoreChange{Change: prComment},
				domain.StoreChange{Change: secondComment},
				domain.StartRun{Kind: domain.RoleFollowUpRun, Changes: []domain.AntChange{prComment, secondComment}, NewBudget: 30 * time.Minute},
			},
		},
		{
			name: "follow-up budget setting",
			snapshot: succeeded(func(s *domain.AntSnapshot) {
				s.Base, s.FollowUpPercent = 90*time.Minute, 20
				s.Stored = []domain.AntChange{prComment}
			}),
			want: []domain.AntDecision{
				domain.StartRun{Kind: domain.RoleFollowUpRun, Changes: []domain.AntChange{prComment}, NewBudget: 18 * time.Minute},
			},
		},
		{
			name: "follow-up after the session index was rebuilt",
			snapshot: succeeded(func(s *domain.AntSnapshot) {
				s.Resumable = false
				s.Unrouted = []domain.AntChange{prComment}
			}),
			want: []domain.AntDecision{
				domain.StoreChange{Change: prComment},
				domain.StartRun{Kind: domain.RoleFollowUpRun, Brief: domain.BriefSummary, Changes: []domain.AntChange{prComment}, NewBudget: 30 * time.Minute},
			},
		},
		{
			name: "blocking finding fixed by the ant",
			snapshot: succeeded(func(s *domain.AntSnapshot) {
				s.Unrouted = []domain.AntChange{blockingFinding}
			}),
			want: []domain.AntDecision{
				domain.StoreChange{Change: blockingFinding},
				domain.StartRun{Kind: domain.RoleFollowUpRun, Changes: []domain.AntChange{blockingFinding}, NewBudget: 30 * time.Minute},
			},
		},
		{
			// The PR playbook (16.8) hands the suggestion and the
			// auto-fixable nit over as one change, so one run gets both.
			name: "auto-fixable nit included in the run",
			snapshot: succeeded(func(s *domain.AntSnapshot) {
				s.Unrouted = []domain.AntChange{suggestionAndNit}
			}),
			want: []domain.AntDecision{
				domain.StoreChange{Change: suggestionAndNit},
				domain.StartRun{Kind: domain.RoleFollowUpRun, Changes: []domain.AntChange{suggestionAndNit}, NewBudget: 30 * time.Minute},
			},
		},
		{
			name: "auto-follow-up disabled",
			snapshot: succeeded(func(s *domain.AntSnapshot) {
				s.AutoFollowUp = false
				s.Unrouted = []domain.AntChange{prComment}
			}),
			want: []domain.AntDecision{
				domain.StoreChange{Change: prComment},
				domain.ProposeFollowUp{Changes: []domain.AntChange{prComment}},
			},
		},
		{
			name: "another change while a proposal is pending",
			snapshot: succeeded(func(s *domain.AntSnapshot) {
				s.AutoFollowUp, s.Proposal = false, domain.ProposalPending
				s.Stored = []domain.AntChange{prComment}
				s.Unrouted = []domain.AntChange{secondComment}
			}),
			want: []domain.AntDecision{domain.StoreChange{Change: secondComment}},
		},
		{
			name: "queen accepts a follow-up proposal",
			snapshot: succeeded(func(s *domain.AntSnapshot) {
				s.AutoFollowUp, s.Proposal = false, domain.ProposalAccepted
				s.Stored = []domain.AntChange{prComment, secondComment}
			}),
			want: []domain.AntDecision{
				domain.StartRun{Kind: domain.RoleFollowUpRun, Changes: []domain.AntChange{prComment, secondComment}, NewBudget: 30 * time.Minute},
			},
		},
		{
			name: "queen declines a follow-up proposal",
			snapshot: succeeded(func(s *domain.AntSnapshot) {
				s.AutoFollowUp, s.Proposal = false, domain.ProposalDeclined
				s.Stored = []domain.AntChange{prComment}
			}),
			want: nil,
		},
		{
			name: "new change after a declined proposal",
			snapshot: succeeded(func(s *domain.AntSnapshot) {
				s.AutoFollowUp, s.Proposal = false, domain.ProposalDeclined
				s.Stored = []domain.AntChange{prComment}
				s.Unrouted = []domain.AntChange{secondComment}
			}),
			want: []domain.AntDecision{
				domain.StoreChange{Change: secondComment},
				domain.ProposeFollowUp{Changes: []domain.AntChange{prComment, secondComment}},
			},
		},
		{
			name: "limit reached",
			snapshot: succeeded(func(s *domain.AntSnapshot) {
				s.FollowUpRuns = 5
				s.Unrouted = []domain.AntChange{prComment}
			}),
			want: []domain.AntDecision{domain.StoreChange{Change: prComment, FollowUpLimitReached: true}},
		},
		{
			name: "limit reached with auto-follow-up disabled",
			snapshot: succeeded(func(s *domain.AntSnapshot) {
				s.AutoFollowUp, s.FollowUpRuns = false, 5
				s.Unrouted = []domain.AntChange{prComment}
			}),
			want: []domain.AntDecision{domain.StoreChange{Change: prComment, FollowUpLimitReached: true}},
		},
		{
			name: "follow-up limit of zero",
			snapshot: succeeded(func(s *domain.AntSnapshot) {
				s.FollowUpLimit = 0
				s.Unrouted = []domain.AntChange{prComment}
			}),
			want: []domain.AntDecision{domain.StoreChange{Change: prComment, FollowUpLimitReached: true}},
		},
		{
			name:     "succeeded ant without stored changes",
			snapshot: succeeded(nil),
			want:     nil,
		},
		{
			name: "change during a follow-up run",
			snapshot: succeeded(func(s *domain.AntSnapshot) {
				s.State, s.LastRun = domain.AntRunning, domain.RoleFollowUpRun
				s.Unrouted = []domain.AntChange{prComment}
			}),
			want: []domain.AntDecision{domain.DeliverChange{Change: prComment}},
		},
		{
			name: "follow-up queued for capacity",
			snapshot: succeeded(func(s *domain.AntSnapshot) {
				s.State, s.QueuedFrom = domain.AntQueued, domain.AntSucceeded
				s.Stored = []domain.AntChange{prComment}
			}),
			want: []domain.AntDecision{
				domain.StartRun{Kind: domain.RoleFollowUpRun, Changes: []domain.AntChange{prComment}, NewBudget: 30 * time.Minute},
			},
		},
		{
			// The run that resumes a follow-up after an answer keeps the
			// budget and does not count towards the limit.
			name: "answer to a follow-up run's question at the limit",
			snapshot: succeeded(func(s *domain.AntSnapshot) {
				s.State, s.LastRun, s.FollowUpRuns = domain.AntAwaitingInput, domain.RoleFollowUpRun, 5
				s.Answer = answer
			}),
			want: []domain.AntDecision{domain.StartRun{Kind: domain.RoleFollowUpRun, Answer: answer}},
		},
		{
			// Retry and continue are the services': they start the run of
			// an ant they queued.
			name: "queued by a retry",
			snapshot: antWith(domain.AntQueued, func(s *domain.AntSnapshot) {
				s.QueuedFrom = domain.AntFailed
			}),
			want: nil,
		},
	})
}

// TestPlaybookNeverRepeats checks every state, with and without changes,
// for a decision that comes back after its effect.
func TestPlaybookNeverRepeats(t *testing.T) {
	finishes := []*domain.AntFinish{
		nil,
		{Status: domain.FinishComplete, Summary: "done", NewCommits: 1},
		{Status: domain.FinishPartial, Summary: "half", OpenPoints: []string{"rest"}, NewCommits: 1},
		{Status: domain.FinishNoChange, Summary: "nothing to do"},
	}
	proposals := []struct {
		auto  bool
		state domain.ProposalState
	}{
		{true, domain.ProposalNone},
		{false, domain.ProposalNone},
		{false, domain.ProposalPending},
		{false, domain.ProposalAccepted},
		{false, domain.ProposalDeclined},
	}
	for _, state := range domain.AntStates {
		for _, f := range finishes {
			for _, kind := range []domain.AgentRole{domain.RoleTaskRun, domain.RoleFollowUpRun} {
				for _, p := range proposals {
					s := antWith(state, func(s *domain.AntSnapshot) {
						s.AutoFollowUp = p.auto
						s.Proposal = p.state
						s.LastRun = kind
						s.Finish = f
						s.Answer = answer
						s.Unrouted = []domain.AntChange{descriptionEdited, prComment, secondComment}
						if kind == domain.RoleFollowUpRun {
							s.PR = &domain.AntPR{Ref: prRef}
						}
					})
					name := fmt.Sprintf("%s/%s/finish=%v/auto=%v/proposal=%q", state, kind, f != nil, p.auto, p.state)
					t.Run(name, func(t *testing.T) { decideChain(t, s) })
				}
			}
		}
	}
}
