package auth

import (
	"fmt"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const oneHour = time.Hour

// RateLimiter provides in-memory rate limiting for login and sync operations.
// State is lost on restart, which is acceptable for this use case.
type RateLimiter struct {
	mu       sync.Mutex
	limiters map[string]*rate.Limiter
}

// NewRateLimiter creates a new RateLimiter.
func NewRateLimiter() *RateLimiter {
	return &RateLimiter{
		limiters: make(map[string]*rate.Limiter),
	}
}

// getLimiter returns (or creates) a rate.Limiter for the given key. The limiter
// allows `perHour` events per hour with a burst of 1.
func (rl *RateLimiter) getLimiter(key string, perHour int) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	if lim, ok := rl.limiters[key]; ok {
		return lim
	}

	// rate.Every converts the interval between allowed events.
	// For N events per hour, the interval is 1h/N.
	lim := rate.NewLimiter(rate.Every(oneHour/time.Duration(perHour)), 1)
	rl.limiters[key] = lim
	return lim
}

// AllowLogin checks whether a login attempt is allowed for the given email and
// IP address. Limits: 5 per hour per email, 20 per hour per IP.
func (rl *RateLimiter) AllowLogin(email string, ip string) bool {
	emailOK := rl.getLimiter("login-email:"+email, 5).Allow()
	ipOK := rl.getLimiter("login-ip:"+ip, 20).Allow()
	return emailOK && ipOK
}

// AllowSync checks whether a sync operation is allowed for the given user.
// Limit: 1 per hour per user.
func (rl *RateLimiter) AllowSync(userID int64) bool {
	key := fmt.Sprintf("sync-user:%d", userID)
	return rl.getLimiter(key, 1).Allow()
}

// AllowRecheckConsent checks whether an EFB consent re-check is allowed
// for the given user. The recheck endpoint deliberately bypasses the
// per-user 1/hour sync rate limit (it isn't a poll — the user just
// took action on EFB), so a separate, more permissive limit guards
// against runaway clients hammering EFB on our behalf.
// Limit: 6 per hour per user.
func (rl *RateLimiter) AllowRecheckConsent(userID int64) bool {
	key := fmt.Sprintf("recheck-consent:%d", userID)
	return rl.getLimiter(key, 6).Allow()
}
