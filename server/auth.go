// Package server implements Cats Company authentication middleware with JWT and API Key.
package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// jwtSecret is the signing key. In production, load from env/config.
var jwtSecret []byte

func init() {
	// Generate a random secret on startup. Override with OC_JWT_SECRET env var.
	b := make([]byte, 32)
	rand.Read(b)
	jwtSecret = b
}

// SetJWTSecret allows overriding the JWT secret (e.g., from env).
func SetJWTSecret(secret string) {
	if secret != "" {
		jwtSecret = []byte(secret)
	}
}

// JWTClaims defines the claims stored in the token.
type JWTClaims struct {
	TokenType string `json:"token_type,omitempty"`
	UID       int64  `json:"userId"` // 改为 userId 与 Token 项目统一
	Username  string `json:"username"`
	Email     string `json:"email"` // 添加 email
	jwt.RegisteredClaims
}

const (
	userTokenType           = "user"
	persistentUserTokenType = "user_persistent"
)

// GenerateToken creates a seven-day signed JWT for ordinary web sessions.
func GenerateToken(uid int64, username string, email string) (string, error) {
	return generateUserToken(uid, username, email, false)
}

// GeneratePersistentUserToken creates a non-expiring JWT for a trusted CatsCo
// desktop installation. Account state and signing-key rotation still revoke it.
func GeneratePersistentUserToken(uid int64, username string, email string) (string, error) {
	return generateUserToken(uid, username, email, true)
}

func generateUserToken(uid int64, username string, email string, persistent bool) (string, error) {
	tokenType := userTokenType
	registered := jwt.RegisteredClaims{
		IssuedAt: jwt.NewNumericDate(time.Now()),
		Issuer:   "catscompany",
	}
	if persistent {
		tokenType = persistentUserTokenType
	} else {
		registered.ExpiresAt = jwt.NewNumericDate(time.Now().Add(7 * 24 * time.Hour))
	}
	claims := JWTClaims{
		TokenType:        tokenType,
		UID:              uid,
		Username:         username,
		Email:            email,
		RegisteredClaims: registered,
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(jwtSecret)
}

// GenerateRefreshToken creates a long-lived refresh token.
func GenerateRefreshToken() string {
	b := make([]byte, 32)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ParseToken validates a JWT and returns the claims.
func ParseToken(tokenStr string) (*JWTClaims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &JWTClaims{}, func(t *jwt.Token) (interface{}, error) {
		return jwtSecret, nil
	})
	if err != nil {
		return nil, err
	}
	if claims, ok := token.Claims.(*JWTClaims); ok && token.Valid {
		if claims.TokenType != "" && claims.TokenType != userTokenType && claims.TokenType != persistentUserTokenType {
			return nil, fmt.Errorf("unsupported token type %q", claims.TokenType)
		}
		return claims, nil
	}
	return nil, jwt.ErrSignatureInvalid
}

// GenerateAPIKey creates an API Key for a bot with the given uid.
// Format: "cc_" + hex(uid) + "_" + random(32bytes)
func GenerateAPIKey(uid int64) string {
	b := make([]byte, 32)
	rand.Read(b)
	return fmt.Sprintf("cc_%x_%s", uid, hex.EncodeToString(b))
}

// ParseAPIKey validates an API Key format and extracts the uid.
// It only parses the format; the caller must verify the key exists in the database.
func ParseAPIKey(key string) (int64, error) {
	if !strings.HasPrefix(key, "cc_") {
		return 0, fmt.Errorf("invalid api key prefix")
	}
	rest := key[3:] // strip "cc_"
	idx := strings.Index(rest, "_")
	if idx <= 0 {
		return 0, fmt.Errorf("invalid api key format")
	}
	uidHex := rest[:idx]
	uid, err := strconv.ParseInt(uidHex, 16, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid uid in api key: %w", err)
	}
	return uid, nil
}

type contextKey string

const uidKey contextKey = "uid"
const usernameKey contextKey = "username"

func contextWithClaims(ctx context.Context, claims *JWTClaims) context.Context {
	ctx = context.WithValue(ctx, uidKey, claims.UID)
	ctx = context.WithValue(ctx, usernameKey, claims.Username)
	return ctx
}

func activeUserFromClaims(claims *JWTClaims, lookupUser func(int64) (*types.User, error)) (*types.User, int, string) {
	return activeUserByID(claims.UID, lookupUser)
}

func activeUserByID(uid int64, lookupUser func(int64) (*types.User, error)) (*types.User, int, string) {
	user, err := lookupUser(uid)
	if err != nil {
		return nil, http.StatusInternalServerError, "authentication service unavailable"
	}
	if user == nil {
		return nil, http.StatusUnauthorized, "invalid or expired token"
	}
	if user.State != 0 {
		return nil, http.StatusForbidden, "user account is disabled"
	}
	return user, 0, ""
}

// AuthMiddleware extracts the JWT token and sets uid in context.
func AuthMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenStr := extractToken(r)
		if tokenStr == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		claims, err := ParseToken(tokenStr)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired token"})
			return
		}

		ctx := contextWithClaims(r.Context(), claims)
		next(w, r.WithContext(ctx))
	}
}

// JWTAuthMiddlewareWithDB requires a valid, active user JWT.
func JWTAuthMiddlewareWithDB(db store.Store) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			tokenStr := extractToken(r)
			if tokenStr == "" {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}

			claims, err := ParseToken(tokenStr)
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired token"})
				return
			}
			if _, status, msg := activeUserFromClaims(claims, db.GetUser); status != 0 {
				writeJSON(w, status, map[string]string{"error": msg})
				return
			}

			ctx := contextWithClaims(r.Context(), claims)
			next(w, r.WithContext(ctx))
		}
	}
}

func botAPIKeyUID(r *http.Request, db store.Store) (int64, int, string) {
	apiKey := extractAPIKey(r)
	return botAPIKeyUIDValue(apiKey, db)
}

func botAPIKeyUIDValue(apiKey string, db store.Store) (int64, int, string) {
	if apiKey == "" {
		return 0, http.StatusUnauthorized, "unauthorized"
	}
	parsedUID, err := ParseAPIKey(apiKey)
	if err != nil {
		return 0, http.StatusUnauthorized, "unauthorized"
	}
	botUID, err := db.GetBotByAPIKey(apiKey)
	if err != nil || botUID != parsedUID {
		return 0, http.StatusUnauthorized, "unauthorized"
	}
	if _, status, msg := activeUserByID(parsedUID, db.GetUser); status != 0 {
		return 0, status, msg
	}
	return parsedUID, 0, ""
}

// OpenAICompatibleAuthMiddlewareWithDB accepts ordinary CatsCo JWTs and bot
// API keys using either CatsCo's historical `ApiKey` authorization scheme or
// the OpenAI SDK's standard `Bearer` scheme. Keep this middleware scoped to
// OpenAI-compatible API routes: accepting a bot key as Bearer globally would
// blur the authentication contract of unrelated CatsCo endpoints.
func OpenAICompatibleAuthMiddlewareWithDB(db store.Store) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			// Preserve normal CatsCo user-session authentication first.
			tokenStr := extractToken(r)
			if tokenStr != "" {
				claims, err := ParseToken(tokenStr)
				if err == nil {
					if _, status, msg := activeUserFromClaims(claims, db.GetUser); status != 0 {
						writeJSON(w, status, map[string]string{"error": msg})
						return
					}
					ctx := contextWithClaims(r.Context(), claims)
					next(w, r.WithContext(ctx))
					return
				}

				// OpenAI clients send api_key as a Bearer token. Only attempt bot
				// key authentication for CatsCo-shaped keys, avoiding database
				// lookup and ambiguous semantics for arbitrary invalid JWTs.
				if strings.HasPrefix(tokenStr, "cc_") {
					if uid, status, msg := botAPIKeyUIDValue(tokenStr, db); status == 0 {
						ctx := context.WithValue(r.Context(), uidKey, uid)
						next(w, r.WithContext(ctx))
						return
					} else if status == http.StatusForbidden {
						writeJSON(w, status, map[string]string{"error": msg})
						return
					}
				}
			}

			// Retain the historical CatsCo `ApiKey` scheme for existing bots.
			if uid, status, msg := botAPIKeyUID(r, db); status == 0 {
				ctx := context.WithValue(r.Context(), uidKey, uid)
				next(w, r.WithContext(ctx))
				return
			} else if status == http.StatusForbidden {
				writeJSON(w, status, map[string]string{"error": msg})
				return
			}

			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		}
	}
}

// BotAPIKeyMiddlewareWithDB requires an active bot API key and never accepts JWTs.
// Use it for runtime endpoints that may return bot-only credentials.
func BotAPIKeyMiddlewareWithDB(db store.Store) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			uid, status, msg := botAPIKeyUID(r, db)
			if status != 0 {
				writeJSON(w, status, map[string]string{"error": msg})
				return
			}
			ctx := context.WithValue(r.Context(), uidKey, uid)
			next(w, r.WithContext(ctx))
		}
	}
}

// AuthMiddlewareWithDB returns an auth middleware that accepts both JWT and API Key.
// JWT is tried first; on failure, it falls back to API Key authentication.
func AuthMiddlewareWithDB(db store.Store) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			// Try JWT first
			tokenStr := extractToken(r)
			if tokenStr != "" {
				claims, err := ParseToken(tokenStr)
				if err == nil {
					if _, status, msg := activeUserFromClaims(claims, db.GetUser); status != 0 {
						writeJSON(w, status, map[string]string{"error": msg})
						return
					}
					ctx := contextWithClaims(r.Context(), claims)
					next(w, r.WithContext(ctx))
					return
				}
			}

			// Fallback: API Key from header or query param
			if uid, status, msg := botAPIKeyUID(r, db); status == 0 {
				ctx := context.WithValue(r.Context(), uidKey, uid)
				next(w, r.WithContext(ctx))
				return
			} else if status == http.StatusForbidden {
				writeJSON(w, status, map[string]string{"error": msg})
				return
			}

			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		}
	}
}

func ownerClaimsFromRequest(r *http.Request, lookupUser func(int64) (*types.User, error)) (*JWTClaims, int, string) {
	tokenStr := extractToken(r)
	if tokenStr == "" {
		return nil, http.StatusUnauthorized, "unauthorized"
	}

	claims, err := ParseToken(tokenStr)
	if err != nil {
		return nil, http.StatusUnauthorized, "invalid or expired token"
	}

	user, status, msg := activeUserFromClaims(claims, lookupUser)
	if status != 0 {
		return nil, status, msg
	}

	if user.AccountType != types.AccountHuman {
		return nil, http.StatusForbidden, "owner access requires a human user token"
	}

	return claims, 0, ""
}

// OwnerMiddlewareWithDB requires a valid human-user JWT.
// It is intended for owner-management endpoints, not bot runtime endpoints.
func OwnerMiddlewareWithDB(db store.Store) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			claims, status, msg := ownerClaimsFromRequest(r, db.GetUser)
			if status != 0 {
				writeJSON(w, status, map[string]string{"error": msg})
				return
			}

			ctx := contextWithClaims(r.Context(), claims)
			next(w, r.WithContext(ctx))
		}
	}
}

// AdminMiddleware requires a valid JWT and a username listed in OC_ADMIN_USERNAMES.
func AdminMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tokenStr := extractToken(r)
		if tokenStr == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}

		claims, err := ParseToken(tokenStr)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired token"})
			return
		}

		if !isAdminUsername(claims.Username) {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin access required"})
			return
		}

		ctx := contextWithClaims(r.Context(), claims)
		next(w, r.WithContext(ctx))
	}
}

// AdminMiddlewareWithDB requires an active user JWT and a username listed in OC_ADMIN_USERNAMES.
func AdminMiddlewareWithDB(db store.Store) func(http.HandlerFunc) http.HandlerFunc {
	return func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			tokenStr := extractToken(r)
			if tokenStr == "" {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}

			claims, err := ParseToken(tokenStr)
			if err != nil {
				writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid or expired token"})
				return
			}

			if _, status, msg := activeUserFromClaims(claims, db.GetUser); status != 0 {
				writeJSON(w, status, map[string]string{"error": msg})
				return
			}

			if !isAdminUsername(claims.Username) {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "admin access required"})
				return
			}

			ctx := contextWithClaims(r.Context(), claims)
			next(w, r.WithContext(ctx))
		}
	}
}

// UIDFromContext extracts the user ID from the request context.
func UIDFromContext(ctx context.Context) int64 {
	uid, _ := ctx.Value(uidKey).(int64)
	return uid
}

// UsernameFromContext extracts the username from the request context.
func UsernameFromContext(ctx context.Context) string {
	username, _ := ctx.Value(usernameKey).(string)
	return username
}

// extractToken gets the token from Authorization header or query param.
func extractToken(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		return strings.TrimPrefix(auth, "Bearer ")
	}
	return r.URL.Query().Get("token")
}

// extractAPIKey gets the API key from Authorization header or query param.
func extractAPIKey(r *http.Request) string {
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "ApiKey ") {
		return strings.TrimPrefix(auth, "ApiKey ")
	}
	return r.URL.Query().Get("api_key")
}

func isAdminUsername(username string) bool {
	if username == "" {
		return false
	}

	allowed := os.Getenv("OC_ADMIN_USERNAMES")
	if allowed == "" {
		return false
	}

	for _, candidate := range strings.Split(allowed, ",") {
		if strings.TrimSpace(candidate) == username {
			return true
		}
	}
	return false
}
