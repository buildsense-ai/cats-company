package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
)

func TestShimoConnectorUsesRemoteWorkerWithoutExposingActorIdentity(t *testing.T) {
	const workerToken = "worker-token-that-is-longer-than-thirty-two-characters"
	var mu sync.Mutex
	states := make(map[string]string)
	var bodies []map[string]any
	worker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+workerToken {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": map[string]any{"code": "UNAUTHORIZED", "message": "bad token"}})
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["agent_uid"] != nil || body["actor_user_id"] != nil || body["task_ref"] != nil {
			t.Fatalf("canonical actor identity leaked to worker: %#v", body)
		}
		binding, _ := body["connection_binding"].(string)
		if len(binding) != 32 {
			t.Fatalf("invalid opaque binding %q", binding)
		}
		mu.Lock()
		bodies = append(bodies, body)
		state := states[binding]
		mu.Unlock()
		switch r.URL.Path {
		case "/v1/sessions/status":
			if state == "" {
				state = "disconnected"
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"state": state}})
		case "/v1/sessions/login":
			mu.Lock()
			states[binding] = "connecting"
			mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"login_url": "https://app.catsco.test/shimo-login/" + strings.Repeat("c", 64) + "/"}})
		case "/v1/sheets/read":
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"extracted_at": "2026-09-10T12:00:00Z", "values": [][]any{{"金额"}, {8500}}}})
		case "/v1/sessions/disconnect":
			mu.Lock()
			delete(states, binding)
			mu.Unlock()
			writeJSON(w, http.StatusOK, map[string]any{"ok": true, "data": map[string]any{"state": "disconnected"}})
		default:
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": map[string]any{"code": "NOT_FOUND", "message": "not found"}})
		}
	}))
	defer worker.Close()

	backend, err := newHTTPShimoWorkerBackend(worker.URL, workerToken, worker.URL+"/internal/shimo/login-complete", shimoTestSecret)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewShimoConnectorHandler(ShimoConnectorOptions{
		ActorSecret: shimoTestSecret, PublicURL: "https://app.catsco.test", Backend: backend, WorkerToken: workerToken,
	})
	var resumed ShimoLoginResume
	handler.SetLoginResumePublisher(func(value ShimoLoginResume) bool { resumed = value; return true })
	token := mustShimoActorToken(t, "usr42", "usr7", "catsco:p2p_7_42:91")

	link := callShimoJSON(t, handler.HandleConnectionLink, http.MethodPost, "/v1/shimo/connection-link", token, "")
	connectionURL, err := url.Parse(nestedString(link.body, "data", "connection_url"))
	if err != nil {
		t.Fatal(err)
	}
	loginPage := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, connectionURL.Path, nil)
	handler.HandleLoginAttempt(loginPage, request)
	if loginPage.Code != http.StatusSeeOther || !strings.Contains(loginPage.Header().Get("Location"), "/shimo-login/") {
		t.Fatalf("login redirect status=%d location=%q", loginPage.Code, loginPage.Header().Get("Location"))
	}
	mu.Lock()
	loginBody := bodies[len(bodies)-1]
	mu.Unlock()
	completionToken := stringValueForTest(loginBody["completion_token"])
	if len(completionToken) != 64 || stringValueForTest(loginBody["completion_callback_url"]) != worker.URL+"/internal/shimo/login-complete" {
		t.Fatalf("missing completion callback grant: %#v", loginBody)
	}
	callback := httptest.NewRequest(http.MethodPost, "/internal/shimo/login-complete", strings.NewReader(`{"completion_token":"`+completionToken+`"}`))
	callback.Header.Set("Authorization", "Bearer "+workerToken)
	callback.Header.Set("Content-Type", "application/json")
	callbackResponse := httptest.NewRecorder()
	handler.HandleLoginComplete(callbackResponse, callback)
	if callbackResponse.Code != http.StatusAccepted || resumed.AgentUID != 42 || resumed.ActorUID != 7 || resumed.TopicID != "p2p_7_42" || resumed.MessageID != 91 {
		t.Fatalf("callback status=%d resume=%#v body=%s", callbackResponse.Code, resumed, callbackResponse.Body.String())
	}
	reusedRequest := httptest.NewRequest(http.MethodPost, "/internal/shimo/login-complete", strings.NewReader(`{"completion_token":"`+completionToken+`"}`))
	reusedRequest.Header.Set("Authorization", "Bearer "+workerToken)
	reusedRequest.Header.Set("Content-Type", "application/json")
	reusedCallback := httptest.NewRecorder()
	handler.HandleLoginComplete(reusedCallback, reusedRequest)
	if reusedCallback.Code != http.StatusGone {
		t.Fatalf("reused callback status=%d want=%d", reusedCallback.Code, http.StatusGone)
	}

	status := callShimoJSON(t, handler.HandleConnection, http.MethodGet, "/v1/shimo/connection", token, "")
	if got := nestedString(status.body, "data", "state"); got != "connecting" {
		t.Fatalf("state=%q", got)
	}
	mu.Lock()
	binding := stringValueForTest(bodies[0]["connection_binding"])
	states[binding] = "connected"
	mu.Unlock()
	read := callShimoJSON(t, handler.HandleReadSheet, http.MethodPost, "/v1/shimo/sheets/read", token,
		`{"url":"https://shimo.im/sheets/abc/","sheet_name":"项目表","range":"A1:C20"}`)
	assertShimoStatus(t, read, http.StatusOK, true)
	values := read.body["data"].(map[string]any)["values"].([]any)
	if len(values) != 2 {
		t.Fatalf("unexpected values %#v", values)
	}

	disconnect := callShimoJSON(t, handler.HandleConnection, http.MethodDelete, "/v1/shimo/connection", token, "")
	assertShimoStatus(t, disconnect, http.StatusOK, true)
}
