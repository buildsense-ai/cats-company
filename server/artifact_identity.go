package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

// Artifact identity cookie.
//
// The Artifact applications live on artifact.catsco.cc while the platform's
// login state lives in this origin's storage, which the application origin
// cannot read. This optional cookie bridges that gap at the domain level: it is
// issued on the platform origin with Domain=.catsco.cc, so the browser also
// sends it to artifact.catsco.cc, and the gateway can exchange it for an
// identity. `__Host-` cannot be used here because that prefix forbids Domain.
//
// It is deliberately narrow: a signed uid plus expiry, HttpOnly, Secure,
// SameSite=Lax, and readable only through a read-only lookup that requires the
// shared gateway token. The long-lived platform credential never leaves the
// platform origin.
const (
	artifactIdentityCookieName = "catsco_artifact_id"
	artifactIdentityKeyLabel   = "catscompany/artifact-identity/v1"
	artifactIdentityDefaultTTL = 24 * time.Hour
)

// ArtifactIdentityConfig is the policy for the domain cookie. Disabled unless
// explicitly switched on, and then only with both domains and the shared token
// configured. Opt-in matters here: configuring the gateway token for another
// Artifact feature must not silently start handing out a cross-subdomain
// identity cookie.
type ArtifactIdentityConfig struct {
	OptIn   bool
	Domains []string
	TTL     time.Duration
	Secure  bool
	Token   string
}

func ArtifactIdentityConfigFromEnv() ArtifactIdentityConfig {
	config := ArtifactIdentityConfig{
		OptIn:  os.Getenv("CATSCO_ARTIFACT_IDENTITY_ENABLED") == "1",
		TTL:    artifactIdentityDefaultTTL,
		Secure: os.Getenv("CATSCO_ARTIFACT_IDENTITY_INSECURE") != "1",
		Token:  strings.TrimSpace(os.Getenv("CATSCO_ARTIFACT_GATEWAY_TOKEN")),
	}
	for _, raw := range strings.Split(os.Getenv("CATSCO_ARTIFACT_IDENTITY_DOMAINS"), ",") {
		domain := strings.TrimSpace(raw)
		if domain != "" {
			config.Domains = append(config.Domains, domain)
		}
	}
	if len(config.Domains) == 0 {
		// Both public Artifact domains, so neither site is left without a cookie.
		config.Domains = []string{".catsco.cc", ".catsco.cn"}
	}
	if raw := strings.TrimSpace(os.Getenv("CATSCO_ARTIFACT_IDENTITY_TTL")); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > time.Minute {
			config.TTL = parsed
		}
	}
	return config
}

func (c ArtifactIdentityConfig) Enabled() bool {
	return c.OptIn && len(c.Domains) > 0 && c.Token != ""
}

func artifactIdentitySign(payload string) string {
	mac := hmac.New(sha256.New, jwtSecret)
	mac.Write([]byte(artifactIdentityKeyLabel))
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// Issue writes the cookie for one user. A cookie that is still fresh *and
// belongs to the same user* is left alone, so calling this on every
// authenticated request stays cheap. The uid comparison matters: without it, a
// second account signing in on the same browser would keep presenting the first
// account's identity until the cookie happened to come up for renewal.
func (c ArtifactIdentityConfig) Issue(w http.ResponseWriter, r *http.Request, uid int64) {
	if !c.Enabled() || uid <= 0 {
		return
	}
	if current, exp, ok := c.readCookie(r); ok && current == uid && time.Until(exp) > c.TTL/2 {
		return
	}
	domain := c.domainForHost(r.Host)
	if domain == "" {
		return
	}
	exp := time.Now().Add(c.TTL)
	payload := fmt.Sprintf("%d:%d", uid, exp.Unix())
	http.SetCookie(w, &http.Cookie{
		Name:     artifactIdentityCookieName,
		Value:    payload + "." + artifactIdentitySign(payload),
		Path:     "/",
		Domain:   domain,
		HttpOnly: true,
		Secure:   c.Secure,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(c.TTL.Seconds()),
	})
}

// domainForHost picks the configured domain that covers the request host, so a
// .cn visitor receives the .cn cookie rather than a cookie for the other site.
func (c ArtifactIdentityConfig) domainForHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	for _, domain := range c.Domains {
		base := strings.TrimPrefix(domain, ".")
		if host == base || strings.HasSuffix(host, "."+base) {
			return domain
		}
	}
	return ""
}

func (c ArtifactIdentityConfig) readCookie(r *http.Request) (int64, time.Time, bool) {
	cookie, err := r.Cookie(artifactIdentityCookieName)
	if err != nil || cookie.Value == "" {
		return 0, time.Time{}, false
	}
	payload, signature, found := strings.Cut(cookie.Value, ".")
	if !found || payload == "" || signature == "" {
		return 0, time.Time{}, false
	}
	if !hmac.Equal([]byte(artifactIdentitySign(payload)), []byte(signature)) {
		return 0, time.Time{}, false
	}
	uidPart, expPart, found := strings.Cut(payload, ":")
	if !found {
		return 0, time.Time{}, false
	}
	uid, err := strconv.ParseInt(uidPart, 10, 64)
	if err != nil || uid <= 0 {
		return 0, time.Time{}, false
	}
	expUnix, err := strconv.ParseInt(expPart, 10, 64)
	if err != nil {
		return 0, time.Time{}, false
	}
	exp := time.Unix(expUnix, 0)
	if time.Now().After(exp) {
		return 0, time.Time{}, false
	}
	return uid, exp, true
}

// Package wiring. The zero value keeps the feature off, so nothing changes
// until the platform is configured for it.
var artifactIdentity = ArtifactIdentityConfig{}

// ConfigureArtifactIdentity installs the cookie policy for this process.
func ConfigureArtifactIdentity(config ArtifactIdentityConfig) { artifactIdentity = config }

// MaybeIssueArtifactIdentityCookie is called after a successful user
// authentication and is a no-op unless the feature is configured.
func MaybeIssueArtifactIdentityCookie(w http.ResponseWriter, r *http.Request, uid int64) {
	if !artifactIdentity.Enabled() {
		return
	}
	artifactIdentity.Issue(w, r, uid)
}

// ArtifactIdentityHandler is the read-only lookup the Artifact gateway uses to
// turn a domain cookie into a user id. It accepts the shared gateway token
// instead of a JWT because the caller is a service, and it returns nothing but
// the viewer id.
type ArtifactIdentityHandler struct {
	config ArtifactIdentityConfig
}

func NewArtifactIdentityHandlerFromEnv() *ArtifactIdentityHandler {
	return &ArtifactIdentityHandler{config: ArtifactIdentityConfigFromEnv()}
}

func (h *ArtifactIdentityHandler) Enabled() bool { return h != nil && h.config.Enabled() }

func (h *ArtifactIdentityHandler) HandleIdentity(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}
	if !h.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "artifact_identity_unavailable"})
		return
	}
	provided := ""
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
		provided = strings.TrimSpace(strings.TrimPrefix(auth, "Bearer "))
	}
	if provided == "" || !hmac.Equal([]byte(provided), []byte(h.config.Token)) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "forbidden"})
		return
	}
	uid, exp, ok := h.config.readCookie(r)
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authenticated": true,
		"uid":           uid,
		"expires_at":    exp.UTC().Format(time.RFC3339),
	})
}
