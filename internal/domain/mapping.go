package domain

import (
	"fmt"
	"strings"
)

// ReasonNoTargetRepository is why an Azure DevOps item whose project has
// several repositories, no default and no repo: label is unmapped (spec
// work-item-sources, "Azure DevOps item with no repository").
const ReasonNoTargetRepository = "cannot determine target repository"

// ResolveTargetRepository returns the one code repository an item maps
// to, or an empty repository and the reason it is unmapped (spec
// work-item-sources, "Repository mapping").
//
// A GitHub issue maps to its own repository, unless a repo: label names
// another one, as owner/name or as a name under the same owner.
//
// An Azure DevOps work item maps, in this order, to the repository its
// repo:<name> label names, to defaultRepo (its project's
// default-repository setting), or to the project's only repository.
// repos lists the Git repositories of the item's project, as the code
// host reports them; a named repository must be one of them. Names
// compare in any case, and the result is the entry of repos.
func ResolveTargetRepository(item WorkItem, defaultRepo string, repos []Repository) (Repository, string) {
	label, reason := repoLabel(item.Labels)
	if reason != "" {
		return Repository{}, reason
	}
	scope := item.Ref.Repo
	if scope.Provider == GitHub {
		if label == "" {
			return scope, ""
		}
		owner, name, ok := strings.Cut(label, "/")
		if !ok {
			owner, name = scope.Org, label
		}
		if owner == "" || name == "" || strings.Contains(name, "/") {
			return Repository{}, fmt.Sprintf("label repo:%s does not name a repository", label)
		}
		return Repository{Provider: GitHub, Org: strings.ToLower(owner), Name: strings.ToLower(name)}, ""
	}

	project := Repository{Provider: scope.Provider, Org: scope.Org, Project: scope.Project}
	find := func(name string) (Repository, string) {
		for _, r := range repos {
			if strings.EqualFold(r.Name, name) {
				return r, ""
			}
		}
		return Repository{}, fmt.Sprintf("repository %s not found in project %s", name, project)
	}
	switch {
	case label != "":
		return find(label)
	case defaultRepo != "":
		return find(defaultRepo)
	case len(repos) == 1:
		return repos[0], ""
	}
	return Repository{}, ReasonNoTargetRepository
}

// repoLabel returns the value of the item's repo: label, "" when it has
// none, or a reason when its repo: labels disagree.
func repoLabel(labels []string) (string, string) {
	settings, warnings := ParseSettingLabels(labels, 0)
	for _, w := range warnings {
		if w.Reason == WarnConflict && strings.HasPrefix(strings.ToLower(w.Label), labelRepo) {
			return "", "conflicting labels " + w.Label
		}
	}
	return settings.Repo, ""
}
