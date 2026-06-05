package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

func TestDeviceConnectorTokenIsNotAcceptedAsUserJWT(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("device-connector-token-test")

	token, err := GenerateDeviceConnectorToken(DeviceConnectorTokenInput{
		UID:      7,
		Username: "alice",
		DeviceID: "alice-laptop",
	})
	if err != nil {
		t.Fatalf("GenerateDeviceConnectorToken: %v", err)
	}
	if _, err := ParseDeviceConnectorToken(token); err != nil {
		t.Fatalf("ParseDeviceConnectorToken: %v", err)
	}
	if _, err := ParseToken(token); err == nil {
		t.Fatal("device connector token must not parse as ordinary user JWT")
	}

	store := &deviceHandlerStore{users: map[int64]*types.User{
		7: {ID: 7, Username: "alice", AccountType: types.AccountHuman, State: 0},
	}}
	handler := AuthMiddlewareWithDB(store)(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodGet, "/api/messages", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("device connector token status=%d, want 401", rec.Code)
	}
}

func TestDeviceConnectorPairingEnrollmentAndScopedRegistration(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("device-connector-enroll-test")

	store := &deviceHandlerStore{users: map[int64]*types.User{
		7: {ID: 7, Username: "alice", AccountType: types.AccountHuman, State: 0},
	}}
	hub := NewHub(store, nil)
	handler := NewDeviceConnectorHandler(store, hub)

	pairingReq := httptest.NewRequest(http.MethodPost, "/api/device-connectors/pairings", bytes.NewBufferString(`{
		"device_name": "Alice Laptop",
		"capabilities": ["read_file", "glob", "write_file"]
	}`))
	pairingReq = pairingReq.WithContext(context.WithValue(pairingReq.Context(), uidKey, int64(7)))
	pairingRec := httptest.NewRecorder()
	handler.HandleCreatePairing(pairingRec, pairingReq)
	if pairingRec.Code != http.StatusOK {
		t.Fatalf("create pairing status=%d body=%s", pairingRec.Code, pairingRec.Body.String())
	}
	var pairing struct {
		PairingID   string `json:"pairing_id"`
		PairingCode string `json:"pairing_code"`
	}
	if err := json.Unmarshal(pairingRec.Body.Bytes(), &pairing); err != nil {
		t.Fatalf("decode pairing: %v", err)
	}
	if pairing.PairingCode == "" {
		t.Fatal("pairing code missing")
	}

	enrollReq := httptest.NewRequest(http.MethodPost, "/api/device-connectors/enroll", bytes.NewBufferString(`{
		"pairing_code": "`+pairing.PairingCode+`",
		"device_id": "alice-laptop",
		"installation_id": "install-alice",
		"device_name": "Alice Laptop",
		"capabilities": ["read_file", "glob"]
	}`))
	enrollRec := httptest.NewRecorder()
	handler.HandleEnroll(enrollRec, enrollReq)
	if enrollRec.Code != http.StatusOK {
		t.Fatalf("enroll status=%d body=%s", enrollRec.Code, enrollRec.Body.String())
	}
	var enroll struct {
		ConnectorToken string     `json:"connector_token"`
		Device         UserDevice `json:"device"`
	}
	if err := json.Unmarshal(enrollRec.Body.Bytes(), &enroll); err != nil {
		t.Fatalf("decode enroll: %v", err)
	}
	if enroll.ConnectorToken == "" || enroll.Device.DeviceID != "alice-laptop" {
		t.Fatalf("unexpected enroll response: %#v", enroll)
	}

	registerReq := httptest.NewRequest(http.MethodPost, "/api/device-connectors/register", bytes.NewBufferString(`{
		"device_id": "mallory-desktop",
		"display_name": "Mallory Desktop",
		"capabilities": ["execute_shell"]
	}`))
	registerReq.Header.Set("Authorization", "DeviceConnector "+enroll.ConnectorToken)
	registerRec := httptest.NewRecorder()
	handler.HandleRegisterDevice(registerRec, registerReq)
	if registerRec.Code != http.StatusOK {
		t.Fatalf("register status=%d body=%s", registerRec.Code, registerRec.Body.String())
	}
	if _, ok := hub.userDevices.activeDevice(7, "mallory-desktop"); ok {
		t.Fatal("connector token must not register a different device_id")
	}
	device, ok := hub.userDevices.activeDevice(7, "alice-laptop")
	if !ok {
		t.Fatal("expected token-bound device to be active")
	}
	if len(device.Capabilities) != 2 || device.Capabilities[0] != DeviceGrantReadFile || device.Capabilities[1] != DeviceGrantGlob {
		t.Fatalf("registration should be limited to token capabilities, got %#v", device.Capabilities)
	}

	statusReq := httptest.NewRequest(http.MethodGet, "/api/device-connectors/pairings/"+pairing.PairingID, nil)
	statusReq = statusReq.WithContext(context.WithValue(statusReq.Context(), uidKey, int64(7)))
	statusRec := httptest.NewRecorder()
	handler.HandlePairingByID(statusRec, statusReq)
	if statusRec.Code != http.StatusOK || !strings.Contains(statusRec.Body.String(), `"status":"consumed"`) {
		t.Fatalf("consumed pairing status=%d body=%s", statusRec.Code, statusRec.Body.String())
	}
}

func TestDeviceConnectorEnrollmentCannotEscalatePairingCapabilities(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("device-connector-capability-test")

	store := &deviceHandlerStore{users: map[int64]*types.User{
		7: {ID: 7, Username: "alice", AccountType: types.AccountHuman, State: 0},
	}}
	hub := NewHub(store, nil)
	handler := NewDeviceConnectorHandler(store, hub)

	pairingReq := httptest.NewRequest(http.MethodPost, "/api/device-connectors/pairings", bytes.NewBufferString(`{
		"device_name": "Read Only Device",
		"capabilities": ["read_file"]
	}`))
	pairingReq = pairingReq.WithContext(context.WithValue(pairingReq.Context(), uidKey, int64(7)))
	pairingRec := httptest.NewRecorder()
	handler.HandleCreatePairing(pairingRec, pairingReq)
	if pairingRec.Code != http.StatusOK {
		t.Fatalf("create pairing status=%d body=%s", pairingRec.Code, pairingRec.Body.String())
	}
	var pairing struct {
		PairingCode string `json:"pairing_code"`
	}
	if err := json.Unmarshal(pairingRec.Body.Bytes(), &pairing); err != nil {
		t.Fatalf("decode pairing: %v", err)
	}

	enrollReq := httptest.NewRequest(http.MethodPost, "/api/device-connectors/enroll", bytes.NewBufferString(`{
		"pairing_code": "`+pairing.PairingCode+`",
		"device_id": "alice-laptop",
		"capabilities": ["read_file", "execute_shell", "write_file"]
	}`))
	enrollRec := httptest.NewRecorder()
	handler.HandleEnroll(enrollRec, enrollReq)
	if enrollRec.Code != http.StatusOK {
		t.Fatalf("enroll status=%d body=%s", enrollRec.Code, enrollRec.Body.String())
	}
	var enroll struct {
		ConnectorToken string     `json:"connector_token"`
		Device         UserDevice `json:"device"`
	}
	if err := json.Unmarshal(enrollRec.Body.Bytes(), &enroll); err != nil {
		t.Fatalf("decode enroll: %v", err)
	}
	if got := enroll.Device.Capabilities; len(got) != 1 || got[0] != DeviceGrantReadFile {
		t.Fatalf("device capabilities escalated beyond pairing: %#v", got)
	}
	claims, err := ParseDeviceConnectorToken(enroll.ConnectorToken)
	if err != nil {
		t.Fatalf("ParseDeviceConnectorToken: %v", err)
	}
	if len(claims.Capabilities) != 1 || claims.Capabilities[0] != "read_file" {
		t.Fatalf("token capabilities escalated beyond pairing: %#v", claims.Capabilities)
	}
}

func TestDeviceConnectorRefreshPreservesScopes(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("device-connector-refresh-scope-test")

	store := &deviceHandlerStore{users: map[int64]*types.User{
		7: {ID: 7, Username: "alice", AccountType: types.AccountHuman, State: 0},
	}}
	hub := NewHub(store, nil)
	handler := NewDeviceConnectorHandler(store, hub)
	token, err := GenerateDeviceConnectorToken(DeviceConnectorTokenInput{
		UID:      7,
		Username: "alice",
		DeviceID: "alice-laptop",
		Scopes:   []string{"device:refresh"},
	})
	if err != nil {
		t.Fatalf("GenerateDeviceConnectorToken: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/device-connectors/token/refresh", nil)
	req.Header.Set("Authorization", "DeviceConnector "+token)
	rec := httptest.NewRecorder()
	handler.HandleRefreshToken(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status=%d body=%s", rec.Code, rec.Body.String())
	}
	var out struct {
		ConnectorToken string `json:"connector_token"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode refresh: %v", err)
	}
	claims, err := ParseDeviceConnectorToken(out.ConnectorToken)
	if err != nil {
		t.Fatalf("ParseDeviceConnectorToken refreshed: %v", err)
	}
	if len(claims.Scopes) != 1 || claims.Scopes[0] != "device:refresh" {
		t.Fatalf("refresh expanded scopes: %#v", claims.Scopes)
	}
}

func TestUnlinkDeviceRevokesConnectorToken(t *testing.T) {
	oldSecret := append([]byte(nil), jwtSecret...)
	defer func() { jwtSecret = oldSecret }()
	SetJWTSecret("device-connector-unlink-test")

	store := &deviceHandlerStore{users: map[int64]*types.User{
		7: {ID: 7, Username: "alice", AccountType: types.AccountHuman, State: 0},
	}}
	hub := NewHub(store, nil)
	connectorHandler := NewDeviceConnectorHandler(store, hub)
	deviceHandler := NewDeviceHandler(store, hub)
	token, err := GenerateDeviceConnectorToken(DeviceConnectorTokenInput{
		UID:          7,
		Username:     "alice",
		DeviceID:     "alice-laptop",
		Capabilities: []string{"read_file"},
	})
	if err != nil {
		t.Fatalf("GenerateDeviceConnectorToken: %v", err)
	}

	registerReq := httptest.NewRequest(http.MethodPost, "/api/device-connectors/register", bytes.NewBufferString(`{
		"device_id": "alice-laptop",
		"capabilities": ["read_file"]
	}`))
	registerReq.Header.Set("Authorization", "DeviceConnector "+token)
	registerRec := httptest.NewRecorder()
	connectorHandler.HandleRegisterDevice(registerRec, registerReq)
	if registerRec.Code != http.StatusOK {
		t.Fatalf("initial register status=%d body=%s", registerRec.Code, registerRec.Body.String())
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/api/devices/alice-laptop", nil)
	deleteReq = deleteReq.WithContext(context.WithValue(deleteReq.Context(), uidKey, int64(7)))
	deleteRec := httptest.NewRecorder()
	deviceHandler.HandleDeviceByID(deleteRec, deleteReq)
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	if _, ok := hub.userDevices.activeDevice(7, "alice-laptop"); ok {
		t.Fatal("device should be removed after unlink")
	}

	registerAgainReq := httptest.NewRequest(http.MethodPost, "/api/device-connectors/register", bytes.NewBufferString(`{
		"device_id": "alice-laptop",
		"capabilities": ["read_file"]
	}`))
	registerAgainReq.Header.Set("Authorization", "DeviceConnector "+token)
	registerAgainRec := httptest.NewRecorder()
	connectorHandler.HandleRegisterDevice(registerAgainRec, registerAgainReq)
	if registerAgainRec.Code != http.StatusForbidden {
		t.Fatalf("revoked token register status=%d body=%s", registerAgainRec.Code, registerAgainRec.Body.String())
	}
}
