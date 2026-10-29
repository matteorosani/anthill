package domain

import (
	"fmt"
	"slices"
	"strings"
	"unicode"
)

// CommandName is the word after /anthill.
type CommandName string

// The commands (spec agent-conversation).
const (
	CmdReview  CommandName = "review"
	CmdFix     CommandName = "fix"
	CmdStop    CommandName = "stop"
	CmdRelease CommandName = "release"
	CmdRetry   CommandName = "retry"
	CmdHelp    CommandName = "help"
)

// commandPrefix starts every command line.
const commandPrefix = "/anthill"

type commandInfo struct {
	name CommandName
	help string
}

// commands lists the known commands with their help, in help order.
var commands = []commandInfo{
	{CmdReview, "request a shepherd round"},
	{CmdFix, "fix the referenced thread"},
	{CmdStop, "stop the ant's active run, keeping its claim"},
	{CmdRelease, "force-release claims"},
	{CmdRetry, "retry a failed or stopped ant"},
	{CmdHelp, "list commands"},
}

// Known reports whether n is one of the commands.
func (n CommandName) Known() bool {
	return slices.ContainsFunc(commands, func(c commandInfo) bool { return c.name == n })
}

// String prints the command as written in a comment, such as
// "/anthill review".
func (n CommandName) String() string {
	return commandPrefix + " " + string(n)
}

// HelpText lists every command with what it does.
func HelpText() string {
	var b strings.Builder
	b.WriteString("Anthill commands, one per line:\n")
	for _, c := range commands {
		fmt.Fprintf(&b, "- `%s`: %s\n", c.name, c.help)
	}
	return strings.TrimSuffix(b.String(), "\n")
}

// ParseCommands finds the commands in a human comment. A command is a line
// whose text starts with /anthill, in any case, followed by a space or the
// end of the line. The next word is the command name, and the rest of the
// line is ignored. A bare /anthill is help. Lines in fenced code blocks
// and quoted lines are skipped, so quoting an old command does not run it
// again. Commands keep their order, and a repeated name counts once.
// Unknown names are returned too, so they can be answered with the help.
func ParseCommands(body string) []CommandName {
	var found []CommandName
	inFence := false
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "```") || strings.HasPrefix(line, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence || strings.HasPrefix(line, ">") {
			continue
		}
		if len(line) < len(commandPrefix) || !strings.EqualFold(line[:len(commandPrefix)], commandPrefix) {
			continue
		}
		rest := line[len(commandPrefix):]
		if rest != "" && !unicode.IsSpace([]rune(rest)[0]) {
			continue
		}
		name := CmdHelp
		if words := strings.Fields(rest); len(words) > 0 {
			name = CommandName(strings.ToLower(words[0]))
		}
		if !slices.Contains(found, name) {
			found = append(found, name)
		}
	}
	return found
}

// CommandPlace is where a command comment was posted.
type CommandPlace string

// The places.
const (
	PlaceTicket CommandPlace = "ticket"
	PlacePR     CommandPlace = "pull-request"
	// PlaceThread is a reply in a review thread of a pull request.
	PlaceThread CommandPlace = "review-thread"
)

// CommandOutcome is what Anthill does with a command.
type CommandOutcome string

// The command outcomes.
const (
	// CommandRun: execute the command.
	CommandRun CommandOutcome = "run"
	// CommandHelp: reply with the help text.
	CommandHelp CommandOutcome = "help"
	// CommandUnknown: reply that the command is unknown, with the help.
	CommandUnknown CommandOutcome = "unknown"
	// CommandDenied: the commenter may not run it; reply so.
	CommandDenied CommandOutcome = "denied"
	// CommandWrongPlace: the command does not apply where it was posted;
	// reply so.
	CommandWrongPlace CommandOutcome = "wrong-place"
)

// CommandContext is what authorization needs to know about a command.
type CommandContext struct {
	Commenter string
	// Writers are the users with write access to the repository.
	Writers UserSet
	// PRAuthor is the author of the pull request the command is on, or ""
	// on a ticket.
	PRAuthor string
	Place    CommandPlace
}

// CommandDecision is the outcome of a command and the reply to post. The
// reply is empty for CommandRun, whose acknowledgement depends on what the
// command did.
type CommandDecision struct {
	Outcome CommandOutcome
	Reply   string
}

// AuthorizeCommand decides what to do with one command (spec
// agent-conversation). Only users with write access may use commands,
// help and unknown ones included. The one exception is /anthill fix on a
// pull request, which its author may also use.
func AuthorizeCommand(name CommandName, ctx CommandContext) CommandDecision {
	onPR := ctx.Place == PlacePR || ctx.Place == PlaceThread
	allowed := ctx.Writers.Contains(ctx.Commenter) ||
		(name == CmdFix && onPR && sameUser(ctx.Commenter, ctx.PRAuthor))
	if !allowed {
		return CommandDecision{CommandDenied, fmt.Sprintf("`%s` requires write access to the repository.", name)}
	}
	switch {
	case !name.Known():
		return CommandDecision{CommandUnknown, fmt.Sprintf("Unknown command `%s`.\n\n%s", name, HelpText())}
	case name == CmdHelp:
		return CommandDecision{CommandHelp, HelpText()}
	case name == CmdReview && !onPR:
		return CommandDecision{CommandWrongPlace, fmt.Sprintf("`%s` works only on a pull request.", name)}
	case name == CmdFix && ctx.Place != PlaceThread:
		return CommandDecision{CommandWrongPlace, fmt.Sprintf("`%s` must be a reply in a finding's thread.", name)}
	}
	return CommandDecision{Outcome: CommandRun}
}
