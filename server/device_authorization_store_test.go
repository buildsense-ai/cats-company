package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	redisserver "github.com/alicebob/miniredis/v2/server"
	"github.com/golang-jwt/jwt/v5"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type authorizationSelectionStore struct {
	*identityMessageStore
	selections durableSelectionTestStore
}

func (s *authorizationSelectionStore) GetConversationDeviceSelection(ctx context.Context, actor int64, session string) (*store.ConversationDeviceSelection, error) {
	return s.selections.GetConversationDeviceSelection(ctx, actor, session)
}

func (s *authorizationSelectionStore) CompareConversationDeviceSelection(ctx context.Context, actor int64, session, expected string, next store.ConversationDeviceSelection) (bool, error) {
	return s.selections.CompareConversationDeviceSelection(ctx, actor, session, expected, next)
}

func authorizationStoreForTest(t *testing.T, url string) *RedisDeviceAuthorizationStore {
	t.Helper()
	s, err := NewRedisDeviceAuthorizationStore(context.Background(), RedisDeviceAuthorizationOptions{URL: url})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestDeviceAuthorizationOnlySurvivesReplacementWithoutMigratingRuntime(t *testing.T) {
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", "1")
	mr := miniredis.RunT(t)
	fixture, _, request, clock := grantRenewalFixture(t)
	old, ok := fixture.userDevices.lookupGrant(request.GrantID)
	if !ok {
		t.Fatal("missing fixture grant")
	}
	db := &authorizationSelectionStore{identityMessageStore: fixture.db.(*identityMessageStore)}
	first := authorizationStoreForTest(t, "redis://"+mr.Addr())
	before, err := NewHubWithDeviceAuthorization(db, nil, first)
	if err != nil {
		t.Fatal(err)
	}
	before.userDevices.now = func() time.Time { return *clock }
	selection, err := before.userDevices.confirmDeviceSelection(7, old.SessionKey, "a", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	old.SelectionRevision = selection.Revision
	before.userDevices.rememberGrants([]ScopedDeviceGrant{old})
	_, err = before.userDevices.register(7, RegisterUserDeviceRequest{DeviceID: "not-reconnected", Capabilities: []string{"read_file"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second := authorizationStoreForTest(t, "redis://"+mr.Addr())
	after, err := NewHubWithDeviceAuthorization(db, nil, second)
	if err != nil {
		t.Fatal(err)
	}
	after.userDevices.now = func() time.Time { return *clock }
	after.bodyLeases.now = func() time.Time { return *clock }
	recovered, ok := after.userDevices.lookupGrant(old.GrantID)
	if !ok || !reflect.DeepEqual(recovered, old) {
		t.Fatal("grant lost or authority changed on replacement")
	}
	if after.sharedRuntime != nil || after.userDevices.shared != nil || after.deviceRPC.shared != nil || after.bodyLeases.shared != nil {
		t.Fatal("authorization-only mode changed shared runtime")
	}
	if after.RuntimeRouteState() != "process_local" || after.RuntimeMode() == "redis" {
		t.Fatal("authorization mode reported shared Redis routing")
	}
	if len(after.userDevices.list(7)) != 0 || len(after.deviceClients) != 0 || len(after.listDeviceAudit(7, 10)) != 0 {
		t.Fatal("non-authorization state unexpectedly restored")
	}
	if len(second.state.nodes) != 0 || len(second.state.subscribed) != 0 {
		t.Fatal("Redis runtime heartbeat/pubsub workers registered")
	}
	if _, ok := any(second).(sharedRuntimeState); ok {
		t.Fatal("narrow store implements shared runtime")
	}
	bot := &Client{hub: after, uid: 42, accountType: types.AccountBot, bodyID: "cloud", connectionID: "replacement-cloud", send: make(chan []byte, 16)}
	bot.setRuntimeCapabilities([]string{xiaobaDeviceTargetsCapability})
	after.addClient(bot)
	if _, err := after.bodyLeases.acquire(42, "cloud", bot.connectionID); err != nil {
		t.Fatal(err)
	}
	if _, err := after.renewDeviceGrant(bot, request); err == nil {
		t.Fatal("grant alone authorized disconnected target")
	}
	device, err := after.userDevices.register(7, RegisterUserDeviceRequest{DeviceID: "a", Capabilities: []string{"read_file"}})
	if err != nil {
		t.Fatal(err)
	}
	desktop := &Client{uid: 7, accountType: types.AccountHuman, send: make(chan []byte, 8)}
	after.addClient(desktop)
	after.bindDeviceClient(7, device, desktop)
	*clock = clock.Add(time.Minute)
	renewed, err := after.renewDeviceGrant(bot, request)
	if err != nil {
		t.Fatal(err)
	}
	if renewed.DeviceID != old.DeviceID || renewed.CreatedAt != old.CreatedAt || renewed.SelectionRevision != old.SelectionRevision || renewed.ExpiresAt <= old.ExpiresAt {
		t.Fatal("renewal changed authority")
	}
	if !reflect.DeepEqual(renewed.Operations, old.Operations) || renewed.AgentBodyID != old.AgentBodyID || renewed.DeviceBodyID != old.DeviceBodyID || renewed.DeviceInstallationID != old.DeviceInstallationID {
		t.Fatal("renewal changed capabilities or incarnations")
	}
	for _, name := range []string{"wrong bot", "wrong body", "device incarnation changed", "capability removed", "24h ceiling"} {
		t.Run(name, func(t *testing.T) {
			originalBotUID, originalBody := bot.uid, bot.bodyID
			defer func() { bot.uid, bot.bodyID = originalBotUID, originalBody }()
			changed := old
			if name == "wrong bot" {
				bot.uid = 43
			}
			if name == "wrong body" {
				bot.bodyID = "other"
			}
			if name == "device incarnation changed" {
				changed.DeviceBodyID = "different-incarnation"
			}
			if name == "capability removed" {
				changed.Operations = []DeviceGrantOperation{DeviceGrantWriteFile}
			}
			if name == "24h ceiling" {
				changed.RenewableUntil = unixMillis(*clock)
			}
			second.rememberDeviceGrants([]ScopedDeviceGrant{changed}, unixMillis(*clock))
			defer second.rememberDeviceGrants([]ScopedDeviceGrant{old}, unixMillis(*clock))
			if _, err := after.renewDeviceGrant(bot, request); err == nil {
				t.Fatal("changed authority renewed")
			}
		})
	}
	// Revocation is required alongside grants: reconnect must not revive old tasks.
	if err := after.revokeDeviceConnectorDevice(7, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := after.renewDeviceGrant(bot, &MsgDeviceGrantRenewal{Type: "request", RequestID: "after-unlink", GrantID: renewed.GrantID}); err == nil {
		t.Fatal("unlink did not revoke old task")
	}
	if _, err := after.userDevices.confirmDeviceSelection(7, old.SessionKey, "b", "test", selection.Revision); err != nil {
		t.Fatal(err)
	}
	if _, ok := after.userDevices.lookupGrant(old.GrantID); ok {
		t.Fatal("selection switch revived old grant")
	}
	current, err := after.userDevices.selectionRecord(7, old.SessionKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := after.userDevices.confirmDeviceSelection(7, old.SessionKey, "a", "test", current.Revision); err != nil {
		t.Fatal(err)
	}
	if _, ok := after.userDevices.lookupGrant(old.GrantID); ok {
		t.Fatal("switch-back revived original grant")
	}
	if _, ok := after.userDevices.lookupGrant("unknown-model-supplied-id"); ok {
		t.Fatal("unknown grant resurrected")
	}
	db.selections.mu.Lock()
	db.selections.fail = true
	db.selections.mu.Unlock()
	if _, ok := after.userDevices.lookupGrant(renewed.GrantID); ok {
		t.Fatal("selection DB failure bypassed validation")
	}
	for _, key := range mr.Keys() {
		if !strings.HasPrefix(key, "catsco:device-auth:device_grant:") && !strings.HasPrefix(key, "catsco:device-auth:device_connector_revoked_device:") {
			t.Fatalf("non-authorization Redis key: %s", key)
		}
	}
}

func TestDeviceAuthorizationOnlyRevocationsPersistAndAreMonotonic(t *testing.T) {
	mr := miniredis.RunT(t)
	s := authorizationStoreForTest(t, "redis://"+mr.Addr())
	now := time.Now().Truncate(time.Second)
	if err := s.revokeDevice(7, " a ", now); err != nil {
		t.Fatal(err)
	}
	if err := s.revokeDevice(7, "a", now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := s.revokeToken("token", now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	_ = s.Close()
	s = authorizationStoreForTest(t, "redis://"+mr.Addr())
	claims := &DeviceConnectorClaims{UID: 7, DeviceID: "a", RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(now.Add(-time.Minute))}}
	if !s.isDeviceConnectorRevoked(claims, now) {
		t.Fatal("restart or older revocation write revived old login")
	}
	claims.IssuedAt = jwt.NewNumericDate(now.Add(time.Minute))
	if s.isDeviceConnectorRevoked(claims, now) {
		t.Fatal("fresh login after revocation denied")
	}
	claims.ID = "token"
	if !s.isDeviceConnectorRevoked(claims, now) {
		t.Fatal("token revocation lost")
	}
	claims.ID = "other-token"
	claims.DeviceID = "b"
	if s.isDeviceConnectorRevoked(claims, now) {
		t.Fatal("revocation leaked to other device")
	}
	mr.Set(s.state.deviceConnectorRevokedDeviceKey(7, "b"), "corrupt")
	if !s.isDeviceConnectorRevoked(claims, now) {
		t.Fatal("corrupt watermark authorized")
	}
	if err := s.revokeDevice(7, "b", now); err == nil {
		t.Fatal("corrupt watermark silently overwritten")
	}
}

func TestDeviceAuthorizationOnlyOutageNeverFallsBackOrAcknowledgesUnlink(t *testing.T) {
	mr := miniredis.RunT(t)
	s := authorizationStoreForTest(t, "redis://"+mr.Addr())
	db := &authorizationSelectionStore{identityMessageStore: &identityMessageStore{users: map[int64]*types.User{
		7: {ID: 7, AccountType: types.AccountHuman},
	}}}
	h, err := NewHubWithDeviceAuthorization(db, nil, s)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.userDevices.register(7, RegisterUserDeviceRequest{DeviceID: "a", Capabilities: []string{"read_file"}}); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	grant := ScopedDeviceGrant{GrantID: "memory-fallback", Status: "active", ExpiresAt: now.Add(time.Minute).UnixMilli()}
	h.userDevices.grants[grant.GrantID] = grant
	mr.SetError("OOM private upstream data")
	h.userDevices.rememberGrants([]ScopedDeviceGrant{grant})
	if _, ok := h.userDevices.lookupGrant(grant.GrantID); ok {
		t.Fatal("failed Redis read fell back to memory")
	}
	if !h.isDeviceConnectorRevoked(&DeviceConnectorClaims{UID: 7, DeviceID: "a"}) {
		t.Fatal("storage outage bypassed revocation check")
	}
	req := httptest.NewRequest(http.MethodDelete, "/api/devices/a", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
	rec := httptest.NewRecorder()
	NewDeviceHandler(db, h).HandleDeviceByID(rec, req)
	if rec.Code != http.StatusServiceUnavailable || strings.Contains(rec.Body.String(), "private") {
		t.Fatal("failed revocation acknowledged or leaked raw error")
	}
	if _, ok := h.userDevices.activeDevice(7, "a"); !ok {
		t.Fatal("failed unlink partially removed device")
	}
	mr.SetError("")
	rec = httptest.NewRecorder()
	NewDeviceHandler(db, h).HandleDeviceByID(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("retry unlink status: %d", rec.Code)
	}
}

func TestDeviceAuthorizationOnlyExpiryAndSocketBudgets(t *testing.T) {
	for _, operation := range []string{"get", "set", "exists", "eval"} {
		t.Run(operation, func(t *testing.T) {
			mr := miniredis.RunT(t)
			s := authorizationStoreForTest(t, "redis://"+mr.Addr())
			now := time.Now()
			grant := ScopedDeviceGrant{GrantID: "expiry", Status: "active", ExpiresAt: now.Add(time.Minute).UnixMilli()}
			s.rememberDeviceGrants([]ScopedDeviceGrant{grant}, now.UnixMilli())
			if _, ok := s.lookupDeviceGrant(grant.GrantID, now.Add(2*time.Minute).UnixMilli()); ok {
				t.Fatal("expired grant recovered")
			}
			mr.Server().SetPreHook(func(_ *redisserver.Peer, cmd string, _ ...string) bool {
				if strings.EqualFold(cmd, operation) {
					time.Sleep(600 * time.Millisecond)
				}
				return false
			})
			start := time.Now()
			switch operation {
			case "get":
				if _, ok := s.lookupDeviceGrant(grant.GrantID, now.UnixMilli()); ok {
					t.Fatal("slow read authorized")
				}
			case "set":
				if err := s.revokeToken("token", now.Add(time.Hour)); err == nil {
					t.Fatal("slow revoke succeeded")
				}
			case "exists":
				if !s.isDeviceConnectorRevoked(&DeviceConnectorClaims{UID: 7, DeviceID: "a"}, now) {
					t.Fatal("slow revocation check authorized")
				}
			case "eval":
				if err := s.revokeDevice(7, "a", now); err == nil {
					t.Fatal("slow unlink succeeded")
				}
			}
			if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
				t.Fatalf("authorization operation exceeded bounded budget: %s", elapsed)
			}
		})
	}
}

func TestDeviceAuthorizationOnlyConstructorRejectsUnsafeSetup(t *testing.T) {
	if _, err := NewRedisDeviceAuthorizationStore(context.Background(), RedisDeviceAuthorizationOptions{URL: "redis://private:password@invalid:port"}); err == nil || strings.Contains(err.Error(), "password") {
		t.Fatal("invalid connection accepted or credentials leaked")
	}
	if _, err := NewHubWithDeviceAuthorization(nil, nil, nil); err == nil {
		t.Fatal("nil auth accepted")
	}
	mr := miniredis.RunT(t)
	s := authorizationStoreForTest(t, "redis://"+mr.Addr())
	if _, err := NewHubWithDeviceAuthorization(&deviceHandlerStore{}, nil, s); err == nil {
		t.Fatal("missing durable selection DB accepted")
	}
}
