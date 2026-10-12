package server

import (
	"net/http"
	"testing"
	"time"
)

// These tests exercise the platform-managed cloud worker enforcement in
// ServeWS: a bot with a non-empty cloud official body accepts only that body,
// repairs a drifted durable binding, and takes the active lease over from a
// foreign runtime (the 2026-10-12 incident shape).

func TestServeWSRejectsNonOfficialBodyForCloudWorker(t *testing.T) {
	botUID := int64(42)
	apiKey := GenerateAPIKey(botUID)
	botStore := &wsBotBodyStore{apiKey: apiKey, botUID: botUID, bodyID: "official-body", officialBodyID: "official-body"}
	wsURL, _, cleanup := newBotBodyTestServerWithStore(botStore)
	defer cleanup()

	conn, resp, err := dialBotBody(wsURL, apiKey, "some-other-body")
	closeResponse(resp)
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("expected a non-official body to be rejected for a cloud worker")
	}
	if resp == nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("non-official body status = %v, want 409", responseStatus(resp))
	}
}

func TestServeWSRejectsLegacyDialForCloudWorker(t *testing.T) {
	botUID := int64(42)
	apiKey := GenerateAPIKey(botUID)
	botStore := &wsBotBodyStore{apiKey: apiKey, botUID: botUID, bodyID: "official-body", officialBodyID: "official-body"}
	wsURL, _, cleanup := newBotBodyTestServerWithStore(botStore)
	defer cleanup()

	// Without CATSCO_REQUIRE_BOT_BODY_ID the legacy path accepts an ordinary
	// bot; a platform-managed worker must still be refused (any client that
	// cannot prove the official body is not the provisioned runtime).
	conn, resp, err := dialBotWithoutBody(wsURL, apiKey)
	closeResponse(resp)
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("expected a legacy dial to be rejected for a cloud worker")
	}
	if resp == nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("legacy cloud worker dial status = %v, want 409", responseStatus(resp))
	}
}

func TestServeWSRepairsDriftedCloudWorkerBinding(t *testing.T) {
	botUID := int64(42)
	apiKey := GenerateAPIKey(botUID)
	// The durable binding drifted to a foreign body while the worker was away.
	botStore := &wsBotBodyStore{apiKey: apiKey, botUID: botUID, bodyID: "foreign-body", officialBodyID: "official-body"}
	wsURL, hub, cleanup := newBotBodyTestServerWithStore(botStore)
	defer cleanup()

	conn, resp, err := dialBotBody(wsURL, apiKey, "official-body")
	closeResponse(resp)
	if err != nil {
		t.Fatalf("official body dial failed: %v", err)
	}
	defer conn.Close()
	waitForClientCount(t, hub, botUID, 1)

	if got, _ := botStore.GetBotBodyID(botUID); got != "official-body" {
		t.Fatalf("binding=%q want official-body (self-heal failed)", got)
	}
}

func TestServeWSTakesOverCloudWorkerLeaseFromForeignRuntime(t *testing.T) {
	botUID := int64(42)
	apiKey := GenerateAPIKey(botUID)
	// Start unmanaged: a local runtime connects normally and grabs the lease.
	botStore := &wsBotBodyStore{apiKey: apiKey, botUID: botUID}
	wsURL, hub, cleanup := newBotBodyTestServerWithStore(botStore)
	defer cleanup()

	foreign, resp, err := dialBotBody(wsURL, apiKey, "foreign-body")
	closeResponse(resp)
	if err != nil {
		t.Fatalf("foreign dial failed: %v", err)
	}
	defer foreign.Close()
	waitForClientCount(t, hub, botUID, 1)

	// The bot becomes platform-managed while the foreign runtime holds the
	// binding -- the shape of the 2026-10-12 incident (worker restarted and
	// was locked out by a local runtime that grabbed the binding offline).
	botStore.setOfficialBodyID("official-body")

	worker, resp, err := dialBotBody(wsURL, apiKey, "official-body")
	closeResponse(resp)
	if err != nil {
		t.Fatalf("official body dial failed: %v", err)
	}
	defer worker.Close()
	waitForClientCount(t, hub, botUID, 1)

	if got, _ := botStore.GetBotBodyID(botUID); got != "official-body" {
		t.Fatalf("binding=%q want official-body", got)
	}
	// The foreign runtime must have been disconnected by the takeover.
	foreign.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, readErr := foreign.ReadMessage(); readErr == nil {
		t.Fatal("expected the foreign runtime connection to be closed by the takeover")
	}
}
