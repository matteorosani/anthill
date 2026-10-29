package domain_test

import (
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/domain"
)

var adoCode = domain.Repository{Provider: domain.AzureDevOps, Org: "contoso", Project: "shop", Name: "web"}

func TestTrailers(t *testing.T) {
	gh12 := domain.Ref{Repo: ghRepo, Kind: domain.KindItem, Number: 12}
	tr := domain.NewTrailers(gh12, domain.RunnerClaudeCode, "mrosani@LAPTOP-42")
	if want := (domain.Trailers{Item: "gh-12", Agent: "claude-code", Instance: "mrosani@LAPTOP-42"}); tr != want {
		t.Errorf("NewTrailers() = %+v, want %+v", tr, want)
	}
	if err := tr.Validate(); err != nil {
		t.Errorf("Validate() = %v", err)
	}
	if tr.Owner() != (domain.BranchOwner{Item: "gh-12", Instance: "mrosani@LAPTOP-42"}) {
		t.Errorf("Owner() = %+v", tr.Owner())
	}
	tests := []struct {
		name string
		tr   domain.Trailers
		want string
	}{
		{"empty item", domain.Trailers{Agent: "codex", Instance: "a@b"}, "trailer Anthill-Item is empty"},
		{"newline", domain.Trailers{Item: "gh-1", Agent: "codex", Instance: "a@b\nAnthill-Item: gh-2"}, "Anthill-Instance"},
		{"surrounding space", domain.Trailers{Item: " gh-1", Agent: "codex", Instance: "a@b"}, "Anthill-Item"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := tt.tr.Validate(); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("Validate() = %v, want it to mention %q", err, tt.want)
			}
		})
	}
}

// Scenario "Name already taken remotely by someone else".
func TestChooseBranchName(t *testing.T) {
	me := domain.BranchOwner{Item: "gh-12", Instance: "alice@laptop"}
	expired := domain.BranchOwner{Item: "gh-12", Instance: "bob@desktop"}
	other := domain.BranchOwner{Item: "gh-13", Instance: "alice@laptop"}
	const name = "ant/gh-12-fix"
	tests := []struct {
		name      string
		branches  map[string]domain.BranchCandidate
		want      string
		wantReuse bool
	}{
		{"free", nil, name, false},
		{"taken by an expired claim's instance", map[string]domain.BranchCandidate{
			name: {Exists: true, Owners: []domain.BranchOwner{expired}},
		}, name + "-2", false},
		{"taken by a human, no trailers", map[string]domain.BranchCandidate{
			name: {Exists: true},
		}, name + "-2", false},
		{"same instance, other item", map[string]domain.BranchCandidate{
			name: {Exists: true, Owners: []domain.BranchOwner{other}},
		}, name + "-2", false},
		{"ours, with a human commit on top", map[string]domain.BranchCandidate{
			name: {Exists: true, Owners: []domain.BranchOwner{expired, me}},
		}, name, true},
		{"suffix also taken", map[string]domain.BranchCandidate{
			name:        {Exists: true},
			name + "-2": {Exists: true, Owners: []domain.BranchOwner{expired}},
		}, name + "-3", false},
		{"suffix is ours", map[string]domain.BranchCandidate{
			name:        {Exists: true},
			name + "-2": {Exists: true, Owners: []domain.BranchOwner{me}},
		}, name + "-2", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var asked []string
			got, reuse, err := domain.ChooseBranchName(name, me, func(n string) (domain.BranchCandidate, error) {
				asked = append(asked, n)
				return tt.branches[n], nil
			})
			if err != nil || got != tt.want || reuse != tt.wantReuse {
				t.Errorf("ChooseBranchName() = %q, %v, %v; want %q, %v", got, reuse, err, tt.want, tt.wantReuse)
			}
			if asked[len(asked)-1] != got {
				t.Errorf("looked up %v after choosing %q", asked, got)
			}
		})
	}
}

func TestChooseBranchNameErrors(t *testing.T) {
	boom := errors.New("git failed")
	if _, _, err := domain.ChooseBranchName("ant/x", domain.BranchOwner{}, func(string) (domain.BranchCandidate, error) {
		return domain.BranchCandidate{}, boom
	}); !errors.Is(err, boom) {
		t.Errorf("lookup error: %v", err)
	}
	calls := 0
	_, _, err := domain.ChooseBranchName("ant/x", domain.BranchOwner{Item: "gh-1", Instance: "a"}, func(string) (domain.BranchCandidate, error) {
		calls++
		return domain.BranchCandidate{Exists: true}, nil
	})
	if err == nil || calls != domain.MaxBranchSuffix {
		t.Errorf("all taken: %v after %d lookups", err, calls)
	}
}

func TestResolveBase(t *testing.T) {
	tests := []struct{ configured, def, want string }{
		{"release/2.4", "main", "release/2.4"},
		{"", "main", "main"},
		{"develop", "", "develop"},
	}
	for _, tt := range tests {
		if got := domain.ResolveBase(tt.configured, tt.def); got != tt.want {
			t.Errorf("ResolveBase(%q, %q) = %q, want %q", tt.configured, tt.def, got, tt.want)
		}
	}
	// The label wins over the repository setting, which wins over the
	// remote default: Effective.ForItem already applies the label.
	e := domain.ConfigLayers{RepositoryFile: &domain.SettingValues{BaseBranch: ptr("develop")}}.Resolve()
	if got := domain.ResolveBase(e.BaseBranch, "main"); got != "develop" {
		t.Errorf("repository setting: %q", got)
	}
	labelled, _ := e.ForItem([]string{"base:release/2.4"})
	if got := domain.ResolveBase(labelled.BaseBranch, "main"); got != "release/2.4" {
		t.Errorf("base: label: %q", got)
	}
	if got := domain.ResolveBase(domain.Defaults().BaseBranch, "main"); got != "main" {
		t.Errorf("remote default: %q", got)
	}
}

// Scenario "Base branch missing": the message the queen sees.
func TestBaseBranchMissingError(t *testing.T) {
	var err error = &domain.BaseBranchMissingError{Branch: "release/2.4"}
	if err.Error() != "base branch release/2.4 not found" || !errors.Is(err, domain.ErrBaseBranchMissing) {
		t.Errorf("error = %q", err)
	}
}

func TestWorktreePath(t *testing.T) {
	home := filepath.Join(string(filepath.Separator)+"home", "me")
	root := domain.WorktreeRoot("", home)
	if want := filepath.Join(home, ".anthill", "worktrees"); root != want {
		t.Errorf("WorktreeRoot() = %q, want %q", root, want)
	}
	custom := filepath.Join(home, "wt")
	if got := domain.WorktreeRoot(custom+string(filepath.Separator), home); got != custom {
		t.Errorf("WorktreeRoot(custom) = %q", got)
	}
	tests := []struct {
		repo   domain.Repository
		branch string
		want   string
	}{
		{ghRepo, "ant/gh-12-fix-login", filepath.Join(root, "gh-matteorosani-anthill", "ant", "gh-12-fix-login")},
		{adoCode, "ant/ado-7-export", filepath.Join(root, "ado-contoso-shop-web", "ant", "ado-7-export")},
	}
	for _, tt := range tests {
		if got := domain.WorktreePath(root, tt.repo, tt.branch); got != tt.want {
			t.Errorf("WorktreePath(%s) = %q, want %q", tt.branch, got, tt.want)
		}
	}
}

// The branching-policy scenarios of "Push restrictions" and "History
// rewriting" that the policy decides.
func TestCheckPush(t *testing.T) {
	ant := domain.PushRequest{
		Kind: domain.PushAnt, Branch: "ant/gh-12-fix", Allowed: "ant/gh-12-fix",
		Base: "develop", Default: "main", Protected: []string{"release/*"},
	}
	with := func(f func(*domain.PushRequest)) domain.PushRequest {
		r := ant
		f(&r)
		return r
	}
	autofix := domain.PushRequest{Kind: domain.PushAutoFix, Branch: "feature/login", Allowed: "feature/login", OptedIn: true, Base: "main", Default: "main"}
	tests := []struct {
		name string
		req  domain.PushRequest
		want string // "" means allowed
	}{
		{"ant pushes its own branch", ant, ""},
		{"ant attempts to push to main", with(func(r *domain.PushRequest) { r.Branch = "main" }), "push to main is not allowed"},
		{"base branch", with(func(r *domain.PushRequest) { r.Branch = "develop" }), "push to develop is not allowed"},
		{"protected pattern", with(func(r *domain.PushRequest) { r.Branch = "release/2.4" }), "push to release/2.4 is not allowed"},
		{"protected even when allowed", with(func(r *domain.PushRequest) { r.Branch, r.Allowed = "main", "main" }), "push to main is not allowed"},
		{"another branch", with(func(r *domain.PushRequest) { r.Branch = "feature/x" }), "push to feature/x is not allowed: an ant pushes only its own branch ant/gh-12-fix"},
		{"another ant's branch", with(func(r *domain.PushRequest) { r.Branch = "ant/gh-13-other" }), "push to ant/gh-13-other is not allowed: an ant pushes only its own branch ant/gh-12-fix"},
		{"detached head", with(func(r *domain.PushRequest) { r.Branch = "" }), "push needs a checked-out branch, and HEAD is detached"},
		{"force-push", with(func(r *domain.PushRequest) { r.Force = true }), domain.MessageForcePush},
		{"opted-in human PR", autofix, ""},
		{"human PR not opted in", func() domain.PushRequest { r := autofix; r.OptedIn = false; return r }(), "push to feature/login is not allowed: the PR has not opted in to auto-fix"},
		{"human PR from main", func() domain.PushRequest { r := autofix; r.Branch, r.Allowed = "main", "main"; return r }(), "push to main is not allowed"},
		{"auto-fix to another branch", func() domain.PushRequest { r := autofix; r.Branch = "feature/other"; return r }(), "push to feature/other is not allowed: an auto-fix pushes only the PR branch feature/login"},
		{"unknown kind", domain.PushRequest{Branch: "x", Allowed: "x"}, "push to x is not allowed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := domain.CheckPush(tt.req)
			if tt.want == "" {
				if err != nil {
					t.Errorf("CheckPush() = %v, want allowed", err)
				}
				return
			}
			if err == nil || err.Error() != tt.want || !errors.Is(err, domain.ErrPushRefused) {
				t.Errorf("CheckPush() = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestIsProtected(t *testing.T) {
	patterns := []string{"main", "release/*", "hotfix-*"}
	for branch, want := range map[string]bool{
		"main": true, "release/2.4": true, "hotfix-1": true,
		"release/2.4/x": false, "ant/gh-1": false, "mainline": false,
	} {
		if got := domain.IsProtected(branch, patterns); got != want {
			t.Errorf("IsProtected(%q) = %v", branch, got)
		}
	}
}

// Scenarios "PR merged" and "Unpushed work" of "Worktree cleanup".
func TestPlanCleanup(t *testing.T) {
	merged := domain.CleanupInput{PR: domain.PRMerged, Worktree: true, RemoteBranch: true}
	tests := []struct {
		name string
		in   domain.CleanupInput
		want domain.CleanupPlan
	}{
		{"PR merged", merged, domain.CleanupPlan{RemoveWorktree: true, DeleteLocalBranch: true, DeleteRemoteBranch: true}},
		{"PR merged, provider deleted the branch", func() domain.CleanupInput { in := merged; in.RemoteBranch = false; return in }(),
			domain.CleanupPlan{RemoveWorktree: true, DeleteLocalBranch: true}},
		{"PR closed keeps the remote branch", domain.CleanupInput{PR: domain.PRClosed, Worktree: true, RemoteBranch: true},
			domain.CleanupPlan{RemoveWorktree: true, DeleteLocalBranch: true}},
		{"PR open", domain.CleanupInput{PR: domain.PROpen, Worktree: true, RemoteBranch: true, Requested: true}, domain.CleanupPlan{}},
		{"abandoned without a request", domain.CleanupInput{Abandoned: true, Worktree: true}, domain.CleanupPlan{}},
		{"abandoned and requested", domain.CleanupInput{Abandoned: true, Requested: true, Worktree: true, RemoteBranch: true},
			domain.CleanupPlan{RemoveWorktree: true, DeleteLocalBranch: true}},
		{"unpushed work", domain.CleanupInput{Abandoned: true, Requested: true, Worktree: true, UnpushedCommits: 2},
			domain.CleanupPlan{NeedsConfirmation: true}},
		{"unpushed work confirmed", domain.CleanupInput{Abandoned: true, Requested: true, Worktree: true, UnpushedCommits: 2, Confirmed: true},
			domain.CleanupPlan{RemoveWorktree: true, DeleteLocalBranch: true}},
		{"uncommitted changes", func() domain.CleanupInput { in := merged; in.Uncommitted = true; return in }(),
			domain.CleanupPlan{NeedsConfirmation: true}},
		{"uncommitted changes confirmed", func() domain.CleanupInput { in := merged; in.Uncommitted, in.Confirmed = true, true; return in }(),
			domain.CleanupPlan{RemoveWorktree: true, Force: true, DeleteLocalBranch: true, DeleteRemoteBranch: true}},
		{"worktree gone, unpushed commits on the branch", domain.CleanupInput{PR: domain.PRClosed, UnpushedCommits: 1},
			domain.CleanupPlan{NeedsConfirmation: true}},
		{"worktree gone", domain.CleanupInput{PR: domain.PRClosed}, domain.CleanupPlan{DeleteLocalBranch: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := domain.PlanCleanup(tt.in); got != tt.want {
				t.Errorf("PlanCleanup() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestAgentEnv(t *testing.T) {
	env := domain.AgentEnv([]string{
		"PATH=/usr/bin",
		"HOME=/home/me",
		"GH_TOKEN=ghp_secret",
		"github_token=lowercase on windows",
		"AZURE_DEVOPS_EXT_PAT=pat",
		"SYSTEM_ACCESSTOKEN=ado",
		"GIT_ASKPASS=/usr/bin/askpass",
		"SSH_ASKPASS=/usr/bin/ssh-askpass",
		"SSH_AUTH_SOCK=/tmp/agent.sock",
		"GIT_TERMINAL_PROMPT=1",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=user.name",
		"GIT_CONFIG_VALUE_0=Me",
	})
	want := []string{
		"PATH=/usr/bin",
		"HOME=/home/me",
		"GIT_CONFIG_KEY_0=user.name",
		"GIT_CONFIG_VALUE_0=Me",
		"GIT_TERMINAL_PROMPT=0",
		"GCM_INTERACTIVE=never",
		"GIT_CONFIG_KEY_1=credential.helper",
		"GIT_CONFIG_VALUE_1=",
		"GIT_CONFIG_KEY_2=core.askPass",
		"GIT_CONFIG_VALUE_2=",
		"GIT_CONFIG_KEY_3=core.sshCommand",
		"GIT_CONFIG_VALUE_3=" + domain.SSHRefusal,
		"GIT_CONFIG_COUNT=4",
	}
	if !slices.Equal(env, want) {
		t.Errorf("AgentEnv() =\n%s\nwant\n%s", strings.Join(env, "\n"), strings.Join(want, "\n"))
	}
	if env := domain.AgentEnv(nil); env[len(env)-1] != "GIT_CONFIG_COUNT=3" {
		t.Errorf("AgentEnv(nil) = %v", env)
	}
}

func TestMatchesRemoteURL(t *testing.T) {
	gh := domain.Repository{Provider: domain.GitHub, Org: "acme", Name: "web"}
	tests := []struct {
		repo domain.Repository
		url  string
		want bool
	}{
		{gh, "https://github.com/acme/web.git", true},
		{gh, "https://github.com/acme/web", true},
		{gh, "https://github.com/Acme/Web/", true},
		{gh, "https://user@github.com/acme/web.git", true},
		{gh, "git@github.com:acme/web.git", true},
		{gh, "ssh://git@github.com/acme/web.git", true},
		{gh, "ssh://git@github.com:22/acme/web", true},
		{gh, "https://github.com/acme/api.git", false},
		{gh, "https://github.com/other/web.git", false},
		{gh, "https://gitlab.com/acme/web.git", false},
		{gh, "/srv/git/web.git", false},
		{gh, "C:/src/web", false},
		{gh, "", false},
		{adoCode, "https://dev.azure.com/contoso/shop/_git/web", true},
		{adoCode, "https://contoso@dev.azure.com/contoso/shop/_git/web", true},
		{adoCode, "git@ssh.dev.azure.com:v3/contoso/shop/web", true},
		{adoCode, "ssh://git@ssh.dev.azure.com/v3/contoso/shop/web", true},
		{adoCode, "https://contoso.visualstudio.com/shop/_git/web", true},
		{adoCode, "https://contoso.visualstudio.com/DefaultCollection/shop/_git/web", true},
		{adoCode, "contoso@vs-ssh.visualstudio.com:v3/contoso/shop/web", true},
		{adoCode, "https://dev.azure.com/contoso/shop/_git/api", false},
		{adoCode, "https://other.visualstudio.com/shop/_git/web", false},
		{domain.Repository{Provider: domain.AzureDevOps, Org: "contoso", Project: "My Shop", Name: "web"},
			"https://dev.azure.com/contoso/My%20Shop/_git/web", true},
		{domain.Repository{Provider: domain.AzureDevOps, Org: "contoso", Project: "My Shop", Name: "web"},
			"git@ssh.dev.azure.com:v3/contoso/My%20Shop/web", true},
	}
	for _, tt := range tests {
		if got := tt.repo.MatchesRemoteURL(tt.url); got != tt.want {
			t.Errorf("%s.MatchesRemoteURL(%q) = %v, want %v", tt.repo, tt.url, got, tt.want)
		}
	}
}
