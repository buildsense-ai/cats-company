package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	botRuntimeCredentialHeader    = "X-CatsCo-Runtime-Credential"
	botRuntimeCredentialTokenType = "bot_runtime_credential"
	botRuntimeCredentialIssuer    = "catscompany"
	botRuntimeCredentialAudience  = "catscompany-bot-runtime"
	botRuntimeCredentialKeyLabel  = "catscompany/bot-runtime-credential/v1"
	botRuntimeSkillMutationScope  = "skill_mutation:grant"

	defaultBotRuntimeCredentialTTL = 30 * 24 * time.Hour
	maxBotRuntimeCredentialTTL     = 30 * 24 * time.Hour
	botRuntimeCredentialClockSkew  = 5 * time.Second
)

// botRuntimeCredentialClaims is an owner-provisioned credential for one
// concrete Bot Runtime installation. Unlike the long-lived Bot API key, it is
// bound to a body and installation and carries only explicitly listed scopes.
// It is never accepted as ordinary user or Bot HTTP authentication.
type botRuntimeCredentialClaims struct {
	TokenType      string   `json:"token_type"`
	OwnerUID       int64    `json:"owner_uid"`
	BotUID         int64    `json:"bot_uid"`
	BodyID         string   `json:"body_id"`
	InstallationID string   `json:"installation_id"`
	Scopes         []string `json:"scopes"`
	jwt.RegisteredClaims
}

type botRuntimeCredentialInput struct {
	OwnerUID       int64
	BotUID         int64
	BodyID         string
	InstallationID string
	TTL            time.Duration
}

type botRuntimeCredentialSigner struct {
	key []byte
	now func() time.Time
}

func newBotRuntimeCredentialSigner(rootSecret []byte, now func() time.Time) (*botRuntimeCredentialSigner, error) {
	if len(rootSecret) < 32 {
		return nil, errors.New("Bot Runtime credential secret must contain at least 32 bytes")
	}
	if now == nil {
		now = time.Now
	}
	mac := hmac.New(sha256.New, rootSecret)
	_, _ = mac.Write([]byte(botRuntimeCredentialKeyLabel))
	return &botRuntimeCredentialSigner{key: mac.Sum(nil), now: now}, nil
}

func (s *botRuntimeCredentialSigner) issue(input botRuntimeCredentialInput) (string, *botRuntimeCredentialClaims, error) {
	if s == nil || len(s.key) == 0 || s.now == nil {
		return "", nil, errors.New("Bot Runtime credential signer is not configured")
	}
	if input.OwnerUID <= 0 || input.BotUID <= 0 {
		return "", nil, errors.New("owner and Bot ids are required")
	}
	bodyID, err := normalizeBotBodyID(input.BodyID)
	if err != nil || isLegacyBotBodyID(bodyID) {
		return "", nil, errors.New("valid non-legacy body_id is required")
	}
	installationID, err := normalizeUserDeviceID(input.InstallationID)
	if err != nil {
		return "", nil, errors.New("valid installation_id is required")
	}
	ttl := input.TTL
	if ttl == 0 {
		ttl = defaultBotRuntimeCredentialTTL
	}
	if ttl <= 0 || ttl > maxBotRuntimeCredentialTTL {
		return "", nil, errors.New("invalid Bot Runtime credential ttl")
	}
	jti, err := randomHex(16)
	if err != nil {
		return "", nil, fmt.Errorf("create Bot Runtime credential id: %w", err)
	}
	now := s.now().UTC()
	claims := &botRuntimeCredentialClaims{
		TokenType:      botRuntimeCredentialTokenType,
		OwnerUID:       input.OwnerUID,
		BotUID:         input.BotUID,
		BodyID:         bodyID,
		InstallationID: installationID,
		Scopes:         []string{botRuntimeSkillMutationScope},
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        "brc_" + jti,
			Issuer:    botRuntimeCredentialIssuer,
			Subject:   fmt.Sprintf("bot:%d:body:%s", input.BotUID, bodyID),
			Audience:  jwt.ClaimStrings{botRuntimeCredentialAudience},
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now.Add(-botRuntimeCredentialClockSkew)),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	raw, err := token.SignedString(s.key)
	if err != nil {
		return "", nil, fmt.Errorf("sign Bot Runtime credential: %w", err)
	}
	return raw, claims, nil
}

func (s *botRuntimeCredentialSigner) verify(raw string) (*botRuntimeCredentialClaims, error) {
	if s == nil || len(s.key) == 0 || s.now == nil {
		return nil, errors.New("Bot Runtime credential signer is not configured")
	}
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("Bot Runtime credential is required")
	}
	claims := &botRuntimeCredentialClaims{}
	parser := jwt.NewParser(
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithIssuer(botRuntimeCredentialIssuer),
		jwt.WithAudience(botRuntimeCredentialAudience),
		jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(),
		jwt.WithLeeway(botRuntimeCredentialClockSkew),
		jwt.WithTimeFunc(func() time.Time { return s.now().UTC() }),
	)
	token, err := parser.ParseWithClaims(strings.TrimSpace(raw), claims, func(token *jwt.Token) (interface{}, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("invalid Bot Runtime credential signing method")
		}
		return s.key, nil
	})
	if err != nil || token == nil || !token.Valid {
		if err == nil {
			err = errors.New("invalid Bot Runtime credential")
		}
		return nil, err
	}
	if err := validateBotRuntimeCredentialClaims(claims, s.now().UTC()); err != nil {
		return nil, err
	}
	return claims, nil
}

func validateBotRuntimeCredentialClaims(claims *botRuntimeCredentialClaims, now time.Time) error {
	if claims == nil || claims.TokenType != botRuntimeCredentialTokenType ||
		!strings.HasPrefix(claims.ID, "brc_") || len(claims.ID) <= len("brc_") ||
		claims.OwnerUID <= 0 || claims.BotUID <= 0 || claims.IssuedAt == nil ||
		claims.NotBefore == nil || claims.ExpiresAt == nil {
		return errors.New("invalid Bot Runtime credential claims")
	}
	bodyID, err := normalizeBotBodyID(claims.BodyID)
	if err != nil || bodyID != claims.BodyID || isLegacyBotBodyID(bodyID) {
		return errors.New("invalid Bot Runtime credential body")
	}
	installationID, err := normalizeUserDeviceID(claims.InstallationID)
	if err != nil || installationID != claims.InstallationID {
		return errors.New("invalid Bot Runtime credential installation")
	}
	issuedAt := claims.IssuedAt.Time.UTC()
	expiresAt := claims.ExpiresAt.Time.UTC()
	if !expiresAt.After(issuedAt) || expiresAt.Sub(issuedAt) > maxBotRuntimeCredentialTTL ||
		issuedAt.After(now.Add(botRuntimeCredentialClockSkew)) {
		return errors.New("invalid Bot Runtime credential lifetime")
	}
	if claims.Subject != fmt.Sprintf("bot:%d:body:%s", claims.BotUID, bodyID) ||
		!botRuntimeCredentialHasScope(claims, botRuntimeSkillMutationScope) {
		return errors.New("invalid Bot Runtime credential binding")
	}
	claims.BodyID = bodyID
	claims.InstallationID = installationID
	return nil
}

func botRuntimeCredentialHasScope(claims *botRuntimeCredentialClaims, scope string) bool {
	if claims == nil || strings.TrimSpace(scope) == "" {
		return false
	}
	for _, current := range claims.Scopes {
		if current == scope {
			return true
		}
	}
	return false
}

func extractBotRuntimeCredential(r *http.Request) string {
	if r == nil {
		return ""
	}
	return strings.TrimSpace(r.Header.Get(botRuntimeCredentialHeader))
}

type issueBotRuntimeCredentialRequest struct {
	BotUID         int64  `json:"bot_uid"`
	BodyID         string `json:"body_id"`
	InstallationID string `json:"installation_id"`
}

// HandleIssueRuntimeCredential lets a human Bot owner provision a scoped
// credential for one Runtime. The token is returned once and is not an
// alternative login credential for any ordinary CatsCo endpoint.
func (h *BotHandler) HandleIssueRuntimeCredential(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	ownerUID := UIDFromContext(r.Context())
	if ownerUID <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if h == nil || h.db == nil || h.hub == nil || h.hub.botRuntimeCredentials == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Bot Runtime credential service unavailable"})
		return
	}
	var req issueBotRuntimeCredentialRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	actualOwner, err := h.db.GetBotOwner(req.BotUID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "bot not found"})
		return
	}
	if actualOwner != ownerUID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not your bot"})
		return
	}
	raw, claims, err := h.hub.botRuntimeCredentials.issue(botRuntimeCredentialInput{
		OwnerUID: ownerUID, BotUID: req.BotUID, BodyID: req.BodyID, InstallationID: req.InstallationID,
	})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"bot_uid":         claims.BotUID,
		"body_id":         claims.BodyID,
		"installation_id": claims.InstallationID,
		"scopes":          claims.Scopes,
		"credential":      raw,
		"expires_at":      claims.ExpiresAt.Time.UnixMilli(),
	})
}
