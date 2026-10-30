package azuredevops

import (
	"html"
	"regexp"
	"strings"
)

// htmlToText turns the HTML of a rich-text work item field, such as
// System.Description, into plain text with the little Markdown the domain
// relies on: headings become # lines, bold becomes **…**, list items
// become "- " lines, and block elements become line breaks. Everything
// else is stripped and entities are decoded. HTML with no visible text,
// such as <div><br></div>, becomes "", so that the has-description
// check sees an empty description (spec task-eligibility, "Prerequisite
// readiness content").
//
// It is not a full HTML parser: Azure DevOps stores what its editor
// writes, which is simple, well-formed markup.
func htmlToText(s string) string {
	var b strings.Builder
	skip := "" // the element whose content is dropped, such as script
	for s != "" {
		lt := strings.IndexByte(s, '<')
		if lt < 0 {
			lt = len(s)
		}
		if skip == "" {
			b.WriteString(textNode(s[:lt]))
		}
		s = s[lt:]
		if s == "" {
			break
		}
		if strings.HasPrefix(s, "<!--") {
			end := strings.Index(s, "-->")
			if end < 0 {
				break
			}
			s = s[end+len("-->"):]
			continue
		}
		gt := strings.IndexByte(s, '>')
		if gt < 0 {
			// A lone < is text.
			if skip == "" {
				b.WriteString(html.UnescapeString(s))
			}
			break
		}
		name, closing := tagName(s[1:gt])
		s = s[gt+1:]
		if skip != "" {
			if closing && name == skip {
				skip = ""
			}
			continue
		}
		switch {
		case name == "script" || name == "style":
			if !closing {
				skip = name
			}
		case len(name) == 2 && name[0] == 'h' && name[1] >= '1' && name[1] <= '6':
			b.WriteString("\n")
			if !closing {
				b.WriteString(strings.Repeat("#", int(name[1]-'0')) + " ")
			}
		case name == "b" || name == "strong":
			b.WriteString("**")
		case name == "li":
			if !closing {
				b.WriteString("\n- ")
			} else {
				b.WriteString("\n")
			}
		case blockTags[name]:
			b.WriteString("\n")
		}
	}
	return tidy(b.String())
}

// blockTags are the elements that start a new line.
var blockTags = map[string]bool{
	"br": true, "p": true, "div": true, "tr": true, "table": true, "ul": true, "ol": true,
	"blockquote": true, "pre": true, "hr": true, "section": true,
}

// tagName returns the lowercase name of the tag whose text between < and
// > is inner, and whether it is a closing tag.
func tagName(inner string) (string, bool) {
	closing := strings.HasPrefix(inner, "/")
	inner = strings.TrimPrefix(inner, "/")
	end := strings.IndexAny(inner, " \t\r\n/")
	if end >= 0 {
		inner = inner[:end]
	}
	return strings.ToLower(inner), closing
}

// textNode decodes a text node. Line breaks in HTML source are spaces.
func textNode(s string) string {
	s = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ").Replace(s)
	return html.UnescapeString(s)
}

var (
	spaces = regexp.MustCompile(`[ \x{00A0}]+`)
	// emptyBold is bold markup left around no text, such as <b><br></b>.
	emptyBold = regexp.MustCompile(`\*\*\s*\*\*`)
)

// tidy collapses spaces, trims every line, drops empty lines, and drops
// bold markup around nothing.
func tidy(s string) string {
	s = emptyBold.ReplaceAllString(s, "")
	var lines []string
	for l := range strings.SplitSeq(s, "\n") {
		if l = strings.TrimSpace(spaces.ReplaceAllString(l, " ")); l != "" {
			lines = append(lines, l)
		}
	}
	s = strings.Join(lines, "\n")
	if strings.Trim(s, "#-* \n") == "" {
		return ""
	}
	return s
}
