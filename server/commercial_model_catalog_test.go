package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// catalogTestServer serves the relay-admin catalog shape and counts hits, so
// tests can assert the memory cache actually prevents repeat reads.
func catalogTestServer(t *testing.T, models []string, status int) (*httptest.Server, *int32) {
	t.Helper()
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if r.URL.Path != commercialModelCatalogPath {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		if status != http.StatusOK {
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"error":"relay model catalog unavailable"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(catalogPayload{Models: models, Count: len(models), Source: "adapter"})
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func newTestCatalog(t *testing.T, server *httptest.Server) *CommercialModelCatalog {
	t.Helper()
	dir := t.TempDir()
	return &CommercialModelCatalog{
		client: &RelayAdminClient{
			baseURL: server.URL,
			token:   "test-token",
			client:  &http.Client{Timeout: 5 * time.Second},
		},
		snapshotPath: filepath.Join(dir, "catalog.json"),
		ttl:          commercialModelCatalogTTL,
		snapshotTTL:  commercialModelCatalogSnapshotTTL,
		now:          time.Now,
	}
}

// newStubCommercialModelCatalog returns a catalog backed by a test HTTP server
// serving the given models, so handlers under test can validate official paid
// plan saves without reaching a real relay.
func newStubCommercialModelCatalog(t *testing.T, models []string) *CommercialModelCatalog {
	t.Helper()
	server, _ := catalogTestServer(t, models, http.StatusOK)
	return newTestCatalog(t, server)
}

func TestCommercialModelCatalogReadsRelayAndCachesInMemory(t *testing.T) {
	server, hits := catalogTestServer(t, []string{"gpt-6-sol", "gpt-5.6-terra"}, http.StatusOK)
	catalog := newTestCatalog(t, server)

	models, source, err := catalog.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if source != "live" {
		t.Fatalf("first read source = %q, want live", source)
	}
	if strings.Join(models, ",") != "gpt-6-sol,gpt-5.6-terra" {
		t.Fatalf("models = %v", models)
	}

	// A second read inside the TTL must not touch the relay: the catalog is on
	// the plan-save path and must stay cheap.
	models, source, err = catalog.Models(context.Background())
	if err != nil {
		t.Fatalf("cached Models: %v", err)
	}
	if source != "memory" {
		t.Fatalf("second read source = %q, want memory", source)
	}
	if len(models) != 2 {
		t.Fatalf("cached models = %v", models)
	}
	if got := atomic.LoadInt32(hits); got != 1 {
		t.Fatalf("relay hits = %d, want 1 (memory cache must absorb repeat reads)", got)
	}

	// The returned slice must be a copy: callers sort and trim it.
	models[0] = "mutated"
	again, _, err := catalog.Models(context.Background())
	if err != nil {
		t.Fatalf("Models after mutation: %v", err)
	}
	if again[0] == "mutated" {
		t.Fatal("Models returned the cache's own slice; a caller could corrupt it")
	}
}

func TestCommercialModelCatalogRefreshesAfterTTL(t *testing.T) {
	server, hits := catalogTestServer(t, []string{"gpt-6-sol"}, http.StatusOK)
	catalog := newTestCatalog(t, server)

	clock := time.Now()
	catalog.now = func() time.Time { return clock }

	if _, _, err := catalog.Models(context.Background()); err != nil {
		t.Fatalf("Models: %v", err)
	}
	// Advance past the TTL and confirm the relay is consulted again.
	clock = clock.Add(commercialModelCatalogTTL + time.Second)
	if _, source, err := catalog.Models(context.Background()); err != nil || source != "live" {
		t.Fatalf("post-TTL read: source=%q err=%v, want live", source, err)
	}
	if got := atomic.LoadInt32(hits); got != 2 {
		t.Fatalf("relay hits = %d, want 2", got)
	}
}

func TestCommercialModelCatalogFallsBackToSnapshotWhenRelayFails(t *testing.T) {
	server, _ := catalogTestServer(t, []string{"gpt-6-sol", "deepseek-flash"}, http.StatusOK)
	catalog := newTestCatalog(t, server)

	if _, _, err := catalog.Models(context.Background()); err != nil {
		t.Fatalf("priming Models: %v", err)
	}
	if _, err := os.Stat(catalog.snapshotPath); err != nil {
		t.Fatalf("snapshot not written: %v", err)
	}

	// The relay goes down and the memory copy expires: the plan save must keep
	// working against the last known catalog rather than failing.
	server.Close()
	catalog.mu.Lock()
	catalog.models = nil
	catalog.fetchedAt = time.Time{}
	catalog.mu.Unlock()

	models, source, err := catalog.Models(context.Background())
	if err != nil {
		t.Fatalf("Models with relay down: %v", err)
	}
	if source != "snapshot" {
		t.Fatalf("source = %q, want snapshot", source)
	}
	if strings.Join(models, ",") != "gpt-6-sol,deepseek-flash" {
		t.Fatalf("snapshot models = %v", models)
	}
}

func TestCommercialModelCatalogRefusesStaleSnapshot(t *testing.T) {
	server, _ := catalogTestServer(t, []string{"gpt-6-sol"}, http.StatusOK)
	catalog := newTestCatalog(t, server)

	if _, _, err := catalog.Models(context.Background()); err != nil {
		t.Fatalf("priming Models: %v", err)
	}

	// Age the snapshot past its own TTL. Acting on a day-old model list would
	// silently write the wrong set into every plan, so this must fail loudly.
	catalog.mu.Lock()
	catalog.models = nil
	catalog.fetchedAt = time.Time{}
	catalog.mu.Unlock()
	server.Close()
	catalog.now = func() time.Time { return time.Now().Add(commercialModelCatalogSnapshotTTL + time.Hour) }

	if _, _, err := catalog.Models(context.Background()); err == nil {
		t.Fatal("Models accepted an expired snapshot; it must refuse so the caller can report the failure")
	}
}

func TestCommercialModelCatalogTreats503AsFailureNotAnEmptyCatalog(t *testing.T) {
	// The relay answers 503 (never an empty list) when the adapter is
	// unreachable. If the control plane read that as "no models", a save would
	// strip every model from every paid plan.
	server, _ := catalogTestServer(t, nil, http.StatusServiceUnavailable)
	catalog := newTestCatalog(t, server)

	if _, _, err := catalog.Models(context.Background()); err == nil {
		t.Fatal("Models accepted a 503 as a valid catalog")
	}
	if catalog.models != nil {
		t.Fatal("a failed read must not populate the cache")
	}
}

func TestCommercialModelCatalogRejectsEmptyModelList(t *testing.T) {
	// A 200 with no models is as dangerous as a 503: it would empty every plan.
	server, _ := catalogTestServer(t, []string{}, http.StatusOK)
	catalog := newTestCatalog(t, server)

	if _, _, err := catalog.Models(context.Background()); err == nil {
		t.Fatal("Models accepted an empty catalog from the relay")
	}
}

func TestCommercialModelCatalogNormalizesModels(t *testing.T) {
	server, _ := catalogTestServer(t,
		[]string{"  gpt-6-sol  ", "GPT-6-SOL", "", "deepseek-flash", "DEEPSEEK-FLASH"},
		http.StatusOK)
	catalog := newTestCatalog(t, server)

	models, _, err := catalog.Models(context.Background())
	if err != nil {
		t.Fatalf("Models: %v", err)
	}
	if strings.Join(models, ",") != "gpt-6-sol,deepseek-flash" {
		t.Fatalf("models = %v, want trimmed case-insensitive de-duplication", models)
	}
}

func TestCommercialModelCatalogSingleFlightsConcurrentReads(t *testing.T) {
	// A cold cache under concurrent saves must produce one relay call, not one
	// per caller.
	release := make(chan struct{})
	var hits int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		<-release
		_ = json.NewEncoder(w).Encode(catalogPayload{Models: []string{"gpt-6-sol"}})
	}))
	defer server.Close()

	catalog := newTestCatalog(t, server)
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			_, _, errs[index] = catalog.Models(context.Background())
		}(i)
	}
	// Give every goroutine time to join the in-flight fetch before releasing.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
	}
	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("relay hits = %d, want 1 (single flight)", got)
	}
}

func TestCommercialModelCatalogSortedModels(t *testing.T) {
	server, _ := catalogTestServer(t, []string{"zebra-model", "alpha-model"}, http.StatusOK)
	catalog := newTestCatalog(t, server)

	models, err := catalog.SortedModels(context.Background())
	if err != nil {
		t.Fatalf("SortedModels: %v", err)
	}
	if strings.Join(models, ",") != "alpha-model,zebra-model" {
		t.Fatalf("models = %v, want sorted", models)
	}
}

func TestCommercialModelCatalogNilIsAnExplicitError(t *testing.T) {
	var catalog *CommercialModelCatalog
	if _, _, err := catalog.Models(context.Background()); err == nil {
		t.Fatal("a nil catalog must report an error, not an empty model list")
	}
}
