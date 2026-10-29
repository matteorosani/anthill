package domain

import "strings"

// UserSet is a set of provider logins, such as the users with write access
// to a repository. Logins compare without regard to case, as they do on
// GitHub and Azure DevOps. The zero UserSet is empty and ready to use.
type UserSet struct {
	logins map[string]struct{}
}

// NewUserSet returns a set holding the given logins.
func NewUserSet(logins ...string) UserSet {
	s := UserSet{logins: make(map[string]struct{}, len(logins))}
	for _, l := range logins {
		s.logins[strings.ToLower(l)] = struct{}{}
	}
	return s
}

// Contains reports whether login is in the set. The empty login is never
// in it.
func (s UserSet) Contains(login string) bool {
	if login == "" {
		return false
	}
	_, ok := s.logins[strings.ToLower(login)]
	return ok
}

// sameUser reports whether two logins name the same user.
func sameUser(a, b string) bool {
	return a != "" && strings.EqualFold(a, b)
}
