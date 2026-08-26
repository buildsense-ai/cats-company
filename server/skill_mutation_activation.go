package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const skillMutationActivationPathPrefix = "/api/bot/skill-mutations/"

type skillMutationActivationRequest struct {
	AppliedDefinitionRevision int64  `json:"appliedDefinitionRevision"`
	SkillSetHash              string `json:"skillSetHash,omitempty"`
	Result                    string `json:"result"`
	ErrorCode                 string `json:"errorCode,omitempty"`
}

type skillMutationActivationFailurePolicy struct {
	summary   string
	permanent bool
}

var skillMutationActivationFailurePolicies = map[string]skillMutationActivationFailurePolicy{
	"NETWORK_TIMEOUT":       {summary: "Runtime could not reach the Skill package service"},
	"PACKAGE_UNAVAILABLE":   {summary: "A Skill package is temporarily unavailable"},
	"WORKSPACE_BUSY":        {summary: "Runtime Skill workspace is temporarily busy"},
	"FILESYSTEM_BUSY":       {summary: "Runtime filesystem is temporarily busy"},
	"PACKAGE_HASH_MISMATCH": {summary: "A Skill package failed integrity verification", permanent: true},
	"REFERENCE_MISMATCH":    {summary: "The applied Skill reference does not match BotDefinition", permanent: true},
	"INVALID_PACKAGE":       {summary: "A Skill package is invalid", permanent: true},
	"WORKSPACE_COLLISION":   {summary: "The Skill workspace contains a conflicting package", permanent: true},
}

// SkillMutationActivationHandler is the dedicated Runtime-only boundary that
// can finish one activation_pending mutation. It is feature-flagged separately
// from ordinary BotDefinition acknowledgement and first-phase SkillHub APIs.
type SkillMutationActivationHandler struct {
	owners        botModelOwnershipStore
	mutations     store.BotSkillMutationActivationStore
	credentials   *botRuntimeCredentialSigner
	publicEnabled bool
	botUIDs       map[int64]bool
	now           func() time.Time
}

func NewSkillMutationActivationHandler(
	owners botModelOwnershipStore,
	mutations store.BotSkillMutationActivationStore,
	hub *Hub,
) *SkillMutationActivationHandler {
	var credentials *botRuntimeCredentialSigner
	if hub != nil {
		credentials = hub.botRuntimeCredentials
	}
	return &SkillMutationActivationHandler{
		owners: owners, mutations: mutations, credentials: credentials,
		botUIDs: make(map[int64]bool), now: time.Now,
	}
}

func (h *SkillMutationActivationHandler) SetRollout(publicEnabled bool, botUIDs map[int64]bool) {
	if h == nil {
		return
	}
	h.publicEnabled = publicEnabled
	h.botUIDs = make(map[int64]bool, len(botUIDs))
	for uid, enabled := range botUIDs {
		if uid > 0 && enabled {
			h.botUIDs[uid] = true
		}
	}
}

func (h *SkillMutationActivationHandler) Allowed(botUID int64) bool {
	return h != nil && botUID > 0 && (h.publicEnabled || h.botUIDs[botUID])
}

func (h *SkillMutationActivationHandler) Handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if h == nil || (!h.publicEnabled && len(h.botUIDs) == 0) {
		http.NotFound(w, r)
		return
	}
	if h.credentials == nil || h.owners == nil || h.mutations == nil || h.now == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "Skill activation acknowledgement is unavailable"})
		return
	}
	claims, err := h.credentials.verify(extractBotRuntimeCredential(r))
	if err != nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "valid Bot Runtime credential required"})
		return
	}
	if !h.Allowed(claims.BotUID) || !botRuntimeCredentialHasScope(claims, botRuntimeSkillActivationScope) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Skill activation acknowledgement is not enabled for this Runtime"})
		return
	}
	actualOwner, err := h.owners.GetBotOwner(claims.BotUID)
	if err != nil || actualOwner != claims.OwnerUID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Bot Runtime credential owner no longer matches"})
		return
	}
	mutationID, ok := parseSkillMutationActivationPath(r.URL.Path)
	if !ok {
		http.NotFound(w, r)
		return
	}
	var req skillMutationActivationRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	req.Result = strings.ToLower(strings.TrimSpace(req.Result))
	switch req.Result {
	case "applied":
		h.handleApplied(w, claims, mutationID, req)
	case "failed":
		h.handleFailed(w, claims, mutationID, req)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid result"})
	}
}

func (h *SkillMutationActivationHandler) handleApplied(
	w http.ResponseWriter,
	claims *botRuntimeCredentialClaims,
	mutationID int64,
	req skillMutationActivationRequest,
) {
	if strings.TrimSpace(req.ErrorCode) != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "errorCode is not valid for an applied result"})
		return
	}
	mutation, definition, idempotent, err := h.mutations.ActivateBotSkillMutation(
		types.BotSkillMutationActivationInput{
			BotUID: claims.BotUID, MutationID: mutationID,
			AppliedDefinitionRevision: req.AppliedDefinitionRevision,
			SkillSetHash:              req.SkillSetHash, RuntimeBodyID: claims.BodyID,
			RuntimeInstallationID: claims.InstallationID,
		},
		h.now().UTC(),
	)
	if err != nil {
		writeSkillMutationActivationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"mutation_id": mutation.ID, "status": mutation.Status,
		"applied_definition_revision": req.AppliedDefinitionRevision,
		"desired_definition_revision": definition.Runtime.DesiredRevision,
		"idempotent":                  idempotent,
	})
}

func (h *SkillMutationActivationHandler) handleFailed(
	w http.ResponseWriter,
	claims *botRuntimeCredentialClaims,
	mutationID int64,
	req skillMutationActivationRequest,
) {
	if strings.TrimSpace(req.SkillSetHash) != "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "skillSetHash is not valid for a failed result"})
		return
	}
	code := strings.ToUpper(strings.TrimSpace(req.ErrorCode))
	policy, ok := skillMutationActivationFailurePolicies[code]
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported errorCode"})
		return
	}
	mutation, definition, idempotent, err := h.mutations.RecordBotSkillMutationActivationFailure(
		types.BotSkillMutationActivationFailureInput{
			BotUID: claims.BotUID, MutationID: mutationID,
			AttemptedDefinitionRevision: req.AppliedDefinitionRevision,
			RuntimeBodyID:               claims.BodyID, RuntimeInstallationID: claims.InstallationID,
			ErrorCode: code, ErrorSummary: policy.summary, Permanent: policy.permanent,
		},
		h.now().UTC(),
	)
	if err != nil {
		writeSkillMutationActivationError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"mutation_id": mutation.ID, "status": mutation.Status,
		"attempted_definition_revision": req.AppliedDefinitionRevision,
		"desired_definition_revision":   definition.Runtime.DesiredRevision,
		"error_code":                    code, "retryable": !policy.permanent, "idempotent": idempotent,
	})
}

func parseSkillMutationActivationPath(path string) (int64, bool) {
	if !strings.HasPrefix(path, skillMutationActivationPathPrefix) {
		return 0, false
	}
	remainder := strings.TrimPrefix(path, skillMutationActivationPathPrefix)
	parts := strings.Split(remainder, "/")
	if len(parts) != 2 || parts[1] != "activation" {
		return 0, false
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	return id, err == nil && id > 0
}

func writeSkillMutationActivationError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrBotSkillMutationNotFound):
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Skill mutation not found"})
	case errors.Is(err, store.ErrBotSkillMutationRuntimeMismatch):
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Runtime does not match the Skill mutation"})
	case errors.Is(err, store.ErrBotSkillMutationDefinitionStale),
		errors.Is(err, store.ErrBotSkillMutationVersionFactsConflict),
		errors.Is(err, store.ErrBotSkillMutationActivationFactConflict),
		errors.Is(err, store.ErrBotSkillMutationStateConflict):
		writeJSON(w, http.StatusConflict, map[string]string{"error": "Skill activation acknowledgement conflicts with current state"})
	default:
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to record Skill activation acknowledgement"})
	}
}
