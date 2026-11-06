package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// The roster used to ship `status: "unknown"` as a literal for every worker.
// That is worse than omitting the key: `worker.status || fallback` in a client
// is then dead code, and no client can tell "the server never checked" from
// "the server checked and cannot decide". The roster has real answers —
// runtime_status per worker, deployment_status on create — so the constant is
// gone.
//
// This pins the absence, otherwise it is the kind of field that quietly comes
// back with a new placeholder value.
func TestCloudWorkerRosterHasNoPlaceholderStatus(t *testing.T) {
	h, store := newCloudWorkerTestHandler("7=1")
	store.ownerBots = []map[string]interface{}{
		{"id": int64(42), "username": "worker", "tenant_name": "bot-worker"},
	}

	rec := httptest.NewRecorder()
	h.HandleList(rec, cloudWorkerRequest(7, http.MethodGet, "/api/cloud-workers", nil))
	out := decodeCloudWorkerList(t, rec)

	workers, ok := out["workers"].([]interface{})
	if !ok || len(workers) != 1 {
		t.Fatalf("workers=%v want exactly one roster item", out["workers"])
	}
	worker, ok := workers[0].(map[string]interface{})
	if !ok {
		t.Fatalf("worker item is not an object: %v", workers[0])
	}
	if _, present := worker["status"]; present {
		t.Fatalf("roster still ships a placeholder status field: %v", worker)
	}
	// The authoritative field must still be there, so removing the placeholder
	// did not take real information with it.
	if _, present := worker["runtime_status"]; !present {
		t.Fatalf("roster lost runtime_status: %v", worker)
	}
}
