package domain_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

// reviewer gives the findings a round posts on a head commit, for the
// effect of StartRound.
type reviewer func(head string) []domain.PRFinding

// noFindings is a reviewer for clean rounds.
func noFindings(string) []domain.PRFinding { return nil }

// fixPush is the head commit a fix pass pushes in the tests.
const fixPush = "fix1"

// applyPREffect returns the snapshot after the shepherd has carried out d
// and recorded its effect, as each decision's doc comment describes.
func applyPREffect(t *testing.T, s domain.PRSnapshot, d domain.PRDecision, review reviewer) domain.PRSnapshot {
	t.Helper()
	s.Rounds = slices.Clone(s.Rounds)
	switch d := d.(type) {
	case domain.StartRound:
		s.Rounds = append(s.Rounds, domain.PRRound{Head: d.Head, Requested: d.Requested, Findings: review(d.Head)})
		if d.Requested {
			s.RoundRequested = false
		}
	case domain.PostRoundLimit:
		s.LimitPosted = true
	case domain.PostAutofixUnavailable:
		s.UnavailablePosted = append(slices.Clone(s.UnavailablePosted), d.Reason)
	case domain.HandFindings:
		i := slices.IndexFunc(s.Rounds, func(r domain.PRRound) bool { return r.Head == d.Head && !r.Handed })
		if i < 0 {
			t.Fatalf("%#v for no round", d)
		}
		s.Rounds[i].Handed = true
	case domain.StartFixPass:
		// The fix pass fixes the first finding and fails on the others.
		i := slices.IndexFunc(s.Rounds, func(r domain.PRRound) bool { return r.Head == d.Head })
		r := s.Rounds[i]
		r.Findings = slices.Clone(r.Findings)
		for j := range r.Findings {
			if r.Findings[j].Classification != domain.AutoFixable || r.Findings[j].Resolved {
				continue
			}
			if j == 0 {
				r.Findings[j].Resolved = true
			} else {
				r.Findings[j].Classification = domain.NeedsDeveloper
			}
		}
		s.Rounds[i] = r
		s.FixHeads = append(slices.Clone(s.FixHeads), fixPush)
		s.Head = fixPush
	default:
		t.Fatalf("no effect for decision %#v", d)
	}
	return s
}

// decidePRChain calls the playbook, applies each decision's effect and
// calls it again until it returns Nothing. It fails when a decision comes
// back after its effect was recorded, or when the chain does not end
// within maxDecisions.
func decidePRChain(t *testing.T, s domain.PRSnapshot, review reviewer) []domain.PRDecision {
	t.Helper()
	var chain []domain.PRDecision
	for range maxDecisions {
		d := domain.DecidePR(s)
		if _, ok := d.(domain.Nothing); ok {
			return chain
		}
		for _, earlier := range chain {
			if reflect.DeepEqual(d, earlier) {
				t.Fatalf("decision %#v returned again after its effect was recorded; chain so far: %#v", d, chain)
			}
		}
		chain = append(chain, d)
		s = applyPREffect(t, s, d, review)
	}
	t.Fatalf("no Nothing within %d decisions: %#v", maxDecisions, chain)
	return nil
}

var (
	blockingNull = domain.PRFinding{Finding: domain.Finding{
		Location: domain.Location{File: "src/auth.ts", StartLine: 42}, Severity: domain.SeverityBlocking,
		Classification: domain.NeedsDeveloper, Message: "null dereference",
	}}
	extractCheck = domain.PRFinding{Finding: domain.Finding{
		Location: domain.Location{File: "src/auth.ts", StartLine: 20}, Severity: domain.SeveritySuggestion,
		Classification: domain.NeedsDeveloper, Message: "extract the check",
	}}
	unusedImport = domain.PRFinding{Finding: domain.Finding{
		Location: domain.Location{File: "src/auth.ts", StartLine: 3}, Severity: domain.SeverityNit,
		Classification: domain.AutoFixable, Message: "unused import",
	}, Thread: "t-import"}
	unusedVariable = domain.PRFinding{Finding: domain.Finding{
		Location: domain.Location{File: "src/auth.ts", StartLine: 9}, Severity: domain.SeverityNit,
		Classification: domain.AutoFixable, Message: "unused variable",
	}, Thread: "t-variable"}
	naming = domain.PRFinding{Finding: domain.Finding{
		Location: domain.Location{File: "src/auth.ts", StartLine: 7}, Severity: domain.SeverityNit,
		Classification: domain.NeedsDeveloper, Message: "unclear name",
	}}
)

// prWith returns the snapshot of a ready, in-scope human PR at head c1
// with no round yet and the default round limit, changed by edit.
func prWith(edit func(*domain.PRSnapshot)) domain.PRSnapshot {
	s := domain.PRSnapshot{
		Head:                "c1",
		InScope:             true,
		RoundLimit:          domain.DefaultRoundLimit,
		VerificationCommand: true,
		Pushable:            true,
	}
	if edit != nil {
		edit(&s)
	}
	return s
}

// rounds returns automatic rounds on the heads, without findings.
func rounds(heads ...string) []domain.PRRound {
	var rs []domain.PRRound
	for _, h := range heads {
		rs = append(rs, domain.PRRound{Head: h})
	}
	return rs
}

func findings(fs ...domain.PRFinding) []domain.Finding {
	var out []domain.Finding
	for _, f := range fs {
		out = append(out, f.Finding)
	}
	return out
}

// fixPass is the fix pass of the findings on head c1.
func fixPass(fs ...domain.PRFinding) domain.StartFixPass {
	d := domain.StartFixPass{Head: "c1", Findings: findings(fs...)}
	for _, f := range fs {
		d.Threads = append(d.Threads, f.Thread)
	}
	return d
}

func TestDecidePR(t *testing.T) {
	tests := []struct {
		name string
		snap domain.PRSnapshot
		want domain.PRDecision
	}{
		// pr-shepherd, "Shepherd scope".
		{"round on a new PR", prWith(nil), domain.StartRound{Head: "c1"}},
		{"human PR in ant-authored scope", prWith(func(s *domain.PRSnapshot) { s.InScope = false }), domain.Nothing{}},
		{"draft PR", prWith(func(s *domain.PRSnapshot) { s.Draft = true }), domain.Nothing{}},
		// pr-shepherd, "Round triggers".
		{"developer pushes fixes", prWith(func(s *domain.PRSnapshot) {
			s.Head, s.Rounds = "c2", rounds("c1")
		}), domain.StartRound{Head: "c2"}},
		{"no new commits", prWith(func(s *domain.PRSnapshot) { s.Rounds = rounds("c1") }), domain.Nothing{}},
		{"round in progress on the head", prWith(func(s *domain.PRSnapshot) {
			s.Rounds = []domain.PRRound{{Head: "c1"}}
		}), domain.Nothing{}},
		// pr-shepherd, "Round limit".
		{"limit reached", prWith(func(s *domain.PRSnapshot) {
			s.Head, s.Rounds = "c4", rounds("c1", "c2", "c3")
		}), domain.PostRoundLimit{Limit: 3}},
		{"limit comment already posted", prWith(func(s *domain.PRSnapshot) {
			s.Head, s.Rounds, s.LimitPosted = "c5", rounds("c1", "c2", "c3"), true
		}), domain.Nothing{}},
		{"limit reached and nothing pushed", prWith(func(s *domain.PRSnapshot) {
			s.Rounds = rounds("a", "b", "c1")
		}), domain.Nothing{}},
		{"requested rounds do not count against the limit", prWith(func(s *domain.PRSnapshot) {
			s.Head = "c4"
			s.Rounds = []domain.PRRound{{Head: "c1"}, {Head: "c2", Requested: true}, {Head: "c3"}}
		}), domain.StartRound{Head: "c4"}},
		{"round limit 0", prWith(func(s *domain.PRSnapshot) { s.RoundLimit = 0 }), domain.PostRoundLimit{Limit: 0}},
		// pr-shepherd, "Developer-requested rounds".
		{"extra round after limit", prWith(func(s *domain.PRSnapshot) {
			s.Rounds, s.RoundRequested, s.LimitPosted = rounds("a", "b", "c1"), true, true
		}), domain.StartRound{Head: "c1", Requested: true}},
		{"requested round out of scope", prWith(func(s *domain.PRSnapshot) {
			s.InScope, s.RoundRequested = false, true
		}), domain.StartRound{Head: "c1", Requested: true}},
		{"requested round on a draft", prWith(func(s *domain.PRSnapshot) {
			s.Draft, s.RoundRequested = true, true
		}), domain.StartRound{Head: "c1", Requested: true}},
		// pr-shepherd, "Findings are posted as PR comments" and
		// change-reaction, "Shepherd findings on ant PRs".
		{"findings handed to the owning ant", prWith(func(s *domain.PRSnapshot) {
			s.AntPR, s.OwnAnt = true, true
			s.Rounds = []domain.PRRound{{Head: "c1", Findings: []domain.PRFinding{blockingNull, naming}}}
		}), domain.HandFindings{Head: "c1", Findings: findings(blockingNull)}},
		{"auto-fixable nit included in the run", prWith(func(s *domain.PRSnapshot) {
			s.AntPR, s.OwnAnt = true, true
			s.Rounds = []domain.PRRound{{Head: "c1", Findings: []domain.PRFinding{extractCheck, unusedImport, naming}}}
		}), domain.HandFindings{Head: "c1", Findings: findings(extractCheck, unusedImport)}},
		{"nits only on an ant PR", prWith(func(s *domain.PRSnapshot) {
			s.AntPR, s.OwnAnt = true, true
			s.Rounds = []domain.PRRound{{Head: "c1", Findings: []domain.PRFinding{unusedImport, naming}}}
		}), domain.Nothing{}},
		{"findings of a round already handed", prWith(func(s *domain.PRSnapshot) {
			s.AntPR, s.OwnAnt = true, true
			s.Rounds = []domain.PRRound{{Head: "c1", Handed: true, Findings: []domain.PRFinding{blockingNull}}}
		}), domain.Nothing{}},
		{"ant PR of another instance", prWith(func(s *domain.PRSnapshot) {
			s.AntPR = true
			s.Rounds = []domain.PRRound{{Head: "c1", Findings: []domain.PRFinding{blockingNull}}}
		}), domain.Nothing{}},
		{"handed before the next round", prWith(func(s *domain.PRSnapshot) {
			s.AntPR, s.OwnAnt, s.Head = true, true, "c2"
			s.Rounds = []domain.PRRound{{Head: "c1", Findings: []domain.PRFinding{blockingNull}}}
		}), domain.HandFindings{Head: "c1", Findings: findings(blockingNull)}},
		// pr-shepherd, "Automatic fixes".
		{"auto-fixable finding on an ant PR", prWith(func(s *domain.PRSnapshot) {
			s.AntPR, s.OwnAnt, s.Autofix = true, true, true
			s.Rounds = []domain.PRRound{{Head: "c1", Findings: []domain.PRFinding{unusedImport}}}
		}), domain.Nothing{}},
		{"auto-fix on an opted-in human PR", prWith(func(s *domain.PRSnapshot) {
			s.Autofix = true
			s.Rounds = []domain.PRRound{{Head: "c1", Findings: []domain.PRFinding{naming, unusedImport}}}
		}), fixPass(unusedImport)},
		// agent-conversation, "Fix on a human PR": /anthill fix opts in
		// its own thread only.
		{"auto-fix requested on one thread", prWith(func(s *domain.PRSnapshot) {
			s.FixThreads = []string{"t-variable"}
			s.Rounds = []domain.PRRound{{Head: "c1", Findings: []domain.PRFinding{unusedImport, unusedVariable}}}
		}), fixPass(unusedVariable)},
		{"auto-fix requested on a thread that is not auto-fixable", prWith(func(s *domain.PRSnapshot) {
			s.FixThreads = []string{"t-naming"}
			n := naming
			n.Thread = "t-naming"
			s.Rounds = []domain.PRRound{{Head: "c1", Findings: []domain.PRFinding{n, unusedImport}}}
		}), domain.Nothing{}},
		{"auto-fixable finding without a thread", prWith(func(s *domain.PRSnapshot) {
			s.Autofix = true
			summaryOnly := unusedImport
			summaryOnly.Thread = ""
			s.Rounds = []domain.PRRound{{Head: "c1", Findings: []domain.PRFinding{summaryOnly}}}
		}), domain.Nothing{}},
		{"human PR not opted in", prWith(func(s *domain.PRSnapshot) {
			s.Rounds = []domain.PRRound{{Head: "c1", Findings: []domain.PRFinding{unusedImport}}}
		}), domain.Nothing{}},
		{"fork branch not pushable", prWith(func(s *domain.PRSnapshot) {
			s.Autofix, s.Pushable = true, false
			s.Rounds = []domain.PRRound{{Head: "c1", Findings: []domain.PRFinding{unusedImport}}}
		}), domain.PostAutofixUnavailable{Reason: domain.AutofixNotPushable}},
		{"fork branch not pushable, comment posted", prWith(func(s *domain.PRSnapshot) {
			s.Autofix, s.Pushable = true, false
			s.UnavailablePosted = []domain.AutofixUnavailableReason{domain.AutofixNotPushable}
			s.Rounds = []domain.PRRound{{Head: "c1", Findings: []domain.PRFinding{unusedImport}}}
		}), domain.Nothing{}},
		{"no verification command", prWith(func(s *domain.PRSnapshot) {
			s.Autofix, s.VerificationCommand = true, false
			s.Rounds = []domain.PRRound{{Head: "c1", Findings: []domain.PRFinding{unusedImport}}}
		}), domain.PostAutofixUnavailable{Reason: domain.AutofixNoVerification}},
		{"no verification command, nothing to fix", prWith(func(s *domain.PRSnapshot) {
			s.Autofix, s.VerificationCommand = true, false
			s.Rounds = []domain.PRRound{{Head: "c1", Findings: []domain.PRFinding{naming}}}
		}), domain.Nothing{}},
		{"fix pushed starts no round", prWith(func(s *domain.PRSnapshot) {
			s.Autofix, s.Head, s.FixHeads = true, fixPush, []string{fixPush}
			s.Rounds = []domain.PRRound{{Head: "c1", Findings: []domain.PRFinding{
				{Finding: unusedImport.Finding, Resolved: true},
			}}}
		}), domain.Nothing{}},
		{"failed fix left to developers", prWith(func(s *domain.PRSnapshot) {
			s.Autofix = true
			reclassified := unusedImport
			reclassified.Classification = domain.NeedsDeveloper
			s.Rounds = []domain.PRRound{{Head: "c1", Findings: []domain.PRFinding{reclassified}}}
		}), domain.Nothing{}},
		{"author pushed since the round", prWith(func(s *domain.PRSnapshot) {
			s.Autofix, s.Head = true, "c2"
			s.Rounds = []domain.PRRound{{Head: "c1", Findings: []domain.PRFinding{unusedImport}}}
		}), domain.StartRound{Head: "c2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domain.DecidePR(tt.snap); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("DecidePR = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// TestDecidePRChains plays whole scenarios, applying each decision's
// effect, and checks that no decision comes back after its effect is
// recorded (design D22).
func TestDecidePRChains(t *testing.T) {
	tests := []struct {
		name   string
		snap   domain.PRSnapshot
		review reviewer
		want   []domain.PRDecision
	}{
		{"clean round on a new PR", prWith(nil), noFindings, []domain.PRDecision{
			domain.StartRound{Head: "c1"},
		}},
		{"limit reached", prWith(func(s *domain.PRSnapshot) {
			s.Head, s.Rounds = "c4", rounds("c1", "c2", "c3")
		}), noFindings, []domain.PRDecision{
			domain.PostRoundLimit{Limit: 3},
		}},
		{"requested round serves every request and the head", prWith(func(s *domain.PRSnapshot) {
			s.RoundRequested = true
		}), noFindings, []domain.PRDecision{
			domain.StartRound{Head: "c1", Requested: true},
		}},
		{"requested round on a new head after the limit", prWith(func(s *domain.PRSnapshot) {
			s.Head, s.Rounds, s.RoundRequested = "c4", rounds("c1", "c2", "c3"), true
		}), noFindings, []domain.PRDecision{
			domain.StartRound{Head: "c4", Requested: true},
		}},
		{"findings handed to the owning ant", prWith(func(s *domain.PRSnapshot) {
			s.AntPR, s.OwnAnt = true, true
		}), func(string) []domain.PRFinding { return []domain.PRFinding{blockingNull, unusedImport, naming} }, []domain.PRDecision{
			domain.StartRound{Head: "c1"},
			domain.HandFindings{Head: "c1", Findings: findings(blockingNull, unusedImport)},
		}},
		{"two fixes on an opted-in human PR", prWith(func(s *domain.PRSnapshot) {
			s.Autofix = true
		}), func(string) []domain.PRFinding { return []domain.PRFinding{unusedImport, unusedVariable} }, []domain.PRDecision{
			domain.StartRound{Head: "c1"},
			fixPass(unusedImport, unusedVariable),
		}},
		{"no verification command on an opted-in human PR", prWith(func(s *domain.PRSnapshot) {
			s.Autofix, s.VerificationCommand = true, false
		}), func(string) []domain.PRFinding { return []domain.PRFinding{unusedImport} }, []domain.PRDecision{
			domain.StartRound{Head: "c1"},
			domain.PostAutofixUnavailable{Reason: domain.AutofixNoVerification},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decidePRChain(t, tt.snap, tt.review); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("chain = %#v\nwant %#v", got, tt.want)
			}
		})
	}
}

// TestDecidePRNeverRepeats checks every single decision of TestDecidePR:
// after its effect is recorded, the playbook returns another decision or
// Nothing, never the same one.
func TestDecidePRNeverRepeats(t *testing.T) {
	snaps := []domain.PRSnapshot{
		prWith(nil),
		prWith(func(s *domain.PRSnapshot) { s.Head, s.Rounds = "c4", rounds("c1", "c2", "c3") }),
		prWith(func(s *domain.PRSnapshot) { s.InScope, s.RoundRequested = false, true }),
		prWith(func(s *domain.PRSnapshot) {
			s.AntPR, s.OwnAnt, s.Head = true, true, "c2"
			s.Rounds = []domain.PRRound{{Head: "c1", Findings: []domain.PRFinding{blockingNull}}}
		}),
		prWith(func(s *domain.PRSnapshot) {
			s.Autofix = true
			s.Rounds = []domain.PRRound{{Head: "c1", Findings: []domain.PRFinding{unusedImport, unusedVariable}}}
		}),
		prWith(func(s *domain.PRSnapshot) {
			s.FixThreads, s.VerificationCommand, s.Pushable = []string{"t-import"}, false, false
			s.Rounds = []domain.PRRound{{Head: "c1", Findings: []domain.PRFinding{unusedImport}}}
		}),
		prWith(func(s *domain.PRSnapshot) {
			s.Autofix, s.Pushable = true, false
			s.Rounds = []domain.PRRound{{Head: "c1", Findings: []domain.PRFinding{unusedImport}}}
		}),
	}
	for _, s := range snaps {
		for _, review := range []reviewer{noFindings, func(string) []domain.PRFinding {
			return []domain.PRFinding{blockingNull, unusedImport}
		}} {
			decidePRChain(t, s, review)
		}
	}
}

func TestInScope(t *testing.T) {
	repo := domain.Repository{Provider: domain.GitHub, Org: "contoso", Name: "api"}
	ant := domain.PullRequest{Ref: domain.Ref{Repo: repo, Kind: domain.KindPR, Number: 7}, SourceBranch: "ant/gh-12-fix-login"}
	human := domain.PullRequest{Ref: domain.Ref{Repo: repo, Kind: domain.KindPR, Number: 8}, SourceBranch: "feature/login"}
	labelled := human
	labelled.Labels = []string{"Anthill:Review"}
	if !domain.IsAntPR(ant, "ant") || domain.IsAntPR(human, "ant") || domain.IsAntPR(ant, "bot") {
		t.Fatal("IsAntPR does not follow the ant branch prefix")
	}
	tests := []struct {
		scope domain.ShepherdScope
		pr    domain.PullRequest
		want  bool
	}{
		{domain.ScopeAntAuthored, ant, true},
		{domain.ScopeAntAuthored, human, false},
		{domain.ScopeAntAuthored, labelled, false},
		// Labelled: ant PRs, and human PRs with the review label.
		{domain.ScopeLabelled, ant, true},
		{domain.ScopeLabelled, human, false},
		{domain.ScopeLabelled, labelled, true},
		{domain.ScopeAll, human, true},
		{domain.ScopeAll, ant, true},
		{"unknown", ant, false},
	}
	for _, tt := range tests {
		if got := domain.InScope(tt.scope, tt.pr, domain.IsAntPR(tt.pr, "ant")); got != tt.want {
			t.Errorf("InScope(%s, %s) = %v, want %v", tt.scope, tt.pr.SourceBranch, got, tt.want)
		}
	}
}
