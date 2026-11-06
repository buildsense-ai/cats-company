package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

var errCloudWorkerAdminUnavailable = errors.New("cloud worker admin store unavailable")

// shellSingleQuote 把一个值安全地嵌进 POSIX shell 单引号里。
// 面板给的是让人复制到终端执行的命令，路径里带空格/引号时不能拼错。
func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// CloudWorkerAdminDataStore is the intentionally narrow database projection
// used by the internal cloud-worker roster. It must never be wired to a public
// user route or return credentials.
type CloudWorkerAdminDataStore interface {
	ListCloudWorkerAdminRecords() ([]types.CloudWorkerAdminRecord, error)
}

type CloudWorkerAdminImportStore interface {
	UpsertExternalCloudWorkerBinding(types.CloudWorkerBindingRecord) error
}

// CloudWorkerAdminOverviewHandler is the internal read-only endpoint wired
// behind CommercialOpsHandler's service authentication.
type CloudWorkerAdminOverviewHandler interface {
	HandleAdminOverview(http.ResponseWriter, *http.Request)
}

type CloudWorkerAdminImporter interface {
	HandleAdminImport(http.ResponseWriter, *http.Request)
}

type cloudWorkerAdminItem struct {
	BillingMode       string `json:"billing_mode,omitempty"`
	ConversionPending bool   `json:"conversion_pending"`
	BillingAction     string `json:"billing_action,omitempty"`
	DeploymentProfile string `json:"deployment_profile,omitempty"`
	// DeploymentDrift is advisory: the platform record says packed_shared but
	// the cluster keeps reporting the container as absent. It is never used to
	// rewrite the record — a container being recreated looks identical, and a
	// wrong rewrite would move billing onto the ECS path.
	DeploymentDrift    string     `json:"deployment_drift,omitempty"`
	SSHKeyPath         string     `json:"ssh_key_path,omitempty"`
	SSHExecutionHost   string     `json:"ssh_execution_host,omitempty"`
	UID                int64      `json:"uid"`
	OwnerUID           int64      `json:"owner_uid"`
	OwnerUsername      string     `json:"owner_username,omitempty"`
	OwnerDisplayName   string     `json:"owner_display_name,omitempty"`
	Username           string     `json:"username"`
	DisplayName        string     `json:"display_name"`
	TenantName         string     `json:"tenant_name"`
	BotState           int        `json:"bot_state"`
	BotEnabled         bool       `json:"bot_enabled"`
	Visibility         string     `json:"visibility"`
	ProviderStatus     string     `json:"provider_status"`
	AppVersion         string     `json:"app_version"`
	ImageID            string     `json:"image_id,omitempty"`
	ImageVersion       string     `json:"image_version,omitempty"`
	LifecycleState     string     `json:"lifecycle_state,omitempty"`
	PackageExpiresAt   *time.Time `json:"package_expires_at,omitempty"`
	DeleteAfter        *time.Time `json:"delete_after,omitempty"`
	LifecycleLastError string     `json:"lifecycle_last_error,omitempty"`
	CreditState        string     `json:"credit_state,omitempty"`
	CreditSourceRef    string     `json:"credit_source_ref,omitempty"`
	CreditExpiresAt    *time.Time `json:"credit_expires_at,omitempty"`
	Provider           string     `json:"provider,omitempty"`
	RegionID           string     `json:"region_id,omitempty"`
	ProjectID          string     `json:"project_id,omitempty"`
	AZName             string     `json:"az_name,omitempty"`
	InstanceID         string     `json:"instance_id,omitempty"`
	InstanceName       string     `json:"instance_name,omitempty"`
	PrivateIP          string     `json:"private_ip,omitempty"`
	PublicIP           string     `json:"public_ip,omitempty"`
	SSHUser            string     `json:"ssh_user,omitempty"`
	SSHPort            string     `json:"ssh_port,omitempty"`
	SSHJumpHost        string     `json:"ssh_jump_host,omitempty"`
	SSHJumpAlias       string     `json:"ssh_jump_alias,omitempty"`
	SSHJumpPort        string     `json:"ssh_jump_port,omitempty"`
	SSHJumpUser        string     `json:"ssh_jump_user,omitempty"`
	SSHJumpKey         string     `json:"ssh_jump_key,omitempty"`
	SSHHostJumpKey     string     `json:"ssh_host_jump_key,omitempty"`
	SSHStateRoot       string     `json:"ssh_state_root,omitempty"`
	SSHHostStateRoot   string     `json:"ssh_host_state_root,omitempty"`
	// 共享集群的接入信息。集群租户没有独占实例、没有公网 IP、也没有租户密钥，
	// 但运维仍然要能进容器排障 —— 这里直接给出拼好的命令，面板只负责展示和复制。
	PackedContainer    string     `json:"packed_container,omitempty"`
	PackedControlPlane string     `json:"packed_control_plane,omitempty"`
	PackedSSHCommand   string     `json:"packed_ssh_command,omitempty"`
	ManagementMode     string     `json:"management_mode,omitempty"`
	LifecycleMode      string     `json:"lifecycle_mode,omitempty"`
	BindingSource      string     `json:"binding_source,omitempty"`
	BindingStatus      string     `json:"binding_status,omitempty"`
	LastVerifiedAt     *time.Time `json:"last_verified_at,omitempty"`
}

type cloudWorkerAdminOverview struct {
	DeploymentProfiles       []cloudWorkerProfileOption `json:"deployment_profiles"`
	GeneratedAt              time.Time                  `json:"generated_at"`
	WorkerCount              int                        `json:"worker_count"`
	ProviderStatusAvailable  bool                       `json:"provider_status_available"`
	ProviderStatusRefreshing bool                       `json:"provider_status_refreshing"`
	ProviderStatusUpdatedAt  *time.Time                 `json:"provider_status_updated_at,omitempty"`
	StatusCounts             map[string]int             `json:"status_counts"`
	LifecycleCounts          map[string]int             `json:"lifecycle_counts"`
	CreditCounts             map[string]int             `json:"credit_counts"`
	Workers                  []cloudWorkerAdminItem     `json:"workers"`
}

// CloudWorkerAdminOverview returns a read-only, platform-wide roster. The
// provider script is read through the same short-lived cached snapshot as the
// user cloud-worker API, so a slow/unavailable Tianyi CLI never blocks the
// dashboard request.
func (h *CloudWorkerHandler) CloudWorkerAdminOverview(now time.Time) (*cloudWorkerAdminOverview, error) {
	if h == nil || h.db == nil {
		return nil, errCloudWorkerAdminUnavailable
	}
	data, ok := h.db.(CloudWorkerAdminDataStore)
	if !ok {
		return nil, errCloudWorkerAdminUnavailable
	}
	records, err := data.ListCloudWorkerAdminRecords()
	if err != nil {
		return nil, err
	}

	infos, statusLoaded, statusRefreshing, statusUpdatedAt := h.cloudStatusSnapshot()
	// 面板展示的部署形态直接来自 bot_config，而它可能在迁移/重装后与集群事实脱节
	// （bot-live-38 就被显示成天翼云 ECS 整整一段）。这里以集群回报的 backend 为准
	// 做一次对账，只在证据明确时改，失败不阻断列表。
	if statusLoaded {
		if fixed := h.reconcilePackedDeployments(records, infos); fixed > 0 {
			log.Printf("[cloud-worker] admin overview reconciled %d deployment profile(s)", fixed)
		}
	}
	// 反方向：平台记的是共享集群，但集群连续多次快照都查不到这个容器。
	// 这里只统计、只暴露信号，不写库 —— 容器重建期间和"已经迁走"长得一样，
	// 贸然改成 ECS 形态会把计费带上续费路径。
	packedAbsence := map[string]int{}
	if statusLoaded {
		packedAbsence = h.observePackedAbsence(records, infos, statusUpdatedAt)
	}
	overview := &cloudWorkerAdminOverview{
		DeploymentProfiles:       h.deploymentProfileOptions(),
		GeneratedAt:              now.UTC(),
		WorkerCount:              len(records),
		ProviderStatusAvailable:  statusLoaded,
		ProviderStatusRefreshing: statusRefreshing,
		StatusCounts:             map[string]int{},
		LifecycleCounts:          map[string]int{},
		CreditCounts:             map[string]int{},
		Workers:                  make([]cloudWorkerAdminItem, 0, len(records)),
	}
	if !statusUpdatedAt.IsZero() {
		updatedAt := statusUpdatedAt
		overview.ProviderStatusUpdatedAt = &updatedAt
	}
	for _, record := range records {
		providerStatus := "unavailable"
		appVersion := ""
		imageID := ""
		imageVersion := ""
		if record.ManagementMode == "manual_import" || record.LifecycleMode == "external" {
			// Imported machines are inventory-only. Their provider status is
			// populated by a future explicit verify action, never inferred from
			// the platform worker status script (which uses tenant names).
			providerStatus = record.BindingStatus
			if providerStatus == "" {
				providerStatus = "unverified"
			}
		} else if statusLoaded {
			providerStatus = "missing"
			if info, found := infos[record.TenantName]; found {
				providerStatus = strings.TrimSpace(info.Status)
				if providerStatus == "" {
					providerStatus = "unknown"
				}
				appVersion = info.AppVersion
				imageID = info.ImageID
				imageVersion = info.Version
				if info.PrivateIP != "" {
					record.PrivateIP = info.PrivateIP
				}
				if info.PublicIP != "" {
					record.PublicIP = info.PublicIP
				}
			}
		}
		item := cloudWorkerAdminItem{
			UID:                record.WorkerUID,
			OwnerUID:           record.OwnerUID,
			OwnerUsername:      record.OwnerUsername,
			OwnerDisplayName:   record.OwnerDisplayName,
			Username:           record.Username,
			DisplayName:        record.DisplayName,
			TenantName:         record.TenantName,
			BotState:           record.BotState,
			BotEnabled:         record.BotEnabled,
			Visibility:         record.Visibility,
			ProviderStatus:     providerStatus,
			AppVersion:         appVersion,
			ImageID:            imageID,
			ImageVersion:       imageVersion,
			LifecycleState:     record.LifecycleState,
			PackageExpiresAt:   record.PackageExpiresAt,
			DeleteAfter:        record.DeleteAfter,
			LifecycleLastError: record.LifecycleLastError,
			CreditState:        record.CreditState,
			CreditSourceRef:    record.CreditSourceRef,
			CreditExpiresAt:    record.CreditExpiresAt,
			Provider:           record.Provider,
			RegionID:           record.RegionID,
			ProjectID:          record.ProjectID,
			AZName:             record.AZName,
			InstanceID:         record.InstanceID,
			InstanceName:       record.InstanceName,
			PublicIP:           record.PublicIP,
			PrivateIP:          record.PrivateIP,
			SSHUser:            h.sshUser,
			SSHPort:            h.sshPort,
			SSHJumpHost:        h.sshJumpHost,
			SSHJumpAlias:       h.sshJumpAlias,
			SSHJumpPort:        h.sshJumpPort,
			SSHJumpUser:        h.sshJumpUser,
			SSHJumpKey:         h.sshJumpKey,
			SSHHostJumpKey:     h.sshHostJumpKey,
			SSHStateRoot:       h.sshStateRoot,
			SSHHostStateRoot:   h.sshHostStateRoot,
			ManagementMode:     record.ManagementMode,
			LifecycleMode:      record.LifecycleMode,
			BindingSource:      record.BindingSource,
			BindingStatus:      record.BindingStatus,
			LastVerifiedAt:     record.LastVerifiedAt,
		}
		if record.ManagementMode != "manual_import" && record.LifecycleMode != "external" {
			if billing, ok := h.credits.(cloudWorkerBillingStore); ok {
				if lifecycle, err := billing.GetCloudWorkerBillingLifecycle(record.TenantName); err == nil {
					item.BillingMode = lifecycle.BillingMode
					item.ConversionPending = lifecycle.ConversionPending
					item.BillingAction = lifecycle.BillingAction
				}
			}
			deployment, err := h.deploymentForTenant(record.TenantName)
			if err != nil {
				return nil, err
			}
			item.DeploymentProfile = deployment.Profile
			if deployment.Profile == types.CloudWorkerPackedShared && !isDeletedLifecycle(record.LifecycleState) {
				// 只有在"记录确实是共享集群"时才把这个信号挂上去：
				// ECS 租户查不到实例是另一回事，别混在一起。
				if streak := packedAbsence[record.TenantName]; streak >= packedAbsenceThreshold {
					item.DeploymentDrift = fmt.Sprintf(
						"packed container packed-%s missing from %d consecutive cluster snapshots",
						record.TenantName, streak)
				}
			}
			item.Provider = "ctyun"
			item.RegionID = deployment.Env["CTYUN_WORKER_REGION_ID"]
			item.ProjectID = deployment.Env["CTYUN_WORKER_PROJECT_ID"]
			item.AZName = deployment.Env["CTYUN_WORKER_AZ_NAME"]
			item.InstanceName = "worker-" + record.TenantName
			if h.sshHostStateRoot != "" && workerUsernameRe.MatchString(record.TenantName) {
				item.SSHKeyPath = strings.TrimRight(h.sshHostStateRoot, "/") + "/" + record.TenantName + "/id_rsa"
				item.SSHExecutionHost = "cats-ctyun"
			}
			if deployment.Profile == types.CloudWorkerPublicIP {
				item.SSHJumpHost, item.SSHJumpAlias, item.SSHJumpKey, item.SSHHostJumpKey = "", "", "", ""
			}
			if deployment.Profile == types.CloudWorkerPackedShared {
				// A shared container has no dedicated instance, region or
				// per-tenant SSH key. Reporting the ECS fields would make
				// operators chase a machine that does not exist.
				item.Provider = "packed"
				item.RegionID, item.ProjectID, item.AZName = "", "", ""
				item.InstanceID, item.InstanceName = "", ""
				item.SSHKeyPath, item.SSHExecutionHost = "", ""
				item.SSHJumpHost, item.SSHJumpAlias, item.SSHJumpKey, item.SSHHostJumpKey = "", "", "", ""
				// 但"没有 ECS 实例"不等于"运维无路可进"。容器在共享集群里，
				// 运维是从控制面 incus exec 进去的 —— 把这条命令直接给出来，
				// 否则面板上这一格是空的，排障时只能到处翻文档。
				item.PackedContainer = "packed-" + record.TenantName
				item.PackedControlPlane = h.packedControlPlane
				if h.packedControlPlane != "" {
					container := item.PackedContainer
					jump := strings.TrimSpace(h.sshJumpHost)
					user := strings.TrimSpace(h.sshUser)
					if user == "" {
						user = "root"
					}
					// 控制面只认 agents.key。不给 -i 的话运维得自己去翻密钥路径，
					// 或者用默认密钥对着控制面反复被拒。
					keyPath := strings.TrimSpace(h.packedSSHKey)
					if keyPath == "" {
						keyPath = "~/.ssh/agents.key"
					}
					keyArg := " -i " + shellSingleQuote(keyPath)
					if jump != "" {
						jumpUser := strings.TrimSpace(h.sshJumpUser)
						if jumpUser == "" {
							jumpUser = "root"
						}
						item.PackedSSHCommand = fmt.Sprintf(
							"ssh -J %s@%s%s %s@%s 'incus exec %s -- bash'",
							jumpUser, jump, keyArg, user, h.packedControlPlane, container)
					} else {
						item.PackedSSHCommand = fmt.Sprintf(
							"ssh%s %s@%s 'incus exec %s -- bash'",
							keyArg, user, h.packedControlPlane, container)
					}
				}
				// Subscription billing (month vs on-demand, trial conversion)
				// belongs to the ECS fleet. The shared fleet is billed by the
				// package, so surfacing these would look like a pending task
				// that can never be completed.
				item.BillingMode, item.ConversionPending, item.BillingAction = "", false, ""
			}
		}
		overview.StatusCounts[providerStatus]++
		if item.LifecycleState != "" {
			overview.LifecycleCounts[item.LifecycleState]++
		}
		if item.CreditState != "" {
			overview.CreditCounts[item.CreditState]++
		}
		overview.Workers = append(overview.Workers, item)
	}
	sort.Slice(overview.Workers, func(i, j int) bool {
		if overview.Workers[i].OwnerUID != overview.Workers[j].OwnerUID {
			return overview.Workers[i].OwnerUID < overview.Workers[j].OwnerUID
		}
		return overview.Workers[i].UID < overview.Workers[j].UID
	})
	return overview, nil
}

type externalCloudWorkerImportRequest struct {
	WorkerUID    *int64 `json:"worker_uid"`
	OwnerUID     *int64 `json:"owner_uid"`
	TenantName   string `json:"tenant_name"`
	Provider     string `json:"provider"`
	RegionID     string `json:"region_id"`
	ProjectID    string `json:"project_id"`
	AZName       string `json:"az_name"`
	InstanceID   string `json:"instance_id"`
	InstanceName string `json:"instance_name"`
	PublicIP     string `json:"public_ip"`
}

// HandleAdminImport registers an existing provider instance as external
// inventory. Management/lifecycle modes are intentionally not accepted from
// the request: every import is fail-safe manual_import/external.
func (h *CloudWorkerHandler) HandleAdminImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	data, ok := h.db.(CloudWorkerAdminImportStore)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "cloud worker admin store unavailable"})
		return
	}
	var req externalCloudWorkerImportRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	req.Provider = strings.ToLower(strings.TrimSpace(req.Provider))
	if req.Provider == "" {
		req.Provider = "ctyun"
	}
	if req.Provider != "ctyun" || strings.TrimSpace(req.RegionID) == "" || strings.TrimSpace(req.InstanceID) == "" || strings.TrimSpace(req.InstanceName) == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "provider, region_id, instance_id and instance_name are required"})
		return
	}
	if req.OwnerUID == nil || *req.OwnerUID <= 0 || req.WorkerUID != nil && *req.WorkerUID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "owner_uid is required and worker_uid must be positive when provided"})
		return
	}
	if strings.TrimSpace(req.TenantName) == "" {
		req.TenantName = strings.TrimSpace(req.InstanceName)
	}
	record := types.CloudWorkerBindingRecord{WorkerUID: req.WorkerUID, OwnerUID: req.OwnerUID, TenantName: strings.TrimSpace(req.TenantName), Provider: req.Provider, RegionID: strings.TrimSpace(req.RegionID), ProjectID: strings.TrimSpace(req.ProjectID), AZName: strings.TrimSpace(req.AZName), InstanceID: strings.TrimSpace(req.InstanceID), InstanceName: strings.TrimSpace(req.InstanceName), PublicIP: strings.TrimSpace(req.PublicIP), ManagementMode: "manual_import", LifecycleMode: "external", Source: "manual", Status: "unverified"}
	if err := data.UpsertExternalCloudWorkerBinding(record); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "failed to import cloud worker binding"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"binding": record})
}

// reconcilePackedDeployments 纠正"平台记成 ECS、集群证明是容器"的租户。
//
// 背景（2026-11-06）：bot-live-38 当年从 ECS 迁到共享集群时，只搬了容器，
// 平台 bot_config.cloud_deployment 里还留着 private_nat + 一整套天翼云参数。
// 面板据此显示"华南2·无公网IP / 天翼云实例正常"，管理员的 SSH 列是空的。
//
// 判据只用**集群事实**：状态快照的 backend 列（packed-status.sh 固定输出 packed）。
// 只在证据明确时修正；反方向（记录是 packed_shared 但快照里没有）不动 ——
// 那更可能是容器正在重建，贸然改成 ECS 会把计费带上 ECS 的续费路径。
// 返回修正条数，供调用方记录。
func (h *CloudWorkerHandler) reconcilePackedDeployments(
	records []types.CloudWorkerAdminRecord,
	infos map[string]cloudInstanceInfo,
) int {
	store, ok := h.db.(cloudWorkerDeploymentStore)
	if !ok || len(records) == 0 {
		return 0
	}
	fixed := 0
	for _, record := range records {
		tenant := record.TenantName
		info, found := infos[tenant]
		if !found || strings.ToLower(strings.TrimSpace(info.Backend)) != "packed" {
			continue
		}
		current, err := store.GetCloudWorkerDeployment(tenant)
		if err != nil {
			log.Printf("[cloud-worker] reconcile skip %s: %v", tenant, err)
			continue
		}
		if current != nil && current.Profile == types.CloudWorkerPackedShared {
			continue
		}
		wanted, err := h.deploymentForProfile(types.CloudWorkerPackedShared)
		if err != nil {
			continue
		}
		// SetCloudWorkerDeployment 按 uid 落库（bot_config.user_id）。
		if record.WorkerUID <= 0 {
			log.Printf("[cloud-worker] reconcile skip %s: missing worker uid", tenant)
			continue
		}
		if err := store.SetCloudWorkerDeployment(record.WorkerUID, wanted); err != nil {
			log.Printf("[cloud-worker] reconcile write %s (uid=%d) failed: %v", tenant, record.WorkerUID, err)
			continue
		}
		log.Printf("[cloud-worker] reconciled %s (uid=%d): deployment profile -> packed_shared (cluster reports backend=packed)",
			tenant, record.WorkerUID)
		fixed++
	}
	return fixed
}

// packedAbsenceThreshold is how many consecutive *distinct* cluster status
// snapshots must report a packed tenant as absent before it is called drift.
// One snapshot can miss a container that is being recreated; three in a row
// (the status snapshot refreshes at least once a minute) cannot be explained
// that way.
const packedAbsenceThreshold = 3

// observePackedAbsence counts, per tenant, how many consecutive status
// snapshots report "missing". The counter only advances when the snapshot
// itself advances: the admin roster is polled far more often than the cluster
// is queried, and counting requests would make three refreshes of one page look
// like three independent observations.
//
// It never writes the database. A shared-fleet record is what puts a worker on
// the container billing path; rewriting it to an ECS profile because a
// container is briefly absent would silently move that worker onto instance
// renewal. The result is surfaced on the roster as an advisory field instead.
func (h *CloudWorkerHandler) observePackedAbsence(
	records []types.CloudWorkerAdminRecord,
	infos map[string]cloudInstanceInfo,
	snapshotAt time.Time,
) map[string]int {
	h.packedDriftMu.Lock()
	defer h.packedDriftMu.Unlock()
	if h.packedMissingStreak == nil {
		h.packedMissingStreak = map[string]int{}
	}
	if snapshotAt.IsZero() || snapshotAt.Equal(h.packedDriftSnapshot) {
		return copyPackedAbsenceStreaks(h.packedMissingStreak)
	}
	h.packedDriftSnapshot = snapshotAt
	for _, record := range records {
		tenant := record.TenantName
		if tenant == "" {
			continue
		}
		// 已删除/正在删除的租户本来就没有容器 —— 那是正常终态，不是对账不符。
		// 第一次上线时这三条历史 deleted 记录立刻被标成漂移，噪音淹掉了真信号。
		if isDeletedLifecycle(record.LifecycleState) {
			delete(h.packedMissingStreak, tenant)
			continue
		}
		// A tenant absent from the map was never reported by this snapshot's
		// deployment group, which is not evidence about the container itself.
		info, found := infos[tenant]
		if !found || !strings.EqualFold(strings.TrimSpace(info.Status), "missing") {
			delete(h.packedMissingStreak, tenant)
			continue
		}
		h.packedMissingStreak[tenant]++
		if h.packedMissingStreak[tenant] == packedAbsenceThreshold {
			log.Printf("[cloud-worker] packed tenant %s reported missing by %d consecutive cluster snapshots",
				tenant, packedAbsenceThreshold)
		}
	}
	return copyPackedAbsenceStreaks(h.packedMissingStreak)
}

// isDeletedLifecycle reports terminal/in-flight deletion states. A deleted
// tenant keeps its bot_config row for the retention window while its container
// is already gone, so "container missing" proves nothing there.
func isDeletedLifecycle(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "deleted", "delete_running":
		return true
	}
	return false
}

func copyPackedAbsenceStreaks(src map[string]int) map[string]int {
	out := make(map[string]int, len(src))
	for tenant, streak := range src {
		out[tenant] = streak
	}
	return out
}

// HandleAdminOverview is kept separate from the public cloud-worker routes;
// CommercialOpsHandler performs the internal-source and service-scope checks.

func (h *CloudWorkerHandler) HandleAdminOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	// 管理员每次拉列表时顺手对齐一次部署形态：面板显示的就是这一份记录，
	// 让它跟着集群事实走，而不是等人发现"面板在撒谎"。对账在
	// CloudWorkerAdminOverview 内部完成（那里同时拿得到 records 和状态快照）。
	overview, err := h.CloudWorkerAdminOverview(time.Now().UTC())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "cloud worker admin store unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, overview)
}
