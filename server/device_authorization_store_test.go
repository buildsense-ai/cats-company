package server

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
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

func TestPermanentDeviceGrantHasNoRedisExpiryAndIsRevokedBySelectionOrUnlink(t *testing.T) {
	mr := miniredis.RunT(t)
	store := authorizationStoreForTest(t, "redis://"+mr.Addr())
	now := time.Now().Truncate(time.Millisecond)
	grant := ScopedDeviceGrant{
		Kind: "user_device_grant", Source: "catscompany", Status: "active", Permanent: true,
		IdentityTrust: "server_canonical", DeviceID: "device-a", OwnerUserID: "usr7", ActorUserID: "usr7",
		SessionKey: "session-a", TopicID: "topic-a", TopicType: "p2p", AgentID: "usr42", AgentBodyID: "body-a",
		Operations: []DeviceGrantOperation{DeviceGrantReadFile}, CreatedAt: unixMillis(now),
		ExpiresAt: permanentDeviceGrantExpiresAt, SelectionRevision: "revision-a",
	}
	grant.GrantID = persistentDeviceGrantID(grant)
	store.rememberDeviceGrants([]ScopedDeviceGrant{grant}, unixMillis(now))
	key := store.state.deviceGrantKey(grant.GrantID)
	if mr.TTL(key) != 0 {
		t.Fatal("permanent device grant received a Redis expiration")
	}
	if _, ok := store.lookupDeviceGrant(grant.GrantID, unixMillis(now.Add(365*24*time.Hour))); !ok {
		t.Fatal("permanent grant expired while its selection remained active")
	}
	grant.CreatedAt = unixMillis(now.Add(time.Minute))
	store.rememberDeviceGrants([]ScopedDeviceGrant{grant}, grant.CreatedAt)
	if count, _ := store.state.client.SCard(context.Background(), store.state.deviceGrantSelectionKey(7, grant.SessionKey)).Result(); count != 2 {
		t.Fatalf("same scope accumulated %d permanent grants", count)
	}
	finite := grant
	finite.Permanent = false
	finite.GrantID = "device_grant_legacy_finite"
	finite.ExpiresAt = unixMillis(now.Add(10 * time.Minute))
	store.rememberDeviceGrants([]ScopedDeviceGrant{finite}, unixMillis(now))
	selectionIndex := store.state.deviceGrantSelectionKey(7, grant.SessionKey)
	deviceIndex := store.state.deviceGrantDeviceKey(7, grant.DeviceID)
	if mr.TTL(selectionIndex) != 0 || mr.TTL(deviceIndex) != 0 {
		t.Fatal("finite grant write added an expiry to a mixed index containing a permanent grant")
	}
	finiteKey := store.state.deviceGrantKey(finite.GrantID)
	if mr.TTL(finiteKey) <= 0 {
		t.Fatal("legacy finite grant lost its own expiration")
	}
	mr.FastForward(11 * time.Minute)
	if _, ok := store.lookupDeviceGrant(finite.GrantID, unixMillis(now.Add(11*time.Minute))); ok {
		t.Fatal("legacy finite grant survived its own expiration")
	}
	if _, ok := store.lookupDeviceGrant(grant.GrantID, unixMillis(now.Add(11*time.Minute))); !ok {
		t.Fatal("finite grant expiration expired the permanent grant")
	}
	store.revokeDeviceGrantsForSelection(7, grant.SessionKey)
	if mr.Exists(key) {
		t.Fatal("selection revision change left old permanent grant in Redis")
	}
	store.rememberDeviceGrants([]ScopedDeviceGrant{grant}, grant.CreatedAt)
	if err := store.revokeDevice(7, grant.DeviceID, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if mr.Exists(key) {
		t.Fatal("device unlink left old permanent grant in Redis")
	}
}

func TestPermanentDeviceGrantCleanupRetriesAfterRedisOutage(t *testing.T) {
	t.Setenv("CATSCO_XIAOBA_RUNTIME_V2_ENABLED", "1")
	mr := miniredis.RunT(t)
	fixture, _, request, clock := grantRenewalFixture(t)
	grant, ok := fixture.userDevices.lookupGrant(request.GrantID)
	if !ok {
		t.Fatal("missing fixture grant")
	}
	db := &authorizationSelectionStore{identityMessageStore: fixture.db.(*identityMessageStore)}
	auth := authorizationStoreForTest(t, "redis://"+mr.Addr())
	h, err := NewHubWithDeviceAuthorization(db, nil, auth)
	if err != nil {
		t.Fatal(err)
	}
	h.userDevices.now = func() time.Time { return *clock }
	selection, err := h.userDevices.confirmDeviceSelection(7, grant.SessionKey, "a", "test", "")
	if err != nil {
		t.Fatal(err)
	}
	grant.SelectionRevision = selection.Revision
	h.userDevices.rememberGrants([]ScopedDeviceGrant{grant})
	grantKey := auth.state.deviceGrantKey(grant.GrantID)
	mr.SetError("temporary redis outage")
	if _, err := h.userDevices.confirmDeviceSelection(7, grant.SessionKey, "b", "test", selection.Revision); err != nil {
		t.Fatal("selection update should still be committed", err)
	}
	mr.SetError("")
	if !mr.Exists(grantKey) {
		t.Fatal("expected cleanup to have failed during the simulated outage")
	}
	if _, ok := h.userDevices.lookupGrant(grant.GrantID); ok {
		t.Fatal("stale selection grant remained usable")
	}
	if mr.Exists(grantKey) {
		t.Fatal("stale permanent grant was not cleaned on the next lookup")
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
	if err := s.revokeDevice(7, "b", now); err != nil {
		t.Fatalf("explicit revocation must repair corrupt watermark: %v", err)
	}
}

func TestDeviceAuthorizationOnlyUnlinkRepairsCorruptWatermark(t *testing.T) {
	for _, corrupt := range []string{"PRIVATE_CORRUPT", "", "0", "-1", "1.5", "1e12", "+123", " 123 ", "nan", "inf", "9223372036854775808"} {
		t.Run(corrupt, func(t *testing.T) {
			mr := miniredis.RunT(t)
			s := authorizationStoreForTest(t, "redis://"+mr.Addr())
			db := &authorizationSelectionStore{identityMessageStore: &identityMessageStore{users: map[int64]*types.User{
				7: {ID: 7, AccountType: types.AccountHuman},
			}}}
			h, err := NewHubWithDeviceAuthorization(db, nil, s)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := h.userDevices.register(7, RegisterUserDeviceRequest{DeviceID: "private-device", Capabilities: []string{"read_file"}}); err != nil {
				t.Fatal(err)
			}
			key := s.state.deviceConnectorRevokedDeviceKey(7, "private-device")
			mr.Set(key, corrupt)
			claims := &DeviceConnectorClaims{UID: 7, DeviceID: "private-device", RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(time.Now().Add(-time.Hour))}}
			var output bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&output)
			defer log.SetOutput(previous)
			// Reads cannot repair an unknown value or erase the security boundary.
			if !h.isDeviceConnectorRevoked(claims) {
				t.Fatal("corrupt watermark must deny before explicit repair")
			}
			if raw, _ := mr.Get(key); raw != corrupt {
				t.Fatal("read mutated corrupt watermark")
			}
			start := time.Now().UnixMilli()
			for attempt := 0; attempt < 2; attempt++ {
				req := httptest.NewRequest(http.MethodDelete, "/api/devices/private-device", nil)
				req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(7)))
				rec := httptest.NewRecorder()
				NewDeviceHandler(db, h).HandleDeviceByID(rec, req)
				if rec.Code != http.StatusOK {
					t.Fatalf("unlink attempt %d: status=%d", attempt+1, rec.Code)
				}
				if _, ok := h.userDevices.activeDevice(7, "private-device"); ok {
					t.Fatal("successful unlink retained registered device")
				}
				if !h.isDeviceConnectorRevoked(claims) {
					t.Fatal("repair revived old credential")
				}
			}
			raw, _ := mr.Get(key)
			if watermark := parseInt64(raw); watermark < start || watermark > time.Now().UnixMilli() {
				t.Fatal("repair did not save current server watermark")
			}
			if mr.TTL(key) <= 0 || mr.TTL(key) > deviceConnectorTokenTTL {
				t.Fatal("repair lost bounded retention")
			}
			text := output.String()
			if strings.Count(text, "reason=invalid_watermark_repaired") != 1 || !strings.Contains(text, "action=revocation_saved") {
				t.Fatal("repair diagnostic missing or repeated")
			}
			if strings.Contains(text, "private-device") || strings.Contains(text, "PRIVATE_CORRUPT") || strings.Contains(text, "redis://") {
				t.Fatal("repair diagnostic leaked private data")
			}
			_ = s.Close()
			s = authorizationStoreForTest(t, "redis://"+mr.Addr())
			if !s.isDeviceConnectorRevoked(claims, time.Now()) {
				t.Fatal("restart lost repaired revocation")
			}
			claims.IssuedAt = jwt.NewNumericDate(time.Now().Add(time.Minute))
			if s.isDeviceConnectorRevoked(claims, time.Now()) {
				t.Fatal("new login after repair denied")
			}
		})
	}
}

func TestDeviceAuthorizationOnlyRepairKeepsWatermarkMonotonic(t *testing.T) {
	mr := miniredis.RunT(t)
	s := authorizationStoreForTest(t, "redis://"+mr.Addr())
	key := s.state.deviceConnectorRevokedDeviceKey(7, "a")
	mr.Set(key, "corrupt")
	now := time.Now().Truncate(time.Millisecond)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(offset int) {
			defer wg.Done()
			if err := s.revokeDevice(7, "a", now.Add(time.Duration(offset)*time.Millisecond)); err != nil {
				t.Errorf("concurrent unlink: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if raw, _ := mr.Get(key); parseInt64(raw) != now.Add(11*time.Millisecond).UnixMilli() {
		t.Fatal("concurrent repair moved watermark backwards")
	}
	// Values beyond Lua's exact floating-point integer range must also remain
	// monotonic: comparing rounded doubles could erase a later valid watermark.
	for _, raw := range []string{"9223372036854775807", "0009223372036854775807"} {
		mr.Set(key, raw)
		if err := s.revokeDevice(7, "a", now); err != nil {
			t.Fatal(err)
		}
		if retained, _ := mr.Get(key); retained != raw {
			t.Fatal("valid later watermark was overwritten")
		}
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
	// Corruption is repairable, but not while persistence itself is failing.
	mr.Set(s.state.deviceConnectorRevokedDeviceKey(7, "a"), "corrupt")
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
	if raw, _ := mr.Get(s.state.deviceConnectorRevokedDeviceKey(7, "a")); raw != "corrupt" {
		t.Fatal("failed write falsely repaired corruption")
	}
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
