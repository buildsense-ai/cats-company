package server

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxRateLimitKeyBodyBytes int64 = 1 << 20

type HTTPRateLimitConfig struct {
	Name   string
	Limit  int
	Window time.Duration
	Burst  int
}

type HTTPRateLimiter struct {
	mu          sync.Mutex
	buckets     map[string]*httpRateBucket
	lastCleanup time.Time
}

type httpRateBucket struct {
	tokens     float64
	maxTokens  float64
	refillRate float64
	lastRefill time.Time
}

func NewHTTPRateLimiter() *HTTPRateLimiter {
	return &HTTPRateLimiter{
		buckets:     make(map[string]*httpRateBucket),
		lastCleanup: time.Now(),
	}
}

func (rl *HTTPRateLimiter) LimitIP(config HTTPRateLimitConfig) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			key := clientIPFromRequest(r)
			if key == "" {
				key = "unknown"
			}
			if ok, retryAfter := rl.allowWithRetry(config, "ip:"+key); !ok {
				writeRateLimitExceeded(w, retryAfter)
				return
			}
			next(w, r)
		}
	}
}

func (rl *HTTPRateLimiter) LimitUser(config HTTPRateLimitConfig) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if uid := UIDFromContext(r.Context()); uid != 0 {
				if ok, retryAfter := rl.allowWithRetry(config, "uid:"+strconv.FormatInt(uid, 10)); !ok {
					writeRateLimitExceeded(w, retryAfter)
					return
				}
			}
			next(w, r)
		}
	}
}

func (rl *HTTPRateLimiter) LimitJSONField(config HTTPRateLimitConfig, field string) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if value := normalizedJSONField(r, field); value != "" {
				if ok, retryAfter := rl.allowWithRetry(config, "field:"+value); !ok {
					writeRateLimitExceeded(w, retryAfter)
					return
				}
			}
			next(w, r)
		}
	}
}

// allow keeps the original one-value shape for callers that only need the
// verdict; allowWithRetry also reports how long the caller should wait.
func (rl *HTTPRateLimiter) allow(config HTTPRateLimitConfig, key string) bool {
	ok, _ := rl.allowWithRetry(config, key)
	return ok
}

func (rl *HTTPRateLimiter) allowWithRetry(config HTTPRateLimitConfig, key string) (bool, time.Duration) {
	if config.Limit <= 0 || config.Window <= 0 {
		return true, 0
	}
	burst := config.Burst
	if burst <= 0 {
		burst = config.Limit
	}
	namespacedKey := config.Name + ":" + key

	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	if now.Sub(rl.lastCleanup) > time.Minute {
		rl.cleanupLocked(now)
	}

	b, ok := rl.buckets[namespacedKey]
	if !ok {
		b = &httpRateBucket{
			tokens:     float64(burst),
			maxTokens:  float64(burst),
			refillRate: float64(config.Limit) / config.Window.Seconds(),
			lastRefill: now,
		}
		rl.buckets[namespacedKey] = b
	}

	elapsed := now.Sub(b.lastRefill).Seconds()
	b.tokens += elapsed * b.refillRate
	if b.tokens > b.maxTokens {
		b.tokens = b.maxTokens
	}
	b.lastRefill = now

	if b.tokens < 1 {
		// Tell the caller how long the wait really is. A fixed 60s is wrong for
		// slow buckets (3 per 10 minutes refills one token every 200s), and a
		// client that trusts a too-short Retry-After retries straight into the
		// same limit.
		wait := time.Second
		if b.refillRate > 0 {
			seconds := (1 - b.tokens) / b.refillRate
			wait = time.Duration(math.Ceil(seconds)) * time.Second
			if wait < time.Second {
				wait = time.Second
			}
			if wait > config.Window {
				wait = config.Window
			}
		}
		return false, wait
	}
	b.tokens--
	return true, 0
}

func (rl *HTTPRateLimiter) cleanupLocked(now time.Time) {
	cutoff := now.Add(-30 * time.Minute)
	for key, b := range rl.buckets {
		if b.lastRefill.Before(cutoff) {
			delete(rl.buckets, key)
		}
	}
	rl.lastCleanup = now
}

func writeRateLimitExceeded(w http.ResponseWriter, retryAfter time.Duration) {
	seconds := int(math.Ceil(retryAfter.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	writeJSON(w, http.StatusTooManyRequests, map[string]any{
		"error":               "操作太频繁，请稍后再试",
		"retry_after_seconds": seconds,
	})
}

func clientIPFromRequest(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		remoteIP := net.ParseIP(strings.TrimSpace(host))
		if remoteIP != nil {
			if isTrustedProxyIP(remoteIP) {
				if ip := clientIPFromForwardedFor(r.Header.Get("X-Forwarded-For")); ip != "" {
					return ip
				}
				if ip := normalizeIP(r.Header.Get("X-Real-IP")); ip != "" {
					return ip
				}
			}
			return remoteIP.String()
		}
	}
	return normalizeIP(r.RemoteAddr)
}

func clientIPFromForwardedFor(value string) string {
	parts := strings.Split(value, ",")
	nearestIP := ""
	for i := len(parts) - 1; i >= 0; i-- {
		candidate := normalizeIP(parts[i])
		if candidate == "" {
			continue
		}
		if nearestIP == "" {
			nearestIP = candidate
		}
		if !isTrustedProxyIP(net.ParseIP(candidate)) {
			return candidate
		}
	}
	return nearestIP
}

func isTrustedProxyIP(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate()
}

func normalizeIP(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	ip := net.ParseIP(value)
	if ip == nil {
		return ""
	}
	return ip.String()
}

func normalizedJSONField(r *http.Request, field string) string {
	if r.Body == nil {
		return ""
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, maxRateLimitKeyBodyBytes+1))
	if err != nil {
		r.Body = io.NopCloser(bytes.NewReader(body))
		return ""
	}
	r.Body.Close()
	r.Body = io.NopCloser(bytes.NewReader(body))

	if int64(len(body)) > maxRateLimitKeyBodyBytes {
		return ""
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	raw, ok := payload[field]
	if !ok {
		return ""
	}
	value, ok := raw.(string)
	if !ok {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(value))
}
