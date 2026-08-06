package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCORSMiddlewareAllowsRawUploadHeaders(t *testing.T) {
	nextCalled := false
	handler := CORSMiddleware(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		nextCalled = true
	}))
	req := httptest.NewRequest(http.MethodOptions, "/api/upload?type=image&raw=1", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if nextCalled {
		t.Fatal("preflight request reached the wrapped handler")
	}
	if got := rec.Header().Get("Access-Control-Allow-Headers"); got != allowedCORSHeaders {
		t.Fatalf("Access-Control-Allow-Headers = %q, want %q", got, allowedCORSHeaders)
	}
}
