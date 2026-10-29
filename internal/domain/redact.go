package domain

import (
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

// Redacted replaces every secret Anthill removes from text it posts.
const Redacted = "[REDACTED]"

// secretGroup is the name of the capture group that holds the secret. When
// a pattern has it, only that group is replaced and the rest of the match,
// such as "Bearer " or "Password=", stays readable. Otherwise the whole
// match is replaced.
const secretGroup = "secret"

// builtinSecretPatterns are always applied and cannot be removed (spec
// anthill-configuration). There is no Azure DevOps PAT pattern: those are
// 52 plain base32 characters, which would match too much ordinary text.
var builtinSecretPatterns = []*regexp.Regexp{
	// GitHub classic tokens: personal, OAuth, user-to-server,
	// server-to-server and refresh.
	regexp.MustCompile(`\b(?:ghp|gho|ghu|ghs|ghr)_[A-Za-z0-9]{36}\b`),
	// GitHub fine-grained personal access tokens.
	regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{82}\b`),
	// AWS access key ids, long-lived and temporary.
	regexp.MustCompile(`\b(?:AKIA|ASIA)[A-Z0-9]{16}\b`),
	// Google API keys.
	regexp.MustCompile(`\bAIza[0-9A-Za-z_\-]{35}`),
	// Slack tokens.
	regexp.MustCompile(`\bxox[abpr]-[A-Za-z0-9\-]{10,}`),
	// PEM private keys of any type, from BEGIN to END.
	regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----.*?-----END [A-Z0-9 ]*PRIVATE KEY-----`),
	// Bearer tokens in Authorization headers.
	regexp.MustCompile(`(?i)\bbearer\s+(?P<secret>[A-Za-z0-9\-._~+/]{20,}=*)`),
	// Azure storage and service bus connection string keys.
	regexp.MustCompile(`(?i)\b(?:AccountKey|SharedAccessKey)=(?P<secret>[^;\s"']+)`),
	// Passwords in database connection strings.
	regexp.MustCompile(`(?i)\b(?:password|pwd)=(?P<secret>[^;\s"']+)`),
	// Passwords in URLs, such as https://user:password@host.
	regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9+.\-]*://[^/\s:@]+:(?P<secret>[^/\s@]+)@`),
}

// Redactor removes secrets from text Anthill posts or logs.
type Redactor struct {
	patterns []*regexp.Regexp
}

// NewRedactor returns a redactor with the built-in patterns plus the local
// patterns from the user's configuration. Local patterns use Go regexp
// (RE2) syntax and may name a group "secret" to redact only that part.
func NewRedactor(localPatterns []string) (*Redactor, error) {
	patterns := append([]*regexp.Regexp(nil), builtinSecretPatterns...)
	for _, p := range localPatterns {
		re, err := regexp.Compile(p)
		if err != nil {
			return nil, fmt.Errorf("secret pattern %q: %w", p, err)
		}
		patterns = append(patterns, re)
	}
	return &Redactor{patterns: patterns}, nil
}

// Redact returns text with every secret replaced by [REDACTED].
func (r *Redactor) Redact(text string) string {
	for _, re := range r.patterns {
		text = redactPattern(re, text)
	}
	return text
}

// Summary redacts text and then truncates it to limit characters. It
// redacts first, so that truncation cannot cut a secret in half and leave
// a part no pattern matches.
func (r *Redactor) Summary(text string, limit int) string {
	return Truncate(r.Redact(text), limit)
}

func redactPattern(re *regexp.Regexp, text string) string {
	group := re.SubexpIndex(secretGroup)
	var out strings.Builder
	last := 0
	for _, loc := range re.FindAllStringSubmatchIndex(text, -1) {
		start, end := loc[0], loc[1]
		if group > 0 && loc[2*group] >= 0 {
			start, end = loc[2*group], loc[2*group+1]
		}
		out.WriteString(text[last:start])
		out.WriteString(Redacted)
		last = end
	}
	out.WriteString(text[last:])
	return out.String()
}

// DefaultSummaryLimit is the default length of a summary, in characters.
const DefaultSummaryLimit = 4000

// Truncate shortens text to at most limit characters, counted as Unicode
// code points, not bytes. Longer text is cut and ends with a note saying
// how much is shown; the note counts toward the limit. A limit of 0 or less
// means DefaultSummaryLimit.
func Truncate(text string, limit int) string {
	if limit <= 0 {
		limit = DefaultSummaryLimit
	}
	total := utf8.RuneCountInString(text)
	if total <= limit {
		return text
	}
	runes := []rune(text)
	// The note is longest when it states the full limit, so sizing it with
	// limit leaves enough room for any smaller number.
	shown := limit - utf8.RuneCountInString(truncationNote(limit, total))
	if shown <= 0 {
		return string(runes[:limit])
	}
	return string(runes[:shown]) + truncationNote(shown, total)
}

func truncationNote(shown, total int) string {
	return fmt.Sprintf("\n\n… (truncated, %d of %d characters shown)", shown, total)
}
