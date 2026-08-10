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
	rollbackScript  string
	destroyScript   string
	imagesScript    string

	scriptTimeout time.Duration

	// opMu serializes all cloud operations (create / rollback / reset /
	// delete). These are low-frequency, long-running, paid-instance actions;
	// a global lock keeps quota checks atomic and prevents a single user from
	// piling up concurrent script processes.
	opMu sync.Mutex
}

// workerUsernameRe constrains cloud worker usernames so the derived tenant
// name stays safe to embed in URL paths and script argv.
var workerUsernameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{1,63}$`)

// CloudWorkerConfig configures the cloud worker control plane.
type CloudWorkerConfig struct {
	CreateQuota     string // CATSCO_WORKER_CREATE_QUOTA "<uid>=<n>;<uid>=<n>" — unset means 0 (disabled)
	ProvisionScript string // CATSCO_WORKER_PROVISION_SCRIPT
	ResetScript     string // CATSCO_WORKER_RESET_SCRIPT
	RollbackScript  string // CATSCO_WORKER_ROLLBACK_SCRIPT
	DestroyScript   string // CATSCO_WORKER_DESTROY_SCRIPT
	ImagesScript    string // CATSCO_WORKER_IMAGES_SCRIPT
}

// CloudWorkerConfigFromEnv reads configuration from the environment.
func CloudWorkerConfigFromEnv() CloudWorkerConfig {
	return CloudWorkerConfig{
		CreateQuota:     strings.TrimSpace(os.Getenv("CATSCO_WORKER_CREATE_QUOTA")),
		ProvisionScript: strings.TrimSpace(os.Getenv("CATSCO_WORKER_PROVISION_SCRIPT")),
		ResetScript:     strings.TrimSpace(os.Getenv("CATSCO_WORKER_RESET_SCRIPT")),
		RollbackScript:  strings.TrimSpace(os.Getenv("CATSCO_WORKER_ROLLBACK_SCRIPT")),
		DestroyScript:   strings.TrimSpace(os.Getenv("CATSCO_WORKER_DESTROY_SCRIPT")),
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
		destroyScript:   cfg.DestroyScript,
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
	// Image listing is a cheap, read-only probe: use a short timeout and
	// never block the request for the full scriptTimeout.
	if h.imagesScript != "" {
		const imageListTimeout = 30 * time.Second
		// list-worker-images.sh 无参数（TSV 契约，见 parseImageLines）——不传 -Action
		if out, listErr := h.runScriptTimeout(imageListTimeout, h.imagesScript); listErr == nil {
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

	// All paid-instance operations are serialized so the quota check and the
	// bot creation stay atomic and no single user can pile up concurrent
	// script processes.
	h.opMu.Lock()
	defer h.opMu.Unlock()

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
	// Constrain the username so the derived tenant name stays safe in URL
	// paths and script argv (no '/', '..', whitespace, or shell metachars).
	if !workerUsernameRe.MatchString(req.Username) {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "invalid username: only [a-z0-9_-] allowed, 2-64 chars",
		})
		return
	}

	result, status, err := h.bots.createBotAccount(uid, req)
	if err != nil {
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	tenantName := fmt.Sprintf("bot-%s", result.Username)

	// 在创建任何云资源之前持久化 tenant 标识。这样无论 provision 后续怎么失败，
	// bot 记录都有 tenant handle —— 云托管列表可见、可重试删除、且计入创建配额。
	// 若这里写入失败，云资源尚未创建，直接回滚删 bot 是安全的（不会产生孤儿实例）。
	if err := h.db.SetTenantName(result.UID, tenantName); err != nil {
		log.Printf("[cloud-worker] failed to persist tenant_name for uid %d before provision: %v", result.UID, err)
		if rollbackErr := h.db.DeleteBot(result.UID); rollbackErr != nil {
			log.Printf("[cloud-worker] rollback delete for uid %d failed: %v", result.UID, rollbackErr)
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to finalize cloud worker"})
		return
	}

	// Provision the cloud instance (Tianyi worker image). Without a configured
	// script this control plane cannot provision, so roll back the bot account
	// (no cloud resource was created yet, so deleting the record is safe).
	if h.provisionScript == "" {
		log.Printf("[cloud-worker] provision script not configured; rolling back bot %d", result.UID)
		if rollbackErr := h.db.DeleteBot(result.UID); rollbackErr != nil {
			log.Printf("[cloud-worker] rollback delete for uid %d failed: %v", result.UID, rollbackErr)
		}
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "cloud worker provisioning is not configured"})
		return
	}
	// 创建者登录凭证（JWT）+ 身份信息：worker 以创建者登录态 + 新 bot 的连接凭证
	// 启动（B4-1 供给契约：provision-worker.sh 需要 --login-token 必填 + 写
	// localConfig 的 bot/user 身份）。JWT 从请求 Authorization 头取（context 只有 uid）。
	creatorJWT := extractToken(r)
	creatorName, creatorDisplay := "", ""
	if creator, err := h.db.GetUser(uid); err == nil && creator != nil {
		creatorName = creator.Username
		creatorDisplay = creator.DisplayName
	}
	if _, err := h.runScript(h.provisionScript,
		"--name", tenantName,
		"--login-token", creatorJWT,
		"--api-key", result.APIKey,
		"--bot-uid", strconv.FormatInt(result.UID, 10),
		"--user-uid", strconv.FormatInt(uid, 10),
		"--user-name", creatorName,
		"--user-display", creatorDisplay); err != nil {
		log.Printf("[cloud-worker] provision %s failed: %v", tenantName, err)
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
			if rollbackErr := h.db.DeleteBot(result.UID); rollbackErr != nil {
				log.Printf("[cloud-worker] rollback delete for uid %d failed: %v", result.UID, rollbackErr)
			}
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to provision cloud worker"})
			return
		}
		// Destroy could not be confirmed: the instance may still exist and
		// keep billing. The bot record already carries tenant_name (persisted
		// before provision), so the roster still shows this worker and the
		// owner can retry delete (which attempts the destroy again) — the
		// record is never lost while an instance might still be billed.
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"error": "failed to provision cloud worker; the instance may still exist, retry delete to clean up",
		})
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

	// The provision script ran synchronously to completion, so the worker is
	// provisioned/running rather than still "provisioning".
	writeJSON(w, http.StatusCreated, map[string]interface{}{
		"uid":               result.UID,
		"username":          result.Username,
		"tenant_name":       tenantName,
		"deployment_status": "running",
		"friend_auto_added": friendAutoAdded,
	})
}

// HandleRollback handles POST /api/cloud-workers/{name}/rollback — swap Part A
// artifacts to the chosen image version while KEEPING worker data.
func (h *CloudWorkerHandler) HandleRollback(w http.ResponseWriter, r *http.Request) {
	h.handleWorkerAction(w, r, h.rollbackScript, "rollback", true)
}

// HandleReset handles POST /api/cloud-workers/{name}/reset — DESTROY the worker
// instance and recreate from the selected image, DROPPING all worker data.
func (h *CloudWorkerHandler) HandleReset(w http.ResponseWriter, r *http.Request) {
	// reset always rebuilds from the latest image; it does not accept a
	// version selector (paired reset-worker.sh only takes --image-id).
	h.handleWorkerAction(w, r, h.resetScript, "reset", false)
}

// handleWorkerAction guards a per-worker destructive action with ownership
// checks and delegates to the configured script. acceptVersion controls
// whether an optional "version" selector is forwarded to the script
// (rollback yes, reset no).
func (h *CloudWorkerHandler) handleWorkerAction(w http.ResponseWriter, r *http.Request, script, action string, acceptVersion bool) {
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

	// Optional version selector forwarded to the script (rollback/reset can
	// target a specific image version when the script supports it).
	var body struct {
		Version string `json:"version,omitempty"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&body) // malformed body is ignored
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

	h.opMu.Lock()
	defer h.opMu.Unlock()

	if _, err := h.runScript(script, args...); err != nil {
		log.Printf("[cloud-worker] %s %s failed: %v", action, name, err)
		// Script output stays in the server logs; never echo it back (the
		// provision script receives an API key via argv).
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "cloud worker " + action + " failed"})
		return
	}
	// The script ran synchronously to completion.
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

	h.opMu.Lock()
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
		})
		return
	}
	if _, err := h.runScript(h.destroyScript, "--name", name); err != nil {
		log.Printf("[cloud-worker] destroy %s failed: %v", name, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "failed to destroy cloud worker instance"})
		return
	}
	if botUID != 0 {
		if err := h.db.DeleteBot(botUID); err != nil {
			log.Printf("[cloud-worker] delete bot %d failed: %v", botUID, err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to delete cloud worker"})
			return
		}
	}

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
	return images
}
