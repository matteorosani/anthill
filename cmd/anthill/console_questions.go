package main

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// The commands for questions, extension requests and follow-up proposals
// (spec queen-console, "Controlling ants").

func newQuestionsCommand(env *environment) *cobra.Command {
	return &cobra.Command{
		Use:   "questions",
		Short: "List the pending questions of the ants",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var res questionsResult
			if err := consoleCall(cmd, env, methodQuestions, nil, &res); err != nil {
				return err
			}
			if len(res.Questions) == 0 {
				return printf(cmd, "no pending questions\n")
			}
			t := newTable(cmd.OutOrStdout(), "ITEM", "ASKED ON", "AGE", "QUESTION")
			for _, q := range res.Questions {
				age := formatDuration(res.Now.Sub(q.AskedAt))
				if q.Stale {
					age += " (stale)"
				}
				t.row(q.Item.Name, q.On.Name, age, firstLine(q.Text))
			}
			return t.flush()
		},
	}
}

func newAnswerCommand(env *environment) *cobra.Command {
	return &cobra.Command{
		Use:   "answer <item> <text>...",
		Short: "Answer the pending question of an item's ant, where the ant asked it",
		Long: "Answer the pending question of an item's ant. The answer is posted as your own comment where the question was asked, " +
			"and a new run resumes the ant's session with it. Give the text as arguments, or - to read it from standard input.",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) < 2 {
				return &usageError{fmt.Errorf("%s takes an item and the answer, such as #12 \"60 seconds\"", commandName(cmd))}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			text := strings.Join(args[1:], " ")
			if text == "-" {
				data, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return fmt.Errorf("reading the answer: %w", err)
				}
				text = string(data)
			}
			var res answerResult
			if err := consoleCall(cmd, env, methodAnswer, answerParams{Item: args[0], Text: text}, &res); err != nil {
				return err
			}
			if res.URL == "" {
				return printf(cmd, "answered the question of the ant for %s\n", args[0])
			}
			return printf(cmd, "answered the question of the ant for %s: %s\n", args[0], res.URL)
		},
	}
}

func newExtensionsCommand(env *environment) *cobra.Command {
	return &cobra.Command{
		Use:   "extensions",
		Short: "List the extension requests waiting for you",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var res extensionsResult
			if err := consoleCall(cmd, env, methodExtensions, nil, &res); err != nil {
				return err
			}
			if len(res.Extensions) == 0 {
				return printf(cmd, "no open extension requests\n")
			}
			t := newTable(cmd.OutOrStdout(), "ITEM", "MINUTES", "REMAINING", "REASON")
			for _, x := range res.Extensions {
				t.row(x.Item.Name, strconv.Itoa(x.Minutes), formatDuration(time.Duration(x.Remaining)*time.Second), firstLine(x.Reason))
			}
			return t.flush()
		},
	}
}

func newExtensionCommand(env *environment) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "extension",
		Short: "Approve, reduce or deny an ant's extension request",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return &usageError{fmt.Errorf("%s needs approve, reduce or deny", commandName(cmd))}
		},
	}
	decide := func(use, short, decision string, args cobra.PositionalArgs) *cobra.Command {
		return &cobra.Command{
			Use:   use,
			Short: short,
			Args:  args,
			RunE: func(cmd *cobra.Command, args []string) error {
				p := decideExtensionParams{Item: args[0], Decision: decision}
				if decision == "reduce" {
					minutes, err := strconv.Atoi(args[1])
					if err != nil || minutes <= 0 {
						return &usageError{fmt.Errorf("invalid minutes %q", args[1])}
					}
					p.Minutes = minutes
				}
				var res decisionResult
				if err := consoleCall(cmd, env, methodDecideExtension, p, &res); err != nil {
					return err
				}
				if res.Status == "denied" {
					return printf(cmd, "denied the extension of the ant for %s\n", args[0])
				}
				return printf(cmd, "%s the extension of the ant for %s: %d minutes granted\n", res.Status, args[0], res.Granted)
			},
		}
	}
	cmd.AddCommand(
		decide("approve <item>", "Grant the minutes the ant asked for", "approve", exactArgs(1, "one item, such as #12")),
		decide("reduce <item> <minutes>", "Grant fewer minutes than the ant asked for", "reduce", exactArgs(2, "an item and the minutes to grant, such as #12 20")),
		decide("deny <item>", "Grant no extension", "deny", exactArgs(1, "one item, such as #12")),
	)
	return cmd
}

func newFollowUpsCommand(env *environment) *cobra.Command {
	return &cobra.Command{
		Use:   "follow-ups",
		Short: "List the follow-up proposals waiting for you",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			var res followUpsResult
			if err := consoleCall(cmd, env, methodFollowUps, nil, &res); err != nil {
				return err
			}
			if len(res.FollowUps) == 0 {
				return printf(cmd, "no follow-up proposals\n")
			}
			for _, f := range res.FollowUps {
				pr := "no PR"
				if f.PR != nil {
					pr = "PR " + f.PR.Name
				}
				if err := printf(cmd, "%s (%s), %d changes:\n", f.Item.Name, pr, len(f.Changes)); err != nil {
					return err
				}
				for _, c := range f.Changes {
					if err := printf(cmd, "  %s\n", c); err != nil {
						return err
					}
				}
			}
			return nil
		},
	}
}

func newFollowUpCommand(env *environment) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "follow-up",
		Short: "Accept or decline an ant's follow-up proposal",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return &usageError{fmt.Errorf("%s needs accept or decline", commandName(cmd))}
		},
	}
	cmd.AddCommand(
		newAntActionCommand(env, "accept", "Start the follow-up run with the stored changes", methodAcceptFollowUp, "accepted the follow-up of"),
		newAntActionCommand(env, "decline", "Decline the follow-up; the changes are kept for the next proposal", methodDeclineFollowUp, "declined the follow-up of"),
	)
	return cmd
}
