package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// 服务 token（设计 §5.1 / 计划① Task 5）：
// 格式 st1.<b64url(payload_json)>.<b64url(hmac_sha256(secret, "st1."+payload_b64))>
// 跨语言契约：签名覆盖字面量 "st1.<payload_b64>"，Go/Python 两侧不做重序列化。
// env 名：CATS_SERVICE_TOKEN_SECRET（平台与 relay 统一）。

const ServiceTokenPrefix = "st1."

var (
	ErrServiceTokenFormat    = errors.New("service token format invalid")
	ErrServiceTokenMalformed = errors.New("service token malformed")
	ErrServiceTokenExpired   = errors.New("service token expired")
	ErrServiceTokenInvalid   = errors.New("service token invalid")
)

type ServiceTokenClaims struct {
	V         int
	CallerUID int64
	Agent     string
	Models    []string
	RunID     string
	Purpose   string
	IssuedAt  time.Time
	ExpiresAt time.Time
}

type serviceTokenWire struct {
	V         int      `json:"v"`
	CallerUID int64    `json:"caller_uid"`
	Agent     string   `json:"agent,omitempty"`
	Models    []string `json:"models,omitempty"`
	RunID     string   `json:"run_id,omitempty"`
	Purpose   string   `json:"purpose,omitempty"`
	IssuedAt  int64    `json:"iat"`
	ExpiresAt int64    `json:"exp"`
}

func IssueServiceToken(secret string, claims ServiceTokenClaims) string {
	wire := serviceTokenWire{
		V:         1,
		CallerUID: claims.CallerUID,
		Agent:     claims.Agent,
		Models:    claims.Models,
		RunID:     claims.RunID,
		Purpose:   claims.Purpose,
		ExpiresAt: claims.ExpiresAt.Unix(),
	}
	if claims.IssuedAt.IsZero() {
		wire.IssuedAt = time.Now().Unix()
	} else {
		wire.IssuedAt = claims.IssuedAt.Unix()
	}
	if wire.Purpose == "" {
		wire.Purpose = "service"
	}
	payload, _ := json.Marshal(wire)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payload)
	signed := ServiceTokenPrefix + payloadB64
	sig := signServiceToken(secret, signed)
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func signServiceToken(secret, signed string) []byte {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(signed))
	return mac.Sum(nil)
}

func VerifyServiceToken(secret, token string) (ServiceTokenClaims, error) {
	if !strings.HasPrefix(token, ServiceTokenPrefix) {
		return ServiceTokenClaims{}, ErrServiceTokenFormat
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || parts[0] != "st1" {
		return ServiceTokenClaims{}, ErrServiceTokenFormat
	}
	payloadB64, sigB64 := parts[1], parts[2]
	signed := ServiceTokenPrefix + payloadB64
	expected := signServiceToken(secret, signed)
	actual, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return ServiceTokenClaims{}, ErrServiceTokenMalformed
	}
	if subtle.ConstantTimeCompare(expected, actual) != 1 {
		return ServiceTokenClaims{}, ErrServiceTokenInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return ServiceTokenClaims{}, ErrServiceTokenMalformed
	}
	var wire serviceTokenWire
	if err := json.Unmarshal(payload, &wire); err != nil {
		return ServiceTokenClaims{}, ErrServiceTokenMalformed
	}
	if wire.ExpiresAt <= 0 || wire.ExpiresAt < time.Now().Unix() {
		return ServiceTokenClaims{}, ErrServiceTokenExpired
	}
	return ServiceTokenClaims{
		V:         wire.V,
		CallerUID: wire.CallerUID,
		Agent:     wire.Agent,
		Models:    wire.Models,
		RunID:     wire.RunID,
		Purpose:   wire.Purpose,
		IssuedAt:  time.Unix(wire.IssuedAt, 0),
		ExpiresAt: time.Unix(wire.ExpiresAt, 0),
	}, nil
}
