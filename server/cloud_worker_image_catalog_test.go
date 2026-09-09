package server

import (
	"github.com/openchat/openchat/server/store/types"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCloudWorkerMetaScopesImageCatalogToOwnedWorkerRegion(t *testing.T) {
	h, store := newCloudWorkerTestHandler("7=3")
	store.ownerBots = []map[string]interface{}{
		{"id": int64(1), "username": "bot-a", "tenant_name": "tenant-a"},
		{"id": int64(2), "username": "bot-b", "tenant_name": "tenant-b"},
	}
	nat := types.CloudWorkerDeployment{Profile: types.CloudWorkerPrivateNAT, Env: map[string]string{"CTYUN_WORKER_REGION_ID": "nat", "CTYUN_IMAGE_PROJECT_ID": "project"}}
	public := types.CloudWorkerDeployment{Profile: types.CloudWorkerPublicIP, Env: map[string]string{"CTYUN_WORKER_REGION_ID": "public", "CTYUN_IMAGE_PROJECT_ID": "project"}}
	h.db = &deploymentTestStore{cloudWorkerTestStore: store, deployments: map[string]types.CloudWorkerDeployment{"tenant-a": nat, "tenant-b": public}}
	now := time.Now()
	h.imagesScript = "must-not-run"
	h.imagesLoaded = true
	h.imageUpdatedAt = now
	h.regionalImageCaches = map[string]*cloudRegionalImageCache{
		regionalImageKey(nat):    {images: []cloudImageSummary{{Version: "nat-only"}}, updatedAt: now, loaded: true},
		regionalImageKey(public): {images: []cloudImageSummary{{Version: "public-only"}}, updatedAt: now, loaded: true},
		"foreign":                {images: []cloudImageSummary{{Version: "foreign-only"}}, updatedAt: now, loaded: true},
	}
	rec := httptest.NewRecorder()
	h.HandleMeta(rec, cloudWorkerRequest(7, http.MethodGet, "/api/cloud-workers/meta", nil))
	if rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}
	data := decodeCloudWorkerList(t, rec)
	catalogs := data["images_by_worker"].(map[string]interface{})
	if len(catalogs) != 2 {
		t.Fatalf("unexpected catalogs: %v", catalogs)
	}
	for tenant, version := range map[string]string{"tenant-a": "nat-only", "tenant-b": "public-only"} {
		rows := catalogs[tenant].([]interface{})
		if len(rows) != 1 || rows[0].(map[string]interface{})["version"] != version {
			t.Fatalf("cross-region images: %v", catalogs)
		}
	}
	h.imagesScript = ""
	h.regionalImageCaches[regionalImageKey(public)].updatedAt = now.Add(-2 * cloudWorkerImageMaxTrustAge)
	_, trusted, _ := h.regionalCloudImageSnapshot(public)
	if trusted {
		t.Fatal("expired regional catalog must not be trusted")
	}
	_, trusted, _ = h.regionalCloudImageSnapshot(nat)
	if !trusted {
		t.Fatal("another region must retain its fresh catalog")
	}
}
