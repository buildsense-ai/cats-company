package server

import (
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

var errCloudWorkerAdminUnavailable = errors.New("cloud worker admin store unavailable")

// CloudWorkerAdminDataStore is the intentionally narrow database projection
// used by the internal cloud-worker roster. It must never be wired to a public
// user route or return credentials.
type CloudWorkerAdminDataStore interface {
	ListCloudWorkerAdminRecords() ([]types.CloudWorkerAdminRecord, error)
}

// CloudWorkerAdminOverviewHandler is the internal read-only endpoint wired
// behind CommercialOpsHandler's service authentication.
type CloudWorkerAdminOverviewHandler interface {
	HandleAdminOverview(http.ResponseWriter, *http.Request)
}

type cloudWorkerAdminItem struct {
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
}

type cloudWorkerAdminOverview struct {
	GeneratedAt              time.Time              `json:"generated_at"`
	WorkerCount              int                    `json:"worker_count"`
	ProviderStatusAvailable  bool                   `json:"provider_status_available"`
	ProviderStatusRefreshing bool                   `json:"provider_status_refreshing"`
	ProviderStatusUpdatedAt  *time.Time             `json:"provider_status_updated_at,omitempty"`
	StatusCounts             map[string]int         `json:"status_counts"`
	LifecycleCounts          map[string]int         `json:"lifecycle_counts"`
	CreditCounts             map[string]int         `json:"credit_counts"`
	Workers                  []cloudWorkerAdminItem `json:"workers"`
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
	overview := &cloudWorkerAdminOverview{
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
		if statusLoaded {
			providerStatus = "missing"
			if info, found := infos[record.TenantName]; found {
				providerStatus = strings.TrimSpace(info.Status)
				if providerStatus == "" {
					providerStatus = "unknown"
				}
				appVersion = info.AppVersion
				imageID = info.ImageID
				imageVersion = info.Version
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

// HandleAdminOverview is kept separate from the public cloud-worker routes;
// CommercialOpsHandler performs the internal-source and service-scope checks.
func (h *CloudWorkerHandler) HandleAdminOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	overview, err := h.CloudWorkerAdminOverview(time.Now().UTC())
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "cloud worker admin store unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, overview)
}
