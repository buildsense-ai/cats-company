package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestCloudWorkerRuntimeStatusRequiresItsRegisteredLiveRuntime(t *testing.T) {
	for _, tc := range []struct {
		name               string
		owner, bot         int64
		role, model        string
		connected, expired bool
		want               string
	}{
		{"ready", 7, 42, "server", "test-model", true, false, "connected"},
		{"other owner", 8, 42, "server", "test-model", true, false, "not_connected"},
		{"other bot", 7, 43, "server", "test-model", true, false, "not_connected"},
		{"desktop", 7, 42, "desktop", "test-model", true, false, "not_connected"},
		{"model absent", 7, 42, "server", "", true, false, "not_connected"},
		{"stale registration", 7, 42, "server", "test-model", false, false, "not_connected"},
		{"expired registration", 7, 42, "server", "test-model", true, true, "not_connected"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, store := newCloudWorkerTestHandler("7=1")
			hub := NewHub(nil, nil)
			h.bots.SetHub(hub)
			device, err := hub.userDevices.register(tc.owner, RegisterUserDeviceRequest{
				BotUID: tc.bot, DeviceID: "cloud-device", BodyID: "cloud-body", InstallationID: "cloud-install",
				RuntimeRole: tc.role, Status: "online", Capabilities: []string{"read_file"},
				ModelStatus: &DeviceModelStatus{Source: "relay", Model: tc.model},
			})
			if err != nil {
				t.Fatal(err)
			}
			client := &Client{uid: tc.bot, accountType: types.AccountBot, bodyID: device.BodyID,
				installationID: device.InstallationID, send: make(chan []byte, 4)}
			if tc.connected {
				hub.addClient(client)
				hub.bindDeviceClient(tc.owner, device, client)
			}
			if tc.expired {
				hub.userDevices.now = func() time.Time { return time.Now().Add(24 * time.Hour) }
			}
			if got := h.cloudWorkerRuntimeStatus(7, 42); got != tc.want {
				t.Fatalf("got %s want %s", got, tc.want)
			}
			store.ownerBots = []map[string]interface{}{{"id": int64(42), "username": "worker", "tenant_name": "bot-worker"}}
			rec := httptest.NewRecorder()
			h.HandleList(rec, cloudWorkerRequest(7, http.MethodGet, "/api/cloud-workers", nil))
			out := decodeCloudWorkerList(t, rec)
			worker := out["workers"].([]interface{})[0].(map[string]interface{})
			if worker["runtime_status"] != tc.want {
				t.Fatalf("roster: %v", worker)
			}
			if tc.want == "connected" {
				hub.unbindDeviceClient(client)
				hub.removeClient(client)
				if got := h.cloudWorkerRuntimeStatus(7, 42); got != "not_connected" {
					t.Fatalf("disconnected runtime: got %s", got)
				}
			}
		})
	}
}
