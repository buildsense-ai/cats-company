package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const (
	maxBotSkillConfigBodyBytes         = 128 << 10
	maxBotSkillRefs                    = 256
	maxBotSkillIDBytes                 = 240
	maxBotSkillVersionBytes            = 120
	maxBotRuntimeSkillEntries          = 256
	maxBotRuntimeSkillNameBytes        = 240
	maxBotRuntimeDescriptionBytes      = 4 << 10
	maxBotRuntimePathBytes             = 512
	maxBotRuntimeInstanceIDBytes       = 128
	botRuntimeSkillInventoryStaleAfter = 15 * time.Minute
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

type botRuntimeSkillsResponse struct {
	BotID            string                    `json:"botId"`
	SkillsVisibility types.BotSkillsVisibility `json:"skills_visibility"`
	RuntimeStatus    string                    `json:"runtime_status"`
	Stale            bool                      `json:"stale,omitempty"`
	ObservedAt       string                    `json:"observedAt,omitempty"`
	Skills           []botViewerRuntimeSkill   `json:"skills"`
	Truncated        bool                      `json:"truncated,omitempty"`
}

// botViewerRuntimeSkill deliberately excludes file and package hashes. Those
// values help server-side diagnostics but are not needed to render a user's
// runtime inventory, and the established viewer API already redacts hashes.
type botViewerRuntimeSkill struct {
	Name          string                       `json:"name"`
	Description   string                       `json:"description"`
	RelativePath  string                       `json:"relativePath"`
	UserInvocable bool                         `json:"userInvocable"`
	SkillHub      *botViewerRuntimeSkillHubRef `json:"skillHub,omitempty"`
}

type botViewerRuntimeSkillHubRef struct {
	SkillID string `json:"skillId"`
	Version string `json:"version"`
}

const botSkillInventorySchema = "xiaoba.bot-runtime-skills.v1"

type botSkillInventoryRequest struct {
	Schema            string                           `json:"schema"`
	BotID             string                           `json:"botId"`
	ObservedAt        string                           `json:"observedAt"`
	RuntimeInstanceID string                           `json:"runtimeInstanceId,omitempty"`
	ReportSequence    uint64                           `json:"reportSequence,omitempty"`
	Skills            *[]botRuntimeSkillInventoryEntry `json:"skills"`
	Truncated         bool                             `json:"truncated,omitempty"`
}

// botRuntimeSkillInventoryEntry is intentionally an input-only DTO. The
// contentHash aliases preserve compatibility with the first runtime-inventory
// reporter, while the stored model uses unambiguous hash names.
type botRuntimeSkillInventoryEntry struct {
	Name          string                                `json:"name"`
	Description   string                                `json:"description"`
	RelativePath  string                                `json:"relativePath"`
	UserInvocable bool                                  `json:"userInvocable"`
	FileHash      string                                `json:"fileHash,omitempty"`
	ContentHash   string                                `json:"contentHash,omitempty"`
	SkillHub      *botRuntimeSkillHubInventoryReference `json:"skillHub,omitempty"`
}

type botRuntimeSkillHubInventoryReference struct {
	SkillID               string `json:"skillId"`
	Version               string `json:"version"`
	PackageChecksumSHA256 string `json:"packageChecksumSha256,omitempty"`
	ContentHash           string `json:"contentHash,omitempty"`
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

// HandleViewerRuntimeSkills exposes the last inventory reported by the
// runtime, using the same owner/public/authorized policy as configured skills.
func (h *BotDefinitionHandler) HandleViewerRuntimeSkills(w http.ResponseWriter, r *http.Request) {
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
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load bot runtime skills"})
		return
	}
	response := botRuntimeSkillsResponse{
		BotID:            strconv.FormatInt(botUID, 10),
		SkillsVisibility: visibility,
		RuntimeStatus:    "unreported",
		Skills:           []botViewerRuntimeSkill{},
	}
	if record != nil && record.Runtime.SkillInventory != nil {
		inventory := record.Runtime.SkillInventory
		response.RuntimeStatus = botRuntimeSkillInventoryStatus(inventory, time.Now().UTC())
		response.Stale = response.RuntimeStatus == "stale"
		response.ObservedAt = inventory.ObservedAt
		for _, skill := range inventory.Skills {
			response.Skills = append(response.Skills, redactBotRuntimeSkill(skill))
		}
		response.Truncated = inventory.Truncated
		if strings.TrimSpace(inventory.BotID) != "" {
			response.BotID = strings.TrimSpace(inventory.BotID)
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, response)
}

func redactBotRuntimeSkill(skill types.BotRuntimeSkill) botViewerRuntimeSkill {
	viewer := botViewerRuntimeSkill{
		Name:          skill.Name,
		Description:   skill.Description,
		RelativePath:  skill.RelativePath,
		UserInvocable: skill.UserInvocable,
	}
	if skill.SkillHub != nil {
		viewer.SkillHub = &botViewerRuntimeSkillHubRef{
			SkillID: skill.SkillHub.SkillID,
			Version: skill.SkillHub.Version,
		}
	}
	return viewer
}

// HandleRuntimeSkillInventory accepts a sanitized snapshot from the Bot API
// key. Validation happens before the store boundary so no server filesystem
// path or skill content can be persisted accidentally.
func (h *BotDefinitionHandler) HandleRuntimeSkillInventory(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
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
	var request botSkillInventoryRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBotSkillConfigBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	skills, err := canonicalBotSkillInventoryRequest(botUID, &request)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	inventory := types.BotSkillInventory{
		Schema: request.Schema, BotID: request.BotID, ObservedAt: request.ObservedAt,
		RuntimeInstanceID: request.RuntimeInstanceID, ReportSequence: request.ReportSequence,
		ReportedAt: time.Now().UTC().Format(time.RFC3339Nano),
		Skills:     skills, Truncated: request.Truncated,
	}
	if _, err := h.definitions.ReportBotSkillInventory(botUID, inventory); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save bot runtime skills"})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"botId":      strconv.FormatInt(botUID, 10),
		"observedAt": inventory.ObservedAt,
		"skillCount": len(inventory.Skills),
	})
}

func canonicalBotSkillInventoryRequest(botUID int64, request *botSkillInventoryRequest) ([]types.BotRuntimeSkill, error) {
	if request.Schema != botSkillInventorySchema {
		return nil, errors.New("invalid inventory schema")
	}
	if strings.TrimSpace(request.BotID) != strconv.FormatInt(botUID, 10) {
		return nil, errors.New("inventory botId does not match api key")
	}
	if strings.TrimSpace(request.ObservedAt) == "" || len(request.ObservedAt) > 80 || strings.ContainsAny(request.ObservedAt, "\r\n") {
		return nil, errors.New("invalid observedAt")
	}
	if _, err := time.Parse(time.RFC3339, request.ObservedAt); err != nil {
		return nil, errors.New("invalid observedAt")
	}
	if request.Skills == nil {
		return nil, errors.New("skills is required")
	}
	request.RuntimeInstanceID = strings.TrimSpace(request.RuntimeInstanceID)
	if request.RuntimeInstanceID != "" && !validBotRuntimeText(request.RuntimeInstanceID, maxBotRuntimeInstanceIDBytes, false) {
		return nil, errors.New("invalid runtimeInstanceId")
	}
	if request.ReportSequence > 0 && request.RuntimeInstanceID == "" {
		return nil, errors.New("runtimeInstanceId is required with reportSequence")
	}
	if len(*request.Skills) > maxBotRuntimeSkillEntries {
		return nil, errors.New("too many runtime skills")
	}
	skills := make([]types.BotRuntimeSkill, 0, len(*request.Skills))
	seen := make(map[string]struct{}, len(*request.Skills))
	for _, input := range *request.Skills {
		skill := types.BotRuntimeSkill{
			Name:          strings.TrimSpace(input.Name),
			Description:   strings.TrimSpace(input.Description),
			RelativePath:  strings.TrimSpace(input.RelativePath),
			UserInvocable: input.UserInvocable,
		}
		if !validBotRuntimeText(skill.Name, maxBotRuntimeSkillNameBytes, false) ||
			!validBotRuntimeText(skill.Description, maxBotRuntimeDescriptionBytes, true) ||
			!validBotRuntimeRelativePath(skill.RelativePath, maxBotRuntimePathBytes) {
			return nil, errors.New("invalid runtime skill metadata")
		}
		fileHash, err := canonicalRuntimeSkillHash(input.FileHash, input.ContentHash, "runtime skill fileHash")
		if err != nil {
			return nil, err
		}
		skill.FileHash = fileHash
		if _, exists := seen[skill.Name]; exists {
			return nil, errors.New("duplicate runtime skill name")
		}
		seen[skill.Name] = struct{}{}
		if input.SkillHub != nil {
			checksum, err := canonicalRuntimeSkillHash(
				input.SkillHub.PackageChecksumSHA256,
				input.SkillHub.ContentHash,
				"runtime SkillHub packageChecksumSha256",
			)
			if err != nil {
				return nil, err
			}
			skill.SkillHub = &types.BotRuntimeSkillHubReference{
				SkillID:               strings.TrimSpace(input.SkillHub.SkillID),
				Version:               strings.TrimSpace(input.SkillHub.Version),
				PackageChecksumSHA256: checksum,
			}
			if !validBotSkillID(skill.SkillHub.SkillID) ||
				!validBotSkillRefPart(skill.SkillHub.Version, maxBotSkillVersionBytes) {
				return nil, errors.New("invalid runtime SkillHub reference")
			}
		}
		skills = append(skills, skill)
	}
	return skills, nil
}

func canonicalRuntimeSkillHash(preferred, legacy, label string) (string, error) {
	preferred = strings.TrimSpace(strings.ToLower(preferred))
	legacy = strings.TrimSpace(strings.ToLower(legacy))
	if preferred != "" && !validBotSkillContentHash(preferred) {
		return "", errors.New("invalid " + label)
	}
	if legacy != "" && !validBotSkillContentHash(legacy) {
		return "", errors.New("invalid " + label)
	}
	if preferred != "" && legacy != "" && preferred != legacy {
		return "", errors.New("conflicting " + label)
	}
	if preferred != "" {
		return preferred, nil
	}
	return legacy, nil
}

func validBotRuntimeText(value string, maxBytes int, allowEmpty bool) bool {
	if (!allowEmpty && value == "") || !utf8.ValidString(value) || len(value) > maxBytes {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func validBotRuntimeRelativePath(value string, maxBytes int) bool {
	if !validBotRuntimeText(value, maxBytes, false) || strings.HasPrefix(value, "/") || strings.Contains(value, `\`) {
		return false
	}
	// Windows absolute paths can be serialized with forward slashes too
	// (for example, C:/Users/agent/skills). Reject drive-qualified values
	// before treating the path as a portable relative path.
	if len(value) >= 2 && ((value[0] >= 'A' && value[0] <= 'Z') || (value[0] >= 'a' && value[0] <= 'z')) && value[1] == ':' {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func botRuntimeSkillInventoryStatus(inventory *types.BotSkillInventory, now time.Time) string {
	if inventory == nil {
		return "unreported"
	}
	freshnessAt := strings.TrimSpace(inventory.ReportedAt)
	if freshnessAt == "" {
		// Records written before ReportedAt existed are still readable, but a
		// future client clock must never make them appear healthy indefinitely.
		freshnessAt = inventory.ObservedAt
	}
	reportedAt, err := time.Parse(time.RFC3339Nano, freshnessAt)
	if err != nil || reportedAt.After(now) || now.Sub(reportedAt) > botRuntimeSkillInventoryStaleAfter {
		return "stale"
	}
	return "reported"
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
