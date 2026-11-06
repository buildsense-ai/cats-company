package server

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
)

const skillHubPublicationPrefix = "/api/skillhub/publications"

var skillHubPublicationEntryID = regexp.MustCompile(`^pubentry_[a-f0-9]{32}$`)

// SkillHubPublicationReadHandler authenticates active humans itself, so even
// authentication errors are private/no-store and never use query credentials.
// It also serves a separately gated pre-Cloud cancellation action. It has no
// creation, runtime dispatch or Cloud transport method; GET never mutates state.
type SkillHubPublicationReadHandler struct {
	db                                 store.Store
	entries                            store.SkillHubPublicationEntryStore
	enabled                            bool
	limits                             *HTTPRateLimiter
	reads                              chan struct{}
	workspaceEntriesEnabled            bool
	workspaceDurableEntriesEnabled     bool
	workspaceDurablePublishUIEnabled   bool
	workspaceDurableHumanCommitEnabled bool
	cancellationEnabled                bool
	descriptionReleaseHistoryEnabled   bool
	descriptionConfirmationUIEnabled   bool
	repairAdmissionUIEnabled           bool
}

func NewSkillHubPublicationReadHandler(db store.Store, enabled bool) *SkillHubPublicationReadHandler {
	entries, _ := db.(store.SkillHubPublicationEntryStore)
	return &SkillHubPublicationReadHandler{
		db: db, entries: entries, enabled: enabled,
		limits: NewHTTPRateLimiter(), reads: make(chan struct{}, 16),
	}
}

func NewSkillHubPublicationReadHandlerFromEnv(db store.Store) *SkillHubPublicationReadHandler {
	v := strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_READS_ENABLED"))
	h := NewSkillHubPublicationReadHandler(db, v == "1" || strings.EqualFold(v, "true"))
	_, generations := db.(store.ConversationTaskGenerationStore)
	_, recovery := db.(store.SkillHubPublicationEntryRequestReader)
	h.workspaceEntriesEnabled = publicationWorkspaceEntriesEnabled() && h.entries != nil && generations && recovery
	_, delivery := db.(store.SkillHubPublicationDeliveryStore)
	_, lister := db.(store.SkillHubPublicationRuntimeDeliveryLister)
	_, statusReader := db.(store.SkillHubPublicationSourceStatusReader)
	h.workspaceDurableEntriesEnabled = h.workspaceEntriesEnabled && publicationDeliveryEnabled() && delivery && lister && statusReader
	// This is a separate, default-off rollout flag. Durable admission and
	// read-only status are safe to expose before the UI is ready to replace the
	// existing "publish and add to Agent" contract.
	v = strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DURABLE_PUBLISH_UI_ENABLED"))
	h.workspaceDurablePublishUIEnabled = h.workspaceDurableEntriesEnabled && (v == "1" || strings.EqualFold(v, "true"))
	v = strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_HUMAN_COMMIT_ENABLED"))
	h.workspaceDurableHumanCommitEnabled = h.workspaceDurableEntriesEnabled && (v == "1" || strings.EqualFold(v, "true"))
	v = strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_CANCELLATION_ENABLED"))
	h.cancellationEnabled = h.enabled && (v == "1" || strings.EqualFold(v, "true"))
	v = strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_RELEASE_HISTORY_ENABLED"))
	h.descriptionReleaseHistoryEnabled = h.enabled && (v == "1" || strings.EqualFold(v, "true"))
	_, repairs := db.(store.SkillHubPublicationRepairStore)
	_, descriptions := db.(store.SkillHubPublicationRepairDescriptionStore)
	flag := func(name string) bool {
		value := strings.TrimSpace(os.Getenv(name))
		return value == "1" || strings.EqualFold(value, "true")
	}
	// UI discovery is not authority: the human handler independently checks its
	// gates, peer readiness and current private-task ownership on GET and POST.
	h.descriptionConfirmationUIEnabled = h.enabled && h.entries != nil && repairs && descriptions &&
		flag("CATSCO_SKILLHUB_PUBLICATION_HUMAN_COMMIT_ENABLED") &&
		flag("CATSCO_SKILLHUB_PUBLICATION_REPAIR_ADMISSION_ENABLED") &&
		flag("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_CONFIRMATION_UI_ENABLED")
	_, commitReader := db.(store.SkillHubPublicationCommitContextReader)
	_, commitStore := db.(store.SkillHubPublicationCommitStore)
	h.repairAdmissionUIEnabled = h.enabled && h.entries != nil && repairs && commitReader && commitStore &&
		flag("CATSCO_SKILLHUB_PUBLICATION_HUMAN_COMMIT_ENABLED") &&
		flag("CATSCO_SKILLHUB_PUBLICATION_REPAIR_ADMISSION_ENABLED") &&
		flag("CATSCO_SKILLHUB_PUBLICATION_REPAIR_ADMISSION_UI_ENABLED")
	return h
}

// Explicit projection: adding internal fields to the store record cannot expose
// source paths/references, request keys, identity hashes or credentials here.
type skillHubPublicationEntryView struct {
	EntryRequestID string    `json:"entryRequestId"`
	BotUID         string    `json:"botUid"`
	Entrypoint     string    `json:"entrypoint"`
	State          string    `json:"state"`
	CreatedAt      time.Time `json:"createdAt"`
}

func skillHubPublicationReadError(w http.ResponseWriter, status int, code string) {
	problem := map[string]string{
		"code": code, "message": "SkillHub publication request could not be processed.",
	}
	if status == http.StatusServiceUnavailable {
		// Never log raw database errors, bearer tokens or source information.
		problem["correlationId"] = rand.Text()
		log.Printf("[skillhub_publication_read] code=%s correlation_id=%s", code, problem["correlationId"])
	}
	writeSkillHubPublicationJSON(w, status, map[string]any{"error": problem})
}

func writeSkillHubPublicationJSON(w http.ResponseWriter, status int, body any) {
	// The shared writeJSON resets Cache-Control; keep this private API's policy
	// explicit without changing legacy handlers.
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func (h *SkillHubPublicationReadHandler) Handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	if h == nil || h.db == nil {
		skillHubPublicationReadError(w, http.StatusServiceUnavailable, "publication.unavailable")
		return
	}
	// No cookies, query tokens, API keys or caller-provided context identities.
	auth := r.Header.Values("Authorization")
	if len(auth) != 1 || !strings.HasPrefix(auth[0], "Bearer ") || strings.TrimSpace(strings.TrimPrefix(auth[0], "Bearer ")) == "" {
		skillHubPublicationReadError(w, http.StatusUnauthorized, "publication.human_login_required")
		return
	}
	claims, status, _ := ownerClaimsFromRequest(r, h.db.GetUser)
	if status != 0 {
		code := "publication.human_login_required"
		if status >= 500 {
			status, code = http.StatusServiceUnavailable, "publication.authority_unavailable"
		} else if status == http.StatusForbidden {
			code = "publication.access_denied"
		}
		skillHubPublicationReadError(w, status, code)
		return
	}
	if claims.UID <= 0 {
		skillHubPublicationReadError(w, http.StatusUnauthorized, "publication.human_login_required")
		return
	}
	path := r.URL.Path
	capabilities := path == skillHubPublicationPrefix+"/capabilities"
	id := strings.TrimPrefix(path, skillHubPublicationPrefix+"/entries/")
	sourceStatus := strings.HasPrefix(path, skillHubPublicationPrefix+"/entries/") && strings.HasSuffix(id, "/source-status")
	captureStatus := strings.HasPrefix(path, skillHubPublicationPrefix+"/entries/") && strings.HasSuffix(id, "/capture-status")
	cloudStatus := strings.HasPrefix(path, skillHubPublicationPrefix+"/entries/") && strings.HasSuffix(id, "/cloud-status")
	descriptionReleaseHistory := strings.HasPrefix(path, skillHubPublicationPrefix+"/entries/") && strings.HasSuffix(id, "/description-release-history")
	cancelEntry := strings.HasPrefix(path, skillHubPublicationPrefix+"/entries/") && strings.HasSuffix(id, "/cancel")
	if sourceStatus {
		id = strings.TrimSuffix(id, "/source-status")
	}
	if captureStatus {
		id = strings.TrimSuffix(id, "/capture-status")
	}
	if cloudStatus {
		id = strings.TrimSuffix(id, "/cloud-status")
	}
	if descriptionReleaseHistory {
		id = strings.TrimSuffix(id, "/description-release-history")
	}
	if cancelEntry {
		id = strings.TrimSuffix(id, "/cancel")
	}
	byRequest := strings.HasPrefix(path, skillHubPublicationPrefix+"/requests/")
	valid := strings.HasPrefix(path, skillHubPublicationPrefix+"/entries/") && skillHubPublicationEntryID.MatchString(id)
	if byRequest {
		id = strings.TrimPrefix(path, skillHubPublicationPrefix+"/requests/")
		valid = store.ValidSkillHubPublicationRequestID(id)
	}
	if r.URL.RawPath != "" || (!capabilities && !valid) {
		skillHubPublicationReadError(w, http.StatusNotFound, "publication.not_found")
		return
	}
	method := http.MethodGet
	if descriptionReleaseHistory && (!h.enabled || !h.descriptionReleaseHistoryEnabled) {
		skillHubPublicationReadError(w, http.StatusNotFound, "publication.not_found")
		return
	}
	if cancelEntry {
		if !h.enabled || !h.cancellationEnabled {
			skillHubPublicationReadError(w, http.StatusNotFound, "publication.not_found")
			return
		}
		method = http.MethodPost
	}
	if r.Method != method {
		w.Header().Set("Allow", method)
		skillHubPublicationReadError(w, http.StatusMethodNotAllowed, "publication.method_not_allowed")
		return
	}
	if r.URL.RawQuery != "" || r.URL.ForceQuery || r.ContentLength != 0 || len(r.TransferEncoding) != 0 {
		skillHubPublicationReadError(w, http.StatusBadRequest, "publication.invalid_request")
		return
	}
	actor := strconv.FormatInt(claims.UID, 10)
	if !h.limits.allow(HTTPRateLimitConfig{Name: "publication_reads_actor", Limit: 60, Window: time.Minute, Burst: 10}, actor) {
		w.Header().Set("Retry-After", "5")
		skillHubPublicationReadError(w, http.StatusTooManyRequests, "publication.rate_limited")
		return
	}
	if capabilities {
		_, sourceStatusSupported := h.db.(store.SkillHubPublicationSourceStatusReader)
		_, captureStatusSupported := h.db.(store.SkillHubPublicationCaptureStatusReader)
		_, cloudStatusSupported := h.db.(store.SkillHubPublicationCloudStatusReader)
		_, cancelSupported := h.db.(store.SkillHubPublicationCancellationStore)
		_, historySupported := h.db.(store.SkillHubPublicationDescriptionReleaseTerminalReader)
		writeSkillHubPublicationJSON(w, http.StatusOK, map[string]any{
			"schemaVersion": 1, "entryReadsEnabled": h.enabled && h.entries != nil,
			"sourceStatusReadsEnabled":              h.enabled && h.entries != nil && sourceStatusSupported,
			"captureStatusReadsEnabled":             h.enabled && h.entries != nil && captureStatusSupported,
			"cloudStatusReadsEnabled":               h.enabled && h.entries != nil && cloudStatusSupported,
			"descriptionReleaseHistoryReadsEnabled": h.enabled && h.entries != nil && h.descriptionReleaseHistoryEnabled && historySupported,
			"descriptionConfirmationUiEnabled":      h.descriptionConfirmationUIEnabled,
			"repairTaskAdmissionUiEnabled":          h.repairAdmissionUIEnabled,
			"workspaceCreateEnabled":                h.workspaceEntriesEnabled, "chatCreateEnabled": false,
			"workspaceDurableCreateEnabled":      h.workspaceDurableEntriesEnabled,
			"workspaceDurablePublishUiEnabled":   h.workspaceDurablePublishUIEnabled,
			"workspaceDurableHumanCommitEnabled": h.workspaceDurableHumanCommitEnabled,
			"entryCancellationEnabled":           h.enabled && h.entries != nil && h.cancellationEnabled && cancelSupported,
			"minPollIntervalMs":                  5000,
		})
		return
	}
	if !h.enabled || (cancelEntry && !h.cancellationEnabled) {
		skillHubPublicationReadError(w, http.StatusNotFound, "publication.not_found")
		return
	}
	if h.entries == nil {
		skillHubPublicationReadError(w, http.StatusServiceUnavailable, "publication.storage_unsupported")
		return
	}
	pollKey := path
	if sourceStatus || cloudStatus || captureStatus || descriptionReleaseHistory {
		pollKey = skillHubPublicationPrefix + "/entries/" + id
	}
	if !h.limits.allow(HTTPRateLimitConfig{Name: "publication_reads_entry", Limit: 12, Window: time.Minute, Burst: 3}, actor+":"+pollKey) {
		w.Header().Set("Retry-After", "5")
		skillHubPublicationReadError(w, http.StatusTooManyRequests, "publication.rate_limited")
		return
	}
	select {
	case h.reads <- struct{}{}:
		defer func() { <-h.reads }()
	default:
		w.Header().Set("Retry-After", "5")
		skillHubPublicationReadError(w, http.StatusServiceUnavailable, "publication.busy")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var entry *store.SkillHubPublicationEntry
	var err error
	if byRequest {
		reader, ok := h.db.(store.SkillHubPublicationEntryRequestReader)
		if !ok {
			skillHubPublicationReadError(w, http.StatusServiceUnavailable, "publication.storage_unsupported")
			return
		}
		entry, err = reader.GetSkillHubPublicationEntryByRequest(ctx, claims.UID, id)
	} else {
		entry, err = h.entries.GetSkillHubPublicationEntry(ctx, claims.UID, id)
	}
	if errors.Is(err, store.ErrSkillHubPublicationDenied) || errors.Is(err, store.ErrSkillHubPublicationInvalid) {
		skillHubPublicationReadError(w, http.StatusNotFound, "publication.not_found")
		return
	}
	if err != nil || ctx.Err() != nil {
		skillHubPublicationReadError(w, http.StatusServiceUnavailable, "publication.read_unavailable")
		return
	}
	if entry == nil || entry.Input.ActorUID != claims.UID {
		skillHubPublicationReadError(w, http.StatusNotFound, "publication.not_found")
		return
	}
	// Unknown future states must be deliberately mapped, never passed through
	// as arbitrary strings or mistaken for a successful Cloud publication.
	identityMatches := (!byRequest && entry.ID == id) || (byRequest && entry.Input.RequestID == id)
	if !identityMatches || !skillHubPublicationEntryID.MatchString(entry.ID) || (entry.State != "waiting_runtime" && entry.State != "cancelled") || entry.Input.Entrypoint != "workspace" || entry.Input.BotUID <= 0 || entry.CreatedAt.IsZero() {
		skillHubPublicationReadError(w, http.StatusServiceUnavailable, "publication.record_unavailable")
		return
	}
	if sourceStatus {
		h.readSourceStatus(w, ctx, claims.UID, entry.ID)
		return
	}
	if captureStatus {
		h.readCaptureStatus(w, ctx, claims.UID, entry.ID)
		return
	}
	if cloudStatus {
		h.readCloudStatus(w, ctx, claims.UID, entry.ID)
		return
	}
	if descriptionReleaseHistory {
		h.readDescriptionReleaseHistory(w, ctx, claims.UID, entry.ID)
		return
	}
	if cancelEntry {
		canceller, ok := h.db.(store.SkillHubPublicationCancellationStore)
		if !ok {
			skillHubPublicationReadError(w, http.StatusServiceUnavailable, "publication.storage_unsupported")
			return
		}
		cancelled, err := canceller.CancelSkillHubPublicationEntry(ctx, claims.UID, entry.ID)
		if errors.Is(err, store.ErrSkillHubPublicationDenied) || errors.Is(err, store.ErrSkillHubPublicationInvalid) {
			skillHubPublicationReadError(w, http.StatusNotFound, "publication.not_found")
			return
		}
		if errors.Is(err, store.ErrSkillHubPublicationConflict) {
			skillHubPublicationReadError(w, http.StatusConflict, "publication.cancellation_conflict")
			return
		}
		if err != nil || ctx.Err() != nil || cancelled == nil || cancelled.ID != entry.ID || cancelled.Input != entry.Input || cancelled.State != "cancelled" || cancelled.CreatedAt.IsZero() {
			// A cancelled context/lost response may follow a successful commit.
			// Preserve the original ID; GET the state or repeat this same action.
			skillHubPublicationReadError(w, http.StatusServiceUnavailable, "publication.cancellation_uncertain")
			return
		}
		entry = cancelled
	}
	writeSkillHubPublicationJSON(w, http.StatusOK, map[string]any{
		"schemaVersion": 1,
		"entry": skillHubPublicationEntryView{
			EntryRequestID: entry.ID, BotUID: strconv.FormatInt(entry.Input.BotUID, 10),
			Entrypoint: "workspace", State: entry.State, CreatedAt: entry.CreatedAt.UTC(),
		},
	})
}
