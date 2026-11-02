package server

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/openchat/openchat/server/store/types"
)

// Separate runtime clients/registries model API process replacement. Redis
// remains alive, exactly as it must during an API-only production rollout.
func TestRedisDeviceGrantsSurviveAPIReplacementWithoutChangingAuthority(t *testing.T) {
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", "1")
	url, stop := newRedisRuntimeTestServer(t)
	defer stop()
	now := time.Now().Truncate(time.Millisecond)
	state := newRedisRuntimeStateForTest(t, url, "grant-restart")
	before := newUserDeviceRegistry(defaultUserDeviceTTL).withSharedRuntime(state)
	before.now = func() time.Time { return now }
	session := buildCatsCoSessionKey("p2p_7_42", "p2p", "usr42", 7)
	selection, err := before.confirmDeviceSelection(7, session, "a", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	a, err := before.register(7, RegisterUserDeviceRequest{DeviceID: "a", BodyID: "desktop-a", InstallationID: "install-a", Capabilities: []string{"read_file"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := before.register(7, RegisterUserDeviceRequest{DeviceID: "b", BodyID: "desktop-b", InstallationID: "install-b", Capabilities: []string{"read_file"}})
	if err != nil {
		t.Fatal(err)
	}
	grants := before.grantsForOwnerDevices(7, 7, "p2p_7_42", "p2p", 42, "cloud", []UserDevice{a}, selection.Revision)
	grants = append(grants, before.grantsForExplicitDevices(7, "p2p_7_42", "p2p", 42, "cloud", []UserDevice{b}, selection.Revision)...)
	if len(grants) != 2 {
		t.Fatalf("grants = %d", len(grants))
	}
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	state = newRedisRuntimeStateForTest(t, url, "grant-restart")
	defer state.Close()
	after := newUserDeviceRegistry(defaultUserDeviceTTL).withSharedRuntime(state)
	after.now = func() time.Time { return now }
	for _, original := range grants {
		recovered, ok := after.lookupGrant(original.GrantID)
		if !ok || !reflect.DeepEqual(recovered, original) {
			t.Fatalf("grant scope changed/lost after API replacement: ok=%v", ok)
		}
	}
	if _, ok := after.lookupGrant("unknown-client-supplied-id"); ok {
		t.Fatal("unknown grant resurrected")
	}
	if _, err := after.confirmDeviceSelection(7, session, "b", "test", selection.Revision); err != nil {
		t.Fatal(err)
	}
	for _, grant := range grants {
		if _, ok := after.lookupGrant(grant.GrantID); ok {
			t.Fatal("selection change must invalidate default AND explicit grants")
		}
	}
	// Check expiry independently of selection revision.
	after.now = func() time.Time { return now.Add(after.grantTT + time.Minute) }
	if _, ok := state.lookupDeviceGrant(grants[0].GrantID, unixMillis(after.now())); ok {
		t.Fatal("expired grant recovered")
	}
}

func TestRedisGrantRenewalAfterHubReplacement(t *testing.T) {
	originalHub, _, request, now := grantRenewalFixture(t)
	original, ok := originalHub.userDevices.lookupGrant(request.GrantID)
	if !ok {
		t.Fatal("missing fixture grant")
	}
	url, stop := newRedisRuntimeTestServer(t)
	defer stop()
	first := newRedisRuntimeStateForTest(t, url, "renew-restart")
	first.rememberDeviceGrants([]ScopedDeviceGrant{original}, unixMillis(*now))
	if err := first.compareDeviceSelection(7, original.SessionKey, "", deviceSelectionPreference{
		DeviceID: "a", Revision: original.SelectionRevision, ExpiresAt: unixMillis(now.Add(time.Hour)),
	}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := newRedisRuntimeStateForTest(t, url, "renew-restart")
	defer second.Close()
	h := NewHubWithRuntime(originalHub.db, nil, second, "replacement")
	h.userDevices.now = func() time.Time { return *now }
	h.bodyLeases.now = func() time.Time { return *now }
	bot := &Client{hub: h, uid: 42, accountType: types.AccountBot, bodyID: "cloud", connectionID: "new-cloud", send: make(chan []byte, 8)}
	bot.setRuntimeCapabilities([]string{xiaobaDeviceTargetsCapability})
	h.addClient(bot)
	if _, err := h.bodyLeases.acquire(42, "cloud", bot.connectionID); err != nil {
		t.Fatal(err)
	}
	device, err := h.userDevices.register(7, RegisterUserDeviceRequest{DeviceID: "a", Capabilities: []string{"read_file"}})
	if err != nil {
		t.Fatal(err)
	}
	desktop := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 8)}
	h.addClient(desktop)
	h.bindDeviceClient(7, device, desktop)
	*now = now.Add(time.Minute)
	next, err := h.renewDeviceGrant(bot, request)
	if err != nil {
		t.Fatal(err)
	}
	if next.DeviceID != original.DeviceID || next.CreatedAt != original.CreatedAt || next.SelectionRevision != original.SelectionRevision || next.ExpiresAt <= original.ExpiresAt {
		t.Fatal("renewal changed authority or failed to extend original short expiry")
	}
	second.revokeDeviceConnectorDevice(7, "a", *now)
	if _, err := h.renewDeviceGrant(bot, &MsgDeviceGrantRenewal{Type: "request", RequestID: "after-logout", GrantID: next.GrantID}); err == nil {
		t.Fatal("renewed pre-logout task must remain revoked")
	}
}

func TestRedisDeviceRevocationSurvivesAPIReplacementAndFailsClosed(t *testing.T) {
	url, stop := newRedisRuntimeTestServer(t)
	defer stop()
	now := time.Now().Truncate(time.Millisecond)
	state := newRedisRuntimeStateForTest(t, url, "grant-logout-restart")
	state.revokeDeviceConnectorDevice(7, "a", now)
	if err := state.Close(); err != nil {
		t.Fatal(err)
	}
	state = newRedisRuntimeStateForTest(t, url, "grant-logout-restart")
	defer state.Close()
	claims := &DeviceConnectorClaims{UID: 7, DeviceID: "a"}
	claims.IssuedAt = &jwt.NumericDate{Time: now.Add(-time.Minute)}
	if !state.isDeviceConnectorRevoked(claims, now) {
		t.Fatal("old task revived after logout and API restart")
	}
	claims.IssuedAt = &jwt.NumericDate{Time: now.Add(time.Minute)}
	if state.isDeviceConnectorRevoked(claims, now) {
		t.Fatal("genuine post-logout login denied")
	}
	claims.DeviceID = "b"
	if state.isDeviceConnectorRevoked(claims, now) {
		t.Fatal("device a logout must not revoke b")
	}
	if err := state.client.Set(context.Background(), state.deviceConnectorRevokedDeviceKey(7, "b"), "corrupt", time.Hour).Err(); err != nil {
		t.Fatal(err)
	}
	if !state.isDeviceConnectorRevoked(claims, now) {
		t.Fatal("corrupt watermark must fail closed")
	}
	// A closed Redis client simulates unavailable storage without a reconnect delay.
	if err := state.client.Close(); err != nil {
		t.Fatal(err)
	}
	if !state.isDeviceConnectorRevoked(claims, now) {
		t.Fatal("unavailable revocation state must fail closed")
	}
}
