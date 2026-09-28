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

	mu                sync.Mutex
	models            []string
	internalOnly      []string
	internalOnlyKnown bool
	fetchedAt         time.Time
	source            string
	inflight          *catalogFetch
}

// catalogFetch lets concurrent readers share one relay call instead of each
// starting their own when the cache expires.
type catalogFetch struct {
	done              chan struct{}
	models            []string
	internalOnly      []string
	internalOnlyKnown bool
	source            string
	err               error
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
//
// InternalOnly is a pointer so "the relay reported no internal-only models"
// (an empty array) stays distinct from "the relay predates the field" (absent).
// The reconcile needs that difference: reading absence as "none exist" would
// strip a model an internal package grants, and the two are indistinguishable
// once the field decodes into a nil slice.
type catalogPayload struct {
	Models       []string  `json:"models"`
	Count        int       `json:"count"`
	InternalOnly *[]string `json:"internal_only"`
	Source       string    `json:"source"`
}

type catalogSnapshot struct {
	Models []string `json:"models"`
	// InternalOnly keeps the same distinction across a snapshot: a copy written
	// before the split has no field, which must not be read as an assertion that
	// the relay had no internal-only models.
	InternalOnly *[]string `json:"internal_only,omitempty"`
	FetchedAt    time.Time `json:"fetched_at"`
}

// Models returns the sellable model list and the source tier that answered.
//
// Callers that decide whether a plan model should be kept must use Catalog
// instead: without the internal-only list they cannot tell a model the relay
// merely does not sell from one it retired, and the reconcile's removal rule
// treats both as gone. This accessor stays for plan-save validation, which only
// asks whether a name is sellable.
func (c *CommercialModelCatalog) Models(ctx context.Context) ([]string, string, error) {
	models, _, _, source, err := c.Catalog(ctx)
	if err != nil {
		return nil, "", err
	}
	return models, source, nil
}

// Catalog is Models plus the internal-only list, and reports whether the relay
// declared that list at all.
//
// internalOnlyKnown is false when the answer came from a relay (or a snapshot)
// that predates the internal-only split. A caller about to remove a plan model
// must treat that as "unknown", not "none": the model may be one the relay
// routes but does not sell, and removing it would strip an internal package.
func (c *CommercialModelCatalog) Catalog(ctx context.Context) (models, internalOnly []string, internalOnlyKnown bool, source string, err error) {
	if c == nil {
		return nil, nil, false, "", fmt.Errorf("commercial model catalog is not configured")
	}
	now := c.now()

	c.mu.Lock()
	if len(c.models) > 0 && now.Sub(c.fetchedAt) < c.ttl {
		models, source := copyCatalogModels(c.models), "memory"
		internalOnly, known := copyCatalogModels(c.internalOnly), c.internalOnlyKnown
		c.mu.Unlock()
		return models, internalOnly, known, source, nil
	}
	// Single flight: the first caller refreshes, the rest wait on the same
	// result instead of piling onto the relay.
	if flight := c.inflight; flight != nil {
		c.mu.Unlock()
		select {
		case <-flight.done:
		case <-ctx.Done():
			return nil, nil, false, "", ctx.Err()
		}
		if flight.err != nil {
			return nil, nil, false, "", flight.err
		}
		return copyCatalogModels(flight.models), copyCatalogModels(flight.internalOnly), flight.internalOnlyKnown, flight.source, nil
	}
	flight := &catalogFetch{done: make(chan struct{})}
	c.inflight = flight
	c.mu.Unlock()

	models, internalOnly, known, source, err := c.refresh(ctx)

	c.mu.Lock()
	flight.models, flight.internalOnly, flight.internalOnlyKnown, flight.source, flight.err = models, internalOnly, known, source, err
	if err == nil {
		c.models = copyCatalogModels(models)
		c.internalOnly = copyCatalogModels(internalOnly)
		c.internalOnlyKnown = known
		c.fetchedAt = c.now()
		c.source = source
	}
	c.inflight = nil
	c.mu.Unlock()
	close(flight.done)

	if err != nil {
		return nil, nil, false, "", err
	}
	return copyCatalogModels(models), copyCatalogModels(internalOnly), known, source, nil
}

// refresh walks the live -> snapshot ladder. A relay failure is not fatal while
// a usable snapshot exists, because refusing to save a plan is worse than
// saving it against a slightly older model list.
func (c *CommercialModelCatalog) refresh(ctx context.Context) ([]string, []string, bool, string, error) {
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
			// A nil pointer means the relay answered without the field: it predates
			// the split, so the list is unknown rather than empty.
			internalOnly := []string(nil)
			if payload.InternalOnly != nil {
				internalOnly = normalizeCatalogModels(*payload.InternalOnly)
			}
			known := payload.InternalOnly != nil
			c.writeSnapshot(models, internalOnly, known)
			return models, internalOnly, known, "live", nil
		}
	}

	snapshot, snapErr := c.readSnapshot()
	if snapErr != nil {
		return nil, nil, false, "", fmt.Errorf("relay model catalog unavailable: %w", err)
	}
	if age := c.now().Sub(snapshot.FetchedAt); age > c.snapshotTTL {
		return nil, nil, false, "", fmt.Errorf(
			"relay model catalog unavailable and the cached copy is %s old: %w",
			age.Round(time.Minute), err,
		)
	}
	// A snapshot written before the split has no field, so the list is unknown
	// for the same reason a live answer without it is.
	internalOnly := []string(nil)
	if snapshot.InternalOnly != nil {
		internalOnly = normalizeCatalogModels(*snapshot.InternalOnly)
	}
	return snapshot.Models, internalOnly, snapshot.InternalOnly != nil, "snapshot", nil
}

// writeSnapshot persists the last good catalog. A write failure is logged by
// the caller's error path rather than failing the read, because the in-memory
// copy is still usable.
//
// internalOnly is stored with the models so a relay outage cannot lose the list
// that keeps an internal package's models from being stripped: falling back to a
// snapshot without it would make the reconcile treat them as retired.
func (c *CommercialModelCatalog) writeSnapshot(models, internalOnly []string, internalOnlyKnown bool) {
	if c.snapshotPath == "" {
		return
	}
	snapshot := catalogSnapshot{Models: models, FetchedAt: c.now()}
	if internalOnlyKnown {
		// Store the empty list explicitly so the next read can tell it from the
		// absent field a pre-split snapshot has.
		list := internalOnly
		if list == nil {
			list = []string{}
		}
		snapshot.InternalOnly = &list
	}
	data, err := json.Marshal(snapshot)
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
