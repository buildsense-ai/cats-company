package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Transfer and status API behavior for platform-managed cloud workers: the
// owner-facing transfer API must refuse to move a cloud worker's binding to a
// local body, and body-status must flag the bot so local runtimes can refuse
// to start it.

func TestHandleTransferBotBodyRejectsCloudWorkerManagedBinding(t *testing.T) {
	store := &botBodyStatusStore{ownerUID: 7, bodyID: "official-body", officialBody: "official-body"}
	handler := NewBotHandler(store)
	handler.SetHub(NewHub(nil, nil))
	req := httptest.NewRequest(http.MethodPost, "/api/bots/body-transfer", strings.NewReader(`{"bot_uid":42,"body_id":"local-body"}`))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleTransferBotBody(rec, req)

	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d want=409 body=%s", rec.Code, rec.Body.String())
	}
	if store.bodyID != "official-body" {
		t.Fatalf("binding=%q want official-body (transfer must not move a cloud worker)", store.bodyID)
	}
	if !strings.Contains(rec.Body.String(), "cloud_worker_managed_binding") {
		t.Fatalf("body=%s want cloud_worker_managed_binding code", rec.Body.String())
	}
}

func TestHandleGetBotBodyStatusMarksCloudDeployed(t *testing.T) {
	store := &botBodyStatusStore{ownerUID: 7, bodyID: "official-body", officialBody: "official-body"}
	handler := NewBotHandler(store)
	handler.SetHub(NewHub(nil, nil))
	req := httptest.NewRequest(http.MethodGet, "/api/bots/body-status?uid=42", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleGetBotBodyStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"cloud_deployed":true`) {
		t.Fatalf("body=%s want cloud_deployed true", rec.Body.String())
	}
}

func TestHandleGetBotBodyStatusOmitsCloudDeployedForOrdinaryBots(t *testing.T) {
	store := &botBodyStatusStore{ownerUID: 7, bodyID: "body-a"}
	handler := NewBotHandler(store)
	handler.SetHub(NewHub(nil, nil))
	req := httptest.NewRequest(http.MethodGet, "/api/bots/body-status?uid=42", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()

	handler.HandleGetBotBodyStatus(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "cloud_deployed") {
		t.Fatalf("body=%s must not carry cloud_deployed for an ordinary bot", rec.Body.String())
	}
}
