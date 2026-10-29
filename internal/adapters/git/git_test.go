package git_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/matteorosani/anthill/internal/adapters/git"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

func TestParseVersion(t *testing.T) {
	tests := []struct {
		in      string
		want    git.Version
		wantErr bool
	}{
		{"git version 2.43.0\n", git.Version{2, 43, 0}, false},
		{"git version 2.45.1.windows.1", git.Version{2, 45, 1}, false},
		{"git version 2.39.3 (Apple Git-145)", git.Version{2, 39, 3}, false},
		{"git version 2.25", git.Version{2, 25, 0}, false},
		{"git version 2.24.4", git.Version{2, 24, 4}, false},
		{"git version 3.0.0-rc1", git.Version{3, 0, 0}, false},
		{"hub version 2.14.2", git.Version{}, true},
		{"", git.Version{}, true},
	}
	for _, tt := range tests {
		got, err := git.ParseVersion(tt.in)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("ParseVersion(%q) = %v, %v; want %v, error %v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestCheckMinimum(t *testing.T) {
	tests := []struct {
		v    git.Version
		want string
	}{
		{git.Version{2, 25, 0}, ""},
		{git.Version{2, 45, 1}, ""},
		{git.Version{3, 0, 0}, ""},
		{git.Version{2, 24, 4}, "git 2.25.0 or later is required, found 2.24.4"},
		{git.Version{1, 9, 5}, "git 2.25.0 or later is required, found 1.9.5"},
	}
	for _, tt := range tests {
		err := git.CheckMinimum(tt.v)
		if got := fmtErr(err); got != tt.want {
			t.Errorf("CheckMinimum(%v) = %q, want %q", tt.v, got, tt.want)
		}
	}
}

func fmtErr(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// Scenario "Repository listed without a clone" (anthill-configuration),
// git side: the configured path must hold a clone of the configured
// remote.
func TestOpenClone(t *testing.T) {
	f := newFixture(t)
	if f.c.Path != f.clone || f.c.Remote != "origin" {
		t.Errorf("OpenClone() = %+v", f.c)
	}
	// A clone whose matching remote is not origin.
	gitIn(t, f.clone, "remote", "rename", "origin", "upstream")
	gitIn(t, f.clone, "remote", "add", "origin", "https://github.com/someone/fork")
	if c, err := f.git.OpenClone(f.ctx, f.clone, webRepo); err != nil || c.Remote != "upstream" {
		t.Errorf("OpenClone() with upstream = %+v, %v", c, err)
	}

	worktree := filepath.Join(f.tmp, "linked")
	gitIn(t, f.clone, "worktree", "add", "--quiet", "--detach", worktree)
	plain := filepath.Join(f.tmp, "plain")
	if err := os.MkdirAll(plain, 0o755); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(f.clone, "docs")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		path string
		repo domain.Repository
	}{
		{"another repository", f.clone, domain.Repository{Provider: domain.GitHub, Org: "acme", Name: "api"}},
		{"missing path", filepath.Join(f.tmp, "missing"), webRepo},
		{"not a git working copy", plain, webRepo},
		{"a folder inside the clone", sub, webRepo},
		{"a linked worktree", worktree, webRepo},
		{"a relative path", "clone", webRepo},
		{"a file", filepath.Join(f.clone, "README.md"), webRepo},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := f.git.OpenClone(f.ctx, tt.path, tt.repo); !errors.Is(err, ports.ErrNoClone) {
				t.Errorf("OpenClone() = %v, want ErrNoClone", err)
			}
		})
	}
}

func TestRemoteDefaultBranch(t *testing.T) {
	f := newFixture(t)
	if got, err := f.git.RemoteDefaultBranch(f.ctx, f.c); err != nil || got != "main" {
		t.Errorf("RemoteDefaultBranch() = %q, %v", got, err)
	}
	// Without origin/HEAD, the remote is asked.
	f.seedCommit("dev.txt", "dev\n", "develop")
	gitIn(t, f.seed, "push", "--quiet", "origin", "main:develop")
	gitIn(t, f.bare, "symbolic-ref", "HEAD", "refs/heads/develop")
	gitIn(t, f.clone, "remote", "set-head", "origin", "--delete")
	if got, err := f.git.RemoteDefaultBranch(f.ctx, f.c); err != nil || got != "develop" {
		t.Errorf("RemoteDefaultBranch() from the remote = %q, %v", got, err)
	}
}

func TestFetchBranchAndCreateBranch(t *testing.T) {
	f := newFixture(t)
	tip := f.seedCommit("fix.txt", "fix\n", "release fix")
	gitIn(t, f.seed, "push", "--quiet", "origin", "main:release/2.4")
	if _, err := f.git.Branch(f.ctx, f.c, "release/2.4", ""); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("Branch() before fetching = %v", err)
	}
	if err := f.git.CreateBranch(f.ctx, f.c, "ant/x", "release/2.4"); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("CreateBranch() from an unfetched base = %v", err)
	}
	if err := f.git.Fetch(f.ctx, f.c); err != nil {
		t.Fatal(err)
	}
	info, err := f.git.Branch(f.ctx, f.c, "release/2.4", "")
	if err != nil || !info.Remote || info.Local || info.Commit != tip {
		t.Errorf("Branch() = %+v, %v", info, err)
	}
	if err := f.git.CreateBranch(f.ctx, f.c, "ant/x", "release/2.4"); err != nil {
		t.Fatal(err)
	}
	if got := gitIn(t, f.clone, "rev-parse", "ant/x"); got != tip {
		t.Errorf("ant/x = %s, want the fetched release tip %s", got, tip)
	}
	if got := gitIn(t, f.clone, "symbolic-ref", "--short", "HEAD"); got != "main" {
		t.Errorf("the clone switched to %s", got)
	}
	if err := f.git.CreateBranch(f.ctx, f.c, "ant/x", "release/2.4"); err == nil {
		t.Error("created a branch twice")
	}

	// A remote branch deleted on the remote is pruned by the next fetch.
	gitIn(t, f.seed, "push", "--quiet", "origin", "--delete", "release/2.4")
	if err := f.git.Fetch(f.ctx, f.c); err != nil {
		t.Fatal(err)
	}
	if _, err := f.git.Branch(f.ctx, f.c, "release/2.4", ""); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("Branch() after the remote deleted it = %v", err)
	}
}

func TestBranchOwners(t *testing.T) {
	f := newFixture(t)
	gitIn(t, f.seed, "checkout", "--quiet", "-b", "ant/gh-12-x")
	f.seedCommit("a.txt", "a\n", "bob's work\n\nAnthill-Item: gh-12\nAnthill-Agent: codex\nAnthill-Instance: bob@desktop")
	f.seedCommit("b.txt", "b\n", "human fix on top")
	f.seedCommit("c.txt", "c\n", "older\n\nanthill-item: gh-12\nANTHILL-INSTANCE: bob@desktop")
	f.seedCommit("d.txt", "d\n", "item only\n\nAnthill-Item: gh-13")
	gitIn(t, f.seed, "push", "--quiet", "origin", "ant/gh-12-x")
	if err := f.git.Fetch(f.ctx, f.c); err != nil {
		t.Fatal(err)
	}
	info, err := f.git.Branch(f.ctx, f.c, "ant/gh-12-x", "main")
	bob := domain.BranchOwner{Item: "gh-12", Instance: "bob@desktop"}
	if err != nil || !slices.Equal(info.Owners, []domain.BranchOwner{bob}) {
		t.Errorf("Branch() = %+v, %v", info, err)
	}
	if info, _ := f.git.Branch(f.ctx, f.c, "main", "main"); len(info.Owners) != 0 {
		t.Errorf("owners of main = %v", info.Owners)
	}
}

func TestDeleteBranch(t *testing.T) {
	f := newFixture(t)
	if err := f.git.DeleteBranch(f.ctx, f.c, "none", false); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("deleting a missing local branch: %v", err)
	}
	if err := f.git.DeleteBranch(f.ctx, f.c, "none", true); !errors.Is(err, ports.ErrNotFound) {
		t.Errorf("deleting a missing remote branch: %v", err)
	}
	gitIn(t, f.seed, "push", "--quiet", "origin", "main:old")
	_ = f.git.Fetch(f.ctx, f.c)
	if err := f.git.DeleteBranch(f.ctx, f.c, "old", true); err != nil || f.remoteTip("old") != "" {
		t.Errorf("DeleteBranch(remote) = %v, tip %q", err, f.remoteTip("old"))
	}
}

func TestCanceledContext(t *testing.T) {
	f := newFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.git.Fetch(ctx, f.c); !errors.Is(err, context.Canceled) {
		t.Errorf("Fetch() with a canceled context = %v", err)
	}
	// A check cut short does not stick.
	g := git.New(nil)
	if _, err := g.OpenClone(ctx, f.clone, webRepo); !errors.Is(err, context.Canceled) {
		t.Errorf("OpenClone() with a canceled context = %v", err)
	}
	if _, err := g.OpenClone(context.Background(), f.clone, webRepo); err != nil {
		t.Errorf("OpenClone() afterwards = %v", err)
	}
}

func TestErrorRedactsSecrets(t *testing.T) {
	f := newFixture(t)
	token := "ghp_" + strings.Repeat("a1B2", 9)
	_, err := f.git.OpenClone(f.ctx, f.clone, webRepo)
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, f.clone, "remote", "add", "leak", "https://x:"+token+"@127.0.0.1:1/none.git")
	err = f.git.DeleteBranch(f.ctx, ports.Clone{Path: f.clone, Remote: "leak"}, "x", true)
	if err == nil || strings.Contains(err.Error(), token) {
		t.Errorf("error = %v", err)
	}
}
