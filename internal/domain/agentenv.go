package domain

import (
	"strconv"
	"strings"
)

// MessageUsePushTool is shown when an agent tries to push, or to use ssh,
// from its worktree (spec ant-tools, "Redirect hooks").
const MessageUsePushTool = "anthill: git push is disabled for agents; call the push tool instead"

// SSHRefusal is the core.sshCommand of an ant worktree. Git runs it with
// a shell and appends the ssh arguments, which false ignores, so every
// ssh connection fails before it is made.
const SSHRefusal = "echo '" + MessageUsePushTool + "' >&2; false"

// AgentGitConfig is the git configuration that keeps an agent from
// pushing with the developer's credentials (design D4): no credential
// helper, no askpass program, and no ssh. Anthill writes it to the
// worktree's own configuration and repeats it in the agent's environment.
var AgentGitConfig = [][2]string{
	{"credential.helper", ""},
	{"core.askPass", ""},
	{"core.sshCommand", SSHRefusal},
}

// agentRemovedVariables are taken out of an agent's environment: provider
// tokens, and the programs and agent socket git and ssh could use to get
// credentials.
var agentRemovedVariables = []string{
	"GH_TOKEN",
	"GITHUB_TOKEN",
	"GH_ENTERPRISE_TOKEN",
	"GITHUB_ENTERPRISE_TOKEN",
	"AZURE_DEVOPS_EXT_PAT",
	"AZURE_DEVOPS_EXT_GIT_SOURCE_PASSWORD",
	"SYSTEM_ACCESSTOKEN",
	"GIT_ASKPASS",
	"SSH_ASKPASS",
	"SSH_AUTH_SOCK",
	"GIT_TERMINAL_PROMPT",
	"GCM_INTERACTIVE",
}

// AgentEnv returns the environment of an agent, given Anthill's own as
// KEY=value pairs. It removes provider tokens and credential programs,
// turns off git's terminal prompt and the credential manager's dialogs,
// and adds AgentGitConfig through GIT_CONFIG_COUNT, after any entries
// already there. Names are compared without case, as Windows does.
func AgentEnv(environ []string) []string {
	out := make([]string, 0, len(environ)+3+2*len(AgentGitConfig))
	count := 0
	for _, kv := range environ {
		name, value, _ := strings.Cut(kv, "=")
		if strings.EqualFold(name, "GIT_CONFIG_COUNT") {
			count, _ = strconv.Atoi(value)
			continue
		}
		if containsFold(agentRemovedVariables, name) {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never")
	for _, c := range AgentGitConfig {
		n := strconv.Itoa(count)
		out = append(out, "GIT_CONFIG_KEY_"+n+"="+c[0], "GIT_CONFIG_VALUE_"+n+"="+c[1])
		count++
	}
	return append(out, "GIT_CONFIG_COUNT="+strconv.Itoa(count))
}

func containsFold(list []string, s string) bool {
	for _, v := range list {
		if strings.EqualFold(v, s) {
			return true
		}
	}
	return false
}
