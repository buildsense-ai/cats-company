package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store/types"
)

const (
	maxBotDefaultPromptBodyBytes = maxCustomSystemPromptBytes + 4096
	maxPromptVersionBytes        = 128
)

type botPromptAccessStore interface {
	GetBotOwner(botUID int64) (int64, error)
	AreFriends(uid1, uid2 int64) (bool, error)
}

type botPromptVisibilityRequest struct {
	PromptVisibility types.BotPromptVisibility `json:"prompt_visibility"`
}

type botDefaultPromptReportRequest struct {
	Content        string `json:"content"`
	ContentHash    string `json:"contentHash"`
	XiaoBaVersion  string `json:"xiaobaVersion,omitempty"`
	RuntimeVersion string `json:"runtimeVersion,omitempty"`
}

type botDefaultPromptReportResponse struct {
	UID         int64                             `json:"uid"`
	Changed     bool                              `json:"changed"`
	ContentHash string                            `json:"contentHash"`
	ReportedAt  string                            `json:"reportedAt"`
	Snapshot    *botDefaultPromptSnapshotMetadata `json:"snapshot,omitempty"`
}

type botDefaultPromptSnapshotMetadata struct {
	ContentHash    string `json:"contentHash"`
	XiaoBaVersion  string `json:"xiaobaVersion,omitempty"`
	RuntimeVersion string `json:"runtimeVersion,omitempty"`
	ReportedAt     string `json:"reportedAt"`
}

type botViewerPromptResponse struct {
	UID              int64                             `json:"uid"`
	BotID            string                            `json:"botId"`
	Selected         string                            `json:"selected"`
	Content          string                            `json:"content"`
	ContentAvailable bool                              `json:"content_available"`
	PromptVisibility types.BotPromptVisibility         `json:"prompt_visibility"`
	Relation         string                            `json:"relation"`
	CanEdit          bool                              `json:"can_edit"`
	Revision         int64                             `json:"revision"`
	UpdatedAt        string                            `json:"updated_at,omitempty"`
	DefaultSnapshot  *botDefaultPromptSnapshotMetadata `json:"default_snapshot,omitempty"`
	DefaultContent   string                            `json:"default_content,omitempty"`
	DefaultAvailable bool                              `json:"default_content_available"`
}

// HandleOwnerPromptVisibility updates only the prompt read policy. It does not
// change the desired definition revision because runtimes do not apply it.
func (h *BotDefinitionHandler) HandleOwnerPromptVisibility(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPatch {
		w.Header().Set("Allow", "PATCH")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	_, botUID, ok := h.authorizeOwner(w, r)
	if !ok {
		return
	}
	var req botPromptVisibilityRequest
	if err := decodeStrictJSON(w, r, 4096, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	req.PromptVisibility = types.BotPromptVisibility(strings.ToLower(strings.TrimSpace(string(req.PromptVisibility))))
	if !validBotPromptVisibility(req.PromptVisibility) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "visibility must be owner or friends"})
		return
	}
	record, err := h.definitions.UpdateBotPromptVisibility(botUID, req.PromptVisibility)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update prompt visibility"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"uid":               botUID,
		"prompt_visibility": normalizeBotPromptVisibility(record.PromptVisibility),
	})
}

// HandleViewerPrompt returns the active prompt only. In particular, friends
// never receive a saved but inactive custom prompt or any other definition
// fields such as model credentials, skills, endpoints, or runtime errors.
func (h *BotDefinitionHandler) HandleViewerPrompt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	viewerUID := UIDFromContext(r.Context())
	if viewerUID <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	botUID, err := strconv.ParseInt(r.URL.Query().Get("uid"), 10, 64)
	if err != nil || botUID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid uid"})
		return
	}
	access, ok := h.owners.(botPromptAccessStore)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "prompt access policy is unavailable"})
		return
	}
	ownerUID, err := access.GetBotOwner(botUID)
	if err != nil || ownerUID <= 0 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
		return
	}
	relation := "owner"
	canEdit := viewerUID == ownerUID
	if !canEdit {
		relation = "friend"
		friends, friendErr := access.AreFriends(viewerUID, botUID)
		if friendErr != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to check Agent access"})
			return
		}
		if !friends {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "Agent not found"})
			return
		}
	}
	record, err := h.loadDefinition(botUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load Agent prompt"})
		return
	}
	visibility := normalizeBotPromptVisibility(record.PromptVisibility)
	if !canEdit && visibility != types.BotPromptFriends {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Agent owner has not shared this prompt"})
		return
	}

	selected := "default"
	if record.Definition.Prompt != nil {
		candidate := strings.ToLower(strings.TrimSpace(record.Definition.Prompt.Selected))
		if candidate == "custom" {
			selected = candidate
		}
	}
	response := botViewerPromptResponse{
		UID:              botUID,
		BotID:            strconv.FormatInt(botUID, 10),
		Selected:         selected,
		PromptVisibility: visibility,
		Relation:         relation,
		CanEdit:          canEdit,
		Revision:         record.Runtime.DesiredRevision,
		UpdatedAt:        record.Runtime.UpdatedAt,
	}
	response.ContentAvailable = false
	response.DefaultAvailable = false
	if strings.TrimSpace(record.Definition.BotID) != "" {
		response.BotID = strings.TrimSpace(record.Definition.BotID)
	}
	if selected == "custom" && record.Definition.Prompt != nil {
		response.Content = record.Definition.Prompt.CustomSystemPrompt
		response.ContentAvailable = strings.TrimSpace(response.Content) != ""
	}
	if record.DefaultPrompt != nil && (canEdit || selected == "default") {
		response.DefaultContent = record.DefaultPrompt.Content
		response.DefaultAvailable = strings.TrimSpace(response.DefaultContent) != ""
		response.DefaultSnapshot = defaultPromptSnapshotMetadata(record.DefaultPrompt)
		if selected == "default" {
			response.Content = record.DefaultPrompt.Content
			response.ContentAvailable = response.DefaultAvailable
		}
	}
	writeJSON(w, http.StatusOK, response)
}

// HandleRuntimeDefaultPrompt accepts only a bot API-key-authenticated runtime.
// The UID is taken from request context and cannot be supplied by the body.
func (h *BotDefinitionHandler) HandleRuntimeDefaultPrompt(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		w.Header().Set("Allow", "POST, PUT")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	botUID := UIDFromContext(r.Context())
	if botUID <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if _, err := h.owners.GetBotOwner(botUID); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "bot api key required"})
		return
	}
	var req botDefaultPromptReportRequest
	if err := decodeStrictJSON(w, r, maxBotDefaultPromptBodyBytes, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	snapshot, err := validateDefaultPromptReport(req)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	record, changed, err := h.definitions.ReportBotDefaultPrompt(botUID, snapshot)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to report default prompt"})
		return
	}
	metadata := defaultPromptSnapshotMetadata(record.DefaultPrompt)
	response := botDefaultPromptReportResponse{UID: botUID, Changed: changed, Snapshot: metadata}
	if metadata != nil {
		response.ContentHash = metadata.ContentHash
		response.ReportedAt = metadata.ReportedAt
	}
	writeJSON(w, http.StatusOK, response)
}

func validBotPromptVisibility(visibility types.BotPromptVisibility) bool {
	return visibility == types.BotPromptOwner || visibility == types.BotPromptFriends
}

func normalizeBotPromptVisibility(visibility types.BotPromptVisibility) types.BotPromptVisibility {
	visibility = types.BotPromptVisibility(strings.ToLower(strings.TrimSpace(string(visibility))))
	if validBotPromptVisibility(visibility) {
		return visibility
	}
	return types.BotPromptOwner
}

func validateDefaultPromptReport(req botDefaultPromptReportRequest) (types.BotDefaultPromptSnapshot, error) {
	if !utf8.ValidString(req.Content) {
		return types.BotDefaultPromptSnapshot{}, errors.New("content must be valid UTF-8")
	}
	if len([]byte(req.Content)) > maxCustomSystemPromptBytes {
		return types.BotDefaultPromptSnapshot{}, errors.New("default system prompt is too large")
	}
	if strings.TrimSpace(req.Content) == "" {
		return types.BotDefaultPromptSnapshot{}, errors.New("default system prompt is required")
	}
	hash := strings.ToLower(strings.TrimSpace(req.ContentHash))
	decodedHash, err := hex.DecodeString(hash)
	if err != nil || len(decodedHash) != sha256.Size {
		return types.BotDefaultPromptSnapshot{}, errors.New("contentHash must be a SHA-256 hex digest")
	}
	expected := sha256.Sum256([]byte(req.Content))
	if !strings.EqualFold(hash, hex.EncodeToString(expected[:])) {
		return types.BotDefaultPromptSnapshot{}, errors.New("contentHash does not match content")
	}
	xiaobaVersion := strings.TrimSpace(req.XiaoBaVersion)
	runtimeVersion := strings.TrimSpace(req.RuntimeVersion)
	if len([]byte(xiaobaVersion)) > maxPromptVersionBytes || len([]byte(runtimeVersion)) > maxPromptVersionBytes {
		return types.BotDefaultPromptSnapshot{}, errors.New("version is too long")
	}
	return types.BotDefaultPromptSnapshot{
		Content: req.Content, ContentHash: hash,
		XiaoBaVersion: xiaobaVersion, RuntimeVersion: runtimeVersion,
	}, nil
}

func defaultPromptSnapshotMetadata(snapshot *types.BotDefaultPromptSnapshot) *botDefaultPromptSnapshotMetadata {
	if snapshot == nil {
		return nil
	}
	return &botDefaultPromptSnapshotMetadata{
		ContentHash: snapshot.ContentHash, XiaoBaVersion: snapshot.XiaoBaVersion,
		RuntimeVersion: snapshot.RuntimeVersion, ReportedAt: snapshot.ReportedAt,
	}
}

func decodeStrictJSON(w http.ResponseWriter, r *http.Request, maxBytes int64, target interface{}) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("request must contain one JSON object")
	}
	return nil
}
