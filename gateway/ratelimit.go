package gateway

import (
	"sync"
	"time"
)

// rateLimiter is a per-key token bucket used to throttle incoming messages
// from individual Telegram users. We use a hand-rolled bucket instead of
// pulling in golang.org/x/time/rate to keep the dependency surface small —
// the algorithm is trivial and we only need coarse pacing.
//
// Defaults (capacity 12, refillEvery 5s) translate to "burst 12 messages,
// then ~1 every 5s" which is generous for a human but stops a runaway
// script from driving Ollama into the ground.
type rateLimiter struct {
	mu          sync.Mutex
	buckets     map[string]*bucket
	capacity    int
	refillEvery time.Duration
}

type bucket struct {
	tokens   int
	lastSeen time.Time
}

func newRateLimiter(capacity int, refillEvery time.Duration) *rateLimiter {
	if capacity <= 0 {
		capacity = 12
	}
	if refillEvery <= 0 {
		refillEvery = 5 * time.Second
	}
	return &rateLimiter{
		buckets:     make(map[string]*bucket),
		capacity:    capacity,
		refillEvery: refillEvery,
	}
}

// allow returns true if the caller may proceed and consumes one token.
// Buckets refill on access — there's no background goroutine, so an idle
// bot won't leak goroutines.
func (r *rateLimiter) allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := time.Now()
	b, ok := r.buckets[key]
	if !ok {
		r.buckets[key] = &bucket{tokens: r.capacity - 1, lastSeen: now}
		return true
	}

	elapsed := now.Sub(b.lastSeen)
	refill := int(elapsed / r.refillEvery)
	if refill > 0 {
		b.tokens += refill
		if b.tokens > r.capacity {
			b.tokens = r.capacity
		}
		b.lastSeen = b.lastSeen.Add(time.Duration(refill) * r.refillEvery)
	}

	if b.tokens <= 0 {
		return false
	}
	b.tokens--
	return true
}
