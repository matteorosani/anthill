package main

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/matteorosani/anthill/internal/adapters/claudecode"
	"github.com/matteorosani/anthill/internal/adapters/control"
	"github.com/matteorosani/anthill/internal/adapters/mcpserver"
)

// noticesTimeout bounds how long the PostToolUse hook waits for the
// daemon, so a daemon that does not answer never holds up the agent.
const noticesTimeout = 5 * time.Second

// newHookCommand returns anthill hook, which Claude Code runs as the
// run's hooks (design D4, D5). Its subcommands speak Claude Code's hook
// protocol: the hook input as JSON on standard input, the decision as
// the exit code, standard error and standard output.
func newHookCommand(env *environment) *cobra.Command {
	cmd := &cobra.Command{
		Use:    "hook",
		Short:  "Run an agent hook (started by the agent's runner, not by hand)",
		Hidden: true,
		Args:   noArgs,
		RunE: func(*cobra.Command, []string) error {
			return &usageError{errors.New("hook needs " + claudecode.HookPreToolUse + " or " + claudecode.HookPostToolUse)}
		},
	}
	cmd.AddCommand(newPreToolUseCommand(env), newPostToolUseCommand(env))
	return cmd
}

// newPreToolUseCommand refuses git push, gh and az: it exits with code 2
// and the message on standard error, which blocks the command and tells
// the agent which ant tool to use. Anything else exits with 0.
func newPreToolUseCommand(env *environment) *cobra.Command {
	return &cobra.Command{
		Use:   claudecode.HookPreToolUse,
		Short: "Refuse git push, gh and az before the agent runs them",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			input, err := io.ReadAll(cmd.InOrStdin())
			if err != nil {
				return err
			}
			if msg, refused := claudecode.PreToolUse(input); refused {
				_, _ = io.WriteString(env.stderr, msg+"\n")
				return &exitError{code: 2}
			}
			return nil
		},
	}
}

// newPostToolUseCommand takes the run's queued notices from the daemon
// and prints them as context for the agent. With no notice, or when the
// daemon cannot be reached, it prints nothing and exits with 0, so it
// never stops the agent.
func newPostToolUseCommand(env *environment) *cobra.Command {
	var token string
	cmd := &cobra.Command{
		Use:   claudecode.HookPostToolUse + " --ant <token>",
		Short: "Add the run's queued notices to the agent's context",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if token == "" || env.err != nil {
				return nil
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), noticesTimeout)
			defer cancel()
			var res antNoticesResult
			if err := control.NewClient(env.address).Call(ctx, methodAntNotices, antToolsParams{Token: token}, &res); err != nil {
				return nil
			}
			notices := make([]string, len(res.Notices))
			for i, n := range res.Notices {
				notices[i] = mcpserver.NoticePrefix + n
			}
			_, _ = env.stdout.Write(claudecode.PostToolUseOutput(notices))
			return nil
		},
	}
	cmd.Flags().StringVar(&token, "ant", "", "the run's tool token")
	return cmd
}
