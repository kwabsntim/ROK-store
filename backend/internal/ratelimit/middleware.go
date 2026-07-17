// Package ratelimit provides per-IP token-bucket rate limiting middleware
// using golang.org/x/time/rate. Each IP gets its own limiter.
package ratelimit

import (
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// ipLimiter holds a token-bucket limiter and the last time it was accessed,
// so we can evict stale entries.
type ipLimiter struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// Store is a thread-safe map of per-IP limiters.
type Store struct {
	mu       sync.Mutex
	limiters map[string]*ipLimiter
	r        rate.Limit // tokens per second
	b        int        // burst size
}

// NewStore creates a Store for the given requests-per-minute limit.
// burst is set equal to ratePerMin so short bursts up to the per-minute
// quota are allowed instantly.
func NewStore(ratePerMin int) *Store {
	s := &Store{
		limiters: make(map[string]*ipLimiter),
		r:        rate.Limit(float64(ratePerMin) / 60.0),
		b:        ratePerMin,
	}
	// Background goroutine cleans up limiters that haven't been used for 5 min.
	go s.cleanupLoop()
	return s
}

// get returns (or creates) the limiter for the given IP.
func (s *Store) get(ip string) *rate.Limiter {
	s.mu.Lock()
	defer s.mu.Unlock()

	entry, ok := s.limiters[ip]
	if !ok {
		entry = &ipLimiter{
			limiter: rate.NewLimiter(s.r, s.b),
		}
		s.limiters[ip] = entry
	}
	entry.lastSeen = time.Now()
	return entry.limiter
}

// cleanupLoop removes limiters that haven't seen activity in 5 minutes.
func (s *Store) cleanupLoop() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		s.mu.Lock()
		for ip, entry := range s.limiters {
			if time.Since(entry.lastSeen) > 5*time.Minute {
				delete(s.limiters, ip)
			}
		}
		s.mu.Unlock()
	}
}

// Middleware returns an http.Handler middleware that enforces the per-IP rate
// limit defined by this Store. Exceeding the limit returns 429 Too Many Requests.
func (s *Store) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip := realIP(r)
		if !s.get(ip).Allow() {
			http.Error(w, `{"error":"too many requests, please slow down"}`, http.StatusTooManyRequests)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// realIP extracts the client IP from X-Forwarded-For (set by Render's proxy)
// or falls back to RemoteAddr.
func realIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// X-Forwarded-For may be a comma-separated list; take the first entry.
		ip, _, _ := net.SplitHostPort(xff)
		if ip == "" {
			// No port in the header value.
			return xff
		}
		return ip
	}
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}
