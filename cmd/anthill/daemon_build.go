package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/matteorosani/anthill/internal/app"
	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// instanceTimeout bounds the read of the login for the shepherd's
// instance id, which has no context of its own.
const instanceTimeout = 30 * time.Second

// services are the application services the daemon runs, wired over its
// ports by buildDaemon.
type services struct {
	routes *routes
	// The decorated seams, kept as named values so that observability
	// can wrap them: the provider connections the scheduler polls, the
	// recorder of run ends, the run activity and the round runner.
	connections []app.Connection
	runEnds     app.RunEnds
	activity    *app.ActivityHub
	rounds      *app.ShepherdRounds

	claiming     *app.Claiming
	eligibility  *app.Eligibility
	tools        *app.AntTools
	fixPass      *app.FixPass
	conversation *app.Conversation
	supervisor   *app.AntSupervisor
	shepherd     *app.Shepherd
	reactor      *app.ChangeReactor
	closure      *app.PRClosure
	recovery     *app.SessionRecovery
	console      *app.Console
	scheduler    *app.Scheduler
	// consoles holds, per provider connection, a console whose proposal
	// list covers only that connection's repositories, for the
	// re-evaluation after its polls.
	consoles map[string]*app.Console
}

// buildOptions are what buildDaemon takes from the machine besides the
// ports.
type buildOptions struct {
	config   *app.Configuration
	clock    ports.Clock
	notifier ports.Notifier
	logger   *slog.Logger
	// userHome is the user's home directory: the default worktree root
	// is under it, and posted text writes it as ~.
	userHome string
	// environ is Anthill's own environment, from which each agent's is
	// made, and mcpCommand the command line of the ant tools server.
	environ    []string
	mcpCommand func(token string) []string
}

// buildDaemon is the composition root: it hands the ports to the
// application services and connects the services with each other. The
// services that need each other in both directions, such as the ant
// tools and the ant supervisor, are joined through late, which forwards
// to services made after them.
func buildDaemon(p daemonPorts, o buildOptions) *services {
	config, clock, notifier := o.config, o.clock, o.notifier
	global := func() domain.Effective {
		if snap := config.Current(); snap != nil {
			return snap.Global
		}
		return domain.Defaults()
	}
	errs := func(err error) {
		if err != nil {
			o.logger.Error("background work failed", "error", err)
		}
	}
	root := domain.WorktreeRoot(global().WorktreeRoot, o.userHome)

	s := &services{consoles: map[string]*app.Console{}}
	late := &late{}
	r := newRoutes(config, p.Git, p.Machine, p.Connections)
	s.routes = r
	for _, c := range p.Connections {
		s.connections = append(s.connections, app.Connection{Name: c.Name, Tracker: c.Provider, CodeHost: c.Provider})
	}

	s.claiming = app.NewClaiming(r.claim, p.Store, clock, notifier, late, app.ClaimingOptions{Lease: global().Lease})
	s.eligibility = app.NewEligibility(r.tracker, s.claiming, r.target, p.Store)
	workspace := app.NewWorkspace(p.Git, root)
	s.activity = app.NewActivityHub()
	usage := app.NewUsageRecorder(p.Store)
	s.runEnds = s.activity.RunEnds(usage)

	s.tools = app.NewAntTools(app.AntToolsDeps{
		Store: p.Store, Claims: s.claiming, Tracker: r.tracker, CodeHost: r.codeHost, Workspace: r.workspace,
		Git: p.Git, Clock: clock, Notifier: notifier, Executor: p.Executor,
		Runs: late, Finishes: late, Reviews: late, Fixes: late,
	}, app.AntToolsOptions{
		ExtensionWait: func() time.Duration { return global().ExtensionWait },
		Redactor: func() *domain.Redactor {
			if snap := config.Current(); snap != nil {
				return snap.Redactor
			}
			return nil
		},
		Home: o.userHome,
	})
	s.fixPass = app.NewFixPass(app.FixPassDeps{
		Git: p.Git, Executor: p.Executor, Tokens: s.tools, Verifier: p.Verifier, CodeHost: r.codeHost, Clock: clock,
	}, app.FixPassOptions{Root: root, MCPCommand: o.mcpCommand, Environ: o.environ})
	s.rounds = app.NewShepherdRounds(app.ShepherdRoundsDeps{
		Route: r.workspace, CodeHost: r.codeHost, Git: p.Git, Executor: p.Executor, Tokens: s.tools, Clock: clock,
	}, app.ShepherdRoundsOptions{Root: root, MCPCommand: o.mcpCommand, Environ: o.environ})

	s.conversation = app.NewConversation(app.ConversationDeps{
		Store: p.Store, Tracker: r.tracker, CodeHost: r.codeHost, Claims: s.claiming,
		Ants: late, Reviews: late, Fixes: late, Queen: r.queen,
		Clock: clock, Notifier: notifier,
		Staleness: func() time.Duration { return global().QuestionStaleness },
	})
	runners := app.NewRunners(p.Runners)
	s.supervisor = app.NewAntSupervisor(app.AntSupervisorDeps{
		Store: p.Store, Claims: s.claiming, Eligibility: s.eligibility,
		Workspace: workspace, Route: r.workspace, Runners: runners, Executor: p.Executor,
		Tokens: s.tools,
		Briefs: &instanceBriefs{deps: app.BriefDeps{
			Tracker: r.tracker, CodeHost: r.codeHost, Workspace: r.workspace, Git: p.Git,
			Files: p.Files, Runs: p.Store, Clock: clock,
		}, instance: r.instance},
		Answers: s.conversation.Answers(), Clock: clock, Notifier: notifier,
		Capacity: func() int { return global().Capacity },
		Runs:     s.runEnds,
		Tracker:  r.tracker, CodeHost: r.codeHost, Git: p.Git,
		Activity: s.activity, Messages: s.tools, Extensions: s.tools,
		Holds: app.NewItemHolds(r.tracker, r.workspace),
	}, app.AntSupervisorOptions{MCPCommand: o.mcpCommand, Environ: o.environ, Errors: errs})
	s.tools.WatchBudgets(s.supervisor)

	s.shepherd = app.NewShepherd(app.ShepherdDeps{
		Store: p.Store, Claims: s.claiming, Route: r.workspace, CodeHost: r.codeHost,
		WriteAccess: r.writeAccess,
		Instance: func(repo domain.Repository) string {
			ctx, cancel := context.WithTimeout(context.Background(), instanceTimeout)
			defer cancel()
			id, err := r.instance(ctx, repo)
			if err != nil {
				o.logger.Warn("instance id unknown", "repository", repo.String(), "error", err)
			}
			return id
		},
		Runners: runners, Rounds: s.rounds, RoundEnds: usage, Fixes: s.fixPass,
		Acks: s.conversation, Ants: s.supervisor, Clock: clock, Errors: errs,
	})
	late.supervisor, late.shepherd, late.rounds, late.fixPass = s.supervisor, s.shepherd, s.rounds, s.fixPass

	s.reactor = app.NewChangeReactor(app.ChangeReactorDeps{
		Store: p.Store, Claims: s.claiming, Ants: s.supervisor,
		Tracker: r.tracker, CodeHost: r.codeHost, Route: r.workspace,
		Classifier:   app.NewClassifier(p.Executor, clock, o.environ),
		Conversation: s.conversation, Findings: s.shepherd,
	})
	s.closure = app.NewPRClosure(app.PRClosureDeps{
		Store: p.Store, CodeHost: r.codeHost, Ants: s.supervisor, Shepherd: s.shepherd,
	})
	s.recovery = app.NewSessionRecovery(app.SessionRecoveryDeps{
		Store: p.Store, Claims: s.claiming, Git: p.Git, Workspace: workspace,
		Sources: r.recoverySources, Clock: clock, Interrupted: s.supervisor,
	})

	consoleDeps := app.ConsoleDeps{
		Store: p.Store, Ants: s.supervisor, Eligibility: s.eligibility,
		Candidates: app.TrackerCandidates(config, r.trackerOf),
		Tracker:    r.tracker, Answers: s.conversation, Activity: s.activity,
		Workspace: workspace, Route: r.workspace, CodeHost: r.codeHost,
		Repositories: r.repositories, Claims: s.claiming, Rounds: s.shepherd, Clock: clock,
	}
	s.console = app.NewConsole(consoleDeps)
	for _, c := range p.Connections {
		deps := consoleDeps
		deps.Candidates = app.ConnectionCandidates(config, c.Name, r.trackerOf)
		s.consoles[c.Name] = app.NewConsole(deps)
	}

	ingestion := app.NewIngestion(p.Store, config, clock)
	s.scheduler = app.NewScheduler(ingestion, config, clock, notifier, s.connections...)
	return s
}

// instanceBriefs builds the briefs of an ant with this instance's id on
// the provider of the ant's item, which the summary brief needs to keep
// only this instance's comments. The id is read when a brief is built,
// so a daemon started offline still starts.
type instanceBriefs struct {
	deps     app.BriefDeps
	instance func(ctx context.Context, repo domain.Repository) (string, error)
}

var _ app.Briefs = (*instanceBriefs)(nil)

func (b *instanceBriefs) builder(ctx context.Context, ant ports.AntRecord) (*app.BriefBuilder, error) {
	id, err := b.instance(ctx, ant.Item.Repo)
	if err != nil {
		return nil, err
	}
	deps := b.deps
	deps.Instance = id
	return app.NewBriefBuilder(deps), nil
}

func (b *instanceBriefs) Task(ctx context.Context, ant ports.AntRecord, answer *domain.ChangeEvent, changes []domain.AntChange) (string, error) {
	bb, err := b.builder(ctx, ant)
	if err != nil {
		return "", err
	}
	return bb.Task(ctx, ant, answer, changes)
}

func (b *instanceBriefs) Summary(ctx context.Context, ant ports.AntRecord, answer *domain.ChangeEvent, changes []domain.AntChange) (string, error) {
	bb, err := b.builder(ctx, ant)
	if err != nil {
		return "", err
	}
	return bb.Summary(ctx, ant, answer, changes)
}

// Resume needs no instance id.
func (b *instanceBriefs) Resume(ctx context.Context, ant ports.AntRecord, answer *domain.ChangeEvent, changes []domain.AntChange) (string, error) {
	return app.NewBriefBuilder(b.deps).Resume(ctx, ant, answer, changes)
}

// userHomeOf returns the user's home directory from <anthill-home>.
func userHomeOf(anthillHome string) string {
	return filepath.Dir(anthillHome)
}
