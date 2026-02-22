package ratelimit

import (
	"sync"
	"time"
)

type bucket struct {
	windowStart time.Time
	count       int
}

type FixedWindowLimiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration

	buckets map[string]bucket
}

func NewFixedWindowLimiter(limit int, window time.Duration) *FixedWindowLimiter {
	if limit <= 0 {
		limit = 1
	}
	if window <= 0 {
		window = time.Minute
	}

	return &FixedWindowLimiter{
		limit:   limit,
		window:  window,
		buckets: make(map[string]bucket),
	}
}

func (l *FixedWindowLimiter) Allow(key string, now time.Time) bool {
	if key == "" {
		key = "anonymous"
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	b, ok := l.buckets[key]
	if !ok || now.Sub(b.windowStart) >= l.window {
		l.buckets[key] = bucket{windowStart: now, count: 1}
		l.cleanup(now)
		return true
	}

	if b.count >= l.limit {
		return false
	}

	b.count++
	l.buckets[key] = b
	return true
}

func (l *FixedWindowLimiter) cleanup(now time.Time) {
	for key, b := range l.buckets {
		if now.Sub(b.windowStart) >= l.window*2 {
			delete(l.buckets, key)
		}
	}
}
