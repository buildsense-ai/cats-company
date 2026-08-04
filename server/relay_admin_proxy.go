package server

import (
	"bytes"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// RelayAdminProxyHandler exposes the relay usage-admin page to whitelisted
// owners through a guarded reverse proxy. The relay itself (port 18090) is
// never exposed to the public; only whitelisted paths are forwarded.
//
// Security layers, in order:
//  1. JWT auth (wired via AuthMiddlewareWithDB at route registration).
//  2. uid whitelist (CATSCO_ADMIN_UID_WHITELIST, server-side enforced).
//  3. path whitelist (only usage-admin /local/* paths; /internal/, /public/,
//     /health, /api/ are never forwarded).
//  4. rate limiting (per uid + per IP) and audit logging (writes highlighted).
type RelayAdminProxyHandler struct {
	config           relayAdminConfig
	client           *http.Client
	rateLimit        int
	rateWindowSeconds int
	rateByUID        map[int64]*fixedWindowRateLimiter
	rateByIP         map[string]*fixedWindowRateLimiter
	rateMu           sync.Mutex
	auditLogger      *log.Logger
}

const relayAdminRewritePrefix = "/api/admin/relay"

type relayAdminConfig struct {
	relayURL    string
	allowedUIDs []int64
}

func (c relayAdminConfig) allows(uid int64) bool {
	for _, id := range c.allowedUIDs {
		if id == uid {
			return true
		}
	}
	return false
}

// relayAdminConfigFromEnv reads the admin portal config from the environment.
// An empty whitelist disables the portal entirely.
func relayAdminConfigFromEnv() relayAdminConfig {
	cfg := relayAdminConfig{
		relayURL: firstNonEmpty(os.Getenv("CATSCO_RELAY_ADMIN_URL"), "http://127.0.0.1:18090"),
	}
	for _, raw := range strings.Split(os.Getenv("CATSCO_ADMIN_UID_WHITELIST"), ",") {
		uid, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err == nil && uid > 0 {
			cfg.allowedUIDs = append(cfg.allowedUIDs, uid)
		}
	}
	return cfg
}

// NewRelayAdminProxyHandler builds the handler with bounded client timeouts.
func NewRelayAdminProxyHandler(cfg relayAdminConfig) *RelayAdminProxyHandler {
	h := &RelayAdminProxyHandler{
		config:            cfg,
		client:            &http.Client{Timeout: 15 * time.Second},
		rateByUID:         map[int64]*fixedWindowRateLimiter{},
		rateByIP:          map[string]*fixedWindowRateLimiter{},
		auditLogger:       nil,
	}
	h.setRateLimit(30, 60)
	return h
}

// HandleAccess reports whether the authenticated uid may use the portal.
// It never reveals why; non-whitelisted users just see allowed=false.
func (h *RelayAdminProxyHandler) HandleAccess(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())
	writeJSON(w, http.StatusOK, map[string]bool{"allowed": h.config.allows(uid)})
}

// HandleProxy forwards whitelisted usage-admin requests to the relay.
func (h *RelayAdminProxyHandler) HandleProxy(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())
	if uid <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if !h.config.allows(uid) {
		h.audit(r, uid, http.StatusForbidden, "uid-not-whitelisted")
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	relayPath := strings.TrimPrefix(r.URL.Path, relayAdminRewritePrefix)
	if !relayAdminPathAllowed(relayPath) {
		h.audit(r, uid, http.StatusForbidden, "path-not-whitelisted:"+relayPath)
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	if !h.allow(uid, r.RemoteAddr) {
		h.audit(r, uid, http.StatusTooManyRequests, "rate-limited")
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate limited"})
		return
	}

	upstream := h.config.relayURL + relayPath
	if r.URL.RawQuery != "" {
		upstream += "?" + r.URL.RawQuery
	}
	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, upstream, r.Body)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	upReq.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	// Deliberately do NOT forward Authorization/Cookie/other sensitive headers.

	resp, err := h.client.Do(upReq)
	if err != nil {
		h.audit(r, uid, http.StatusBadGateway, "relay-unreachable")
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream unavailable"})
		return
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream read failed"})
		return
	}
	ct := resp.Header.Get("Content-Type")
	if strings.HasPrefix(ct, "text/") || strings.Contains(ct, "javascript") || strings.Contains(ct, "json") {
		body = relayAdminRewriteBody(body)
	}
	h.audit(r, uid, resp.StatusCode, r.Method+" "+relayPath)
	w.Header().Set("Content-Type", ct)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// relayAdminRewriteBody rewrites absolute /local/ references to the proxy
// prefix so iframe fetches and links keep going through the guarded proxy.
func relayAdminRewriteBody(body []byte) []byte {
	return bytes.ReplaceAll(body, []byte(`/local/`), []byte(relayAdminRewritePrefix+`/local/`))
}

var relayAdminAllowedPrefixes = []string{
	"/local/usage-admin",
	"/local/usage-summary",
	"/local/usage-users",
	"/local/pricing-analytics",
	"/local/pricing-rules",
}

var relayAdminUserKeyPath = regexp.MustCompile(`^/local/users/[0-9]+/key/(state|limits|usage-reset)/?$`)

// relayAdminPathAllowed reports whether a relay path may be proxied.
func relayAdminPathAllowed(rawPath string) bool {
	path := strings.SplitN(rawPath, "?", 2)[0]
	if relayAdminUserKeyPath.MatchString(path) {
		return true
	}
	for _, prefix := range relayAdminAllowedPrefixes {
		if path == prefix || strings.HasPrefix(path, prefix+"/") {
			return true
		}
	}
	return false
}

// fixedWindowRateLimiter is a simple per-key fixed-window limiter.
type fixedWindowRateLimiter struct {
	limit    int
	windowNS int64
	startNS  int64
	count    int
}

func newFixedWindowRateLimiter(limit, windowSeconds int) *fixedWindowRateLimiter {
	return &fixedWindowRateLimiter{
		limit:    limit,
		windowNS: int64(windowSeconds) * int64(time.Second),
		startNS:  time.Now().UnixNano(),
	}
}

func (l *fixedWindowRateLimiter) allow() bool {
	now := time.Now().UnixNano()
	if now-l.startNS >= l.windowNS {
		l.startNS = now
		l.count = 0
	}
	if l.count >= l.limit {
		return false
	}
	l.count++
	return true
}

func (h *RelayAdminProxyHandler) setRateLimit(limit, windowSeconds int) {
	h.rateMu.Lock()
	defer h.rateMu.Unlock()
	h.rateLimit = limit
	h.rateWindowSeconds = windowSeconds
	h.rateByUID = map[int64]*fixedWindowRateLimiter{}
	h.rateByIP = map[string]*fixedWindowRateLimiter{}
}

func (h *RelayAdminProxyHandler) allow(uid int64, remoteAddr string) bool {
	h.rateMu.Lock()
	defer h.rateMu.Unlock()
	limiter := h.rateByUID[uid]
	if limiter == nil {
		limiter = newFixedWindowRateLimiter(h.rateLimit, h.rateWindowSeconds)
		h.rateByUID[uid] = limiter
	}
	if !limiter.allow() {
		return false
	}
	ip := remoteHost(remoteAddr)
	ipLimiter := h.rateByIP[ip]
	if ipLimiter == nil {
		ipLimiter = newFixedWindowRateLimiter(h.rateLimit, h.rateWindowSeconds)
		h.rateByIP[ip] = ipLimiter
	}
	return ipLimiter.allow()
}

func (h *RelayAdminProxyHandler) audit(r *http.Request, uid int64, status int, detail string) {
	if h.auditLogger != nil {
		h.auditLogger.Printf("relay-admin uid=%d method=%s path=%s status=%d %s", uid, r.Method, r.URL.Path, status, detail)
		return
	}
	log.Printf("relay-admin uid=%d method=%s path=%s status=%d %s", uid, r.Method, r.URL.Path, status, detail)
}

func remoteHost(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}
