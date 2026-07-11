// Package middleware provides Fiber middleware for the application.
//
// Currently: an in-memory, per-key token-bucket rate limiter. It has no
// external dependencies, so it's suited to a single-instance deployment;
// for multiple instances behind a load balancer, back it with a shared
// store instead.
package middleware

import (
	"strconv"
	"sync"
	"time"

	"github.com/gofiber/fiber/v3"

	"github.com/dewlonsystems/platform-go/errors"
)

// bucket is a token bucket for a single key (typically an IP address).
type bucket struct {
	mu         sync.Mutex
	tokens     float64
	lastRefill time.Time
}

// Limiter rate-limits requests per key using a token-bucket algorithm:
// each key accrues up to `burst` tokens, refilling at `rate` tokens/sec,
// and every request costs one token.
type Limiter struct {
	rate  float64
	burst float64

	mu      sync.Mutex
	buckets map[string]*bucket

	// KeyFunc extracts the rate-limit key from a request. Defaults to
	// c.IP(); override for e.g. per-user limits using a Locals value.
	KeyFunc func(fiber.Ctx) string
}

// New creates a Limiter allowing `requests` requests per `window`.
func New(requests int, window time.Duration) *Limiter {
	if requests <= 0 || window <= 0 {
		panic("middleware: New requires requests > 0 and window > 0")
	}

	l := &Limiter{
		rate:    float64(requests) / window.Seconds(),
		burst:   float64(requests),
		buckets: make(map[string]*bucket),
		KeyFunc: func(c fiber.Ctx) string { return c.IP() },
	}
	go l.evictLoop()
	return l
}

// Middleware returns a Fiber handler that rejects requests exceeding the
// limit with a 429 and a Retry-After header.
func (l *Limiter) Middleware() fiber.Handler {
	return func(c fiber.Ctx) error {
		key := l.KeyFunc(c)
		allowed, retryAfter := l.allow(key)
		if !allowed {
			c.Set("Retry-After", strconv.Itoa(int(retryAfter.Seconds())+1))
			return errors.WriteFiberJSON(c, errors.NewRateLimited("too many requests, please slow down", nil))
		}
		return c.Next()
	}
}

func (l *Limiter) allow(key string) (bool, time.Duration) {
	l.mu.Lock()
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: l.burst, lastRefill: time.Now()}
		l.buckets[key] = b
	}
	l.mu.Unlock()

	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens = min(l.burst, b.tokens+elapsed*l.rate)
	b.lastRefill = now

	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}

	deficit := 1 - b.tokens
	return false, time.Duration(deficit/l.rate*1000) * time.Millisecond
}

func (l *Limiter) evictLoop() {
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		l.mu.Lock()
		for key, b := range l.buckets {
			b.mu.Lock()
			idle := time.Since(b.lastRefill) > 10*time.Minute
			b.mu.Unlock()
			if idle {
				delete(l.buckets, key)
			}
		}
		l.mu.Unlock()
	}
}
