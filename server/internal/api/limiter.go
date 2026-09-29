package api

import (
	"sync"
	"time"
)

// limiter blocks a key (username|ip) after max failures inside a window.
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	fails  map[string][]time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, fails: map[string][]time.Time{}}
}

func (l *limiter) prune(k string, now time.Time) []time.Time {
	keep := l.fails[k][:0]
	for _, t := range l.fails[k] {
		if now.Sub(t) < l.window {
			keep = append(keep, t)
		}
	}
	if len(keep) == 0 {
		delete(l.fails, k)
	} else {
		l.fails[k] = keep
	}
	return keep
}

func (l *limiter) allow(k string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.prune(k, time.Now())) < l.max
}

func (l *limiter) fail(k string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	l.prune(k, now)
	l.fails[k] = append(l.fails[k], now)
	if len(l.fails) > 10000 { // bound memory under a spray attack
		for key := range l.fails {
			l.prune(key, now)
		}
	}
}

func (l *limiter) reset(k string) {
	l.mu.Lock()
	delete(l.fails, k)
	l.mu.Unlock()
}
