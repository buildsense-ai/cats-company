package server

import (
	"github.com/openchat/openchat/server/store/types"
	"log"
	"time"
)

type cloudRegionalImageCache struct {
	images                 []cloudImageSummary
	updatedAt, lastAttempt time.Time
	loaded, refreshing     bool
}

func regionalImageKey(d types.CloudWorkerDeployment) string {
	return d.Env["CTYUN_WORKER_REGION_ID"] + "\x00" + d.Env["CTYUN_IMAGE_PROJECT_ID"]
}

// A region failure must not substitute another region's catalog. All owner
// workers sharing an image project share one bounded background query.
func (h *CloudWorkerHandler) regionalCloudImageSnapshot(d types.CloudWorkerDeployment) ([]cloudImageSummary, bool, bool) {
	key := regionalImageKey(d)
	h.cacheMu.Lock()
	defer h.cacheMu.Unlock()
	if h.regionalImageCaches == nil {
		h.regionalImageCaches = map[string]*cloudRegionalImageCache{}
	}
	entry := h.regionalImageCaches[key]
	if entry == nil {
		entry = &cloudRegionalImageCache{}
		h.regionalImageCaches[key] = entry
	}
	now := time.Now()
	if h.imagesScript != "" && !entry.refreshing && (!entry.loaded || now.Sub(entry.updatedAt) >= cloudWorkerImageSnapshotTTL) && (entry.lastAttempt.IsZero() || now.Sub(entry.lastAttempt) >= cloudWorkerImageRetryDelay) {
		entry.refreshing = true
		entry.lastAttempt = now
		go func() {
			out, err := h.runDeploymentScript(cloudWorkerImageProbeTimeout, d, h.imagesScript)
			h.cacheMu.Lock()
			if err == nil {
				entry.images = parseImageLines(out)
				entry.loaded = true
				entry.updatedAt = time.Now()
			}
			entry.refreshing = false
			h.cacheMu.Unlock()
			if err != nil {
				log.Printf("[cloud-worker] regional image catalog refresh failed: %v", err)
			}
		}()
	}
	return append([]cloudImageSummary(nil), entry.images...), entry.loaded && now.Sub(entry.updatedAt) <= cloudWorkerImageMaxTrustAge, entry.refreshing
}
