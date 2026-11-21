package domain

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
)

// ResolveItemRef reads an item reference the queen typed in the console
// (spec queen-console) and finds its repository among repos, the tracker
// repositories of this instance. It accepts:
//   - <repository>#<number>, such as matteorosani/anthill#12 or
//     contoso/shop#12, where the repository is one of repos (case does not
//     matter, and an Azure DevOps code repository names its project);
//   - #<number> or <number>, in the only repository of repos;
//   - gh-<number>, and ado-<number> or AB#<number>, in the only
//     repository of that provider.
//
// A short form that more than one repository could hold is refused, and
// the error names them.
func ResolveItemRef(text string, repos []Repository) (Ref, error) {
	text = strings.TrimSpace(text)
	invalid := func(reason string) (Ref, error) {
		return Ref{}, fmt.Errorf("%w %q: %s", ErrInvalidRef, text, reason)
	}
	if path, num, ok := strings.Cut(text, "#"); ok && path != "" && !strings.EqualFold(path, "AB") {
		n, err := itemNumber(num)
		if err != nil {
			return invalid(err.Error())
		}
		for _, r := range repos {
			if strings.EqualFold(r.String(), path) || strings.EqualFold(TrackerRepository(r).String(), path) {
				return Ref{Repo: TrackerRepository(r), Kind: KindItem, Number: n}, nil
			}
		}
		for _, r := range repos {
			if r.Provider == AzureDevOps && strings.HasPrefix(strings.ToLower(path), strings.ToLower(TrackerRepository(r).String())+"/") {
				return Ref{Repo: TrackerRepository(r), Kind: KindItem, Number: n}, nil
			}
		}
		return invalid("repository " + path + " is not configured")
	}

	var provider Provider
	num := text
	switch lower := strings.ToLower(text); {
	case strings.HasPrefix(lower, "ab#"):
		provider, num = AzureDevOps, text[3:]
	case strings.HasPrefix(lower, "#"):
		num = text[1:]
	case strings.HasPrefix(lower, string(GitHub)+"-"):
		provider, num = GitHub, text[len(GitHub)+1:]
	case strings.HasPrefix(lower, string(AzureDevOps)+"-"):
		provider, num = AzureDevOps, text[len(AzureDevOps)+1:]
	}
	n, err := itemNumber(num)
	if err != nil {
		return invalid(err.Error())
	}
	var candidates []Repository
	for _, r := range repos {
		t := TrackerRepository(r)
		if (provider == "" || t.Provider == provider) && !slices.Contains(candidates, t) {
			candidates = append(candidates, t)
		}
	}
	switch len(candidates) {
	case 0:
		return invalid("no repository of that provider is configured")
	case 1:
		return Ref{Repo: candidates[0], Kind: KindItem, Number: n}, nil
	}
	names := make([]string, len(candidates))
	for i, c := range candidates {
		names[i] = c.String()
	}
	return invalid(fmt.Sprintf("it could be in %s; write <repository>#%d", strings.Join(names, " or "), n))
}

// itemNumber reads a positive item number.
func itemNumber(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || n <= 0 || s[0] == '0' || s[0] == '+' {
		return 0, fmt.Errorf("expected an item number such as #12")
	}
	return n, nil
}
