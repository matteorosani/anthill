package claudecode

import (
	"encoding/json"
	"strings"

	"github.com/matteorosani/anthill/internal/domain"
)

// The hook subcommands of the anthill program that the run's settings
// install (spec ant-tools, "Provider commands are redirected to tools";
// design D5).
const (
	HookPreToolUse  = "pre-tool-use"
	HookPostToolUse = "post-tool-use"
)

// hookSettings is the --settings file:
//   - PreToolUse on Bash runs anthill hook pre-tool-use, which refuses
//     git push, gh and az;
//   - PostToolUse on every tool runs anthill hook post-tool-use --ant
//     <token>, which adds the run's queued notices as context.
func hookSettings(anthill, token string) any {
	hook := func(command string) []map[string]string {
		return []map[string]string{{"type": "command", "command": command}}
	}
	pre := shellQuote(anthill) + " hook " + HookPreToolUse
	post := shellQuote(anthill) + " hook " + HookPostToolUse + " --ant " + shellQuote(token)
	return map[string]any{
		"hooks": map[string]any{
			"PreToolUse":  []map[string]any{{"matcher": "Bash", "hooks": hook(pre)}},
			"PostToolUse": []map[string]any{{"matcher": "*", "hooks": hook(post)}},
		},
	}
}

// shellQuote quotes a word for the shell Claude Code runs hook commands
// with. Double quotes work in both a POSIX shell and cmd.exe; the
// characters these shells still expand inside them are escaped.
func shellQuote(s string) string {
	r := strings.NewReplacer(`"`, `\"`, "$", `\$`, "`", "\\`")
	return `"` + r.Replace(s) + `"`
}

// hookInput is what Claude Code gives a hook on its standard input.
type hookInput struct {
	HookEventName string `json:"hook_event_name"`
	ToolName      string `json:"tool_name"`
	ToolInput     struct {
		Command string `json:"command"`
	} `json:"tool_input"`
}

// PreToolUse decides on a PreToolUse hook input: when the agent is about
// to run a Bash command that the redirect hooks refuse, it returns the
// message for the agent. The hook then exits with code 2, which blocks
// the tool call and shows the message to the agent. An input it cannot
// read is not refused.
func PreToolUse(input []byte) (message string, refused bool) {
	var in hookInput
	if err := json.Unmarshal(input, &in); err != nil || in.ToolName != "Bash" {
		return "", false
	}
	return domain.RefuseCommand(in.ToolInput.Command)
}

// PostToolUseOutput is the output of a PostToolUse hook that adds the
// notices to the agent's context, or nil when there are none.
func PostToolUseOutput(notices []string) []byte {
	if len(notices) == 0 {
		return nil
	}
	out := map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName":     "PostToolUse",
			"additionalContext": strings.Join(notices, "\n"),
		},
	}
	b, err := json.Marshal(out)
	if err != nil {
		panic(err)
	}
	return append(b, '\n')
}
