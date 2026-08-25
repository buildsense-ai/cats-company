package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type desktopConnectTestStore struct {
	store.Store
	user *types.User
}

func (s *desktopConnectTestStore) GetUser(id int64) (*types.User, error) {
	if s.user != nil && s.user.ID == id {
		return s.user, nil
	}
	return nil, nil
}

func TestDesktopConnectSessionExchangeIsSingleUse(t *testing.T) {
	handler := NewDesktopConnectHandler(&desktopConnectTestStore{
		user: &types.User{ID: 42, Username: "demo", Email: "demo@example.com", DisplayName: "Demo"},
	})

	createReq := httptest.NewRequest(http.MethodPost, "/api/desktop-connect/session", nil)
	createReq = createReq.WithContext(context.WithValue(createReq.Context(), uidKey, int64(42)))
	createRec := httptest.NewRecorder()
	handler.HandleCreateSession(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status=%d body=%s", createRec.Code, createRec.Body.String())
	}
	var created struct {
		Code        string `json:"code"`
		DeepLinkURL string `json:"deeplink_url"`
	}
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	if created.Code == "" || created.DeepLinkURL == "" {
		t.Fatalf("missing code/deeplink in response: %+v", created)
	}

	body := []byte(`{"code":"` + created.Code + `"}`)
	exchangeReq := httptest.NewRequest(http.MethodPost, "/api/desktop-connect/exchange", bytes.NewReader(body))
	exchangeRec := httptest.NewRecorder()
	handler.HandleExchange(exchangeRec, exchangeReq)
	if exchangeRec.Code != http.StatusOK {
		t.Fatalf("exchange status=%d body=%s", exchangeRec.Code, exchangeRec.Body.String())
	}
	var exchanged struct {
		Token      string `json:"token"`
		UID        int64  `json:"uid"`
		Persistent bool   `json:"persistent"`
		ServerURL  string `json:"server_url"`
	}
	if err := json.Unmarshal(exchangeRec.Body.Bytes(), &exchanged); err != nil {
		t.Fatalf("decode exchange response: %v", err)
	}
	if exchanged.Token == "" || exchanged.UID != 42 || exchanged.ServerURL == "" {
		t.Fatalf("unexpected exchange response: %+v", exchanged)
	}
	if !exchanged.Persistent {
		t.Fatal("desktop exchange must return a persistent token")
	}
	claims, err := ParseToken(exchanged.Token)
	if err != nil {
		t.Fatalf("parse exchange token: %v", err)
	}
	if claims.TokenType != persistentUserTokenType || claims.ExpiresAt != nil {
		t.Fatalf("unexpected desktop token claims: type=%q expires_at=%v", claims.TokenType, claims.ExpiresAt)
	}

	reuseReq := httptest.NewRequest(http.MethodPost, "/api/desktop-connect/exchange", bytes.NewReader(body))
	reuseRec := httptest.NewRecorder()
	handler.HandleExchange(reuseRec, reuseReq)
	if reuseRec.Code != http.StatusConflict {
		t.Fatalf("reuse status=%d body=%s", reuseRec.Code, reuseRec.Body.String())
	}
}

func TestDesktopConnectConfiguredBaseURLsDeriveWebSocketURL(t *testing.T) {
	t.Setenv("CATSCO_PUBLIC_BASE_URL", "https://app.catsco.cc/")
	t.Setenv("CATSCO_PUBLIC_WS_URL", "")

	httpBaseURL, wsURL := configuredDesktopConnectBaseURLs()
	if httpBaseURL != "https://app.catsco.cc" {
		t.Fatalf("httpBaseURL=%q", httpBaseURL)
	}
	if wsURL != "wss://app.catsco.cc/v0/channels" {
		t.Fatalf("wsURL=%q", wsURL)
	}
}

func TestDesktopConnectConfiguredBaseURLsUseExplicitWebSocketURL(t *testing.T) {
	t.Setenv("CATSCO_PUBLIC_BASE_URL", "https://app.catsco.cc/")
	t.Setenv("CATSCO_PUBLIC_WS_URL", "wss://edge.catsco.cc/v0/channels/")

	httpBaseURL, wsURL := configuredDesktopConnectBaseURLs()
	if httpBaseURL != "https://app.catsco.cc" {
		t.Fatalf("httpBaseURL=%q", httpBaseURL)
	}
	if wsURL != "wss://edge.catsco.cc/v0/channels" {
		t.Fatalf("wsURL=%q", wsURL)
	}
}

func TestDesktopConnectUsesRequestCnOriginForMigrationEntrypoint(t *testing.T) {
	t.Setenv("CATSCO_PUBLIC_BASE_URL", "https://app.catsco.cc")
	t.Setenv("CATSCO_PUBLIC_WS_URL", "wss://app.catsco.cc/v0/channels")
	req := httptest.NewRequest(http.MethodPost, "https://app.catsco.cn/api/desktop-connect/session", nil)
	req.Host = "app.catsco.cn"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "app.catsco.cn")
	httpBaseURL, wsURL := desktopConnectBaseURLs(req)
	if httpBaseURL != "https://app.catsco.cn" || wsURL != "wss://app.catsco.cn/v0/channels" {
		t.Fatalf("request origin was not preserved: http=%q ws=%q", httpBaseURL, wsURL)
	}
}
