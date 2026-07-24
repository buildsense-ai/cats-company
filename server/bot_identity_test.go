package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBotIdentityHandlerReturnsOnlyAuthenticatedUID(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/bot/identity", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(42)))
	recorder := httptest.NewRecorder()

	BotIdentityHandler(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Body.String(); got != "{\"uid\":\"42\"}\n" {
		t.Fatalf("unexpected identity response: %s", got)
	}
	if recorder.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("identity response must not be cached")
	}
}

func TestBotIdentityHandlerRejectsUnauthenticatedAndNonGET(t *testing.T) {
	unauthorized := httptest.NewRecorder()
	BotIdentityHandler(unauthorized, httptest.NewRequest(http.MethodGet, "/api/bot/identity", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}

	method := httptest.NewRecorder()
	BotIdentityHandler(method, httptest.NewRequest(http.MethodPost, "/api/bot/identity", nil))
	if method.Code != http.StatusMethodNotAllowed {
		t.Fatalf("method status = %d", method.Code)
	}
}
