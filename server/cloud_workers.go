// Package server - cloud virtual employee control plane.
//
// Cloud workers are virtual employees that run on Tianyi cloud worker images
// (built and managed by the XiaoBa-CLI ops pipeline). This handler exposes the
// web control plane used by the "云托管" entry in the AI-assistant store modal:
//
//   - create quota (CATSCO_WORKER_CREATE_QUOTA, unset = 0 = disabled)
//   - cloud worker roster (name / status / version / image)
//   - rollback (keep data, swap Part A artifacts) vs reset (drop data, destroy
//     and recreate from image) — strictly separate, documented actions
//
// Heavy cloud operations (provision / rollback / reset / image list) are
// delegated to PowerShell scripts configured through environment variables so
// credentials stay server-side. When a script is not configured the matching
// endpoint returns 503, which keeps the control plane safe to ship without the
// worker pipeline wired up.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
)

// CloudWorkerHandler exposes the cloud-managed virtual employee control plane.
type CloudWorkerHandler struct {
	db   store.Store
	bots *BotHandler

	// create quota per owner uid, from CATSCO_WORKER_CREATE_QUOTA.
	quota map[int64]int

	// PowerShell scripts invoked for heavy cloud operations (empty = disabled).
	provisionScript string
	resetScript     string
	rollbackScript  string
	imagesScript    string

	scriptTimeout time.Duration
}

// CloudWorkerConfig configures the cloud worker control plane.
type CloudWorkerConfig struct {
	CreateQuota     string // CATSCO_WORKER_CREATE_QUOTA "<uid>=<n>;<uid>=<n>" — unset means 0 (disabled)
	ProvisionScript string // CATSCO_WORKER_PROVISION_SCRIPT
	ResetScript     string // CATSCO_WORKER_RESET_SCRIPT
	RollbackScript  string // CATSCO_WORKER_ROLLBACK_SCRIPT
	ImagesScript    string // CATSCO_WORKER_IMAGES_SCRIPT
}

// CloudWorkerConfigFromEnv reads configuration from the environment.
func CloudWorkerConfigFromEnv() CloudWorkerConfig {
	return CloudWorkerConfig{
		CreateQuota:     strings.TrimSpace(os.Getenv("CATSCO_WORKER_CREATE_QUOTA")),
		ProvisionScript: strings.TrimSpace(os.Getenv("CATSCO_WORKER_PROVISION_SCRIPT")),
		ResetScript:     strings.TrimSpace(os.Getenv("CATSCO_WORKER_RESET_SCRIPT")),
		RollbackScript:  strings.TrimSpace(os.Getenv("CATSCO_WORKER_ROLLBACK_SCRIPT")),
		ImagesScript:    strings.TrimSpace(os.Getenv("CATSCO_WORKER_IMAGES_SCRIPT")),
	}
}

// NewCloudWorkerHandler creates a CloudWorkerHandler.
func NewCloudWorkerHandler(db store.Store, bots *BotHandler, cfg CloudWorkerConfig) *CloudWorkerHandler {
	return &CloudWorkerHandler{
		db:              db,
		bots:            bots,
		quota:           parseWorkerCreateQuota(cfg.CreateQuota),
		provisionScript: cfg.ProvisionScript,
		resetScript:     cfg.ResetScript,
		rollbackScript:  cfg.RollbackScript,
		imagesScript:    cfg.ImagesScript,
		scriptTimeout:   10 * time.Minute,
	}
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

	total, remaining := h.quotaInfo(uid, len(workers))
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"workers": workers,
		"quota": map[string]interface{}{
			"enabled":   total > 0,
			"total":     total,
			"used":      len(workers),
			"remaining": remaining,
		},
	})
}

// HandleMeta handles GET /api/cloud-workers/meta — quota + available images.
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
	}
	if h.imagesScript != "" {
		if out, listErr := h.runScript(r.Context(), h.imagesScript, "-Action", "List"); listErr == nil {
			meta["images"] = parseImageLines(out)
		}
	}
	writeJSON(w, http.StatusOK, meta)
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

	total := h.quota[uid]
	if total <= 0 {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cloud worker creation is not enabled for this account"})
		return
	}

	workers, err := h.cloudWorkersOfOwner(uid)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list cloud workers"})
		return
	}
	if len(workers) >= total {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cloud worker creation quota exhausted"})
		return
	}

	var req BotRegisterRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}

	result, status, err := h.bots.createBotAccount(uid, req)
	if err != nil {
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	tenantName := fmt.Sprintf("bot-%s", result.Username)

	// Provision the cloud instance (Tianyi worker image). Without a configured
	// script this control plane cannot provision, so roll back the bot account.
	if h.provisionScript == "" {
		log.Printf("[cloud-worker] provision script not configured; rolling back bot %d", result.UID)
		if rollbackErr := h.db.DeleteBot(result.UID); rollbackErr != nil {
			log.Printf("[cloud-worker] rollback delete for uid %d failed: %v", result.UID, rollbackErr)
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "cloud worker provisioning is not configured"})
		return
	}
	if out, err := h.runScript(r.Context(), h.provisionScript,
		"-Action", "Provision", "-Name", tenantName, "-ApiKey", result.APIKey); err != nil {
		log.Printf("[cloud-worker] provision %s failed: %v", tenantName, err)
		if rollbackErr := h.db.DeleteBot(result.UID); rollbackErr != nil {
			log.Printf("[cloud-worker] rollback delete for uid %d failed: %v", result.UID, rollbackErr)
		}
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "failed to provision cloud worker",
			"detail": strings.TrimSpace(out),
		})
		return
	}

	if err := h.db.SetTenantName(result.UID, tenantName); err != nil {
		log.Printf("[cloud-worker] failed to save tenant_name for uid %d: %v", result.UID, err)
		if rollbackErr := h.db.DeleteBot(result.UID); rollbackErr != nil {
			log.Printf("[cloud-worker] rollback delete for uid %d failed: %v", result.UID, rollbackErr)
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to finalize cloud worker"})
		return
	}

	friendAutoAdded := false
	if _, err := h.db.CreateFriendRequest(uid, result.UID, ""); err != nil {
		log.Printf("[cloud-worker] failed to create auto-friend request for uid %d: %v", result.UID, err)
	} else if err := h.db.AcceptFriendRequest(uid, result.UID); err != nil {
		log.Printf("[cloud-worker] failed to auto-accept friend request for uid %d: %v", result.UID, err)
	} else {
		friendAutoAdded = true
	}

	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"uid":               result.UID,
		"username":          result.Username,
		"tenant_name":       tenantName,
		"deployment_status": "provisioning",
		"friend_auto_added": friendAutoAdded,
	})
}

// HandleRollback handles POST /api/cloud-workers/{name}/rollback — swap Part A
// artifacts to the chosen image version while KEEPING worker data.
func (h *CloudWorkerHandler) HandleRollback(w http.ResponseWriter, r *http.Request) {
	h.handleWorkerAction(w, r, h.rollbackScript, "rollback")
}

// HandleReset handles POST /api/cloud-workers/{name}/reset — DESTROY the worker
// instance and recreate from the selected image, DROPPING all worker data.
func (h *CloudWorkerHandler) HandleReset(w http.ResponseWriter, r *http.Request) {
	h.handleWorkerAction(w, r, h.resetScript, "reset")
}

// handleWorkerAction guards a per-worker destructive action with ownership
// checks and delegates to the configured script.
func (h *CloudWorkerHandler) handleWorkerAction(w http.ResponseWriter, r *http.Request, script, action string) {
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
	owned := false
	for _, w := range workers {
		if w.TenantName == name {
			owned = true
			break
		}
	}
	if !owned {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "cloud worker not found"})
		return
	}

	if script == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "cloud worker " + action + " is not configured"})
		return
	}

	out, err := h.runScript(r.Context(), script, "-Action", action, "-Name", name)
	if err != nil {
		log.Printf("[cloud-worker] %s %s failed: %v", action, name, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error":  "cloud worker " + action + " failed",
			"detail": strings.TrimSpace(out),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": action + "_started"})
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
	case strings.HasSuffix(rest, "/reset"):
		r.SetPathValue("name", strings.TrimSuffix(rest, "/reset"))
		h.HandleReset(w, r)
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

// runScript executes a PowerShell script with the given arguments.
func (h *CloudWorkerHandler) runScript(ctx context.Context, script string, args ...string) (string, error) {
	if script == "" {
		return "", fmt.Errorf("cloud worker script not configured")
	}
	cmdCtx, cancel := context.WithTimeout(ctx, h.scriptTimeout)
	defer cancel()

	fullArgs := append([]string{"-NoProfile", "-ExecutionPolicy", "Bypass", "-File", script}, args...)
	cmd := exec.CommandContext(cmdCtx, "powershell", fullArgs...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("script failed: %w", err)
	}
	return string(out), nil
}

// parseImageLines extracts image identifiers from a script's line-based output.
func parseImageLines(out string) []string {
	images := []string{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "Name") {
			continue
		}
		images = append(images, line)
	}
	return images
}
