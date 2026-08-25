// Package server - cloud virtual employee control plane.
//
// Cloud workers are virtual employees that run on Tianyi cloud worker images
// (built and managed by the XiaoBa-CLI ops pipeline). This handler exposes the
// web control plane used by the "云托管" entry in the AI-assistant store modal:
//
//   - create quota (CATSCO_WORKER_CREATE_QUOTA, unset = 0 = disabled)
//   - cloud worker roster (name / status / version / image)
//   - update / rollback (keep data, swap Part A artifacts) vs reset (drop data,
//     destroy and recreate from image) — strictly separate, documented actions
//
// Heavy cloud operations (provision / rollback / reset / image list) are
// delegated to executable scripts configured through environment variables so
// credentials stay server-side. Scripts run on the Linux server image (no
// PowerShell), so each one must be an executable file with a proper shebang
// (e.g. #!/usr/bin/env bash). When a script is not configured the matching
// endpoint returns 503, which keeps the control plane safe to ship without the
// worker pipeline wired up.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// CloudWorkerHandler exposes the cloud-managed virtual employee control plane.
type CloudWorkerHandler struct {
	db   store.Store
	bots *BotHandler

	// create quota per owner uid, from CATSCO_WORKER_CREATE_QUOTA.
	quota map[int64]int

	// Executable scripts invoked for heavy cloud operations (empty = disabled).
	provisionScript string
	resetScript     string
	renewScript     string
	updateScript    string
	rollbackScript  string
	destroyScript   string
	imagesScript    string
	releasesScript  string
	statusScript    string
	credits         CloudWorkerCreditStore

	scriptTimeout time.Duration

	// opMu serializes all cloud operations (create / update / rollback / reset /
	// delete). These are low-frequency, long-running, paid-instance actions;
	// a global lock keeps quota checks atomic and prevents a single user from
	// piling up concurrent script processes.
	opMu sync.Mutex

	// Cloud provider reads take seconds and must never sit on the HTTP request
	// path. Requests read the latest completed snapshot while one background
	// refresh (at most) updates it. Failed refreshes retain the last good value.
	cacheMu sync.Mutex

	statusSnapshot       map[string]cloudInstanceInfo
	statusUpdatedAt      time.Time
	statusLastAttempt    time.Time
	statusLoaded         bool
	statusRefreshing     bool
	statusRefreshPending bool

	imageSnapshot       []cloudImageSummary
	imageUpdatedAt      time.Time
	imageLastAttempt    time.Time
	imagesLoaded        bool
	imagesRefreshing    bool
	imageRefreshPending bool

	releaseSnapshot       []cloudReleaseSummary
	releaseUpdatedAt      time.Time
	releaseLastAttempt    time.Time
	releasesLoaded        bool
	releasesRefreshing    bool
	releaseRefreshPending bool
}

const (
	cloudWorkerStatusSnapshotTTL  = 10 * time.Second
	cloudWorkerStatusRetryDelay   = 15 * time.Second
	cloudWorkerStatusMaxTrustAge  = 2 * time.Minute
	cloudWorkerImageSnapshotTTL   = time.Minute
	cloudWorkerImageRetryDelay    = 15 * time.Second
	cloudWorkerImageMaxTrustAge   = 10 * time.Minute
	cloudWorkerReleaseSnapshotTTL = time.Minute
	cloudWorkerReleaseRetryDelay  = 15 * time.Second
	cloudWorkerReleaseMaxTrustAge = 10 * time.Minute
	// Provider status checks may need one or more paginated ctyun calls plus a
	// bounded SSH version probe. Keep this longer than the script's per-call
	// timeout so a valid provider response is not discarded by the control plane.
	cloudWorkerStatusProbeTimeout  = 2 * time.Minute
	cloudWorkerImageProbeTimeout   = 30 * time.Second
	cloudWorkerReleaseProbeTimeout = 30 * time.Second
)

// workerUsernameRe constrains cloud worker usernames so the derived tenant
// name stays safe to embed in URL paths and script argv.
var workerUsernameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,63}$`)
var workerTenantSeparatorRe = regexp.MustCompile(`-+`)

func cloudWorkerTenantName(username string) string {
	name := workerTenantSeparatorRe.ReplaceAllString("bot-"+strings.TrimSpace(username), "-")
	return strings.Trim(name, "-.")
}

// CloudWorkerConfig configures the cloud worker control plane.
type CloudWorkerConfig struct {
	CreateQuota     string // CATSCO_WORKER_CREATE_QUOTA "<uid>=<n>;<uid>=<n>" — unset means 0 (disabled)
	ProvisionScript string // CATSCO_WORKER_PROVISION_SCRIPT
	ResetScript     string // CATSCO_WORKER_RESET_SCRIPT
	RenewScript     string // CATSCO_WORKER_RENEW_SCRIPT
	UpdateScript    string // CATSCO_WORKER_UPDATE_SCRIPT
	RollbackScript  string // CATSCO_WORKER_ROLLBACK_SCRIPT
	DestroyScript   string // CATSCO_WORKER_DESTROY_SCRIPT
	ImagesScript    string // CATSCO_WORKER_IMAGES_SCRIPT
	ReleasesScript  string // CATSCO_WORKER_RELEASES_SCRIPT
	StatusScript    string // CATSCO_WORKER_STATUS_SCRIPT (batch instance status TSV; empty = status is "unavailable")
}

// CloudWorkerCreditStore is optional for compatibility with focused test
// stores. Production adapters persist one-time paid cloud-worker credits.
type CloudWorkerCreditStore interface {
	CloudWorkerCreditSummary(uid int64) (total, available int, err error)
	ReserveCloudWorkerCredit(uid int64, reservation string) (bool, error)
	CommitCloudWorkerCredit(uid int64, reservation string, workerUID int64, tenantName string, graceDays int) error
	ReleaseCloudWorkerCredit(uid int64, reservation string) error
	ExtendCloudWorkerLifecycles(uid int64, expiresAt time.Time, graceDays int) error
	ListCloudWorkerLifecycleDue(now time.Time, limit int) ([]CloudWorkerLifecycle, error)
	MarkCloudWorkerLifecyclePending(id int64, deleteAfter time.Time) error
	ClaimCloudWorkerLifecycleDeletion(id int64) (bool, error)
	MarkCloudWorkerLifecycleDeleted(id int64, errText string) error
}

// CloudWorkerLifecycleExtender is the per-instance form used when the
// provider returns an authoritative expiry for a renewal. Keeping this
// optional preserves compatibility with focused/test stores that only expose
// the older owner-wide extension method.
type CloudWorkerLifecycleExtender interface {
	ExtendCloudWorkerLifecycle(id int64, expiresAt time.Time, graceDays int) error
}

// CloudWorkerCreditAdminStore is the narrow operator-only surface for
// granting additional one-time cloud-worker credits. Paid plans create
// credits during payment fulfillment; manual credits must go through this
// admin path rather than the static rollout quota.
type CloudWorkerCreditAdminStore interface {
	GrantCloudWorkerCredits(uid int64, count int, sourceRef string, expiresAt *time.Time) (int, error)
}

// CloudWorkerLifecycleRegistrar lets the create path persist an immediately
// due cleanup row when a provider instance was created but its paid credit was
// revoked concurrently (for example by a refund). It is intentionally
// separate from CloudWorkerCreditStore so focused test stores remain small.
type CloudWorkerLifecycleRegistrar interface {
	RegisterCloudWorkerLifecycle(workerUID, ownerUID int64, tenantName string, packageExpiresAt time.Time, graceDays int) error
}

type CloudWorkerLifecycle = types.CloudWorkerLifecycle

// Tianyi ECS monthly instances enter a provider-side frozen retention period
// of 15 days after package expiry. Keep our lifecycle deadline aligned with
// that documented window so we never promise a shorter recovery period.
const cloudWorkerExpiryGraceDays = 15

// CloudWorkerConfigFromEnv reads configuration from the environment.
func CloudWorkerConfigFromEnv() CloudWorkerConfig {
	return CloudWorkerConfig{
		CreateQuota:     strings.TrimSpace(os.Getenv("CATSCO_WORKER_CREATE_QUOTA")),
		ProvisionScript: strings.TrimSpace(os.Getenv("CATSCO_WORKER_PROVISION_SCRIPT")),
		ResetScript:     strings.TrimSpace(os.Getenv("CATSCO_WORKER_RESET_SCRIPT")),
		RenewScript:     strings.TrimSpace(os.Getenv("CATSCO_WORKER_RENEW_SCRIPT")),
		UpdateScript:    strings.TrimSpace(os.Getenv("CATSCO_WORKER_UPDATE_SCRIPT")),
		RollbackScript:  strings.TrimSpace(os.Getenv("CATSCO_WORKER_ROLLBACK_SCRIPT")),
		DestroyScript:   strings.TrimSpace(os.Getenv("CATSCO_WORKER_DESTROY_SCRIPT")),
		ImagesScript:    strings.TrimSpace(os.Getenv("CATSCO_WORKER_IMAGES_SCRIPT")),
		ReleasesScript:  strings.TrimSpace(os.Getenv("CATSCO_WORKER_RELEASES_SCRIPT")),
		StatusScript:    strings.TrimSpace(os.Getenv("CATSCO_WORKER_STATUS_SCRIPT")),
	}
}

// NewCloudWorkerHandler creates a CloudWorkerHandler.
func NewCloudWorkerHandler(db store.Store, bots *BotHandler, cfg CloudWorkerConfig) *CloudWorkerHandler {
	handler := &CloudWorkerHandler{
		db:              db,
		bots:            bots,
		quota:           parseWorkerCreateQuota(cfg.CreateQuota),
		provisionScript: cfg.ProvisionScript,
		resetScript:     cfg.ResetScript,
		renewScript:     cfg.RenewScript,
		updateScript:    cfg.UpdateScript,
		rollbackScript:  cfg.RollbackScript,
		destroyScript:   cfg.DestroyScript,
		imagesScript:    cfg.ImagesScript,
		releasesScript:  cfg.ReleasesScript,
		statusScript:    cfg.StatusScript,
		credits:         nil,
		scriptTimeout:   10 * time.Minute,
	}
	if credits, ok := db.(CloudWorkerCreditStore); ok {
		handler.credits = credits
	}

	// Warm snapshots as soon as the service starts. This is deliberately
	// asynchronous: startup and the first user request remain independent from
	// Tianyi API latency or availability.
	handler.requestCloudStatusRefresh(true)
	handler.requestCloudImageRefresh(true)
	handler.requestCloudReleaseRefresh(true)
	return handler
}

func (h *CloudWorkerHandler) tryBeginOperation(w http.ResponseWriter) bool {
	if h.opMu.TryLock() {
		return true
	}
	w.Header().Set("Retry-After", "15")
	writeJSON(w, http.StatusConflict, map[string]string{
		"error": "another cloud worker operation is already running",
		"code":  "cloud_worker_operation_busy",
	})
	return false
}

// parseWorkerCreateQuota parses "CATSCO_WORKER_CREATE_QUOTA" of the form
// "<uid>=<n>;<uid>=<n>". Unknown or malformed entries are ignored; an unset or
// empty variable yields an empty map (everyone has quota 0 = cannot create).
func parseWorkerCreateQuota(raw string) map[int64]int {
	quota := map[int64]int{}
	for _, item := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ';' || r == ',' || r == '\n' || r == '\t'
	}) {
		parts := strings.SplitN(strings.TrimSpace(item), "=", 2)
		if len(parts) != 2 {
			continue
		}
		uid, uidErr := strconv.ParseInt(strings.TrimSpace(parts[0]), 10, 64)
		n, nErr := strconv.Atoi(strings.TrimSpace(parts[1]))
		if uidErr == nil && nErr == nil && uid > 0 && n >= 0 {
			quota[uid] = n
		}
	}
	return quota
}

// cloudWorkerSummary is a roster item for a cloud-managed virtual employee.
type cloudWorkerSummary struct {
	UID         int64  `json:"uid"`
	Username    string `json:"username"`
	DisplayName string `json:"display_name"`
	TenantName  string `json:"tenant_name"`
	Status      string `json:"status"`
	Version     string `json:"version,omitempty"`
	ImageID     string `json:"image_id,omitempty"`
	CreatedTime string `json:"created_time,omitempty"`

	// Cloud-side facts resolved via the status script (empty = unknown).
	CloudStatus  string `json:"cloud_status,omitempty"`
	CloudVersion string `json:"cloud_version,omitempty"`
	CloudImageID string `json:"cloud_image_id,omitempty"`
	// AppVersion is always present: an empty value means the runtime version
	// has not been observed and must not fall back to the bot definition.
	AppVersion string `json:"app_version"`
}

// cloudWorkersOfOwner returns the cloud-managed workers owned by uid
// (bots with a non-empty tenant_name).
func (h *CloudWorkerHandler) cloudWorkersOfOwner(uid int64) ([]cloudWorkerSummary, error) {
	bots, err := h.db.ListBotsByOwner(uid)
	if err != nil {
		return nil, err
	}
	workers := []cloudWorkerSummary{}
	for _, b := range bots {
		tenantName, _ := b["tenant_name"].(string)
		if tenantName == "" {
			continue
		}
		w := cloudWorkerSummary{
			TenantName: tenantName,
			Status:     "unknown",
		}
		if id, ok := b["id"].(int64); ok {
			w.UID = id
		}
		if s, ok := b["username"].(string); ok {
			w.Username = s
		}
		if s, ok := b["display_name"].(string); ok {
			w.DisplayName = s
		}
		workers = append(workers, w)
	}
	sort.Slice(workers, func(i, j int) bool { return workers[i].Username < workers[j].Username })
	return workers, nil
}

// quotaInfo computes the current quota state for uid.
func (h *CloudWorkerHandler) quotaInfo(uid int64, used int) (total, remaining int) {
	total = h.quota[uid]
	remaining = total - used
	if remaining < 0 {
		remaining = 0
	}
	return total, remaining
}

func (h *CloudWorkerHandler) creditInfo(uid int64) (total, available int) {
	if h.credits == nil {
		return 0, 0
	}
	total, available, err := h.credits.CloudWorkerCreditSummary(uid)
	if err != nil {
		log.Printf("[cloud-worker] credit summary for uid %d failed: %v", uid, err)
		return 0, 0
	}
	return total, available
}

// HandleList handles GET /api/cloud-workers — cloud worker roster + quota.
func (h *CloudWorkerHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	uid := UIDFromContext(r.Context())
	if uid == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	workers, err := h.cloudWorkersOfOwner(uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list cloud workers"})
		return
	}

	// Fill cloud-side facts from the latest completed provider snapshot. A
	// stale/missing snapshot refreshes in the background; this request never
	// waits for Tianyi CLI or provider latency.
	for i := range workers {
		workers[i].CloudStatus = "unavailable"
	}
	infos, statusLoaded, statusRefreshing, statusUpdatedAt := h.cloudStatusSnapshot()
	if statusLoaded {
		for i := range workers {
			workers[i].CloudStatus = "missing"
			info, ok := infos[workers[i].TenantName]
			if !ok {
				continue
			}
			if info.Status == "" {
				workers[i].CloudStatus = "unknown"
			} else {
				workers[i].CloudStatus = info.Status
			}
			workers[i].CloudImageID = info.ImageID
			workers[i].CloudVersion = info.Version
			workers[i].AppVersion = info.AppVersion
		}
	}

	staticTotal, staticRemaining := h.quotaInfo(uid, len(workers))
	creditTotal, creditAvailable := h.creditInfo(uid)
	total, remaining := staticTotal+creditTotal, staticRemaining+creditAvailable
	response := map[string]interface{}{
		"workers":           workers,
		"status_refreshing": statusRefreshing,
		"quota": map[string]interface{}{
			"enabled":   total > 0,
			"total":     total,
			"used":      len(workers),
			"remaining": remaining,
		},
	}
	if statusLoaded {
		response["status_cached_at"] = statusUpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	writeJSON(w, http.StatusOK, response)
}

// cloudInstanceInfo is one worker instance's cloud-side fact set.
type cloudInstanceInfo struct {
	Status     string
	ImageID    string
	Version    string
	AppVersion string
}

// parseCloudWorkerStatusTSV parses status-worker.sh output lines of the form
// "instanceName<TAB>instanceStatus<TAB>imageID<TAB>imageVersion<TAB>appVersion"
// keyed by tenant name (instanceName minus the "worker-" prefix). The fifth
// column is optional so rolling deployments can consume the old four-column
// status script output. Malformed lines are ignored.
func parseCloudWorkerStatusTSV(out string) map[string]cloudInstanceInfo {
	infos := map[string]cloudInstanceInfo{}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) < 2 {
			continue
		}
		name := strings.TrimSpace(parts[0])
		if !strings.HasPrefix(name, "worker-") {
			continue
		}
		info := cloudInstanceInfo{Status: strings.ToLower(strings.TrimSpace(parts[1]))}
		if len(parts) >= 4 {
			info.ImageID = strings.TrimSpace(parts[2])
			info.Version = strings.TrimSpace(parts[3])
		}
		if len(parts) >= 5 {
			info.AppVersion = strings.TrimSpace(parts[4])
		}
		infos[strings.TrimPrefix(name, "worker-")] = info
	}
	return infos
}

// cloudStatusSnapshot returns immediately with the last completed provider
// read and starts a stale refresh in the background. The bools report whether
// a usable snapshot exists and whether a newer one is currently being fetched.
func (h *CloudWorkerHandler) cloudStatusSnapshot() (map[string]cloudInstanceInfo, bool, bool, time.Time) {
	h.requestCloudStatusRefresh(false)

	h.cacheMu.Lock()
	defer h.cacheMu.Unlock()
	infos := make(map[string]cloudInstanceInfo, len(h.statusSnapshot))
	for name, info := range h.statusSnapshot {
		infos[name] = info
	}
	trusted := h.statusLoaded && time.Since(h.statusUpdatedAt) <= cloudWorkerStatusMaxTrustAge
	return infos, trusted, h.statusRefreshing, h.statusUpdatedAt
}

func (h *CloudWorkerHandler) requestCloudStatusRefresh(force bool) {
	if h.statusScript == "" {
		return
	}

	now := time.Now()
	h.cacheMu.Lock()
	due := !h.statusLoaded || now.Sub(h.statusUpdatedAt) >= cloudWorkerStatusSnapshotTTL
	retryAllowed := h.statusLastAttempt.IsZero() || now.Sub(h.statusLastAttempt) >= cloudWorkerStatusRetryDelay
	if (!due && !force) || (!retryAllowed && !force) {
		h.cacheMu.Unlock()
		return
	}
	if h.statusRefreshing {
		if force {
			h.statusRefreshPending = true
		}
		h.cacheMu.Unlock()
		return
	}
	h.statusRefreshing = true
	h.statusLastAttempt = now
	h.cacheMu.Unlock()

	go h.refreshCloudStatusSnapshot()
}

func (h *CloudWorkerHandler) refreshCloudStatusSnapshot() {
	out, err := h.runScriptTimeout(cloudWorkerStatusProbeTimeout, h.statusScript)

	h.cacheMu.Lock()
	if err == nil {
		h.statusSnapshot = parseCloudWorkerStatusTSV(out)
		h.statusUpdatedAt = time.Now()
		h.statusLoaded = true
	}
	pending := h.statusRefreshPending
	h.statusRefreshPending = false
	h.statusRefreshing = false
	h.cacheMu.Unlock()

	if err != nil {
		log.Printf("[cloud-worker] status snapshot refresh failed: %v", err)
	}
	if pending {
		h.requestCloudStatusRefresh(true)
	}
}

func (h *CloudWorkerHandler) cloudImageSnapshot() ([]cloudImageSummary, bool, bool, time.Time) {
	h.requestCloudImageRefresh(false)

	h.cacheMu.Lock()
	defer h.cacheMu.Unlock()
	images := append([]cloudImageSummary(nil), h.imageSnapshot...)
	trusted := h.imagesLoaded && time.Since(h.imageUpdatedAt) <= cloudWorkerImageMaxTrustAge
	return images, trusted, h.imagesRefreshing, h.imageUpdatedAt
}

func (h *CloudWorkerHandler) requestCloudImageRefresh(force bool) {
	if h.imagesScript == "" {
		return
	}

	now := time.Now()
	h.cacheMu.Lock()
	due := !h.imagesLoaded || now.Sub(h.imageUpdatedAt) >= cloudWorkerImageSnapshotTTL
	retryAllowed := h.imageLastAttempt.IsZero() || now.Sub(h.imageLastAttempt) >= cloudWorkerImageRetryDelay
	if (!due && !force) || (!retryAllowed && !force) {
		h.cacheMu.Unlock()
		return
	}
	if h.imagesRefreshing {
		if force {
			h.imageRefreshPending = true
		}
		h.cacheMu.Unlock()
		return
	}
	h.imagesRefreshing = true
	h.imageLastAttempt = now
	h.cacheMu.Unlock()

	go h.refreshCloudImageSnapshot()
}

func (h *CloudWorkerHandler) refreshCloudImageSnapshot() {
	out, err := h.runScriptTimeout(cloudWorkerImageProbeTimeout, h.imagesScript)

	h.cacheMu.Lock()
	if err == nil {
		h.imageSnapshot = parseImageLines(out)
		h.imageUpdatedAt = time.Now()
		h.imagesLoaded = true
	}
	pending := h.imageRefreshPending
	h.imageRefreshPending = false
	h.imagesRefreshing = false
	h.cacheMu.Unlock()

	if err != nil {
		log.Printf("[cloud-worker] image snapshot refresh failed: %v", err)
	}
	if pending {
		h.requestCloudImageRefresh(true)
	}
}

func (h *CloudWorkerHandler) cloudReleaseSnapshot() ([]cloudReleaseSummary, bool, bool, time.Time) {
	h.requestCloudReleaseRefresh(false)

	h.cacheMu.Lock()
	defer h.cacheMu.Unlock()
	releases := append([]cloudReleaseSummary(nil), h.releaseSnapshot...)
	trusted := h.releasesLoaded && time.Since(h.releaseUpdatedAt) <= cloudWorkerReleaseMaxTrustAge
	return releases, trusted, h.releasesRefreshing, h.releaseUpdatedAt
}

func (h *CloudWorkerHandler) requestCloudReleaseRefresh(force bool) {
	if h.releasesScript == "" {
		return
	}

	now := time.Now()
	h.cacheMu.Lock()
	due := !h.releasesLoaded || now.Sub(h.releaseUpdatedAt) >= cloudWorkerReleaseSnapshotTTL
	retryAllowed := h.releaseLastAttempt.IsZero() || now.Sub(h.releaseLastAttempt) >= cloudWorkerReleaseRetryDelay
	if (!due && !force) || (!retryAllowed && !force) {
		h.cacheMu.Unlock()
		return
	}
	if h.releasesRefreshing {
		if force {
			h.releaseRefreshPending = true
		}
		h.cacheMu.Unlock()
		return
	}
	h.releasesRefreshing = true
	h.releaseLastAttempt = now
	h.cacheMu.Unlock()

	go h.refreshCloudReleaseSnapshot()
}

func (h *CloudWorkerHandler) refreshCloudReleaseSnapshot() {
	out, err := h.runScriptTimeout(cloudWorkerReleaseProbeTimeout, h.releasesScript)

	h.cacheMu.Lock()
	if err == nil {
		h.releaseSnapshot = parseReleaseLines(out)
		h.releaseUpdatedAt = time.Now()
		h.releasesLoaded = true
	}
	pending := h.releaseRefreshPending
	h.releaseRefreshPending = false
	h.releasesRefreshing = false
	h.cacheMu.Unlock()

	if err != nil {
		log.Printf("[cloud-worker] release snapshot refresh failed: %v", err)
	}
	if pending {
		h.requestCloudReleaseRefresh(true)
	}
}

// HandleMeta handles GET /api/cloud-workers/meta — quota, application releases
// and base images. Application update/rollback targets must not be inferred
// from the independently managed image catalog.
func (h *CloudWorkerHandler) HandleMeta(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	uid := UIDFromContext(r.Context())
	if uid == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	workers, err := h.cloudWorkersOfOwner(uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list cloud workers"})
		return
	}
	total, remaining := h.quotaInfo(uid, len(workers))

	meta := map[string]interface{}{
		"quota": map[string]interface{}{
			"enabled":   total > 0,
			"total":     total,
			"used":      len(workers),
			"remaining": remaining,
		},
		"actions": map[string]bool{
			"create":   h.provisionScript != "",
			"renew":    h.renewScript != "",
			"update":   h.updateScript != "",
			"rollback": h.rollbackScript != "",
			"reset":    h.resetScript != "",
			"delete":   h.destroyScript != "",
		},
	}
	// Image listing follows the same stale-while-revalidate contract as worker
	// status. The HTTP request never waits for the provider CLI.
	if h.imagesScript != "" {
		images, imagesLoaded, imagesRefreshing, imagesUpdatedAt := h.cloudImageSnapshot()
		meta["images_refreshing"] = imagesRefreshing
		if imagesLoaded {
			meta["images"] = images
			meta["images_cached_at"] = imagesUpdatedAt.UTC().Format(time.RFC3339Nano)
		}
	}
	if h.releasesScript != "" {
		releases, releasesLoaded, releasesRefreshing, releasesUpdatedAt := h.cloudReleaseSnapshot()
		meta["releases_refreshing"] = releasesRefreshing
		if releasesLoaded {
			meta["releases"] = releases
			meta["releases_cached_at"] = releasesUpdatedAt.UTC().Format(time.RFC3339Nano)
		}
	}
	writeJSON(w, http.StatusOK, meta)
}

// RenewForOwner extends or recovers all provider instances attached to an
// owner's paid cloud-worker lifecycles. It is called only after a commercial
// payment has committed; the provider script is idempotent for active workers
// and uses Tianyi's recovery API for unsubscribed retention-state instances.
func (h *CloudWorkerHandler) RenewForOwner(uid int64) {
	if h == nil || uid <= 0 || h.renewScript == "" || h.credits == nil {
		return
	}
	lifecycleStore, ok := h.credits.(interface {
		ListCloudWorkerLifecycles(int64) ([]CloudWorkerLifecycle, error)
	})
	if !ok {
		log.Printf("[cloud-worker] renewal store unavailable uid=%d", uid)
		return
	}
	lifecycles, err := lifecycleStore.ListCloudWorkerLifecycles(uid)
	if err != nil {
		log.Printf("[cloud-worker] list renewal lifecycles uid=%d failed: %v", uid, err)
		return
	}
	for _, lifecycle := range lifecycles {
		if lifecycle.State == "delete_running" || lifecycle.State == "deleted" || strings.TrimSpace(lifecycle.TenantName) == "" {
			continue
		}
		out, err := h.runScript(h.renewScript, "--name", lifecycle.TenantName)
		if err != nil {
			log.Printf("[cloud-worker] renewal/recovery tenant=%s uid=%d failed: %v", lifecycle.TenantName, uid, err)
			continue
		}
		if expiresAt, parseErr := parseCloudWorkerRenewalExpiry(out); parseErr != nil {
			// The provider operation already succeeded. Do not turn a missing
			// informational field into a second charge or a false failure, but
			// leave an auditable warning so operators can reconcile the date.
			log.Printf("[cloud-worker] renewal/recovery tenant=%s uid=%d succeeded but returned no valid expires_at: %v; lifecycle date unchanged", lifecycle.TenantName, uid, parseErr)
		} else {
			var extendErr error
			if extender, ok := h.credits.(CloudWorkerLifecycleExtender); ok {
				extendErr = extender.ExtendCloudWorkerLifecycle(lifecycle.ID, expiresAt, cloudWorkerExpiryGraceDays)
			} else {
				extendErr = h.credits.ExtendCloudWorkerLifecycles(uid, expiresAt, cloudWorkerExpiryGraceDays)
			}
			if extendErr != nil {
				log.Printf("[cloud-worker] renewal/recovery tenant=%s uid=%d provider expiry=%s but lifecycle sync failed: %v", lifecycle.TenantName, uid, expiresAt.UTC().Format(time.RFC3339), extendErr)
			}
		}
		log.Printf("[cloud-worker] renewal/recovery tenant=%s uid=%d completed", lifecycle.TenantName, uid)
	}
	h.requestCloudStatusRefresh(true)
}

// parseCloudWorkerRenewalExpiry extracts the provider's final monthly expiry
// from the JSON emitted by renew-worker.sh. Scripts normally emit one JSON
// line, but scanning from the end tolerates harmless diagnostics before it.
func parseCloudWorkerRenewalExpiry(output string) (time.Time, error) {
	var lastErr error
	lines := strings.Split(output, "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(lines[i])
		if line == "" {
			continue
		}
		var result struct {
			ExpiresAt string `json:"expires_at"`
		}
		if err := json.Unmarshal([]byte(line), &result); err != nil {
			lastErr = err
			continue
		}
		if strings.TrimSpace(result.ExpiresAt) == "" {
			return time.Time{}, fmt.Errorf("provider response omitted expires_at")
		}
		expiresAt, err := time.Parse(time.RFC3339, strings.TrimSpace(result.ExpiresAt))
		if err != nil {
			return time.Time{}, fmt.Errorf("provider expires_at is not RFC3339: %w", err)
		}
		if !expiresAt.After(time.Now().UTC()) {
			return time.Time{}, fmt.Errorf("provider expires_at %s is not in the future", expiresAt.UTC().Format(time.RFC3339))
		}
		return expiresAt.UTC(), nil
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("provider response was empty")
	}
	return time.Time{}, fmt.Errorf("provider response did not contain renewal JSON: %w", lastErr)
}

// HandleCreate handles POST /api/cloud-workers — create a cloud worker within
// the caller's quota, provision the cloud instance, then persist the bot.
func (h *CloudWorkerHandler) HandleCreate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	uid := UIDFromContext(r.Context())
	if uid == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	// All paid-instance operations are serialized so the quota check and the
	// bot creation stay atomic and no single user can pile up concurrent
	// script processes.
	if !h.tryBeginOperation(w) {
		return
	}
	defer h.opMu.Unlock()

	// Parse and validate the request before reserving a paid credit. Invalid
	// requests must never make a user's entitlement appear consumed/reserved.
	if r.Body == nil {
		r.Body = http.NoBody
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8*1024)
	var req BotRegisterRequest
	decoder := json.NewDecoder(r.Body)
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	var extra interface{}
	if err := decoder.Decode(&extra); err != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	// Constrain the username so the derived tenant name stays safe in URL
	// paths and script argv (no '/', '..', whitespace, or shell metachars).
	if !workerUsernameRe.MatchString(req.Username) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid username: only [a-z0-9_-] allowed, 2-64 chars",
			"code":  "cloud_worker_invalid_username",
		})
		return
	}

	staticTotal := h.quota[uid]
	_, creditAvailable := h.creditInfo(uid)
	if staticTotal <= 0 && creditAvailable <= 0 {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "cloud worker creation is not enabled for this account",
			"code":  "cloud_worker_not_enabled",
		})
		return
	}

	workers, err := h.cloudWorkersOfOwner(uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list cloud workers", "code": "cloud_worker_create_failed"})
		return
	}
	staticRemaining := staticTotal - len(workers)
	if staticRemaining < 0 {
		staticRemaining = 0
	}
	reservation := fmt.Sprintf("create-%d-%d", uid, time.Now().UnixNano())
	reservedCredit := false
	if staticRemaining == 0 && h.credits != nil {
		var reserveErr error
		reservedCredit, reserveErr = h.credits.ReserveCloudWorkerCredit(uid, reservation)
		if reserveErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to reserve cloud worker credit", "code": "cloud_worker_create_failed"})
			return
		}
	}
	if staticRemaining == 0 && !reservedCredit {
		writeJSON(w, http.StatusForbidden, map[string]string{
			"error": "cloud worker creation quota exhausted",
			"code":  "cloud_worker_quota_exhausted",
		})
		return
	}

	result, status, err := h.bots.createBotAccount(uid, req)
	if err != nil {
		if reservedCredit {
			_ = h.credits.ReleaseCloudWorkerCredit(uid, reservation)
		}
		writeJSON(w, status, map[string]string{"error": err.Error(), "code": "cloud_worker_create_failed"})
		return
	}

	tenantName := cloudWorkerTenantName(result.Username)

	// 在创建任何云资源之前持久化 tenant 标识。这样无论 provision 后续怎么失败，
	// bot 记录都有 tenant handle —— 云托管列表可见、可重试删除、且计入创建配额。
	// 若这里写入失败，云资源尚未创建，直接回滚删 bot 是安全的（不会产生孤儿实例）。
	if err := h.db.SetTenantName(result.UID, tenantName); err != nil {
		if reservedCredit {
			_ = h.credits.ReleaseCloudWorkerCredit(uid, reservation)
		}
		log.Printf("[cloud-worker] failed to persist tenant_name for uid %d before provision: %v", result.UID, err)
		if rollbackErr := h.db.DeleteBot(result.UID); rollbackErr != nil {
			log.Printf("[cloud-worker] rollback delete for uid %d failed: %v", result.UID, rollbackErr)
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to finalize cloud worker", "code": "cloud_worker_create_failed"})
		return
	}

	// Provision the cloud instance (Tianyi worker image). Without a configured
	// script this control plane cannot provision, so roll back the bot account
	// (no cloud resource was created yet, so deleting the record is safe).
	if h.provisionScript == "" {
		if reservedCredit {
			_ = h.credits.ReleaseCloudWorkerCredit(uid, reservation)
		}
		log.Printf("[cloud-worker] provision script not configured; rolling back bot %d", result.UID)
		if rollbackErr := h.db.DeleteBot(result.UID); rollbackErr != nil {
			log.Printf("[cloud-worker] rollback delete for uid %d failed: %v", result.UID, rollbackErr)
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "cloud worker provisioning is not configured",
			"code":  "cloud_worker_provisioning_unconfigured",
		})
		return
	}
	// Worker 是长期运行的无头进程，不能继承创建请求的短期会话 JWT。
	// 根据已经通过鉴权的 owner UID 重新签发 persistent owner token；这样
	// Web 登录的 7 天会话过期不会让已创建的 worker 失去 BotDefinition 能力。
	// B4-1 供给契约：provision-worker.sh 需要 --credential-file，并写入
	// localConfig 的 bot/user 身份。
	creator, creatorErr := h.db.GetUser(uid)
	if creatorErr != nil || creator == nil {
		if reservedCredit {
			_ = h.credits.ReleaseCloudWorkerCredit(uid, reservation)
		}
		_ = h.db.DeleteBot(result.UID)
		log.Printf("[cloud-worker] failed to load owner identity for %s: %v", tenantName, creatorErr)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to prepare cloud worker identity", "code": "cloud_worker_create_failed"})
		return
	}
	workerToken, tokenErr := GeneratePersistentUserToken(uid, creator.Username, creator.Email)
	if tokenErr != nil {
		if reservedCredit {
			_ = h.credits.ReleaseCloudWorkerCredit(uid, reservation)
		}
		_ = h.db.DeleteBot(result.UID)
		log.Printf("[cloud-worker] failed to sign owner token for %s: %v", tenantName, tokenErr)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to prepare cloud worker identity", "code": "cloud_worker_create_failed"})
		return
	}
	credentialFile, cleanupCredentialFile, credentialErr := writeWorkerCredentialFile(workerToken, result.APIKey)
	if credentialErr != nil {
		if reservedCredit {
			_ = h.credits.ReleaseCloudWorkerCredit(uid, reservation)
		}
		_ = h.db.DeleteBot(result.UID)
		log.Printf("[cloud-worker] failed to create credential file for %s: %v", tenantName, credentialErr)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to prepare cloud worker credentials", "code": "cloud_worker_create_failed"})
		return
	}
	defer cleanupCredentialFile()
	provOut, err := h.runScript(h.provisionScript,
		"--name", tenantName,
		"--credential-file", credentialFile,
		"--bot-uid", strconv.FormatInt(result.UID, 10),
		"--user-uid", strconv.FormatInt(uid, 10),
		"--user-name", creator.Username,
		"--user-display", creator.DisplayName)
	if err != nil {
		// 脚本的具体失败原因（如云资源配额不足）只写入服务器日志，不回显给前端。
		log.Printf("[cloud-worker] provision %s failed: %v; output=%s", tenantName, err, truncateWorkerOutput(provOut))
		// The provision script may have created the cloud instance before
		// failing on a later step. Try to destroy any partially created
		// instance so we do not leave a still-billed orphan behind (the
		// destroy script must be idempotent and tolerate a missing instance).
		destroyOK := true
		if h.destroyScript == "" {
			destroyOK = false
			log.Printf("[cloud-worker] no destroy script configured; cannot clean up partially provisioned %s", tenantName)
		} else if _, destroyErr := h.runScript(h.destroyScript, "--name", tenantName); destroyErr != nil {
			destroyOK = false
			log.Printf("[cloud-worker] destroy %s after provision failure also failed: %v", tenantName, destroyErr)
		}
		if destroyOK {
			if reservedCredit {
				_ = h.credits.ReleaseCloudWorkerCredit(uid, reservation)
			}
			if rollbackErr := h.db.DeleteBot(result.UID); rollbackErr != nil {
				log.Printf("[cloud-worker] rollback delete for uid %d failed: %v", result.UID, rollbackErr)
			}
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error": "failed to provision cloud worker",
				"code":  "cloud_worker_provision_failed",
			})
			return
		}
		// Destroy could not be confirmed: the instance may still exist and
		// keep billing. The bot record already carries tenant_name (persisted
		// before provision), so the roster still shows this worker and the
		// owner can retry delete (which attempts the destroy again). Consume the
		// reservation and register an immediately-due lifecycle when possible so
		// the hourly sweeper also retries cleanup; do not return the paid credit
		// while an instance might still be billed.
		if reservedCredit {
			if commitErr := h.credits.CommitCloudWorkerCredit(uid, reservation, result.UID, tenantName, 0); commitErr != nil {
				log.Printf("[cloud-worker] failed to bind reserved credit to pending cleanup uid=%d worker=%d: %v", uid, result.UID, commitErr)
			} else if lifecycleStore, ok := h.credits.(interface {
				ListCloudWorkerLifecycles(int64) ([]CloudWorkerLifecycle, error)
				MarkCloudWorkerLifecyclePending(int64, time.Time) error
			}); ok {
				if lifecycles, listErr := lifecycleStore.ListCloudWorkerLifecycles(uid); listErr != nil {
					log.Printf("[cloud-worker] failed to list pending cleanup lifecycle uid=%d worker=%d: %v", uid, result.UID, listErr)
				} else {
					for _, lifecycle := range lifecycles {
						if lifecycle.WorkerUID == result.UID && lifecycle.TenantName == tenantName && lifecycle.State == "active" {
							if markErr := lifecycleStore.MarkCloudWorkerLifecyclePending(lifecycle.ID, time.Now().UTC()); markErr != nil {
								log.Printf("[cloud-worker] failed to mark pending cleanup tenant=%s: %v", tenantName, markErr)
							}
							break
						}
					}
				}
			}
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "failed to provision cloud worker; the instance may still exist, retry delete to clean up",
			"code":  "cloud_worker_provision_failed_pending_cleanup",
		})
		return
	}
	if reservedCredit {
		if err := h.credits.CommitCloudWorkerCredit(uid, reservation, result.UID, tenantName, cloudWorkerExpiryGraceDays); err != nil {
			log.Printf("[cloud-worker] commit credit for uid %d failed: %v", uid, err)
			// A refund can revoke a reserved credit while the provider operation is
			// still running. In that specific case the instance must not survive a
			// successful refund; destroy it and remove the bot record. Other commit
			// errors are ambiguous (the transaction may have committed), so retain
			// the bot and register an immediately-due lifecycle for reconciliation.
			if strings.Contains(err.Error(), "reservation not found") && h.destroyScript != "" {
				if _, destroyErr := h.runScript(h.destroyScript, "--name", tenantName); destroyErr == nil {
					if deleteErr := h.db.DeleteBot(result.UID); deleteErr != nil {
						log.Printf("[cloud-worker] delete bot %d after revoked-credit cleanup failed: %v", result.UID, deleteErr)
						writeJSON(w, http.StatusBadGateway, map[string]string{
							"error": "cloud worker was cleaned up but its account record needs reconciliation",
							"code":  "cloud_worker_reconciliation_required",
						})
						return
					}
					writeJSON(w, http.StatusBadGateway, map[string]string{
						"error": "cloud worker entitlement changed while provisioning; worker was cleaned up",
						"code":  "cloud_worker_entitlement_changed",
					})
					return
				}
			}
			if registrar, ok := h.credits.(CloudWorkerLifecycleRegistrar); ok {
				if registerErr := registrar.RegisterCloudWorkerLifecycle(result.UID, uid, tenantName, time.Now().UTC(), 0); registerErr != nil {
					log.Printf("[cloud-worker] failed to register commit-recovery lifecycle uid=%d worker=%d: %v", uid, result.UID, registerErr)
				}
			}
			writeJSON(w, http.StatusBadGateway, map[string]string{
				"error": "cloud worker entitlement could not be finalized; cleanup is pending",
				"code":  "cloud_worker_provision_failed_pending_cleanup",
			})
			return
		}
	}

	friendAutoAdded := false
	if _, err := h.db.CreateFriendRequest(uid, result.UID, ""); err != nil {
		log.Printf("[cloud-worker] failed to create auto-friend request for uid %d: %v", result.UID, err)
	} else if err := h.db.AcceptFriendRequest(uid, result.UID); err != nil {
		log.Printf("[cloud-worker] failed to auto-accept friend request for uid %d: %v", result.UID, err)
	} else {
		friendAutoAdded = true
	}

	// The provision script ran synchronously to completion, so the worker is
	// provisioned/running rather than still "provisioning".
	h.requestCloudStatusRefresh(true)
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"uid":               result.UID,
		"username":          result.Username,
		"tenant_name":       tenantName,
		"deployment_status": "running",
		"friend_auto_added": friendAutoAdded,
	})
}

// SweepExpiredWorkers archives expired monthly workers and, after the grace
// period, destroys their cloud instance before deleting the bot record.
// It is deliberately best-effort and idempotent; a failed destroy remains
// visible as delete_failed for operator retry and never silently disappears.
func (h *CloudWorkerHandler) SweepExpiredWorkers(now time.Time) {
	started := time.Now()
	store, ok := h.credits.(interface {
		ListCloudWorkerLifecycleDue(time.Time, int) ([]CloudWorkerLifecycle, error)
		MarkCloudWorkerLifecyclePending(int64, time.Time) error
		ClaimCloudWorkerLifecycleDeletion(int64) (bool, error)
		MarkCloudWorkerLifecycleDeleted(int64, string) error
	})
	if !ok || h.destroyScript == "" {
		return
	}
	items, err := store.ListCloudWorkerLifecycleDue(now, 100)
	if err != nil {
		log.Printf("[cloud-worker] lifecycle sweep list failed: %v", err)
		return
	}
	if len(items) == 0 {
		return
	}
	markedPending, claimedCount, deleted, failed := 0, 0, 0, 0
	for _, item := range items {
		if item.State == "active" {
			if err := store.MarkCloudWorkerLifecyclePending(item.ID, item.DeleteAfter); err != nil {
				log.Printf("[cloud-worker] lifecycle %s archive failed: %v", item.TenantName, err)
				failed++
			} else {
				markedPending++
			}
			continue
		}
		claimOK, err := store.ClaimCloudWorkerLifecycleDeletion(item.ID)
		if err != nil || !claimOK {
			if err != nil {
				failed++
			}
			continue
		}
		claimedCount++
		if _, err := h.runScript(h.destroyScript, "--name", item.TenantName); err != nil {
			log.Printf("[cloud-worker] lifecycle destroy %s failed: %v", item.TenantName, err)
			_ = store.MarkCloudWorkerLifecycleDeleted(item.ID, truncateWorkerOutput(err.Error()))
			failed++
			continue
		}
		if err := h.db.DeleteBot(item.WorkerUID); err != nil {
			log.Printf("[cloud-worker] lifecycle delete bot %d failed: %v", item.WorkerUID, err)
			_ = store.MarkCloudWorkerLifecycleDeleted(item.ID, err.Error())
			failed++
			continue
		}
		if err := store.MarkCloudWorkerLifecycleDeleted(item.ID, ""); err != nil {
			log.Printf("[cloud-worker] lifecycle mark deleted %s failed: %v", item.TenantName, err)
			failed++
			continue
		}
		deleted++
	}
	log.Printf("[cloud-worker] lifecycle sweep scanned=%d pending=%d claimed=%d deleted=%d failed=%d duration=%s", len(items), markedPending, claimedCount, deleted, failed, time.Since(started).Round(time.Millisecond))
}

// HandleRollback handles POST /api/cloud-workers/{name}/rollback — swap Part A
// artifacts to the chosen version while KEEPING worker data.
func (h *CloudWorkerHandler) HandleRollback(w http.ResponseWriter, r *http.Request) {
	h.handleWorkerAction(w, r, h.rollbackScript, "rollback", true, false, false)
}

// HandleUpdate handles POST /api/cloud-workers/{name}/update — install the
// selected Part A release while KEEPING worker data.
func (h *CloudWorkerHandler) HandleUpdate(w http.ResponseWriter, r *http.Request) {
	h.handleWorkerAction(w, r, h.updateScript, "update", true, true, false)
}

// HandleReset handles POST /api/cloud-workers/{name}/reset — DESTROY the worker
// instance and recreate from the selected image, DROPPING all worker data.
// An optional "version" selector is forwarded to reset-worker.sh which maps it
// to the matching image id (falling back to the latest image when omitted).
func (h *CloudWorkerHandler) HandleReset(w http.ResponseWriter, r *http.Request) {
	h.handleWorkerAction(w, r, h.resetScript, "reset", true, false, true)
}

// handleWorkerAction guards a per-worker destructive action with ownership
// checks and delegates to the configured script. acceptVersion controls
// whether a "version" selector is forwarded to the script; requireVersion
// makes it mandatory for actions such as update, where omission previously
// fell back to the unrelated latest-image catalog.
// refreshIdentity is used only by reset so a recreated instance receives the
// current bot credentials instead of trusting a legacy on-disk snapshot.
func (h *CloudWorkerHandler) handleWorkerAction(w http.ResponseWriter, r *http.Request, script, action string, acceptVersion, requireVersion, refreshIdentity bool) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	uid := UIDFromContext(r.Context())
	if uid == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	name := r.PathValue("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing worker name"})
		return
	}

	// Ownership check: the worker must be one of the caller's cloud workers.
	workers, err := h.cloudWorkersOfOwner(uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list cloud workers"})
		return
	}
	var worker *cloudWorkerSummary
	for i := range workers {
		if workers[i].TenantName == name {
			worker = &workers[i]
			break
		}
	}
	if worker == nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "cloud worker not found"})
		return
	}

	if script == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "cloud worker " + action + " is not configured",
			"code":  "cloud_worker_" + action + "_unconfigured",
		})
		return
	}

	// Version selectors are sourced from different catalogs in the UI:
	// application releases for update/rollback, base images for reset.
	var body struct {
		Version string `json:"version,omitempty"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body) // malformed body is ignored
	}
	body.Version = strings.TrimSpace(body.Version)
	if requireVersion && body.Version == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "cloud worker " + action + " requires an explicit application version",
			"code":  "cloud_worker_" + action + "_version_required",
		})
		return
	}
	// B4-1 脚本契约：--name <tenant> [--version <v>]（脚本按名字区分动作，无 -Action）
	args := []string{"--name", name}
	if body.Version != "" {
		if !acceptVersion {
			// reset-worker.sh only takes --image-id; passing --version would
			// fail at argument parsing. Reject explicitly instead.
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cloud worker " + action + " does not accept a version selector (it always uses the latest image)"})
			return
		}
		args = append(args, "--version", body.Version)
	}
	if refreshIdentity {
		// Keep the endpoint's authenticated-request contract, but never reuse
		// this session token as the worker credential below.
		if extractToken(r) == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing login token for cloud worker reset"})
			return
		}
		apiKey, keyErr := h.db.GetBotAPIKey(worker.UID)
		if keyErr != nil || strings.TrimSpace(apiKey) == "" {
			log.Printf("[cloud-worker] reset %s cannot load bot credential: %v", name, keyErr)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load cloud worker identity"})
			return
		}
		owner, ownerErr := h.db.GetUser(uid)
		if ownerErr != nil || owner == nil {
			log.Printf("[cloud-worker] reset %s cannot load owner identity: %v", name, ownerErr)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load cloud worker owner"})
			return
		}
		workerToken, tokenErr := GeneratePersistentUserToken(uid, owner.Username, owner.Email)
		if tokenErr != nil {
			log.Printf("[cloud-worker] reset %s cannot sign owner token: %v", name, tokenErr)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to prepare cloud worker identity"})
			return
		}
		credentialFile, cleanupCredentialFile, credentialErr := writeWorkerCredentialFile(workerToken, apiKey)
		if credentialErr != nil {
			log.Printf("[cloud-worker] reset %s cannot create credential file: %v", name, credentialErr)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to prepare cloud worker identity"})
			return
		}
		defer cleanupCredentialFile()
		args = append(args,
			"--credential-file", credentialFile,
			"--bot-uid", strconv.FormatInt(worker.UID, 10),
			"--user-uid", strconv.FormatInt(uid, 10),
			"--user-name", owner.Username,
			"--user-display", owner.DisplayName,
		)
		if bodyID, bodyErr := h.db.GetBotBodyID(worker.UID); bodyErr == nil && strings.TrimSpace(bodyID) != "" {
			args = append(args, "--body-id", bodyID)
		}
	}

	if !h.tryBeginOperation(w) {
		return
	}
	defer h.opMu.Unlock()

	out, err := h.runScript(script, args...)
	if err != nil {
		log.Printf("[cloud-worker] %s %s failed: %v; output=%s", action, name, err, truncateWorkerOutput(out))
		// Script output stays in the server logs; never echo it back (the
		// provision script receives an API key via argv).
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "cloud worker " + action + " failed"})
		return
	}
	// The script ran synchronously to completion.
	h.requestCloudStatusRefresh(true)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "action": action})
}

// HandleDelete handles DELETE /api/cloud-workers/{name} — destroy the cloud
// instance (when a destroy script is configured) and then remove the bot
// record. Fail-closed: without a destroy script the record is NOT deleted
// (503) because the instance may still be running and billing; only an
// explicit operator override (?force=1) may skip the destroy step.
func (h *CloudWorkerHandler) HandleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	uid := UIDFromContext(r.Context())
	if uid == 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	name := r.PathValue("name")
	if name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing worker name"})
		return
	}

	workers, err := h.cloudWorkersOfOwner(uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list cloud workers"})
		return
	}
	var botUID int64
	owned := false
	for _, w := range workers {
		if w.TenantName == name {
			owned = true
			botUID = w.UID
			break
		}
	}
	if !owned {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "cloud worker not found"})
		return
	}

	if !h.tryBeginOperation(w) {
		return
	}
	defer h.opMu.Unlock()

	// Fail closed: without a destroy script we cannot guarantee the cloud
	// instance is gone, so deleting the DB record would silently orphan a
	// still-billed instance. There is NO public force override on this route
	// (an unauthenticated ?force=1 would let any owner bypass the guard);
	// operators must configure CATSCO_WORKER_DESTROY_SCRIPT so every delete
	// destroys the instance first.
	if h.destroyScript == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "cloud worker destroy is not configured; refusing to delete the record while the instance may still run",
			"code":  "cloud_worker_delete_unconfigured",
		})
		return
	}
	if _, err := h.runScript(h.destroyScript, "--name", name); err != nil {
		log.Printf("[cloud-worker] destroy %s failed: %v", name, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to destroy cloud worker instance"})
		return
	}
	// Paid workers have a durable lifecycle row in addition to the bot record.
	// Finalize it before deleting the bot: the lifecycle row has a cascading FK
	// to users, so looking it up after DeleteBot would silently lose the chance
	// to record the terminal state. If finalization fails, keep the bot record so
	// an operator/user can retry the idempotent provider delete.
	if lifecycleStore, ok := h.credits.(interface {
		ListCloudWorkerLifecycles(int64) ([]CloudWorkerLifecycle, error)
		MarkCloudWorkerLifecycleDeleted(int64, string) error
	}); ok && botUID != 0 {
		lifecycles, listErr := lifecycleStore.ListCloudWorkerLifecycles(uid)
		if listErr != nil {
			log.Printf("[cloud-worker] list lifecycle after delete %s failed: %v", name, listErr)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to finalize cloud worker deletion"})
			return
		}
		for _, lifecycle := range lifecycles {
			if lifecycle.WorkerUID != botUID || lifecycle.State == "deleted" {
				continue
			}
			if markErr := lifecycleStore.MarkCloudWorkerLifecycleDeleted(lifecycle.ID, ""); markErr != nil {
				log.Printf("[cloud-worker] mark lifecycle deleted %s failed: %v", name, markErr)
				writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to finalize cloud worker deletion"})
				return
			}
		}
	}
	if botUID != 0 {
		if err := h.db.DeleteBot(botUID); err != nil {
			log.Printf("[cloud-worker] delete bot %d failed: %v", botUID, err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to delete cloud worker"})
			return
		}
	}

	h.requestCloudStatusRefresh(true)
	writeJSON(w, http.StatusOK, map[string]interface{}{"status": "deleted"})
}

// HandleSub routes /api/cloud-workers/ subtree by path segment.
func (h *CloudWorkerHandler) HandleSub(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/cloud-workers/")
	switch {
	case rest == "meta":
		h.HandleMeta(w, r)
	case strings.HasSuffix(rest, "/rollback"):
		r.SetPathValue("name", strings.TrimSuffix(rest, "/rollback"))
		h.HandleRollback(w, r)
	case strings.HasSuffix(rest, "/update"):
		r.SetPathValue("name", strings.TrimSuffix(rest, "/update"))
		h.HandleUpdate(w, r)
	case strings.HasSuffix(rest, "/reset"):
		r.SetPathValue("name", strings.TrimSuffix(rest, "/reset"))
		h.HandleReset(w, r)
	case rest != "" && !strings.Contains(rest, "/"):
		// DELETE /api/cloud-workers/{name} (method enforced in HandleDelete)
		r.SetPathValue("name", rest)
		h.HandleDelete(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

// runScript executes the worker operation script with the default timeout. The
// script must be an executable file with a proper shebang (e.g.
// #!/usr/bin/env bash) because the production server runs on a minimal Linux
// image without PowerShell. Execution is decoupled from the request context so
// a client disconnect or proxy timeout cannot kill an in-flight provision or
// reset (which would orphan cloud instances). Arguments are passed through the
// exec argv — no shell interpolation, no injection surface.
func (h *CloudWorkerHandler) runScript(script string, args ...string) (string, error) {
	return h.runScriptTimeout(h.scriptTimeout, script, args...)
}

// runScriptTimeout runs a script with an explicit timeout against a fresh
// background context, so callers can bound short probes (image listing) and
// long operations (provision/reset) independently.
func (h *CloudWorkerHandler) runScriptTimeout(timeout time.Duration, script string, args ...string) (string, error) {
	if script == "" {
		return "", fmt.Errorf("cloud worker script not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, script, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("script failed: %w", err)
	}
	return string(out), nil
}

// truncateWorkerOutput bounds script output before it is written to the server
// log, so a verbose/rogue script cannot flood the log (and to keep any
// incidental secrets out of unbounded log lines).
const maxWorkerOutputLog = 4000

func truncateWorkerOutput(out string) string {
	out = strings.TrimSpace(out)
	if len(out) > maxWorkerOutputLog {
		out = out[:maxWorkerOutputLog] + "...(truncated)"
	}
	return out
}

// writeWorkerCredentialFile keeps worker credentials out of process argv, where
// they would otherwise be visible to /proc and process supervisors. The caller
// owns the returned cleanup function.
func writeWorkerCredentialFile(loginToken, apiKey string) (string, func(), error) {
	file, err := os.CreateTemp("", "catsco-worker-credentials-*")
	if err != nil {
		return "", func() {}, err
	}
	path := file.Name()
	cleanup := func() { _ = os.Remove(path) }
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		cleanup()
		return "", func() {}, err
	}
	if _, err := io.WriteString(file, loginToken+"\n"+apiKey+"\n"); err != nil {
		_ = file.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}

// cloudImageSummary is one image row from the CATSCO_WORKER_IMAGES_SCRIPT
// output. Contract: the script MUST be the bash list-worker-images.sh (or any
// executable emitting the same TSV) — one image per line,
// `imageID<TAB>name<TAB>version<TAB>commit<TAB>createdTime<TAB>status`.
// PowerShell .ps1 scripts are NOT runnable on the Linux server image.
type cloudImageSummary struct {
	ImageID     string `json:"image_id"`
	Name        string `json:"name"`
	Version     string `json:"version"`
	Commit      string `json:"commit"`
	CreatedTime string `json:"created_time,omitempty"`
	Status      string `json:"status,omitempty"`
}

// parseImageLines parses the line-based image listing into structured rows.
// Comment lines ("#") and column headers are skipped.
func parseImageLines(out string) []cloudImageSummary {
	images := []cloudImageSummary{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) == 0 || strings.TrimSpace(fields[0]) == "" {
			continue
		}
		first := strings.TrimSpace(fields[0])
		switch first {
		case "imageID", "name", "version", "commit":
			continue
		}
		img := cloudImageSummary{ImageID: first}
		if len(fields) > 1 {
			img.Name = strings.TrimSpace(fields[1])
		}
		if len(fields) > 2 {
			img.Version = strings.TrimSpace(fields[2])
		}
		if len(fields) > 3 {
			img.Commit = strings.TrimSpace(fields[3])
		}
		if len(fields) > 4 {
			img.CreatedTime = strings.TrimSpace(fields[4])
		}
		if len(fields) > 5 {
			img.Status = strings.TrimSpace(fields[5])
		}
		images = append(images, img)
	}
	sort.SliceStable(images, func(i, j int) bool {
		left, _ := strconv.ParseInt(images[i].CreatedTime, 10, 64)
		right, _ := strconv.ParseInt(images[j].CreatedTime, 10, 64)
		return left > right
	})
	if len(images) > 6 {
		images = images[:6]
	}
	return images
}

// cloudReleaseSummary is one published application release from the private
// TOS catalog. It is intentionally separate from cloudImageSummary: a release
// can be installed on an older base image without a same-version image.
type cloudReleaseSummary struct {
	Version     string `json:"version"`
	PublishedAt string `json:"published_at,omitempty"`
}

var cloudReleaseVersionRe = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,63}$`)

// parseReleaseLines parses `version<TAB>publishedUnixTime` rows emitted by
// list-worker-releases.sh, newest first. Duplicate and malformed versions are
// ignored so provider metadata cannot create ambiguous UI choices.
func parseReleaseLines(out string) []cloudReleaseSummary {
	type releaseWithTime struct {
		release cloudReleaseSummary
		unix    int64
	}
	parsed := []releaseWithTime{}
	seen := map[string]struct{}{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		version := strings.TrimPrefix(strings.TrimSpace(fields[0]), "v")
		if !cloudReleaseVersionRe.MatchString(version) {
			continue
		}
		if _, exists := seen[version]; exists {
			continue
		}
		seen[version] = struct{}{}
		var publishedUnix int64
		if len(fields) > 1 {
			publishedUnix, _ = strconv.ParseInt(strings.TrimSpace(fields[1]), 10, 64)
		}
		release := cloudReleaseSummary{Version: version}
		if publishedUnix > 0 {
			release.PublishedAt = time.Unix(publishedUnix, 0).UTC().Format(time.RFC3339)
		}
		parsed = append(parsed, releaseWithTime{release: release, unix: publishedUnix})
	}
	sort.SliceStable(parsed, func(i, j int) bool { return parsed[i].unix > parsed[j].unix })
	if len(parsed) > 6 {
		parsed = parsed[:6]
	}
	releases := make([]cloudReleaseSummary, len(parsed))
	for i := range parsed {
		releases[i] = parsed[i].release
	}
	return releases
}
