package domain

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// MarkerVersion is the version of the marker format this code writes and
// reads. A marker with another version is not recognised.
const MarkerVersion = 1

// CommentKind is what an Anthill comment is for.
type CommentKind string

// The comment kinds (spec agent-conversation).
const (
	CommentClaim    CommentKind = "claim"
	CommentQuestion CommentKind = "question"
	CommentStatus   CommentKind = "status"
	CommentSummary  CommentKind = "summary"
	CommentFinding  CommentKind = "finding"
	CommentReply    CommentKind = "reply"
)

// Valid reports whether k is one of the comment kinds.
func (k CommentKind) Valid() bool {
	switch k {
	case CommentClaim, CommentQuestion, CommentStatus, CommentSummary, CommentFinding, CommentReply:
		return true
	}
	return false
}

// ClaimKind is what a claim is for (spec work-claiming).
type ClaimKind string

// The claim kinds.
const (
	ClaimTask   ClaimKind = "task"
	ClaimReview ClaimKind = "review"
)

// ClaimFields are the marker fields only claim comments carry.
type ClaimFields struct {
	Claim        ClaimKind `json:"claim"`
	LeaseMinutes int       `json:"lease_minutes"`
	Parked       bool      `json:"parked"`
	Released     bool      `json:"released"`
}

// FindingFields are the marker fields only finding comments carry. They
// let a later round match a new finding to this one (design D9) without
// reading the rendered comment text.
type FindingFields struct {
	Fingerprint string `json:"fingerprint"`
	File        string `json:"file,omitempty"`
	StartLine   int    `json:"start_line,omitempty"`
	EndLine     int    `json:"end_line,omitempty"`
}

// Marker is the hidden machine-readable part of every Anthill comment. It
// is written as an HTML comment holding JSON:
//
//	<!-- anthill {"v":1,"instance":"mrosani@LAPTOP-42","kind":"status","ref":"gh-12"} -->
//
// Both providers keep HTML comments in Markdown without showing them.
type Marker struct {
	Version int `json:"v"`
	// ID is an optional key chosen by the caller, so that a write can check
	// whether a comment with the same marker already exists (design D7).
	ID       string      `json:"id,omitempty"`
	Instance string      `json:"instance"`
	Kind     CommentKind `json:"kind"`
	// Ref is the short form of the item or PR, such as gh-12. The
	// repository is the one the comment is in.
	Ref string `json:"ref"`
	// ClaimFields is set for claim markers and nil for all others. Its
	// fields are written at the top level of the JSON object.
	*ClaimFields
	// FindingFields is set for finding markers that carry a fingerprint,
	// and nil for all others. Its fields are also written at the top level.
	*FindingFields
}

// Validate checks that the marker has every field its kind needs.
func (m Marker) Validate() error {
	switch {
	case m.Version != MarkerVersion:
		return fmt.Errorf("marker version %d is not supported", m.Version)
	case m.Instance == "":
		return fmt.Errorf("marker has no instance")
	case !m.Kind.Valid():
		return fmt.Errorf("marker kind %q is not valid", m.Kind)
	}
	if _, _, _, err := parseShortRef(m.Ref); err != nil {
		return fmt.Errorf("marker ref: %w", err)
	}
	if m.Kind == CommentClaim {
		if m.ClaimFields == nil {
			return fmt.Errorf("claim marker has no claim fields")
		}
		if m.Claim != ClaimTask && m.Claim != ClaimReview {
			return fmt.Errorf("claim kind %q is not valid", m.Claim)
		}
		if m.LeaseMinutes <= 0 {
			return fmt.Errorf("claim lease must be more than 0 minutes")
		}
	} else if m.ClaimFields != nil {
		return fmt.Errorf("only claim markers have claim fields")
	}
	if m.FindingFields != nil {
		if m.Kind != CommentFinding {
			return fmt.Errorf("only finding markers have finding fields")
		}
		if m.Fingerprint == "" {
			return fmt.Errorf("finding marker has no fingerprint")
		}
		if m.StartLine < 0 || m.EndLine < m.StartLine {
			return fmt.Errorf("finding marker has an invalid line range %d-%d", m.StartLine, m.EndLine)
		}
	}
	return nil
}

// String renders the marker as a hidden HTML comment. encoding/json escapes
// <, > and & inside strings, so no value can close the HTML comment early.
func (m Marker) String() string {
	data, err := json.Marshal(m)
	if err != nil {
		// A Marker holds only strings, numbers and booleans, which always
		// marshal.
		panic(fmt.Sprintf("marshal marker: %v", err))
	}
	return "<!-- anthill " + string(data) + " -->"
}

// markerPattern finds candidate markers. (?s) lets . match newlines, and
// .*? stops at the first "} -->".
var markerPattern = regexp.MustCompile(`(?s)<!-- anthill (\{.*?\}) -->`)

// FindMarker returns the first valid marker in a comment body. A body
// without one, or whose markers are all invalid, was written by a human.
func FindMarker(body string) (Marker, bool) {
	for _, match := range markerPattern.FindAllStringSubmatch(body, -1) {
		var m Marker
		if err := json.Unmarshal([]byte(match[1]), &m); err != nil {
			continue
		}
		if m.Validate() == nil {
			return m, true
		}
	}
	return Marker{}, false
}

// Origin tells who wrote a comment or made a change.
type Origin string

// The origins.
const (
	OriginHuman   Origin = "human"
	OriginAnthill Origin = "anthill"
)

// OriginOf decides who wrote a comment body. Anthill acts under each
// developer's own account, so only the marker decides, never the author.
func OriginOf(body string) Origin {
	if _, ok := FindMarker(body); ok {
		return OriginAnthill
	}
	return OriginHuman
}

// Origin reports whether the comment was written by Anthill or by a human,
// which only the marker decides.
func (c Comment) Origin() Origin {
	return OriginOf(c.Body)
}

// Role is who posts an Anthill comment, shown in its visible prefix.
type Role string

// The roles.
const (
	RoleAnt      Role = "Ant"
	RoleShepherd Role = "Shepherd"
)

// prefixPattern matches the visible first line, such as
// "🐜 Ant (mrosani@LAPTOP-42)".
var prefixPattern = regexp.MustCompile(`^🐜 (Ant|Shepherd) \(([^)\n]*)\)\n`)

// AnthillComment is an Anthill comment split into its parts.
type AnthillComment struct {
	Role   Role
	Body   string
	Marker Marker
}

// String renders the full comment: the visible 🐜 prefix naming the role
// and instance, the body, and the marker on its own last line.
func (c AnthillComment) String() string {
	return fmt.Sprintf("🐜 %s (%s)\n\n%s\n\n%s", c.Role, c.Marker.Instance, strings.TrimSpace(c.Body), c.Marker)
}

// ParseAnthillComment splits a comment written by AnthillComment.String
// back into its parts. It returns false when the text has no valid marker
// or no 🐜 prefix.
func ParseAnthillComment(text string) (AnthillComment, bool) {
	m, ok := FindMarker(text)
	if !ok {
		return AnthillComment{}, false
	}
	prefix := prefixPattern.FindStringSubmatch(text)
	if prefix == nil {
		return AnthillComment{}, false
	}
	body := text[len(prefix[0]):]
	body = markerPattern.ReplaceAllString(body, "")
	return AnthillComment{
		Role:   Role(prefix[1]),
		Body:   strings.TrimSpace(body),
		Marker: m,
	}, true
}
