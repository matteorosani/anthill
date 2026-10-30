package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/matteorosani/anthill/internal/domain"
	"github.com/matteorosani/anthill/internal/ports"
)

// ToolSession is what one tool token is valid for: one run of an ant, or
// one review or fix agent of a shepherd round (spec ant-tools, "Tool
// tokens are scoped to one run").
type ToolSession struct {
	Role domain.AgentRole
	// Ant and Run are set for a task or follow-up run.
	Ant ports.AntID
	Run ports.RunID
	// PR and Round are set for a review or fix agent.
	PR    domain.Ref
	Round ports.RoundID
}

// ToolDefinition is one tool as the agent sees it in its tool list.
type ToolDefinition struct {
	Name        string
	Description string
	// InputSchema is the JSON Schema of the tool's arguments.
	InputSchema json.RawMessage
}

// ToolResult is what a tool call returns to the agent. A refused or
// failed call has IsError set and Text says why; nothing was executed.
type ToolResult struct {
	// Text is the result as the agent reads it: the JSON of Data, or the
	// reason of a refusal.
	Text string
	// Data is the structured result, nil for a refusal.
	Data any
	// IsError is set when the call was refused or failed.
	IsError bool
	// Notices are messages for the agent, such as a late extension
	// decision, attached to the result of its next call (design D5).
	Notices []string
}

// ErrUnknownToken is returned for a token that was never issued or was
// revoked.
var ErrUnknownToken = errors.New(domain.MessageUnknownToken)

// AntToolsStore is the part of the local store the ant tools use.
type AntToolsStore interface {
	ports.SessionIndex
	ports.Extensions
}

// HeldClaimLister lists the claims this instance holds; Claiming
// implements it. The call checks use its local view, which Claiming
// renews and checks against the provider at every renewal, so no call
// reads the provider for its claim.
type HeldClaimLister interface {
	Held(ctx context.Context) ([]ports.HeldClaim, error)
}

// TrackerRoute finds the tracker of an item, and CodeHostRoute the code
// host of a PR. The daemon builds them from the provider connections.
type (
	TrackerRoute  func(ctx context.Context, item domain.Ref) (ports.TrackerPort, error)
	CodeHostRoute func(ctx context.Context, pr domain.Ref) (ports.CodeHostPort, error)
)

// AntToolsOptions are the settings of the ant tools.
type AntToolsOptions struct {
	// ExtensionWait returns how long request_extension waits for the
	// queen: the configured extension-wait. Nil means
	// domain.DefaultExtensionWait.
	ExtensionWait func() time.Duration
}

// AntTools executes the ant tools: it issues and revokes tool tokens,
// lists each role's tools, and checks and executes every call (spec
// ant-tools). The MCP server that agents talk to only relays to it over
// the control interface (design D5).
//
// Tokens live in memory only: a daemon restart revokes them all, which
// is safe because restart reconciliation ends or resumes every run that
// was active (spec ant-execution, "Restart resilience").
type AntTools struct {
	store    AntToolsStore
	claims   HeldClaimLister
	tracker  TrackerRoute
	codeHost CodeHostRoute
	clock    ports.Clock
	opts     AntToolsOptions

	mu sync.Mutex
	// sessions is keyed by the SHA-256 of the token, so the tokens
	// themselves are not kept.
	sessions map[[sha256.Size]byte]ToolSession
	// locks serialize the calls of one ant.
	locks map[ports.AntID]*sync.Mutex
	// waiters holds the request_extension calls waiting for the queen.
	waiters map[ports.AntID]chan domain.ExtensionRequest
	// notices holds the messages for an ant's next tool result.
	notices map[ports.AntID][]string
}

// NewAntTools returns the ant tools service.
func NewAntTools(store AntToolsStore, claims HeldClaimLister, tracker TrackerRoute, codeHost CodeHostRoute, clock ports.Clock, opts AntToolsOptions) *AntTools {
	return &AntTools{
		store:    store,
		claims:   claims,
		tracker:  tracker,
		codeHost: codeHost,
		clock:    clock,
		opts:     opts,
		sessions: map[[sha256.Size]byte]ToolSession{},
		locks:    map[ports.AntID]*sync.Mutex{},
		waiters:  map[ports.AntID]chan domain.ExtensionRequest{},
		notices:  map[ports.AntID][]string{},
	}
}

// tokenPrefix starts every tool token, so one is easy to recognise.
const tokenPrefix = "ant_"

// Issue returns a new token for s. The token is valid until Revoke.
func (t *AntTools) Issue(s ToolSession) (string, error) {
	switch {
	case !s.Role.Valid():
		return "", fmt.Errorf("agent role %q is not valid", s.Role)
	case s.Role.IsRun() && s.Ant == 0:
		return "", fmt.Errorf("a %s token needs an ant", s.Role.Words())
	case !s.Role.IsRun() && s.PR.Number == 0:
		return "", fmt.Errorf("a %s token needs a PR", s.Role.Words())
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := tokenPrefix + base64.RawURLEncoding.EncodeToString(b)
	t.mu.Lock()
	t.sessions[sha256.Sum256([]byte(token))] = s
	t.mu.Unlock()
	return token, nil
}

// Revoke ends a token, when its run or agent ends. Revoking an unknown
// token does nothing.
func (t *AntTools) Revoke(token string) {
	t.mu.Lock()
	delete(t.sessions, sha256.Sum256([]byte(token)))
	t.mu.Unlock()
}

func (t *AntTools) session(token string) (ToolSession, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s, ok := t.sessions[sha256.Sum256([]byte(token))]
	return s, ok
}

// Tools lists the tools of the token's role, for the agent's tool list
// (spec ant-tools, "Tool sets per agent role"). It returns
// ErrUnknownToken for an unknown or revoked token.
func (t *AntTools) Tools(token string) ([]ToolDefinition, error) {
	s, ok := t.session(token)
	if !ok {
		return nil, ErrUnknownToken
	}
	var defs []ToolDefinition
	for _, name := range domain.ToolsFor(s.Role) {
		spec := toolCatalogue[name]
		input, err := json.Marshal(spec.input)
		if err != nil {
			return nil, fmt.Errorf("schema of %s: %w", name, err)
		}
		defs = append(defs, ToolDefinition{Name: name, Description: spec.description, InputSchema: input})
	}
	return defs, nil
}

// toolCall is one call being executed.
type toolCall struct {
	session ToolSession
	args    json.RawMessage
	// ant is the ant's record, read under its lock, for a run's call.
	ant ports.AntRecord
	// lock and unlock take and give back the ant's lock, which a call
	// that waits gives back meanwhile.
	lock, unlock func()
}

// Call checks and executes one tool call. The checks run in this order:
// the token, the tool against the role, the arguments against the
// schema, then for a run the ant's state, and the claim. A refusal has
// no effect and its reason is the result's Text (spec ant-tools, "Every
// call is checked and returns a reason"). The calls of one ant run one at
// a time.
func (t *AntTools) Call(ctx context.Context, token, tool string, args json.RawMessage) ToolResult {
	s, ok := t.session(token)
	if !ok {
		return errorResult(domain.MessageUnknownToken)
	}
	result := t.call(ctx, s, tool, args)
	if s.Role.IsRun() {
		result.Notices = t.takeNotices(s.Ant)
	}
	return result
}

func (t *AntTools) call(ctx context.Context, s ToolSession, tool string, args json.RawMessage) ToolResult {
	if err := domain.CheckToolRole(s.Role, tool); err != nil {
		return failure(err)
	}
	spec := toolCatalogue[tool]
	if err := spec.input.validate(args); err != nil {
		return errorResult(err.Error())
	}
	c := &toolCall{session: s, args: args, lock: func() {}, unlock: func() {}}
	if s.Role.IsRun() {
		m := t.antLock(s.Ant)
		locked := true
		c.lock = func() { m.Lock(); locked = true }
		c.unlock = func() { m.Unlock(); locked = false }
		c.lock()
		defer func() {
			if locked {
				m.Unlock()
			}
		}()
		rec, err := t.store.Ant(ctx, s.Ant)
		if err != nil {
			return failure(err)
		}
		if err := domain.CheckToolState(rec.State(), tool); err != nil {
			return failure(err)
		}
		if err := t.checkClaim(ctx, rec.Item, domain.ClaimTask); err != nil {
			return failure(err)
		}
		c.ant = rec
	} else if err := t.checkClaim(ctx, s.PR, domain.ClaimReview); err != nil {
		return failure(err)
	}
	if spec.handler == nil {
		return errorResult(tool + " is not implemented yet")
	}
	data, err := spec.handler(t, ctx, c)
	if err != nil {
		return failure(err)
	}
	return dataResult(data)
}

// checkClaim refuses a call whose claim this instance does not hold.
func (t *AntTools) checkClaim(ctx context.Context, ref domain.Ref, kind domain.ClaimKind) error {
	held, err := t.claims.Held(ctx)
	if err != nil {
		return fmt.Errorf("reading the held claims: %w", err)
	}
	for _, h := range held {
		if h.Ref == ref && h.Kind == kind {
			return nil
		}
	}
	return domain.RefuseTool("%s", domain.MessageClaimNotHeld(ref))
}

func (t *AntTools) antLock(id ports.AntID) *sync.Mutex {
	t.mu.Lock()
	defer t.mu.Unlock()
	m, ok := t.locks[id]
	if !ok {
		m = &sync.Mutex{}
		t.locks[id] = m
	}
	return m
}

// notify queues a message for the ant's next tool result.
func (t *AntTools) notify(id ports.AntID, text string) {
	t.mu.Lock()
	t.notices[id] = append(t.notices[id], text)
	t.mu.Unlock()
}

func (t *AntTools) takeNotices(id ports.AntID) []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := t.notices[id]
	delete(t.notices, id)
	return n
}

func errorResult(reason string) ToolResult {
	return ToolResult{Text: reason, IsError: true}
}

// failure turns an error into a result: a refusal's reason as it is, any
// other error as a failed call.
func failure(err error) ToolResult {
	var refused *domain.ToolRefusedError
	if errors.As(err, &refused) {
		return errorResult(refused.Reason)
	}
	return errorResult("the call failed: " + err.Error())
}

func dataResult(data any) ToolResult {
	text, err := json.Marshal(data)
	if err != nil {
		return errorResult("the call failed: encoding the result: " + err.Error())
	}
	return ToolResult{Text: string(text), Data: data}
}

// decodeArgs decodes arguments the schema has checked into v.
func decodeArgs(raw json.RawMessage, v any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	return json.Unmarshal(raw, v)
}

// minutes prints a duration in whole minutes, rounded down.
func minutes(d time.Duration) int {
	return int(d / time.Minute)
}
