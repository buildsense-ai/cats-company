package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
)

// Excludes routing, leases, pairing, audit and RPC queues. The authorization
// backend must never become sharedRuntime.
type deviceGrantStore interface {
	rememberDeviceGrants([]ScopedDeviceGrant, int64)
	lookupDeviceGrant(string, int64) (ScopedDeviceGrant, bool)
	revokeDeviceGrantsForSelection(int64, string)
	revokeDeviceGrantsForDevice(int64, string)
}

type deviceAuthorizationStore interface {
	deviceGrantStore
	revokeDevice(int64, string, time.Time) error
	revokeToken(string, time.Time) error
	isDeviceConnectorRevoked(*DeviceConnectorClaims, time.Time) bool
}

type RedisDeviceAuthorizationOptions struct {
	URL       string
	KeyPrefix string
}

// Reuses bounded grant/revocation reads without implementing sharedRuntimeState
// or starting its workers. Only grants and revocation watermarks use Redis.
type RedisDeviceAuthorizationStore struct {
	state *RedisRuntimeState
}

func NewRedisDeviceAuthorizationStore(ctx context.Context, opts RedisDeviceAuthorizationOptions) (*RedisDeviceAuthorizationStore, error) {
	prefix := strings.Trim(strings.TrimSpace(opts.KeyPrefix), ":")
	if prefix == "" {
		prefix = "catsco:device-auth"
	}
	state, err := NewRedisRuntimeState(ctx, RedisRuntimeOptions{URL: opts.URL, KeyPrefix: prefix})
	if err != nil {
		// The underlying error may contain a Redis URL, address or credentials.
		return nil, fmt.Errorf("device authorization Redis initialization failed; check private connection configuration and availability")
	}
	return &RedisDeviceAuthorizationStore{state: state}, nil
}

func (s *RedisDeviceAuthorizationStore) Close() error { return s.state.Close() }

func (s *RedisDeviceAuthorizationStore) rememberDeviceGrants(grants []ScopedDeviceGrant, now int64) {
	s.state.rememberDeviceGrants(grants, now)
}

func (s *RedisDeviceAuthorizationStore) lookupDeviceGrant(id string, now int64) (ScopedDeviceGrant, bool) {
	return s.state.lookupDeviceGrant(id, now)
}

func (s *RedisDeviceAuthorizationStore) revokeDeviceGrantsForSelection(actorUID int64, sessionKey string) {
	s.state.revokeDeviceGrantsForSelection(actorUID, sessionKey)
}

func (s *RedisDeviceAuthorizationStore) revokeDeviceGrantsForDevice(ownerUID int64, deviceID string) {
	s.state.revokeDeviceGrantsForDevice(ownerUID, deviceID)
}

func (s *RedisDeviceAuthorizationStore) isDeviceConnectorRevoked(claims *DeviceConnectorClaims, now time.Time) bool {
	return s.state.isDeviceConnectorRevoked(claims, now)
}

func (s *RedisDeviceAuthorizationStore) revokeDevice(owner int64, device string, now time.Time) error {
	normalized, err := normalizeUserDeviceID(device)
	if owner <= 0 || err != nil {
		return fmt.Errorf("invalid device revocation target")
	}
	device = normalized
	ctx, cancel := context.WithTimeout(s.state.ctx, redisRuntimeWriteTimeout)
	defer cancel()
	// Reads remain fail-closed; only explicit unlink repairs corrupt data. Keep
	// read/compare/write atomic and compare decimal strings to avoid rounding
	// a valid int64 watermark backwards through Lua's floating-point numbers.
	result, err := s.state.client.Eval(ctx, `
local function watermark(raw)
  if not raw or not string.match(raw, '^%d+$') then return nil end
  local value = string.gsub(raw, '^0+', '')
  if value == '' or #value > 19 or (#value == 19 and value > '9223372036854775807') then return nil end
  return value
end
local old = redis.call('GET', KEYS[1])
local previous = watermark(old)
local next = ARGV[1]
local repaired = old and not previous
if not previous or #previous < #next or (#previous == #next and previous < next) then
  redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
end
if repaired then return 2 end
return 1`, []string{s.state.deviceConnectorRevokedDeviceKey(owner, device)},
		unixMillis(now), deviceConnectorTokenTTL.Milliseconds()).Int()
	if err := s.revocationWriteResult("revoked_device_write", err); err != nil {
		return err
	}
	s.state.revokeDeviceGrantsForDevice(owner, device)
	if result == 2 {
		s.state.logDeviceAuthorizationEvent("repair", "revoked_device_write", "invalid_watermark_repaired", "revocation_saved")
	}
	return nil
}

func (s *RedisDeviceAuthorizationStore) revokeToken(id string, expires time.Time) error {
	ctx, cancel := context.WithTimeout(s.state.ctx, redisRuntimeWriteTimeout)
	defer cancel()
	ttl := ttlUntil(time.Now(), expires)
	if ttl <= 0 {
		ttl = deviceConnectorTokenTTL
	}
	err := s.state.client.Set(ctx, s.state.deviceConnectorRevokedTokenKey(id), "1", ttl).Err()
	return s.revocationWriteResult("revoked_token_write", err)
}

func (s *RedisDeviceAuthorizationStore) revocationWriteResult(operation string, err error) error {
	if err == nil {
		return nil
	}
	s.state.logDeviceAuthorizationFailure(operation, redisAuthorizationErrorClass(err))
	return fmt.Errorf("device authorization storage unavailable")
}

// Install at construction, before workers start. Durable selection revisions
// remain in SQL; every other runtime component remains process-local.
func NewHubWithDeviceAuthorization(db store.Store, rl *RateLimiter, auth *RedisDeviceAuthorizationStore) (*Hub, error) {
	if auth == nil || auth.state == nil {
		return nil, fmt.Errorf("device authorization store is required")
	}
	if _, ok := db.(store.ConversationDeviceSelectionStore); !ok {
		return nil, fmt.Errorf("authorization-only storage requires durable conversation device selections")
	}
	return newHubWithState(db, rl, nil, "", auth), nil
}
