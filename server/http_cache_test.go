package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestWriteJSONDisablesHTTPCaching(t *testing.T) {
	recorder := httptest.NewRecorder()

	writeJSON(recorder, http.StatusOK, map[string]string{"message": "private"})

	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}
