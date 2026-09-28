package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// CommercialModelCatalog answers "which models may a paid plan sell right now".
//
// The relay owns that answer: a model becomes purchasable only after it is
// registered in the adapter, and relay-admin republishes the adapter's catalog
// at /local/model-catalog because the control plane cannot reach the adapter
// itself. Paid plans used to carry a hardcoded model list, so every relay model
// launch needed a control-plane change; reading the relay instead keeps the two
// in step with no deploy.
//
// Reads are deliberately layered so a relay outage cannot block an operator's
// save or silently empty every plan:
//
//  1. memory   - fresh within ttl, zero network and zero database work
//  2. live     - refresh from relay-admin when the memory copy is stale
//  3. snapshot - last successful answer persisted to disk, used while relay is down
//  4. stale    - snapshot older than snapshotTTL is refused, so callers can
//     surface a real error instead of acting on an ancient list
type CommercialModelCatalog struct {
	client       *RelayAdminClient
	snapshotPath string
	ttl          time.Duration
	snapshotTTL  time.Duration
	now          func() time.Time

	mu           sync.Mutex
	models       []string
	internalOnly []string
	fetchedAt    time.Time
	source       string
	inflight     *catalogFetch
}

// catalogFetch lets concurrent readers share one relay call instead of each
// starting their own when the cache expires.
type catalogFetch struct {
	done         chan struct{}
	models       []string
	internalOnly []string
	source       string
	err          error
}

const (
	commercialModelCatalogPath = "/local/model-catalog"
	// A plan save is an interactive action, so the relay read must not hold the
	// request for long; a slow relay should fall back to the snapshot instead.
	commercialModelCatalogTimeout = 3 * time.Second
	// The catalog changes only when a model is onboarded, so a five-minute
	// memory TTL is plenty and keeps relay traffic negligible.
	commercialModelCatalogTTL = 5 * time.Minute
	// Past this age the snapshot is too old to trust for a write decision.
	commercialModelCatalogSnapshotTTL = 24 * time.Hour
)

// commercialModelCatalogSnapshotPath is where the last successful catalog is
// persisted. It follows the same env override as the rest of the deployment so
// tests and local runs can point it at a temp dir.
func commercialModelCatalogSnapshotPath() string {
	if raw := strings.TrimSpace(os.Getenv("CATSCO_COMMERCIAL_MODEL_CATALOG_PATH")); raw != "" {
		return raw
	}
	if dir := strings.TrimSpace(os.Getenv("CATSCO_DATA_DIR")); dir != "" {
		return filepath.Join(dir, "commercial-model-catalog.json")
	}
	return "/app/uploads/commercial-model-catalog.json"
}

// NewCommercialModelCatalogFromEnv builds the catalog reader from the relay
// admin client env. It returns nil when relay-admin is not configured, which
// callers treat as "catalog unavailable" rather than "no models exist".
func NewCommercialModelCatalogFromEnv() *CommercialModelCatalog {
	client := NewRelayAdminClientFromEnv()
	if client == nil {
		return nil
	}
	return &CommercialModelCatalog{
		client:       client,
		snapshotPath: commercialModelCatalogSnapshotPath(),
		ttl:          commercialModelCatalogTTL,
		snapshotTTL:  commercialModelCatalogSnapshotTTL,
		now:          time.Now,
	}
}

// catalogPayload is the relay-admin response shape. "source" is informational
// and deliberately not trusted for the fallback decision.
type catalogPayload struct {
	Models       []string `json:"models"`
	Count        int      `json:"count"`
	InternalOnly []string `json:"internal_only"`
	Source       string   `json:"source"`
}

type catalogSnapshot struct {
	Models       []string  `json:"models"`
	InternalOnly []string  `json:"internal_only,omitempty"`
	FetchedAt    time.Time `json:"fetched_at"`
}

// Models returns the sellable model list and the internal-only list, refreshing
// from the relay when the in-memory copy is stale. The returned slices are
// copies: callers may sort or trim them without corrupting the cache.
//
// The two lists are separate because they answer different questions. Sellable
// is what a plan may offer; internal-only is what an internal/custom package may
// keep even though no public plan sells it. A caller that only has the sellable
// list cannot tell an internal-only model from a retired one, which is how a
// reconcile ends up stripping a model the internal package grants.
func (c *CommercialModelCatalog) Models(ctx context.Context) ([]string, string, error) {
	models, internalOnly, source, err := c.Catalog(ctx)
	_ = internalOnly
	return models, source, err
}

// Catalog is Models plus the internal-only list. Prefer this from callers that
// make a keep-or-drop decision about a plan model.
func (c *CommercialModelCatalog) Catalog(ctx context.Context) ([]string, []string, string, error) {
	if c == nil {
		return nil, nil, "", fmt.Errorf("commercial model catalog is not configured")
	}
	now := c.now()

	c.mu.Lock()
	if len(c.models) > 0 && now.Sub(c.fetchedAt) < c.ttl {
		models, internalOnly, source := copyCatalogModels(c.models), copyCatalogModels(c.internalOnly), "memory"
		c.mu.Unlock()
		return models, internalOnly, source, nil
	}
	// Single flight: the first caller refreshes, the rest wait on the same
	// result instead of piling onto the relay.
	if flight := c.inflight; flight != nil {
		c.mu.Unlock()
		select {
		case <-flight.done:
		case <-ctx.Done():
			return nil, nil, "", ctx.Err()
		}
		if flight.err != nil {
			return nil, nil, "", flight.err
		}
		return copyCatalogModels(flight.models), copyCatalogModels(flight.internalOnly), flight.source, nil
	}
	flight := &catalogFetch{done: make(chan struct{})}
	c.inflight = flight
	c.mu.Unlock()

	models, internalOnly, source, err := c.refresh(ctx)

	c.mu.Lock()
	flight.models, flight.internalOnly, flight.source, flight.err = models, internalOnly, source, err
	if err == nil {
		c.models = copyCatalogModels(models)
		c.internalOnly = copyCatalogModels(internalOnly)
		c.fetchedAt = c.now()
		c.source = source
	}
	c.inflight = nil
	c.mu.Unlock()
	close(flight.done)

	if err != nil {
		return nil, nil, "", err
	}
	return copyCatalogModels(models), copyCatalogModels(internalOnly), source, nil
}

// refresh walks the live -> snapshot ladder. A relay failure is not fatal while
// a usable snapshot exists, because refusing to save a plan is worse than
// saving it against a slightly older model list.
func (c *CommercialModelCatalog) refresh(ctx context.Context) ([]string, []string, string, error) {
	liveCtx, cancel := context.WithTimeout(ctx, commercialModelCatalogTimeout)
	defer cancel()

	var payload catalogPayload
	err := c.client.Do(liveCtx, "GET", commercialModelCatalogPath, nil, &payload)
	if err == nil {
		models := normalizeCatalogModels(payload.Models)
		if len(models) == 0 {
			// The relay contract is explicit that an unreachable adapter answers
			// 503 rather than an empty list, so an empty list here means the
			// relay is confused. Treat it as a failure and keep the snapshot:
			// acting on it would strip every model from every paid plan.
			err = fmt.Errorf("relay model catalog returned no models")
		} else {
			internalOnly := normalizeCatalogModels(payload.InternalOnly)
			c.writeSnapshot(models, internalOnly)
			return models, internalOnly, "live", nil
		}
	}

	snapshot, snapErr := c.readSnapshot()
	if snapErr != nil {
		return nil, nil, "", fmt.Errorf("relay model catalog unavailable: %w", err)
	}
	if age := c.now().Sub(snapshot.FetchedAt); age > c.snapshotTTL {
		return nil, nil, "", fmt.Errorf(
			"relay model catalog unavailable and the cached copy is %s old: %w",
			age.Round(time.Minute), err,
		)
	}
	return snapshot.Models, snapshot.InternalOnly, "snapshot", nil
}

// writeSnapshot persists the last good catalog. A write failure is logged by
// the caller's error path rather than failing the read, because the in-memory
// copy is still usable.
//
// internalOnly is stored with the models so a relay outage cannot lose the list
// that keeps an internal package's models from being stripped: falling back to a
// snapshot without it would make the reconcile treat them as retired.
func (c *CommercialModelCatalog) writeSnapshot(models, internalOnly []string) {
	if c.snapshotPath == "" {
		return
	}
	data, err := json.Marshal(catalogSnapshot{Models: models, InternalOnly: internalOnly, FetchedAt: c.now()})
	if err != nil {
		return
	}
	if dir := filepath.Dir(c.snapshotPath); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return
		}
	}
	// Write to a temporary file and rename so a crash mid-write cannot leave a
	// truncated snapshot that the next startup would read as the real catalog.
	tmp := c.snapshotPath + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, c.snapshotPath)
}

func (c *CommercialModelCatalog) readSnapshot() (catalogSnapshot, error) {
	var snapshot catalogSnapshot
	if c.snapshotPath == "" {
		return snapshot, fmt.Errorf("no snapshot path configured")
	}
	data, err := os.ReadFile(c.snapshotPath)
	if err != nil {
		return snapshot, err
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return snapshot, err
	}
	models := normalizeCatalogModels(snapshot.Models)
	if len(models) == 0 {
		return snapshot, fmt.Errorf("snapshot contains no models")
	}
	snapshot.Models = models
	snapshot.InternalOnly = normalizeCatalogModels(snapshot.InternalOnly)
	return snapshot, nil
}

// normalizeCatalogModels trims, de-duplicates case-insensitively (keeping the
// relay's own spelling and order), and drops empties. Relay-admin already does
// this, but the control plane must not depend on the relay for its own
// well-formedness.
func normalizeCatalogModels(models []string) []string {
	out := make([]string, 0, len(models))
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		key := strings.ToLower(model)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, model)
	}
	return out
}

func copyCatalogModels(models []string) []string {
	out := make([]string, len(models))
	copy(out, models)
	return out
}

// SortedModels returns the catalog sorted for stable display and diffing.
func (c *CommercialModelCatalog) SortedModels(ctx context.Context) ([]string, error) {
	models, _, err := c.Models(ctx)
	if err != nil {
		return nil, err
	}
	sort.Strings(models)
	return models, nil
}

// commercialCatalogModelSet indexes a catalog for membership checks.
func commercialCatalogModelSet(models []string) map[string]string {
	set := make(map[string]string, len(models))
	for _, model := range models {
		set[strings.ToLower(strings.TrimSpace(model))] = model
	}
	return set
}
