package domain_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

func TestParseCommands(t *testing.T) {
	tests := []struct {
		name string
		body string
		want []domain.CommandName
	}{
		{"every command", "/anthill review\n/anthill fix\n/anthill stop\n/anthill release\n/anthill retry\n/anthill help",
			[]domain.CommandName{domain.CmdReview, domain.CmdFix, domain.CmdStop, domain.CmdRelease, domain.CmdRetry, domain.CmdHelp}},
		{"unknown command: /anthill deploy", "/anthill deploy", []domain.CommandName{"deploy"}},
		{"in a longer comment", "Looks wrong to me.\n\n  /anthill review please\nThanks", []domain.CommandName{domain.CmdReview}},
		{"any case", "/Anthill REVIEW", []domain.CommandName{domain.CmdReview}},
		{"bare /anthill is help", "/anthill", []domain.CommandName{domain.CmdHelp}},
		{"repeated command counts once", "/anthill review\n/anthill review", []domain.CommandName{domain.CmdReview}},
		{"not at the start of the line", "please run /anthill review", nil},
		{"another word", "/anthillreview", nil},
		{"quoted line", "> /anthill stop\nI agree", nil},
		{"fenced code block", "```\n/anthill stop\n```\n/anthill retry", []domain.CommandName{domain.CmdRetry}},
		{"tilde fence", "~~~\n/anthill stop\n~~~", nil},
		{"windows line endings", "/anthill stop\r\n/anthill retry\r\n", []domain.CommandName{domain.CmdStop, domain.CmdRetry}},
		{"no command", "thanks, looks good", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domain.ParseCommands(tt.body); !slices.Equal(got, tt.want) {
				t.Errorf("ParseCommands() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHelpTextListsEveryCommand(t *testing.T) {
	help := domain.HelpText()
	for _, name := range []domain.CommandName{domain.CmdReview, domain.CmdFix, domain.CmdStop, domain.CmdRelease, domain.CmdRetry, domain.CmdHelp} {
		if !strings.Contains(help, "`"+name.String()+"`") {
			t.Errorf("help text does not list %s:\n%s", name, help)
		}
	}
}

func TestAuthorizeCommand(t *testing.T) {
	const prAuthor = "contributor" // a PR author without write access
	tests := []struct {
		name      string
		command   domain.CommandName
		commenter string
		place     domain.CommandPlace
		outcome   domain.CommandOutcome
		reply     string
	}{
		{"review on a PR", domain.CmdReview, "lbianchi", domain.PlacePR, domain.CommandRun, ""},
		{"review in a thread", domain.CmdReview, "lbianchi", domain.PlaceThread, domain.CommandRun, ""},
		{"review on a ticket", domain.CmdReview, "lbianchi", domain.PlaceTicket, domain.CommandWrongPlace, "only on a pull request"},
		{"fix on a thread of an ant PR", domain.CmdFix, "lbianchi", domain.PlaceThread, domain.CommandRun, ""},
		{"fix as a top-level PR comment", domain.CmdFix, "lbianchi", domain.PlacePR, domain.CommandWrongPlace, "reply in a finding's thread"},
		{"fix on a ticket", domain.CmdFix, "lbianchi", domain.PlaceTicket, domain.CommandWrongPlace, "reply in a finding's thread"},
		{"stop command on a ticket", domain.CmdStop, "lbianchi", domain.PlaceTicket, domain.CommandRun, ""},
		{"stop command on the ant PR", domain.CmdStop, "lbianchi", domain.PlacePR, domain.CommandRun, ""},
		{"release on a ticket", domain.CmdRelease, "lbianchi", domain.PlaceTicket, domain.CommandRun, ""},
		{"retry on a ticket", domain.CmdRetry, "lbianchi", domain.PlaceTicket, domain.CommandRun, ""},
		{"help", domain.CmdHelp, "lbianchi", domain.PlaceTicket, domain.CommandHelp, "Anthill commands"},
		{"unknown command: /anthill deploy", "deploy", "lbianchi", domain.PlaceTicket, domain.CommandUnknown, "Unknown command `/anthill deploy`."},
		{"external contributor: review", domain.CmdReview, "outsider", domain.PlacePR, domain.CommandDenied, "`/anthill review` requires write access to the repository."},
		{"PR author opts in: fix by a PR author without write access", domain.CmdFix, prAuthor, domain.PlaceThread, domain.CommandRun, ""},
		{"PR author in another case", domain.CmdFix, "Contributor", domain.PlaceThread, domain.CommandRun, ""},
		{"PR author may not use other commands", domain.CmdReview, prAuthor, domain.PlacePR, domain.CommandDenied, "requires write access"},
		{"outsider: help", domain.CmdHelp, "outsider", domain.PlaceTicket, domain.CommandDenied, "requires write access"},
		{"outsider: unknown command", "deploy", "outsider", domain.PlaceTicket, domain.CommandDenied, "requires write access"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := domain.CommandContext{Commenter: tt.commenter, Writers: writers, Place: tt.place}
			if tt.place != domain.PlaceTicket {
				ctx.PRAuthor = prAuthor
			}
			got := domain.AuthorizeCommand(tt.command, ctx)
			if got.Outcome != tt.outcome {
				t.Errorf("outcome = %s, want %s (reply %q)", got.Outcome, tt.outcome, got.Reply)
			}
			if !strings.Contains(got.Reply, tt.reply) || (tt.reply == "") != (got.Reply == "") {
				t.Errorf("reply = %q, want it to contain %q", got.Reply, tt.reply)
			}
		})
	}
}

func TestUnknownCommandIsAnsweredWithHelp(t *testing.T) {
	names := domain.ParseCommands("/anthill deploy")
	got := domain.AuthorizeCommand(names[0], domain.CommandContext{Commenter: "lbianchi", Writers: writers, Place: domain.PlaceTicket})
	if !strings.HasSuffix(got.Reply, domain.HelpText()) {
		t.Errorf("reply %q does not end with the help text", got.Reply)
	}
}

func TestPRAuthorExceptionNeedsAPR(t *testing.T) {
	// On a ticket there is no PR author, so the ticket's commenter gets no
	// exception even if the context names one by mistake.
	got := domain.AuthorizeCommand(domain.CmdFix, domain.CommandContext{Commenter: "contributor", Writers: writers, PRAuthor: "contributor", Place: domain.PlaceTicket})
	if got.Outcome != domain.CommandDenied {
		t.Errorf("outcome = %s, want denied", got.Outcome)
	}
}
