package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const (
	maxBotSkillConfigBodyBytes = 128 << 10
	maxBotSkillRefs            = 256
	maxBotSkillIDBytes         = 240
	maxBotSkillVersionBytes    = 120
)

type botDefinitionSkillsPatchRequest struct {
	Revision *int64               `json:"revision"`
	Skills   *[]types.BotSkillRef `json:"skills"`
}

type botDefinitionSkillsResponse struct {
	BotID     string              `json:"botId"`
	Skills    []types.BotSkillRef `json:"skills"`
	Revision  int64               `json:"revision"`
	UpdatedAt string              `json:"updatedAt,omitempty"`
}

type botViewerSkill struct {
	Source  string `json:"source"`
	SkillID string `json:"skillId"`
	Version string `json:"version"`
}

type botViewerSkillsResponse struct {
	BotID            string                    `json:"botId"`
	SkillsVisibility types.BotSkillsVisibility `json:"skills_visibility"`
	Skills           []botViewerSkill          `json:"skills"`
}

type botSkillAccessStore interface {
	GetBotConfig(botUID int64) (*types.BotConfig, error)
	AreFriends(uid1, uid2 int64) (bool, error)
}

func validBotSkillsVisibility(visibility types.BotSkillsVisibility) bool {
	return visibility == types.BotSkillsOwner ||
		visibility == types.BotSkillsAuthorized ||
		visibility == types.BotSkillsPublic
}

func normalizeBotSkillsVisibility(visibility types.BotSkillsVisibility) types.BotSkillsVisibility {
	if validBotSkillsVisibility(visibility) {
		return visibility
	}
	return types.BotSkillsOwner
}

// HandleOwnerSkills exposes a field-level convenience API for WebApp callers.
// Persistence and concurrency still belong to the canonical BotDefinition.
func (h *BotDefinitionHandler) HandleOwnerSkills(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPatch {
		w.Header().Set("Allow", "GET, PATCH")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	_, botUID, ok := h.authorizeOwner(w, r)
	if !ok {
		return
	}
	h.handleSkillsForBot(w, r, botUID)
}

// HandleViewerSkills exposes only the skill identity fields allowed by the
// owner's visibility policy. The full Bot definition remains owner-only.
func (h *BotDefinitionHandler) HandleViewerSkills(w http.ResponseWriter, r *http.Request) {
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
	access, ok := h.owners.(botSkillAccessStore)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "skill access policy is unavailable"})
		return
	}
	config, err := access.GetBotConfig(botUID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "bot not found"})
		return
	}
	ownerUID, err := h.owners.GetBotOwner(botUID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "bot not found"})
		return
	}
	visibility := normalizeBotSkillsVisibility(config.SkillsVisibility)
	// A friend may inspect the Bot's synchronized Skill inventory, including
	// private SkillHub references. This endpoint is deliberately metadata-only:
	// it never returns content, local paths, hashes, credentials, or mutation
	// controls. The visibility setting still describes public catalogue sharing
	// for non-friends, while friendship grants the safe read-only inventory view.
	allowed := viewerUID == ownerUID || visibility == types.BotSkillsPublic
	if !allowed {
		allowed, err = access.AreFriends(viewerUID, botUID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to check Agent access"})
			return
		}
	}
	if !allowed {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "Agent 所有者未公开技能列表"})
		return
	}
	if h == nil || h.definitions == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "bot definition is unavailable"})
		return
	}
	record, err := h.loadDefinition(botUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load bot definition"})
		return
	}
	response := botViewerSkillsResponse{
		BotID:            strconv.FormatInt(botUID, 10),
		SkillsVisibility: visibility,
		Skills:           []botViewerSkill{},
	}
	if record != nil {
		if strings.TrimSpace(record.Definition.BotID) != "" {
			response.BotID = strings.TrimSpace(record.Definition.BotID)
		}
		for _, skill := range record.Definition.Skills {
			response.Skills = append(response.Skills, botViewerSkill{
				Source: skill.Source, SkillID: skill.SkillID, Version: skill.Version,
			})
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, response)
}

// HandleRuntimeSkills is the bot API-key form of the same field-level API.
func (h *BotDefinitionHandler) HandleRuntimeSkills(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPatch {
		w.Header().Set("Allow", "GET, PATCH")
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
	h.handleSkillsForBot(w, r, botUID)
}

func (h *BotDefinitionHandler) handleSkillsForBot(w http.ResponseWriter, r *http.Request, botUID int64) {
	w.Header().Set("Cache-Control", "no-store")
	if h == nil || h.definitions == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "bot definition is unavailable"})
		return
	}
	if r.Method == http.MethodGet {
		record, err := h.loadDefinition(botUID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load bot definition"})
			return
		}
		h.writeSkills(w, botUID, record)
		return
	}

	revision, skills, ok := decodeBotDefinitionSkillsPatch(w, r)
	if !ok {
		return
	}
	record, err := h.definitions.UpdateBotDefinitionSkills(botUID, revision, skills)
	if errors.Is(err, store.ErrStaleBotModelRevision) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "bot definition changed before it was saved"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save bot skill definition"})
		return
	}
	h.writeSkills(w, botUID, record)
}

func (h *BotDefinitionHandler) writeSkills(w http.ResponseWriter, botUID int64, record *types.BotDefinitionRecord) {
	response := botDefinitionSkillsResponse{
		BotID:  strconv.FormatInt(botUID, 10),
		Skills: []types.BotSkillRef{},
	}
	if record != nil {
		if strings.TrimSpace(record.Definition.BotID) != "" {
			response.BotID = strings.TrimSpace(record.Definition.BotID)
		}
		response.Skills = append(response.Skills, record.Definition.Skills...)
		response.Revision = record.Runtime.DesiredRevision
		response.UpdatedAt = record.Runtime.UpdatedAt
	}
	writeJSON(w, http.StatusOK, response)
}

func decodeBotDefinitionSkillsPatch(w http.ResponseWriter, r *http.Request) (int64, []types.BotSkillRef, bool) {
	var request botDefinitionSkillsPatchRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBotSkillConfigBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return 0, nil, false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return 0, nil, false
	}
	if request.Revision == nil || *request.Revision < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "revision is required"})
		return 0, nil, false
	}
	if request.Skills == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "skills is required"})
		return 0, nil, false
	}
	skills, err := canonicalBotSkillRefs(*request.Skills)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return 0, nil, false
	}
	return *request.Revision, skills, true
}

func canonicalBotSkillRefs(input []types.BotSkillRef) ([]types.BotSkillRef, error) {
	if len(input) > maxBotSkillRefs {
		return nil, errors.New("too many skills")
	}
	skills := make([]types.BotSkillRef, 0, len(input))
	seen := make(map[string]struct{}, len(input))
	for _, inputRef := range input {
		ref := types.BotSkillRef{
			Source:      strings.ToLower(strings.TrimSpace(inputRef.Source)),
			SkillID:     strings.TrimSpace(inputRef.SkillID),
			Version:     strings.TrimSpace(inputRef.Version),
			ContentHash: strings.TrimSpace(inputRef.ContentHash),
		}
		if ref.Source != "skillhub" {
			return nil, errors.New("invalid skill source")
		}
		if !validBotSkillID(ref.SkillID) {
			return nil, errors.New("invalid skillId")
		}
		if !validBotSkillRefPart(ref.Version, maxBotSkillVersionBytes) {
			return nil, errors.New("invalid skill version")
		}
		if !validBotSkillContentHash(ref.ContentHash) {
			return nil, errors.New("invalid skill contentHash")
		}
		if _, exists := seen[ref.SkillID]; exists {
			return nil, errors.New("duplicate skillId")
		}
		seen[ref.SkillID] = struct{}{}
		skills = append(skills, ref)
	}
	sort.Slice(skills, func(i, j int) bool {
		return skills[i].SkillID < skills[j].SkillID
	})
	return skills, nil
}

func validBotSkillRefPart(value string, maxBytes int) bool {
	if value == "" || value == "." || value == ".." ||
		len(value) > maxBytes || !utf8.ValidString(value) ||
		strings.ContainsAny(value, `/\`) {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func validBotSkillID(value string) bool {
	if value == "" || len(value) > maxBotSkillIDBytes || !utf8.ValidString(value) ||
		strings.Contains(value, `\`) {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if !validBotSkillRefPart(segment, maxBotSkillIDBytes) {
			return false
		}
	}
	return true
}

func validBotSkillContentHash(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !((char >= '0' && char <= '9') || (char >= 'a' && char <= 'f')) {
			return false
		}
	}
	return true
}
