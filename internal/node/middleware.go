package node

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// contextKey is an unexported type for context value keys, preventing collisions
// with keys defined in other packages.
type contextKey string

const clientIPKey contextKey = "client_ip"

// RateLimiter implements a simple token bucket rate limiter per IP
type RateLimiter struct {
	buckets map[string]*tokenBucket
	mutex   sync.RWMutex
	rate    int // requests per second
	burst   int // max burst size
	cleanup chan struct{}
}

type tokenBucket struct {
	tokens     int
	lastRefill time.Time
	mutex      sync.Mutex
}

func NewRateLimiter(rate, burst int) *RateLimiter {
	rl := &RateLimiter{
		buckets: make(map[string]*tokenBucket),
		rate:    rate,
		burst:   burst,
		cleanup: make(chan struct{}),
	}

	go rl.cleanupStaleEntries()
	return rl
}

func (rl *RateLimiter) Allow(ip string) bool {
	rl.mutex.Lock()
	bucket, exists := rl.buckets[ip]
	if !exists {
		bucket = &tokenBucket{
			tokens:     rl.burst,
			lastRefill: time.Now(),
		}
		rl.buckets[ip] = bucket
	}
	rl.mutex.Unlock()

	bucket.mutex.Lock()
	defer bucket.mutex.Unlock()

	// Refill tokens based on time elapsed
	now := time.Now()
	elapsed := now.Sub(bucket.lastRefill)
	tokensToAdd := int(elapsed.Seconds() * float64(rl.rate))

	if tokensToAdd > 0 {
		bucket.tokens += tokensToAdd
		if bucket.tokens > rl.burst {
			bucket.tokens = rl.burst
		}
		bucket.lastRefill = now
	}

	if bucket.tokens > 0 {
		bucket.tokens--
		return true
	}

	return false
}

func (rl *RateLimiter) cleanupStaleEntries() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			rl.mutex.Lock()
			cutoff := time.Now().Add(-10 * time.Minute)
			for ip, bucket := range rl.buckets {
				bucket.mutex.Lock()
				if bucket.lastRefill.Before(cutoff) {
					delete(rl.buckets, ip)
				}
				bucket.mutex.Unlock()
			}
			rl.mutex.Unlock()
		case <-rl.cleanup:
			return
		}
	}
}

func (rl *RateLimiter) Close() {
	close(rl.cleanup)
}

// SecurityMiddleware provides various security features
type SecurityMiddleware struct {
	rateLimiter    *RateLimiter
	maxRequestSize int64
	trustProxy     bool
	metrics        *SecurityMetrics
}

type SecurityMetrics struct {
	rateLimitedRequests prometheus.Counter
	oversizedRequests   prometheus.Counter
	suspiciousRequests  prometheus.Counter
}

var (
	sharedSecurityMetrics     *SecurityMetrics
	sharedSecurityMetricsOnce sync.Once
)

func newSecurityMetrics() *SecurityMetrics {
	sharedSecurityMetricsOnce.Do(func() {
		sharedSecurityMetrics = &SecurityMetrics{
			rateLimitedRequests: prometheus.NewCounter(prometheus.CounterOpts{
				Name: "http_rate_limited_requests_total",
				Help: "Total number of rate-limited requests",
			}),
			oversizedRequests: prometheus.NewCounter(prometheus.CounterOpts{
				Name: "http_oversized_requests_total",
				Help: "Total number of oversized requests rejected",
			}),
			suspiciousRequests: prometheus.NewCounter(prometheus.CounterOpts{
				Name: "http_suspicious_requests_total",
				Help: "Total number of suspicious requests detected",
			}),
		}
		prometheus.MustRegister(
			sharedSecurityMetrics.rateLimitedRequests,
			sharedSecurityMetrics.oversizedRequests,
			sharedSecurityMetrics.suspiciousRequests,
		)
	})
	return sharedSecurityMetrics
}

func NewSecurityMiddleware(rateLimit, burst int, maxRequestSize int64, trustProxy bool) *SecurityMiddleware {
	return &SecurityMiddleware{
		rateLimiter:    NewRateLimiter(rateLimit, burst),
		maxRequestSize: maxRequestSize,
		trustProxy:     trustProxy,
		metrics:        newSecurityMetrics(),
	}
}

// peerEndpoints are inter-cluster paths that the per-IP rate limiter must
// not apply to. Two reasons:
//  1. When NODE_CLUSTER_SECRET is set, these endpoints are HMAC-gated
//     already (verifyGossipSignature). Authenticated peer traffic at
//     cluster volumes legitimately exceeds the client-tier per-IP rate.
//  2. In open mode, the operator has explicitly accepted no-auth on the
//     cluster plane — applying a client-tier rate limit there is
//     inconsistent with that trust model.
//
// Either way, applying the client rate limit here was the F6 burn-in
// symptom: peer-to-peer gossip got 429'd and the cluster broke at modest
// volumes (#86). Other security checks (size, scanner, headers) still
// apply to peer endpoints.
var peerEndpoints = map[string]bool{
	"/v1/gossip/message": true,
	"/v1/bootstrap":      true,
}

func (sm *SecurityMiddleware) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Apply security headers
		sm.applySecurityHeaders(w)

		// Check rate limiting (peer endpoints exempt — see peerEndpoints comment)
		clientIP := sm.getClientIP(r)
		if !peerEndpoints[r.URL.Path] {
			if !sm.rateLimiter.Allow(clientIP) {
				sm.metrics.rateLimitedRequests.Inc()
				http.Error(w, "Rate limit exceeded", http.StatusTooManyRequests)
				return
			}
		}

		// Check request size
		if r.ContentLength > sm.maxRequestSize {
			sm.metrics.oversizedRequests.Inc()
			http.Error(w, "Request too large", http.StatusRequestEntityTooLarge)
			return
		}

		// Check for suspicious patterns
		if sm.isSuspiciousRequest(r) {
			sm.metrics.suspiciousRequests.Inc()
			http.Error(w, "Forbidden", http.StatusForbidden)
			return
		}

		// Add security context
		ctx := r.Context()
		ctx = context.WithValue(ctx, clientIPKey, clientIP)
		r = r.WithContext(ctx)

		next.ServeHTTP(w, r)
	})
}

func (sm *SecurityMiddleware) applySecurityHeaders(w http.ResponseWriter) {
	// Basic security headers
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("X-Frame-Options", "DENY")
	w.Header().Set("X-XSS-Protection", "1; mode=block")
	w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
	w.Header().Set("Content-Security-Policy", "default-src 'self'")

}

func (sm *SecurityMiddleware) getClientIP(r *http.Request) string {
	// Only trust proxy headers when explicitly configured.
	// X-Forwarded-For and X-Real-IP are trivially spoofable by clients
	// in direct-exposure deployments.
	if sm.trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			ips := strings.Split(xff, ",")
			if len(ips) > 0 {
				return strings.TrimSpace(ips[0])
			}
		}
		if xri := r.Header.Get("X-Real-IP"); xri != "" {
			return xri
		}
	}

	// Use direct connection IP
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

func (sm *SecurityMiddleware) isSuspiciousRequest(r *http.Request) bool {
	userAgent := r.Header.Get("User-Agent")

	// Block known vulnerability scanners by user-agent.
	// Only scanner-specific tools — not general-purpose HTTP libraries.
	// The API is permissionless; python-requests, curl, etc. are legitimate clients.
	scannerUAs := []string{
		"sqlmap",
		"nikto",
		"nmap",
		"masscan",
		"gobuster",
		"dirbuster",
	}

	userAgentLower := strings.ToLower(userAgent)
	for _, scanner := range scannerUAs {
		if strings.Contains(userAgentLower, scanner) {
			return true
		}
	}

	// No URL pattern matching. The store treats keys and
	// values as opaque bytes — there is no SQL layer, no HTML rendering, and no
	// filesystem access. Substring checks on URLs would false-positive on legitimate
	// keys like "user_selection", "drop_zone", or "script_output".

	return false
}

// MaxRequestSize returns the configured maximum request body size in bytes.
func (sm *SecurityMiddleware) MaxRequestSize() int64 {
	return sm.maxRequestSize
}

func (sm *SecurityMiddleware) Close() {
	if sm.rateLimiter != nil {
		sm.rateLimiter.Close()
	}
}

// Request size limiting middleware
func MaxRequestSizeMiddleware(maxSize int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > maxSize {
				http.Error(w, "Request too large", http.StatusRequestEntityTooLarge)
				return
			}

			// Also set a limit on the request body reader
			r.Body = http.MaxBytesReader(w, r.Body, maxSize)
			next.ServeHTTP(w, r)
		})
	}
}

// Timeout middleware to prevent slow loris attacks
func TimeoutMiddleware(timeout time.Duration) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.TimeoutHandler(next, timeout, "Request timeout")
	}
}
