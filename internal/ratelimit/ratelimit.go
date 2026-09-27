// Package ratelimit is a keyed token bucket used by the ping ingress, the
// API and the login form.
package ratelimit

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// Limiter keeps one token bucket per key with lazy expiry.
type Limiter struct {
	mu      sync.Mutex
	perMin  int
	burst   int
	buckets map[string]*bucket
	ops     int
	// Now is the clock; tests replace it.
	Now func() time.Time
}

type bucket struct {
	lim  *rate.Limiter
	seen time.Time
}

// New allows perMin events per minute with the given burst.
func New(perMin, burst int) *Limiter {
	if burst < 1 {
		burst = 1
	}
	return &Limiter{perMin: perMin, burst: burst, buckets: map[string]*bucket{}, Now: time.Now}
}

// Allow reports whether one event for key may pass now, and if not, how
// long until the next token.
func (l *Limiter) Allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.Now()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{lim: rate.NewLimiter(rate.Every(time.Minute/time.Duration(l.perMin)), l.burst)}
		l.buckets[key] = b
	}
	b.seen = now
	l.ops++
	if l.ops%1000 == 0 {
		l.sweep(now)
	}
	res := b.lim.ReserveN(now, 1)
	delay := res.DelayFrom(now)
	if delay > 0 {
		res.CancelAt(now)
		return false, delay
	}
	return true, 0
}

// sweep drops buckets idle for ten minutes; they refill to full anyway.
func (l *Limiter) sweep(now time.Time) {
	for k, b := range l.buckets {
		if now.Sub(b.seen) > 10*time.Minute {
			delete(l.buckets, k)
		}
	}
}
