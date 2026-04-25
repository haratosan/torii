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
