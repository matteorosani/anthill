package domain

import (
	"fmt"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// DefaultBranchPrefix is the branch prefix when a repository sets none.
const DefaultBranchPrefix = "ant"

// SlugMaxLength is the longest slug in a branch name, in characters.
const SlugMaxLength = 40

// slugLetters replaces letters that do not decompose into an ASCII letter
// plus marks.
var slugLetters = strings.NewReplacer(
	"ß", "ss",
	"æ", "ae",
	"œ", "oe",
	"ø", "o",
	"đ", "d",
	"ł", "l",
	"þ", "th",
)

// Slug turns an item title into the part of a branch name after the
// reference. It lowercases the title, makes it ASCII (é becomes e, ß
// becomes ss, and other characters are dropped), joins words with single
// hyphens and cuts it to SlugMaxLength characters. A title with no ASCII
// letters or digits left gives an empty slug.
func Slug(title string) string {
	s := slugLetters.Replace(strings.ToLower(title))
	// NFD splits a letter such as é into e plus a combining accent, which
	// the loop below then drops.
	s = norm.NFD.String(s)

	var b strings.Builder
	hyphen := false
	for _, r := range s {
		switch {
		case unicode.Is(unicode.Mn, r):
			// A combining mark: drop it without separating words.
		case r >= 'a' && r <= 'z' || r >= '0' && r <= '9':
			if hyphen && b.Len() > 0 {
				b.WriteByte('-')
			}
			hyphen = false
			b.WriteRune(r)
		default:
			hyphen = true
		}
	}
	slug := b.String()
	if len(slug) > SlugMaxLength {
		slug = strings.TrimRight(slug[:SlugMaxLength], "-")
	}
	return slug
}

// BranchName returns the ant branch for an item: <prefix>/<ref>-<slug>,
// such as ant/gh-12-fix-login-timeout-on-safari, or <prefix>/<ref> when
// the slug is empty.
func BranchName(prefix string, item Ref, title string) (string, error) {
	if err := validateRefName(prefix); err != nil {
		return "", fmt.Errorf("branch prefix: %w", err)
	}
	if item.Kind != KindItem {
		return "", fmt.Errorf("ant branches are for items, not %s", item)
	}
	name := prefix + "/" + item.String()
	if slug := Slug(title); slug != "" {
		name += "-" + slug
	}
	return name, nil
}

// WithSuffix returns the branch name with a numeric suffix, used when the
// name is already taken on the remote by someone else: n = 2 gives
// "…-2". An n below 2 returns the name unchanged.
func WithSuffix(branch string, n int) string {
	if n < 2 {
		return branch
	}
	return fmt.Sprintf("%s-%d", branch, n)
}
