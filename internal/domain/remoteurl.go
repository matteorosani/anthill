package domain

import (
	"net/url"
	"strings"
)

// MatchesRemoteURL reports whether a git remote URL points to the
// repository. It accepts the https and ssh forms of github.com, and of
// dev.azure.com, ssh.dev.azure.com and <org>.visualstudio.com, with or
// without .git, ignoring case. The configured-path check uses it to find
// the clone's remote (spec anthill-configuration, "Repository listed
// without a clone").
func (r Repository) MatchesRemoteURL(raw string) bool {
	host, segs, ok := splitRemoteURL(raw)
	if !ok {
		return false
	}
	same := func(got []string, want ...string) bool {
		if len(got) != len(want) {
			return false
		}
		for i := range got {
			if !strings.EqualFold(got[i], want[i]) {
				return false
			}
		}
		return true
	}
	switch r.Provider {
	case GitHub:
		return host == "github.com" && same(segs, r.Org, r.Name)
	case AzureDevOps:
		switch {
		case host == "dev.azure.com":
			// /org/project/_git/repo
			return same(segs, r.Org, r.Project, "_git", r.Name)
		case host == "ssh.dev.azure.com" || host == "vs-ssh.visualstudio.com":
			// v3/org/project/repo
			return same(segs, "v3", r.Org, r.Project, r.Name)
		case host == strings.ToLower(r.Org)+".visualstudio.com":
			// /project/_git/repo, or /DefaultCollection/project/_git/repo
			if len(segs) == 4 && strings.EqualFold(segs[0], "DefaultCollection") {
				segs = segs[1:]
			}
			return same(segs, r.Project, "_git", r.Name)
		}
	}
	return false
}

// splitRemoteURL returns the lower-case host and the unescaped path
// segments of a URL, or of an scp-like address such as
// git@github.com:owner/name.git, without a trailing .git.
func splitRemoteURL(raw string) (host string, segs []string, ok bool) {
	raw = strings.TrimSpace(raw)
	var p string
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil {
			return "", nil, false
		}
		host, p = u.Hostname(), u.Path
	} else {
		// [user@]host:path
		addr, rest, found := strings.Cut(raw, ":")
		if !found || strings.Contains(addr, "/") {
			return "", nil, false
		}
		if _, h, at := strings.Cut(addr, "@"); at {
			addr = h
		}
		unescaped, err := url.PathUnescape(rest)
		if err != nil {
			return "", nil, false
		}
		host, p = addr, unescaped
	}
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	if host == "" || p == "" {
		return "", nil, false
	}
	return strings.ToLower(host), strings.Split(p, "/"), true
}
