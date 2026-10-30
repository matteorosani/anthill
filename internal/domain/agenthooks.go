package domain

import (
	"strings"
)

// RefuseCommand tells whether the redirect hooks refuse a shell command
// an agent is about to run, and why (spec ant-tools, "Provider commands
// are redirected to tools"; design D4). They refuse git push and the
// provider CLIs gh and az, wherever they appear in the command line:
// after cd x &&, in a pipeline, behind env or sudo, with a full path, or
// inside sh -c. The message names the ant tool to use instead.
//
// This is a guard against accidents, not a sandbox: a command that hides
// the program, such as through a variable or a script, is not refused,
// and the push then fails for lack of credentials.
func RefuseCommand(command string) (message string, refused bool) {
	for _, words := range simpleCommands(command, 0) {
		if m, ok := refuseWords(words, 0); ok {
			return m, true
		}
	}
	return "", false
}

// maxNesting bounds how deep sh -c and eval are followed.
const maxNesting = 4

// commandWrappers run the command that follows their own options.
var commandWrappers = map[string]bool{
	"sudo": true, "env": true, "command": true, "exec": true, "time": true,
	"nohup": true, "nice": true, "xargs": true, "builtin": true, "doas": true,
}

// shells run the command string given to -c.
var shells = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "fish": true,
	"pwsh": true, "powershell": true, "cmd": true,
}

func refuseWords(words []string, depth int) (string, bool) {
	words = skipAssignments(words)
	for len(words) > 0 && commandWrappers[program(words[0])] {
		words = skipAssignments(skipOptions(words[1:]))
	}
	if len(words) == 0 {
		return "", false
	}
	prog := program(words[0])
	switch {
	case prog == "git":
		if gitSubcommand(words[1:]) == "push" {
			return MessageUsePushTool, true
		}
	case prog == "gh" || prog == "az":
		return providerCLIMessage(prog, words[1:]), true
	case prog == "eval" && depth < maxNesting:
		return refuseString(strings.Join(words[1:], " "), depth+1)
	case shells[prog] && depth < maxNesting:
		for i, w := range words[1:] {
			if lw := strings.ToLower(w); (lw == "-c" || lw == "/c" || lw == "-command") && i+2 < len(words) {
				return refuseString(words[i+2], depth+1)
			}
		}
	}
	return "", false
}

func refuseString(command string, depth int) (string, bool) {
	for _, words := range simpleCommands(command, depth) {
		if m, ok := refuseWords(words, depth); ok {
			return m, true
		}
	}
	return "", false
}

// program is the name of a command word without its directory or
// Windows extension, in lower case.
func program(word string) string {
	if i := strings.LastIndexAny(word, `/\`); i >= 0 {
		word = word[i+1:]
	}
	word = strings.ToLower(word)
	for _, ext := range []string{".exe", ".cmd", ".bat"} {
		word = strings.TrimSuffix(word, ext)
	}
	return word
}

// skipAssignments drops leading NAME=value words.
func skipAssignments(words []string) []string {
	for len(words) > 0 {
		w := words[0]
		eq := strings.IndexByte(w, '=')
		if eq <= 0 || strings.ContainsAny(w[:eq], `/\-`) {
			break
		}
		words = words[1:]
	}
	return words
}

// skipOptions drops the leading options of a wrapper such as sudo -u x.
// An option's value is not told apart from the command, so sudo -u root
// git push is seen as root git push: the hooks miss it, and the missing
// credentials still stop the push.
func skipOptions(words []string) []string {
	for len(words) > 0 && strings.HasPrefix(words[0], "-") {
		words = words[1:]
	}
	return words
}

// gitOptionsWithValue are git's global options that take the next word
// as their value.
var gitOptionsWithValue = map[string]bool{
	"-C": true, "-c": true, "--git-dir": true, "--work-tree": true,
	"--namespace": true, "--exec-path": true, "--config-env": true,
	"--super-prefix": true, "--list-cmds": true, "--attr-source": true,
}

// gitSubcommand returns the git subcommand after the global options.
func gitSubcommand(args []string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case gitOptionsWithValue[a]:
			i++
		case strings.HasPrefix(a, "-"):
		default:
			return strings.ToLower(a)
		}
	}
	return ""
}

// providerCLIMessage names the ant tool that replaces a gh or az
// command, or lists the ant tools when no single one does.
func providerCLIMessage(prog string, args []string) string {
	var words []string
	discussion := false
	for _, a := range args {
		a = strings.ToLower(a)
		if !strings.HasPrefix(a, "-") {
			words = append(words, a)
		}
		// az boards work-item update --discussion adds a comment.
		discussion = discussion || a == "--discussion" || strings.HasPrefix(a, "--discussion=")
	}
	has := func(seq ...string) bool {
		for i := 0; i+len(seq) <= len(words); i++ {
			match := true
			for j, w := range seq {
				if words[i+j] != w {
					match = false
					break
				}
			}
			if match {
				return true
			}
		}
		return false
	}
	tool := ""
	switch {
	case has("pr", "create"):
		tool = ToolOpenPullRequest
	case has("pr", "edit"), has("pr", "ready"), has("pr", "update"), has("pr", "set-vote"):
		tool = ToolUpdatePullRequest
	case has("comment"), discussion:
		tool = ToolComment
	}
	if tool != "" {
		return MessageUseTool(prog, tool)
	}
	return "anthill: " + prog + " is disabled for agents; use the ant tools instead: " +
		strings.Join([]string{ToolPush, ToolOpenPullRequest, ToolUpdatePullRequest, ToolComment, ToolReplyToThread, ToolAskQuestion}, ", ")
}

// MessageUseTool is the refusal of a provider CLI command that an ant
// tool replaces.
func MessageUseTool(prog, tool string) string {
	return "anthill: " + prog + " is disabled for agents; call the " + tool + " tool instead"
}

// simpleCommands splits a shell command line into its simple commands,
// as words without quotes. It knows quotes, backslashes, the command
// separators ; & | && || and newlines, grouping with ( ) { }, and
// command substitution with $( ) and backquotes, whose content is split
// as commands too.
func simpleCommands(line string, depth int) [][]string {
	var (
		out     [][]string
		words   []string
		word    strings.Builder
		inWord  bool
		quote   rune
		escaped bool
	)
	endWord := func() {
		if inWord {
			words = append(words, word.String())
			word.Reset()
			inWord = false
		}
	}
	endCommand := func() {
		endWord()
		if len(words) > 0 {
			out = append(out, words)
			words = nil
		}
	}
	runes := []rune(line)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch {
		case escaped:
			word.WriteRune(r)
			inWord, escaped = true, false
		case r == '\\' && quote == 0:
			escaped = true
		case r == '\\' && quote == '"' && i+1 < len(runes) && strings.ContainsRune("$`\"\\\n", runes[i+1]):
			// Inside double quotes, a backslash escapes only these, so a
			// Windows path keeps its backslashes.
			escaped = true
		case quote != 0 && r == quote:
			quote = 0
		case quote == '\'':
			word.WriteRune(r)
		case r == '`' || (r == '$' && i+1 < len(runes) && runes[i+1] == '('):
			// Command substitution: split its content as commands too.
			end, closeRune := i+1, '`'
			if r == '$' {
				end, closeRune = i+2, ')'
			}
			start, level := end, 1
			for ; end < len(runes); end++ {
				if closeRune == ')' && runes[end] == '(' {
					level++
				}
				if runes[end] == closeRune {
					level--
					if level == 0 {
						break
					}
				}
			}
			if depth < maxNesting {
				out = append(out, simpleCommands(string(runes[start:min(end, len(runes))]), depth+1)...)
			}
			i = end
			inWord = true
		case quote == '"':
			word.WriteRune(r)
		case r == '\'' || r == '"':
			quote, inWord = r, true
		case r == ';' || r == '&' || r == '|' || r == '\n' || r == '(' || r == ')':
			endCommand()
		case (r == '{' || r == '}') && !inWord:
			endCommand()
		case r == ' ' || r == '\t' || r == '\r':
			endWord()
		default:
			word.WriteRune(r)
			inWord = true
		}
	}
	endCommand()
	return out
}
