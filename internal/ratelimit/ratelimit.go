// Package ratelimit is an in-memory keyed token bucket. One small server is the PRD's target,
// so process-local state is enough; swap for Redis if the API ever runs on several instances.
package ratelimit

import (
	"sync"
	"time"

	"golang.org/x/time/rate"
)

type Limiter struct {
	mu      sync.Mutex
	every   rate.Limit
	burst   int
	buckets map[string]*bucket
}

type bucket struct {
	lim  *rate.Limiter
	seen time.Time
}

// New allows `burst` events immediately and refills one event every per/burst.
func New(burst int, per time.Duration) *Limiter {
	l := &Limiter{every: rate.Every(per / time.Duration(burst)), burst: burst, buckets: map[string]*bucket{}}
	go l.sweep()
	return l
}

func (l *Limiter) Allow(key string) bool {
	l.mu.Lock()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{lim: rate.NewLimiter(l.every, l.burst)}
		l.buckets[key] = b
	}
	b.seen = time.Now()
	l.mu.Unlock()
	return b.lim.Allow()
}

func (l *Limiter) sweep() {
	for range time.Tick(10 * time.Minute) {
		l.mu.Lock()
		for k, b := range l.buckets {
			if time.Since(b.seen) > time.Hour {
				delete(l.buckets, k)
			}
		}
		l.mu.Unlock()
	}
}
