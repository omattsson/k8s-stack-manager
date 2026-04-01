package handlers

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/metric"
)

// RateLimiter implements a simple rate limiting middleware
type RateLimiter struct {
	sync.RWMutex                             // size: 8
	window        time.Duration              // size: 8
	requests      map[string][]time.Time     // size: 8 (pointer)
	done          chan struct{}              // size: 8
	rejectedTotal metric.Int64Counter        // size: 8 (interface)
	limit         int                        // size: 4
	stopOnce      sync.Once                  // ensures Stop is idempotent
}

func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	meter := otel.Meter("rate_limiter")
	rejectedTotal, err := meter.Int64Counter(
		"rate_limiter.rejected_total",
		metric.WithDescription("Total number of rate-limited (429) responses"),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		otel.Handle(err)
	}

	rl := &RateLimiter{
		requests:      make(map[string][]time.Time),
		limit:         limit,
		window:        window,
		done:          make(chan struct{}),
		rejectedTotal: rejectedTotal,
	}
	go rl.cleanup()
	return rl
}

// Stop terminates the background cleanup goroutine.
// It is safe to call Stop multiple times.
func (rl *RateLimiter) Stop() {
	rl.stopOnce.Do(func() {
		close(rl.done)
	})
}

func (rl *RateLimiter) RateLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		ip := c.ClientIP()
		now := time.Now()
		windowStart := now.Add(-rl.window)

		// Single write lock for the check-and-add to avoid a TOCTOU race
		// where concurrent requests could both pass the limit check.
		rl.Lock()
		// Filter expired timestamps during counting to prevent unbounded
		// slice growth between periodic cleanup cycles.
		valid := rl.requests[ip][:0]
		for _, t := range rl.requests[ip] {
			if t.After(windowStart) {
				valid = append(valid, t)
			}
		}
		rl.requests[ip] = valid

		if len(valid) >= rl.limit {
			rl.Unlock()
			if rl.rejectedTotal != nil {
				rl.rejectedTotal.Add(context.Background(), 1)
			}
			c.JSON(http.StatusTooManyRequests, gin.H{"error": "rate limit exceeded"})
			c.Abort()
			return
		}

		rl.requests[ip] = append(rl.requests[ip], now)
		rl.Unlock()

		c.Next()
	}
}

// cleanup periodically removes expired entries to prevent memory leaks.
func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(rl.window / 2)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			rl.cleanupExpired()
		case <-rl.done:
			return
		}
	}
}

func (rl *RateLimiter) cleanupExpired() {
	rl.Lock()
	defer rl.Unlock()
	now := time.Now()
	for ip, times := range rl.requests {
		var valid []time.Time
		for _, t := range times {
			if now.Sub(t) < rl.window {
				valid = append(valid, t)
			}
		}
		if len(valid) == 0 {
			delete(rl.requests, ip)
		} else {
			rl.requests[ip] = valid
		}
	}
}
