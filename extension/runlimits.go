package extension

import (
	"context"
	"sync/atomic"
)

// EvolutionLimits holds the per-run quotas for an automatic self-evolution
// pass. Counters are decremented atomically by the executor before each
// matching tool call; once a counter goes negative, the call is rejected.
type EvolutionLimits struct {
	addsLeft    atomic.Int32
	updatesLeft atomic.Int32
}

type evoLimitsKey struct{}

// WithEvolutionLimits decorates ctx for a single self-evolution run. Calls to
// Execute that would write skills are rate-capped; calls that would write
// memory are rejected outright (memory is read-only during evolution).
func WithEvolutionLimits(ctx context.Context, adds, updates int32) context.Context {
	l := &EvolutionLimits{}
	l.addsLeft.Store(adds)
	l.updatesLeft.Store(updates)
	return context.WithValue(ctx, evoLimitsKey{}, l)
}

// EvolutionLimitsFromContext returns the active limits if the context has
// been decorated. The scheduler reads this after the agent run to populate
// the audit summary.
func EvolutionLimitsFromContext(ctx context.Context) (*EvolutionLimits, bool) {
	l, ok := ctx.Value(evoLimitsKey{}).(*EvolutionLimits)
	return l, ok
}

// AddsRemaining reports the current value (may be negative if the LLM tried
// to overshoot — counters are not clamped).
func (l *EvolutionLimits) AddsRemaining() int32 { return l.addsLeft.Load() }

// UpdatesRemaining reports the current value.
func (l *EvolutionLimits) UpdatesRemaining() int32 { return l.updatesLeft.Load() }

// APIToolPolicy is the per-API-user tool allowlist. Default-deny: only tools
// whose name is mapped to true are callable. The HTTP layer constructs this
// once per request and decorates ctx so the executor can enforce centrally.
type APIToolPolicy struct {
	Allowed map[string]bool
}

type apiPolicyKey struct{}

// WithAPIToolPolicy decorates ctx with the calling API user's tool allowlist.
// Tools not in the map are rejected by the executor with a clear error so the
// agent can surface the limitation back to the user.
func WithAPIToolPolicy(ctx context.Context, p *APIToolPolicy) context.Context {
	return context.WithValue(ctx, apiPolicyKey{}, p)
}

// APIToolPolicyFromContext returns the active policy if present.
func APIToolPolicyFromContext(ctx context.Context) (*APIToolPolicy, bool) {
	p, ok := ctx.Value(apiPolicyKey{}).(*APIToolPolicy)
	return p, ok
}

// CronExecution marks the active context as a cron-triggered agent run.
// Cron task descriptions are written by the LLM itself at `cron create` time
// and replayed as a fresh user message when the schedule fires — meaning a
// successful prompt-injection during a normal chat could plant a persistent
// daily instruction. To break that loop, the executor refuses high-blast
// tools (shell/sandbox, memory & skills writes) whenever this marker is set.
type CronExecution struct {
	TaskID int64
}

type cronCtxKey struct{}

// WithCronExecution decorates ctx for a scheduler-triggered agent run.
func WithCronExecution(ctx context.Context, taskID int64) context.Context {
	return context.WithValue(ctx, cronCtxKey{}, &CronExecution{TaskID: taskID})
}

// CronExecutionFromContext returns the marker if the run originated from the
// scheduler. The gate uses the bool form; the struct fields are kept for log
// correlation if a tool wants them later.
func CronExecutionFromContext(ctx context.Context) (*CronExecution, bool) {
	c, ok := ctx.Value(cronCtxKey{}).(*CronExecution)
	return c, ok
}
