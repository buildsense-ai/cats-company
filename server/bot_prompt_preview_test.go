package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOwnerPromptPreview(t *testing.T) {
	admin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/users/7/key/reveal" || r.Method != http.MethodPost {
			t.Errorf("unexpected credential scope %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"configured":true,"key":{"state":"active","key":"test-token"}}`))
	}))
	defer admin.Close()
	model := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.URL.Path != "/v1/messages" || r.Header.Get("x-api-key") != "test-token" || body["model"] != previewModel || body["tools"] != nil ||
			!strings.Contains(body["system"].(string), "draft setting") {
			t.Error("unexpected text preview request")
		}
		_, _ = w.Write([]byte(`{"content":[{"type":"thinking","text":"private"},{"type":"text","text":"trial answer"}]}`))
	}))
	defer model.Close()
	t.Setenv("CATS_RELAY_ANTHROPIC_BASE_URL", model.URL)
	db := &botDefinitionTestStore{owners: map[int64]int64{43: 7}}
	models := &botModelConfigTestStore{owners: db.owners}
	config := NewBotModelConfigHandler(db, models)
	config.SetRelayUsageClient(&RelayAdminClient{baseURL: admin.URL, token: "test", client: admin.Client()})
	handler := NewBotDefinitionHandler(db, db, models, config)
	for _, tc := range []struct {
		owner  int64
		input  string
		status int
	}{
		{0, "{}", http.StatusUnauthorized},
		{8, "{}", http.StatusForbidden},
		{7, `{"question":" "}`, http.StatusBadRequest},
		{7, `{"prompt":"draft setting","question":"what next?"}`, http.StatusOK},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/bots/definition/prompt-preview?uid=43", strings.NewReader(tc.input))
		req = req.WithContext(context.WithValue(req.Context(), uidKey, tc.owner))
		rec := httptest.NewRecorder()
		handler.handleOwnerPromptPreview(rec, req, model.Client())
		if rec.Code != tc.status {
			t.Fatalf("status %d, want %d: %s", rec.Code, tc.status, rec.Body)
		}
		if tc.status == http.StatusOK {
			var response map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || len(response) != 1 || response["answer"] != "trial answer" {
				t.Fatalf("unexpected preview response: %s", rec.Body)
			}
		}
	}
	if len(db.records) != 0 {
		t.Fatal("preview wrote a definition")
	}
}
