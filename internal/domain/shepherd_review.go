package domain

import (
	"fmt"
	"path/filepath"
	"strings"
)

// RoundWorktreePath is the temporary detached worktree of round id on
// PR n of repo, under the worktree root, apart from the ants' worktrees:
// <root>/.shepherd/<provider>-<org>-<name>/pr-<n>-round-<id>.
func RoundWorktreePath(root string, repo Repository, n int, id int64) string {
	parts := []string{string(repo.Provider)}
	for _, p := range []string{repo.Org, repo.Project, repo.Name} {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return filepath.Join(root, ".shepherd", strings.Join(parts, "-"), fmt.Sprintf("pr-%d-round-%d", n, id))
}

// ReviewBriefInput is what the review agent of a round is given.
type ReviewBriefInput struct {
	PR PullRequest
	// Base is the ref the PR's change is reviewed against, such as
	// origin/main, and Head the head commit checked out.
	Base string
	Head string
	// Open are the findings of earlier rounds that are still open, which
	// the agent gets as listed data only (spec pr-shepherd, "Second round
	// starts fresh").
	Open []Finding
}

// RenderReviewBrief writes the first input of a round's review agent: the
// PR, how to see its change, the earlier open findings as data, and how
// to report through submit_review (spec pr-shepherd, "Review rounds";
// spec ant-tools, "Review tool").
func RenderReviewBrief(in ReviewBriefInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Review of pull request %s\n\n", oneLine(in.PR.Ref.String()))
	b.WriteString("You are the review agent of one review round. Review the change of the pull request below, which is checked out, " +
		"detached at its head commit, in your working directory. Do not change, commit or push anything.\n\n")
	fmt.Fprintf(&b, "- Title: %s\n", oneLine(in.PR.Title))
	fmt.Fprintf(&b, "- Branch %s into %s\n", code(in.PR.SourceBranch), code(in.PR.TargetBranch))
	fmt.Fprintf(&b, "- Head commit: %s\n", code(in.Head))
	fmt.Fprintf(&b, "- The change: `git diff %s...HEAD`\n\n", strings.ReplaceAll(oneLine(in.Base), "`", "'"))
	b.WriteString("## Description\n\n")
	if strings.TrimSpace(in.PR.Body) == "" {
		b.WriteString("The pull request has no description.\n\n")
	} else {
		writeQuoted(&b, in.PR.Body)
		b.WriteString("\n")
	}
	b.WriteString("## Findings of earlier rounds still open\n\n")
	if len(in.Open) == 0 {
		b.WriteString("None.\n\n")
	} else {
		b.WriteString("These are listed as data. Report one again only if the problem is still present at the head commit.\n\n")
		for _, f := range in.Open {
			writeFinding(&b, f)
		}
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, `## How to report

Call %s exactly once, with a summary of your assessment and one finding per problem. For each finding give:

- file and start_line (and end_line for several lines) when the problem is in specific lines of the change, relative to the repository root; leave them out otherwise;
- severity: %s (must be fixed before merging), %s (should be improved) or %s (minor);
- classification: %s when an agent can fix it without a developer's decision, %s otherwise;
- message: the problem, in words a developer can act on; suggested_fix: the replacement code for the lines, when the fix is clear.

Report an empty list of findings when you find nothing. Your work ends when %s returns.
`, code(ToolSubmitReview), code(string(SeverityBlocking)), code(string(SeveritySuggestion)), code(string(SeverityNit)),
		code(string(AutoFixable)), code(string(NeedsDeveloper)), code(ToolSubmitReview))
	return b.String()
}

// RenderFindingComment is the body of the inline comment of a finding
// (spec pr-shepherd, "Findings are posted as PR comments"): its severity
// and classification, the message, and the suggested change, which a
// developer can apply on GitHub.
func RenderFindingComment(f Finding) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s** · %s\n\n%s", f.Severity, f.Classification, strings.TrimSpace(f.Message))
	if fix := strings.TrimRight(f.SuggestedFix, "\n"); strings.TrimSpace(fix) != "" {
		fence := strings.Repeat("`", max(3, longestRun(fix, '`')+1))
		fmt.Fprintf(&b, "\n\n%ssuggestion\n%s\n%s", fence, fix, fence)
	}
	return b.String()
}

// SummaryFinding is one finding as a round's summary lists it.
type SummaryFinding struct {
	Finding
	// URL is the web address of the finding's thread: its inline comment,
	// or the comment of the earlier open thread it repeats. Empty for a
	// finding only in the summary.
	URL string
	// Repeat reports that the finding repeats an earlier open thread.
	Repeat bool
}

// RoundSummaryInput is what a round's summary comment says.
type RoundSummaryInput struct {
	Head string
	// AntPR reports that the PR is an ant PR, whose findings go to the
	// owning ant (spec change-reaction, "Shepherd findings on ant PRs").
	AntPR    bool
	Findings []SummaryFinding
	// Assessment is the review agent's summary, already safe to post.
	Assessment string
}

// HandedToAnt reports whether a finding on an ant PR goes to the owning
// ant: blocking and suggestion findings, of either classification, and
// auto-fixable nits. The other nits are left to developers.
func HandedToAnt(f Finding) bool {
	return f.Severity != SeverityNit || f.Classification == AutoFixable
}

// RenderRoundSummary is the body of a round's summary comment (spec
// pr-shepherd, "Findings are posted as PR comments", "Findings are not
// repeated"). It never approves the PR.
func RenderRoundSummary(in RoundSummaryInput) string {
	var b strings.Builder
	head := code(shortHash(in.Head))
	if len(in.Findings) == 0 {
		fmt.Fprintf(&b, "Review round on %s: no issues found.\n", head)
	} else {
		noun := "findings"
		if len(in.Findings) == 1 {
			noun = "finding"
		}
		fmt.Fprintf(&b, "Review round on %s: %d %s.\n", head, len(in.Findings), noun)
	}
	var fresh, repeats []SummaryFinding
	for _, f := range in.Findings {
		if f.Repeat {
			repeats = append(repeats, f)
		} else {
			fresh = append(fresh, f)
		}
	}
	if in.AntPR {
		var handed, left []SummaryFinding
		for _, f := range fresh {
			if HandedToAnt(f.Finding) {
				handed = append(handed, f)
			} else {
				left = append(left, f)
			}
		}
		writeSummaryList(&b, "Handed to the owning ant's next follow-up run", handed)
		writeSummaryList(&b, "Left to developers", left)
	} else {
		writeSummaryList(&b, "Findings", fresh)
	}
	writeSummaryList(&b, "Still open from earlier rounds", repeats)
	if a := strings.TrimSpace(in.Assessment); a != "" {
		fmt.Fprintf(&b, "\n### Assessment\n\n%s\n", a)
	}
	return b.String()
}

func writeSummaryList(b *strings.Builder, title string, findings []SummaryFinding) {
	if len(findings) == 0 {
		return
	}
	fmt.Fprintf(b, "\n### %s\n\n", title)
	for _, f := range findings {
		fmt.Fprintf(b, "- **%s** · %s", f.Severity, f.Classification)
		if f.File != "" {
			fmt.Fprintf(b, " · %s", code(f.File))
			if start, end := f.lines(); start > 0 && end > start {
				fmt.Fprintf(b, " lines %d-%d", start, end)
			} else if start > 0 {
				fmt.Fprintf(b, " line %d", start)
			}
		}
		if f.URL != "" {
			fmt.Fprintf(b, " ([thread](%s))", f.URL)
		}
		fmt.Fprintf(b, ": %s\n", oneLine(f.Message))
		// A finding with a thread has its suggested change there.
		if fix := strings.TrimRight(f.SuggestedFix, "\n"); f.URL == "" && strings.TrimSpace(fix) != "" {
			fence := strings.Repeat("`", max(3, longestRun(fix, '`')+1))
			fmt.Fprintf(b, "\n  %s\n  %s\n  %s\n", fence, strings.ReplaceAll(fix, "\n", "\n  "), fence)
		}
	}
}
