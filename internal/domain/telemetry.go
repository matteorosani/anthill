package domain

// The names of Anthill's telemetry (spec observability). They are part
// of the contract with whoever reads the telemetry, and docs/
// observability.md lists them; a test checks the two agree.

// Attribute names on spans, metrics and log records, and the resource
// attributes of an agent's own telemetry.
const (
	AttrItem      = "anthill.item"
	AttrPR        = "anthill.pr"
	AttrAntID     = "anthill.ant_id"
	AttrInstance  = "anthill.instance"
	AttrRunner    = "anthill.runner"
	AttrRepo      = "anthill.repository"
	AttrRunID     = "anthill.run_id"
	AttrRunKind   = "anthill.run_kind"
	AttrOutcome   = "anthill.outcome"
	AttrTool      = "anthill.tool"
	AttrProvider  = "anthill.provider"
	AttrCall      = "anthill.call"
	AttrClaim     = "anthill.claim_kind"
	AttrResult    = "anthill.result"
	AttrSeverity  = "anthill.severity"
	AttrRound     = "anthill.round_id"
	AttrEvent     = "anthill.event"
	AttrRoundKind = "anthill.round_kind"
)

// Span names.
const (
	SpanPoll         = "anthill.poll"
	SpanProviderCall = "anthill.provider_call"
	SpanEligibility  = "anthill.eligibility"
	SpanClaim        = "anthill.claim"
	SpanRun          = "anthill.run"
	SpanToolCall     = "anthill.tool_call"
	SpanRound        = "anthill.round"
)

// Metric names.
const (
	MetricActiveRuns         = "anthill.runs.active"
	MetricQueuedAnts         = "anthill.ants.queued"
	MetricRunDuration        = "anthill.run.duration"
	MetricRuns               = "anthill.runs"
	MetricRunTokens          = "anthill.run.tokens"
	MetricRunCost            = "anthill.run.cost"
	MetricProviderLatency    = "anthill.provider.latency"
	MetricProviderErrors     = "anthill.provider.errors"
	MetricRateLimitReset     = "anthill.provider.rate_limit.reset"
	MetricRateLimitRemaining = "anthill.provider.rate_limit.remaining"
	MetricClaims             = "anthill.claims"
	MetricRounds             = "anthill.review.rounds"
	MetricFindings           = "anthill.review.findings"
	MetricPollDuration       = "anthill.poll.duration"
)

// Values of AttrResult on MetricClaims.
const (
	ClaimResultWon  = "won"
	ClaimResultLost = "lost"
)
