package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store/types"
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
	config            relayAdminConfig
	client            *http.Client
	rateLimit         int
	rateWindowSeconds int
	rateByUID         map[int64]*fixedWindowRateLimiter
	rateByIP          map[string]*fixedWindowRateLimiter
	rateMu            sync.Mutex
	auditLogger       *log.Logger
	managedBudgets    relayAdminManagedBudgetStore
}

type relayAdminManagedBudgetStore interface {
	ListCommercialManagedRelayBudgets(uid int64) ([]*types.CommercialManagedRelayBudget, error)
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

// RelayAdminConfigFromEnv exposes the portal config for main-package wiring.
func RelayAdminConfigFromEnv() relayAdminConfig { return relayAdminConfigFromEnv() }

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
func NewRelayAdminProxyHandler(cfg relayAdminConfig, managedBudgets ...relayAdminManagedBudgetStore) *RelayAdminProxyHandler {
	h := &RelayAdminProxyHandler{
		config:      cfg,
		client:      &http.Client{Timeout: 15 * time.Second},
		rateByUID:   map[int64]*fixedWindowRateLimiter{},
		rateByIP:    map[string]*fixedWindowRateLimiter{},
		auditLogger: nil,
	}
	if len(managedBudgets) > 0 {
		h.managedBudgets = managedBudgets[0]
	}
	h.setRateLimit(30, 60)
	return h
}

const relayAdminCookieName = "catsco_relay_admin"
const relayAdminSessionTTL = 30 * time.Minute

// HandleAccess reports whether the authenticated uid may use the portal.
// It never reveals why; non-whitelisted users just see allowed=false. On
// success it issues a short-lived, path-scoped HttpOnly session cookie so the
// iframe and its same-origin fetches can authenticate without leaking the JWT.
func (h *RelayAdminProxyHandler) HandleAccess(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())
	allowed := h.config.allows(uid)
	if allowed {
		h.issueSessionCookie(w, uid, r.TLS != nil)
	}
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	writeJSON(w, http.StatusOK, map[string]bool{"allowed": allowed})
}

// AuthMiddleware authenticates session-cookie-first and falls back to the
// supplied JWT middleware. The iframe carries the scoped cookie but no
// Authorization header, so cookie validation must happen before the JWT layer;
// direct API calls without a cookie still require a valid JWT.
func (h *RelayAdminProxyHandler) AuthMiddleware(jwt func(http.HandlerFunc) http.HandlerFunc) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if uid := h.verifySessionCookie(r); uid > 0 {
				next(w, r.WithContext(context.WithValue(r.Context(), uidKey, uid)))
				return
			}
			jwt(next)(w, r)
		}
	}
}

// issueSessionCookie signs a short-lived, relay-admin-scoped session cookie.
func (h *RelayAdminProxyHandler) issueSessionCookie(w http.ResponseWriter, uid int64, secure bool) {
	exp := time.Now().Add(relayAdminSessionTTL).Unix()
	payload := fmt.Sprintf("%d:%d", uid, exp)
	http.SetCookie(w, &http.Cookie{
		Name:     relayAdminCookieName,
		Value:    payload + "." + relayAdminSign(payload),
		Path:     "/api/admin/relay/",
		HttpOnly: true,
		Secure:   secure,
		SameSite: http.SameSiteStrictMode,
		MaxAge:   int(relayAdminSessionTTL.Seconds()),
	})
}

// verifySessionCookie validates the signed session cookie and returns its uid.
// Returns 0 when missing, expired, or tampered with.
func (h *RelayAdminProxyHandler) verifySessionCookie(r *http.Request) int64 {
	c, err := r.Cookie(relayAdminCookieName)
	if err != nil {
		return 0
	}
	parts := strings.SplitN(c.Value, ".", 2)
	if len(parts) != 2 {
		return 0
	}
	sig := relayAdminSign(parts[0])
	if !hmac.Equal([]byte(sig), []byte(parts[1])) {
		return 0
	}
	fields := strings.SplitN(parts[0], ":", 2)
	if len(fields) != 2 {
		return 0
	}
	uid, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil || uid <= 0 {
		return 0
	}
	exp, err := strconv.ParseInt(fields[1], 10, 64)
	if err != nil || exp < time.Now().Unix() {
		return 0
	}
	return uid
}

func relayAdminSign(payload string) string {
	mac := hmac.New(sha256.New, jwtSecret)
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// HandleProxy forwards whitelisted usage-admin requests to the relay.
func (h *RelayAdminProxyHandler) HandleProxy(w http.ResponseWriter, r *http.Request) {
	// Authenticate: prefer the short-lived scoped session cookie (iframe/same-origin
	// fetches), fall back to the JWT in the request context (direct API calls).
	uid := h.verifySessionCookie(r)
	if uid <= 0 {
		uid = UIDFromContext(r.Context())
	}
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
	if r.Method == http.MethodPost {
		if targetUID, ok := relayAdminLimitsTargetUID(relayPath); ok && h.managedBudgets != nil {
			managed, err := h.managedBudgets.ListCommercialManagedRelayBudgets(targetUID)
			if err != nil {
				h.audit(r, uid, http.StatusBadGateway, "commercial-budget-check-failed")
				writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to verify the commercial quota state"})
				return
			}
			if len(managed) > 0 {
				h.audit(r, uid, http.StatusConflict, "commercial-budget-managed")
				writeJSON(w, http.StatusConflict, map[string]string{"error": "该账号额度由商业套餐账本管理，请在账号后台的商业化页面调额"})
				return
			}
		}
	}

	upstream := h.config.relayURL + relayPath
	if q := relayAdminSanitizedQuery(r.URL.RawQuery); q != "" {
		upstream += "?" + q
	}
	var upstreamBody io.Reader
	if r.Body != nil && r.Body != http.NoBody {
		limitedBody := http.MaxBytesReader(w, r.Body, 16<<20)
		requestBody, readErr := io.ReadAll(limitedBody)
		if readErr != nil {
			var maxBytesErr *http.MaxBytesError
			if errors.As(readErr, &maxBytesErr) {
				writeJSON(w, http.StatusRequestEntityTooLarge, map[string]string{"error": "request body too large"})
				return
			}
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request body"})
			return
		}
		// Buffering gives the upstream request an explicit Content-Length. The
		// relay admin's lightweight HTTP server does not decode chunked request
		// bodies, so forwarding the original stream would silently submit {}.
		upstreamBody = bytes.NewReader(requestBody)
	}
	upReq, err := http.NewRequestWithContext(r.Context(), r.Method, upstream, upstreamBody)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "internal"})
		return
	}
	upReq.Header.Set("Content-Type", r.Header.Get("Content-Type"))
	// Local write protection requires an explicit server-side marker. Browser
	// headers are intentionally not forwarded; only authenticated, whitelisted
	// proxy routes can receive one here.
	if marker := relayAdminLocalWriteMarker(r.Method, relayPath); marker != "" {
		upReq.Header.Set("X-Cats-Relay-Local-Write", marker)
	}
	// Deliberately do NOT forward Authorization/Cookie/Origin/other sensitive headers.

	resp, err := h.client.Do(upReq)
	if err != nil {
		h.audit(r, uid, http.StatusBadGateway, "relay-unreachable")
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream unavailable"})
		return
	}
	defer resp.Body.Close()
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if readErr != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream read failed"})
		return
	}
	if len(body) > 8<<20 {
		h.audit(r, uid, http.StatusBadGateway, "upstream-response-too-large")
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "upstream response too large"})
		return
	}
	ct := resp.Header.Get("Content-Type")
	// Rewrite only HTML/JS (the embedded page's own references). JSON data must
	// not be rewritten as values could legitimately contain "/local/" substrings.
	if strings.HasPrefix(ct, "text/html") || strings.Contains(ct, "javascript") {
		body = relayAdminRewriteBody(body)
	}
	h.audit(r, uid, resp.StatusCode, r.Method+" "+relayPath)
	// Admin data is sensitive: never cache. SAMEORIGIN (not DENY) so the embedded
	// same-origin iframe can render, while off-origin framing is still blocked.
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
	w.Header().Set("X-Frame-Options", "SAMEORIGIN")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

// relayAdminSanitizedQuery drops credential-bearing parameters before the query
// is forwarded to the relay (they must never reach relay logs).
func relayAdminSanitizedQuery(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		// Unparseable query: drop it entirely rather than risk leaking credentials.
		return ""
	}
	for _, key := range []string{"token", "api_key", "apiKey"} {
		values.Del(key)
	}
	return values.Encode()
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
var relayAdminUserKeyLimitsPath = regexp.MustCompile(`^/local/users/([0-9]+)/key/limits/?$`)
var relayAdminCommercialOpsPath = regexp.MustCompile(`^/local/commercial-ops(?:/api/(?:overview|plans|invites|grants|adjustments|cloud-worker-credits|users|orders|order-refunds|relay-dry-run|relay-sync))?/?$`)
var relayAdminCommercialOpsWritePath = regexp.MustCompile(`^/local/commercial-ops/api/(?:plans|invites|grants|adjustments|cloud-worker-credits|order-refunds|relay-sync)/?$`)

func relayAdminLimitsTargetUID(path string) (int64, bool) {
	matches := relayAdminUserKeyLimitsPath.FindStringSubmatch(path)
	if len(matches) != 2 {
		return 0, false
	}
	uid, err := strconv.ParseInt(matches[1], 10, 64)
	return uid, err == nil && uid > 0
}

func relayAdminLocalWriteMarker(method, path string) string {
	if method != http.MethodPost {
		return ""
	}
	if path == "/local/pricing-rules" {
		return "pricing-rules"
	}
	if relayAdminCommercialOpsWritePath.MatchString(path) {
		return "commercial-ops"
	}
	return ""
}

// relayAdminPathAllowed reports whether a relay path may be proxied.
func relayAdminPathAllowed(rawPath string) bool {
	path := strings.SplitN(rawPath, "?", 2)[0]
	// Defense in depth: reject encoded traversal / control characters outright.
	lower := strings.ToLower(path)
	if strings.Contains(lower, "%2e") || strings.Contains(lower, "%2f") || strings.Contains(lower, "%5c") || strings.Contains(lower, "%00") {
		return false
	}
	if relayAdminUserKeyPath.MatchString(path) {
		return true
	}
	if relayAdminCommercialOpsPath.MatchString(path) {
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
	ip := remoteHost(r.RemoteAddr)
	if h.auditLogger != nil {
		h.auditLogger.Printf("relay-admin uid=%d ip=%s method=%s path=%s status=%d %s", uid, ip, r.Method, r.URL.Path, status, detail)
		return
	}
	log.Printf("relay-admin uid=%d ip=%s method=%s path=%s status=%d %s", uid, ip, r.Method, r.URL.Path, status, detail)
}

func remoteHost(remoteAddr string) string {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		return remoteAddr
	}
	return host
}
