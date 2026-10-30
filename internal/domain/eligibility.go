package domain

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// The default labels (spec anthill-configuration).
const (
	DefaultOptInLabel = "anthill"
	DefaultHoldLabel  = "anthill:hold"
	DefaultClaimLabel = "anthill:claimed"
)

// Prerequisite is the stable identifier of one eligibility check (spec
// task-eligibility).
type Prerequisite string

// The prerequisites.
const (
	PrereqOptedIn             Prerequisite = "opted-in"
	PrereqOpen                Prerequisite = "open"
	PrereqDependencies        Prerequisite = "dependencies-satisfied"
	PrereqLeaf                Prerequisite = "is-leaf"
	PrereqNotClaimed          Prerequisite = "not-claimed"
	PrereqRepositoryAvailable Prerequisite = "repository-available"
	PrereqNoActiveWork        Prerequisite = "no-active-work"
	PrereqNoPendingQuestion   Prerequisite = "no-pending-question"
	PrereqHasDescription      Prerequisite = "has-description"
	PrereqNotExcluded         Prerequisite = "not-excluded"
)

// EligibilityContext is everything besides the item that the checks need.
// The application service gathers it; the domain only reads it.
type EligibilityContext struct {
	// OptInLabel and HoldLabel default to DefaultOptInLabel and
	// DefaultHoldLabel when empty.
	OptInLabel string
	HoldLabel  string
	// States holds the state of every dependency and child Anthill could
	// read. A dependency or child missing from it is not resolvable.
	States map[Ref]ItemState
	// Instance is this instance's id. ClaimHolder is the instance holding
	// the item's live task claim (see ResolveClaim), or "" when none does.
	Instance    string
	ClaimHolder string
	// TargetRepository is the repository the item maps to, or "" when the
	// item is unmapped. RepositoryAvailable tells whether it is configured
	// and cloned on this machine.
	TargetRepository    string
	RepositoryAvailable bool
	// OpenAntPR tells whether an ant PR for the item is open. LocalAnt is
	// the state of this machine's ant for the item, or "" when there is
	// none.
	OpenAntPR bool
	LocalAnt  AntState
	// PendingQuestion is the unanswered question comment of an ant on the
	// item, or nil when there is none.
	PendingQuestion *Comment
	// RequireAcceptanceCriteria is the repository's
	// require-acceptance-criteria setting.
	RequireAcceptanceCriteria bool
}

// CheckResult is the outcome of one prerequisite for one item. Reason
// explains an unmet prerequisite and is empty when it is met.
type CheckResult struct {
	Prerequisite Prerequisite
	Met          bool
	Reason       string
}

// Check is one named prerequisite.
type Check struct {
	Prerequisite Prerequisite
	// Eval returns "" when the prerequisite is met, and otherwise the
	// reason it is not.
	Eval func(item WorkItem, ctx EligibilityContext) string
}

// Checks lists every prerequisite in the order of the spec. Evaluate runs
// all of them.
var Checks = []Check{
	{PrereqOptedIn, checkOptedIn},
	{PrereqOpen, checkOpen},
	{PrereqDependencies, checkDependencies},
	{PrereqLeaf, checkLeaf},
	{PrereqNotClaimed, checkNotClaimed},
	{PrereqRepositoryAvailable, checkRepositoryAvailable},
	{PrereqNoActiveWork, checkNoActiveWork},
	{PrereqNoPendingQuestion, checkNoPendingQuestion},
	{PrereqHasDescription, checkHasDescription},
	{PrereqNotExcluded, checkNotExcluded},
}

// EligibilityResult holds the outcome of every prerequisite, in the order
// of Checks.
type EligibilityResult struct {
	Results []CheckResult
}

// Evaluate runs every prerequisite on the item. It never stops at the
// first unmet one, so the queen sees every reason.
func Evaluate(item WorkItem, ctx EligibilityContext) EligibilityResult {
	results := make([]CheckResult, 0, len(Checks))
	for _, c := range Checks {
		reason := c.Eval(item, ctx)
		results = append(results, CheckResult{Prerequisite: c.Prerequisite, Met: reason == "", Reason: reason})
	}
	return EligibilityResult{Results: results}
}

// Eligible reports whether every prerequisite is met.
func (r EligibilityResult) Eligible() bool {
	return len(r.Unmet()) == 0
}

// Unmet returns the unmet prerequisites with their reasons.
func (r EligibilityResult) Unmet() []CheckResult {
	var unmet []CheckResult
	for _, c := range r.Results {
		if !c.Met {
			unmet = append(unmet, c)
		}
	}
	return unmet
}

// IneligibleMessage is what the queen sees when an accepted item failed
// the re-check, such as "no longer eligible: open, not-claimed". It is ""
// for an eligible item.
func (r EligibilityResult) IneligibleMessage() string {
	unmet := r.Unmet()
	if len(unmet) == 0 {
		return ""
	}
	ids := make([]string, len(unmet))
	for i, c := range unmet {
		ids[i] = string(c.Prerequisite)
	}
	return "no longer eligible: " + strings.Join(ids, ", ")
}

// HasLabel reports whether labels contain label. Labels match in any
// case, because GitHub label names and Azure DevOps tags are both unique
// regardless of case.
func HasLabel(labels []string, label string) bool {
	return slices.ContainsFunc(labels, func(l string) bool { return strings.EqualFold(l, label) })
}

func orDefault(value, def string) string {
	if value == "" {
		return def
	}
	return value
}

func checkOptedIn(item WorkItem, ctx EligibilityContext) string {
	label := orDefault(ctx.OptInLabel, DefaultOptInLabel)
	if HasLabel(item.Labels, label) {
		return ""
	}
	return fmt.Sprintf("item does not carry the opt-in label %s", label)
}

func checkOpen(item WorkItem, _ EligibilityContext) string {
	if item.State == StateOpen {
		return ""
	}
	return fmt.Sprintf("item is %s", item.State)
}

// knownState returns the state of a linked item, or false when Anthill
// could not read it. Native links never cross providers, so a link to
// another provider is never resolvable.
func knownState(item WorkItem, ctx EligibilityContext, link Ref) (ItemState, bool) {
	if link.Repo.Provider != item.Ref.Repo.Provider {
		return "", false
	}
	state, ok := ctx.States[link]
	return state, ok
}

func checkDependencies(item WorkItem, ctx EligibilityContext) string {
	var reasons []string
	for _, dep := range item.Dependencies {
		name := dep.Display(item.Ref.Repo)
		state, ok := knownState(item, ctx, dep)
		switch {
		case !ok:
			reasons = append(reasons, fmt.Sprintf("dependency not resolvable: %s", name))
		case state == StateClosedNotPlanned:
			reasons = append(reasons, fmt.Sprintf("dependency %s closed as not planned — needs queen review", name))
		case state != StateClosedCompleted:
			reasons = append(reasons, fmt.Sprintf("dependency %s is open", name))
		}
	}
	return strings.Join(reasons, "; ")
}

func checkLeaf(item WorkItem, ctx EligibilityContext) string {
	var open, unknown []string
	for _, child := range item.Children {
		name := child.Display(item.Ref.Repo)
		state, ok := knownState(item, ctx, child)
		switch {
		case !ok:
			unknown = append(unknown, fmt.Sprintf("child %s not resolvable", name))
		case state == StateOpen:
			open = append(open, name)
		}
	}
	var reasons []string
	if len(open) > 0 {
		reasons = append(reasons, "open children: "+strings.Join(open, ", "))
	}
	return strings.Join(append(reasons, unknown...), "; ")
}

func checkNotClaimed(_ WorkItem, ctx EligibilityContext) string {
	if ctx.ClaimHolder == "" || ctx.ClaimHolder == ctx.Instance {
		return ""
	}
	return fmt.Sprintf("claimed by %s", ctx.ClaimHolder)
}

func checkRepositoryAvailable(_ WorkItem, ctx EligibilityContext) string {
	switch {
	case ctx.TargetRepository == "":
		return "item is not mapped to a repository"
	case !ctx.RepositoryAvailable:
		return fmt.Sprintf("repository %s not available on this machine", ctx.TargetRepository)
	}
	return ""
}

func checkNoActiveWork(_ WorkItem, ctx EligibilityContext) string {
	var reasons []string
	if ctx.OpenAntPR {
		reasons = append(reasons, "an ant PR is open")
	}
	if ctx.LocalAnt != "" && !ctx.LocalAnt.Final() {
		reasons = append(reasons, fmt.Sprintf("the ant on this machine is %s", ctx.LocalAnt))
	}
	return strings.Join(reasons, "; ")
}

func checkNoPendingQuestion(_ WorkItem, ctx EligibilityContext) string {
	q := ctx.PendingQuestion
	if q == nil {
		return ""
	}
	if q.URL == "" {
		return "a question is pending"
	}
	return "a question is pending: " + q.URL
}

func checkHasDescription(item WorkItem, ctx EligibilityContext) string {
	if strings.TrimSpace(item.Body) == "" {
		return "the description is empty"
	}
	if ctx.RequireAcceptanceCriteria && strings.TrimSpace(item.AcceptanceCriteria) == "" && !hasAcceptanceCriteriaSection(item.Body) {
		return "the description has no acceptance criteria"
	}
	return ""
}

func checkNotExcluded(item WorkItem, ctx EligibilityContext) string {
	label := orDefault(ctx.HoldLabel, DefaultHoldLabel)
	if HasLabel(item.Labels, label) {
		return fmt.Sprintf("item carries the hold label %s", label)
	}
	return ""
}

var (
	// markdownHeading matches "## Title", with optional closing hashes.
	markdownHeading = regexp.MustCompile(`^ {0,3}#{1,6}[ \t]+(.*?)[ \t#]*$`)
	// boldLine matches a line made only of bold text, such as
	// "**Acceptance criteria**" or "__Acceptance criteria:__".
	boldLine = regexp.MustCompile(`^ {0,3}(?:\*\*(.+?)\*\*|__(.+?)__)[ \t]*:?[ \t]*$`)
)

// hasAcceptanceCriteriaSection reports whether a Markdown body has a
// heading or bold line "Acceptance criteria" (any case, with or without a
// trailing colon) followed by at least one non-empty line before the next
// heading.
func hasAcceptanceCriteriaSection(body string) bool {
	inSection := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimRight(line, "\r")
		if m := markdownHeading.FindStringSubmatch(line); m != nil {
			inSection = isAcceptanceCriteriaTitle(m[1])
			continue
		}
		// Other bold lines are ordinary content, such as "**Given**".
		if m := boldLine.FindStringSubmatch(line); m != nil && isAcceptanceCriteriaTitle(m[1]+m[2]) {
			inSection = true
			continue
		}
		if inSection && strings.TrimSpace(line) != "" {
			return true
		}
	}
	return false
}

func isAcceptanceCriteriaTitle(title string) bool {
	title = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(title), ":"))
	return strings.EqualFold(title, "acceptance criteria")
}
