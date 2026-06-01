package server

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultRelaySSOTTL = 2 * time.Minute

type relaySessionResponse struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
}

type relaySSOPayload struct {
	UID      int64  `json:"uid"`
	Username string `json:"username"`
	Issuer   string `json:"iss"`
	Audience string `json:"aud"`
	JTI      string `json:"jti"`
	IssuedAt int64  `json:"iat"`
	Expires  int64  `json:"exp"`
}

func relaySSOSecret() string {
	return strings.TrimSpace(os.Getenv("CATS_RELAY_SSO_SECRET"))
}

func relaySSOTTL() time.Duration {
	raw := strings.TrimSpace(os.Getenv("CATS_RELAY_SSO_TTL_SECONDS"))
	if raw == "" {
		return defaultRelaySSOTTL
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil || seconds <= 0 {
		return defaultRelaySSOTTL
	}
	ttl := time.Duration(seconds) * time.Second
	if ttl > 5*time.Minute {
		return 5 * time.Minute
	}
	return ttl
}

func signRelaySSOTicket(payload relaySSOPayload, secret string) (string, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	encoded := base64.RawURLEncoding.EncodeToString(data)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(encoded))
	signature := base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encoded + "." + signature, nil
}

func generateRelayJTI() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw), nil
}

func (h *RelayConfigHandler) HandleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	secret := relaySSOSecret()
	if secret == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "relay sso is not configured"})
		return
	}

	uid := UIDFromContext(r.Context())
	if uid <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	now := time.Now().UTC()
	exp := now.Add(relaySSOTTL())
	jti, err := generateRelayJTI()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create relay session"})
		return
	}
	ticket, err := signRelaySSOTicket(relaySSOPayload{
		UID:      uid,
		Username: UsernameFromContext(r.Context()),
		Issuer:   "catscompany",
		Audience: "cats-relay",
		JTI:      jti,
		IssuedAt: now.Unix(),
		Expires:  exp.Unix(),
	}, secret)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create relay session"})
		return
	}

	relayURL := relayBaseURL()
	parsed, err := url.Parse(relayURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "invalid relay url"})
		return
	}
	query := parsed.Query()
	query.Set("sso", ticket)
	parsed.RawQuery = query.Encode()

	writeJSON(w, http.StatusOK, relaySessionResponse{
		URL:       parsed.String(),
		ExpiresAt: exp.Format(time.RFC3339),
	})
}
