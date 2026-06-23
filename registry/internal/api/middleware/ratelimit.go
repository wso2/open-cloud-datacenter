package middleware

import (
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/time/rate"
)

type limiterEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

type RateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*limiterEntry
	rate     rate.Limit
	burst    int
	ttl      time.Duration
}

func NewRateLimiter(r rate.Limit, burst int) *RateLimiter {
	rl := &RateLimiter{
		limiters: make(map[string]*limiterEntry),
		rate:     r,
		burst:    burst,
		ttl:      10 * time.Minute,
	}
	go rl.cleanup()
	return rl
}

func (rl *RateLimiter) getLimiter(key string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	entry, ok := rl.limiters[key]
	if !ok {
		entry = &limiterEntry{limiter: rate.NewLimiter(rl.rate, rl.burst)}
		rl.limiters[key] = entry
	}
	entry.lastSeen = time.Now()
	return entry.limiter
}

func (rl *RateLimiter) cleanup() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		rl.mu.Lock()
		for k, v := range rl.limiters {
			if time.Since(v.lastSeen) > rl.ttl {
				delete(rl.limiters, k)
			}
		}
		rl.mu.Unlock()
	}
}

// ByTenant limits per (tenantId, endpoint) pair.
// createLimit: 1 per 10 min for POST /registry
// readLimit:   60 per min for GET /registry
// credLimit:   10 per min for GET /credentials
func ByTenant(createRPS, readRPS float64) gin.HandlerFunc {
	createLimiter := NewRateLimiter(rate.Limit(createRPS), 1)
	readLimiter := NewRateLimiter(rate.Limit(readRPS), 10)

	return func(c *gin.Context) {
		tenantID := c.Param("tenantId")
		if tenantID == "" {
			c.Next()
			return
		}

		var limiter *rate.Limiter
		if c.Request.Method == http.MethodPost && !isSubPath(c) {
			limiter = createLimiter.getLimiter(tenantID)
		} else {
			limiter = readLimiter.getLimiter(tenantID)
		}

		if !limiter.Allow() {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"error":      "RATE_LIMITED",
				"message":    "Too many requests. Please wait before retrying.",
				"retryAfter": "60",
			})
			return
		}
		c.Next()
	}
}

func isSubPath(c *gin.Context) bool {
	// POST /credentials/rotate is not a create — treat as read-rate
	path := c.FullPath()
	return len(path) > len("/api/v1/tenants/:tenantId/registry")
}
