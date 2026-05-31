package server

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

const (
	botBodyIDHeader           = "X-CatsCo-Body-ID"
	botInstallationIDHeader   = "X-CatsCo-Installation-ID"
	defaultBotBodyLeaseTTL    = 2 * time.Minute
	maxBotBodyIdentityLength  = 128
	botBodyConnectionIDLength = 16
)

var (
	botBodyIDPattern        = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	errInvalidBotBodyID     = errors.New("invalid bot body id")
	errBotBodyLeaseConflict = errors.New("bot is already connected from another body")
)

type botBodyLease struct {
	botUID       int64
	bodyID       string
	connectionID string
	acquiredAt   time.Time
	expiresAt    time.Time
}

type BotBodyStatus struct {
	BotUID      int64      `json:"bot_uid"`
	Active      bool       `json:"active"`
	BodyID      string     `json:"body_id,omitempty"`
	Bound       bool       `json:"bound"`
	ConnectedAt *time.Time `json:"connected_at,omitempty"`
}

type botBodyLeaseResult struct {
	Lease    botBodyLease
	Replaced bool
}

// botBodyLeaseManager is process-local for the beta slice. It prevents
// duplicate bot bodies on one CatsCompany server instance; multi-instance
// deployments should move the active lease to Redis or another shared store.
type botBodyLeaseManager struct {
	mu     sync.Mutex
	ttl    time.Duration
	now    func() time.Time
	leases map[int64]botBodyLease
}

func newBotBodyLeaseManager(ttl time.Duration) *botBodyLeaseManager {
	if ttl <= 0 {
		ttl = defaultBotBodyLeaseTTL
	}
	return &botBodyLeaseManager{
		ttl:    ttl,
		now:    time.Now,
		leases: make(map[int64]botBodyLease),
	}
}

func normalizeBotBodyID(value string) (string, error) {
	bodyID := strings.TrimSpace(value)
	if bodyID == "" || len(bodyID) > maxBotBodyIdentityLength || !botBodyIDPattern.MatchString(bodyID) {
		return "", errInvalidBotBodyID
	}
	return bodyID, nil
}

func legacyBotBodyID(botUID int64) string {
	return fmt.Sprintf("legacy:%d", botUID)
}

func isLegacyBotBodyID(bodyID string) bool {
	return strings.HasPrefix(bodyID, "legacy:")
}

func botBodyIDStrictMode() bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv("CATSCO_REQUIRE_BOT_BODY_ID"))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func (m *botBodyLeaseManager) acquire(botUID int64, bodyID string, connectionID string) (botBodyLeaseResult, error) {
	if m == nil {
		return botBodyLeaseResult{}, nil
	}
	if botUID <= 0 || bodyID == "" || connectionID == "" {
		return botBodyLeaseResult{}, errInvalidBotBodyID
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	now := m.now()
	if existing, ok := m.leases[botUID]; ok {
		if existing.bodyID != bodyID {
			if isLegacyBotBodyID(existing.bodyID) && !isLegacyBotBodyID(bodyID) {
				next := botBodyLease{
					botUID:       botUID,
					bodyID:       bodyID,
					connectionID: connectionID,
					acquiredAt:   now,
					expiresAt:    now.Add(m.ttl),
				}
				m.leases[botUID] = next
				return botBodyLeaseResult{Lease: next, Replaced: true}, nil
			}
			return botBodyLeaseResult{Lease: existing}, errBotBodyLeaseConflict
		}

		next := botBodyLease{
			botUID:       botUID,
			bodyID:       bodyID,
			connectionID: connectionID,
			acquiredAt:   now,
			expiresAt:    now.Add(m.ttl),
		}
		m.leases[botUID] = next
		return botBodyLeaseResult{Lease: next, Replaced: true}, nil
	}

	next := botBodyLease{
		botUID:       botUID,
		bodyID:       bodyID,
		connectionID: connectionID,
		acquiredAt:   now,
		expiresAt:    now.Add(m.ttl),
	}
	m.leases[botUID] = next
	return botBodyLeaseResult{Lease: next}, nil
}

func (m *botBodyLeaseManager) conflicts(botUID int64, bodyID string) (botBodyLease, bool) {
	if m == nil || botUID <= 0 || bodyID == "" {
		return botBodyLease{}, false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	existing, ok := m.leases[botUID]
	if !ok || existing.bodyID == bodyID {
		return botBodyLease{}, false
	}
	if isLegacyBotBodyID(existing.bodyID) && !isLegacyBotBodyID(bodyID) {
		return botBodyLease{}, false
	}
	return existing, true
}

func (m *botBodyLeaseManager) status(botUID int64) (botBodyLease, bool) {
	if m == nil || botUID <= 0 {
		return botBodyLease{}, false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	existing, ok := m.leases[botUID]
	return existing, ok
}

func (m *botBodyLeaseManager) isCurrent(botUID int64, bodyID string, connectionID string) bool {
	if m == nil || botUID <= 0 || bodyID == "" || connectionID == "" {
		return false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	existing, ok := m.leases[botUID]
	return ok && existing.bodyID == bodyID && existing.connectionID == connectionID
}

func (m *botBodyLeaseManager) release(botUID int64, bodyID string, connectionID string) bool {
	if m == nil || botUID <= 0 || bodyID == "" || connectionID == "" {
		return false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	existing, ok := m.leases[botUID]
	if !ok {
		return false
	}
	if existing.bodyID != bodyID || existing.connectionID != connectionID {
		return false
	}
	delete(m.leases, botUID)
	return true
}

func newBotBodyConnectionID() string {
	buf := make([]byte, botBodyConnectionIDLength)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(buf)
}
