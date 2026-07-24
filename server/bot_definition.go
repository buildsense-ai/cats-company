package server

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const (
	maxBotDefinitionBodyBytes = 128 << 10
	maxBotDefinitionSkills    = 256
	maxBotSkillIDBytes        = 256
	maxBotSkillVersionBytes   = 128
)

var botDefinitionETagPattern = regexp.MustCompile(
	`^"bot-definition-([1-9][0-9]*)-m([0-9]+)-s([0-9]+)"$`,
)

type BotDefinitionHandler struct {
	models      *BotModelConfigHandler
	definitions store.BotDefinitionStore
}

type botDefinitionSkillsRequest struct {
	Skills *[]types.BotSkillRef `json:"skills"`
}

type botDefinitionResponse struct {
	Schema string               `json:"schema"`
	BotID  string               `json:"botId"`
	Model  interface{}          `json:"model"`
	Skills *[]types.BotSkillRef `json:"skills,omitempty"`
}

type botDefinitionCatalogModel struct {
	Kind            string `json:"kind"`
	ModelID         string `json:"modelId"`
	ReasoningEffort string `json:"reasoningEffort,omitempty"`
}

type botDefinitionCustomModel struct {
	Kind                string   `json:"kind"`
	Protocol            string   `json:"protocol"`
	APIBase             string   `json:"apiBase"`
	Model               string   `json:"model"`
	APIKey              string   `json:"apiKey"`
	ContextWindowTokens int64    `json:"contextWindowTokens"`
	MaxTokens           int64    `json:"maxTokens,omitempty"`
	Temperature         *float64 `json:"temperature,omitempty"`
	ReasoningEffort     string   `json:"reasoningEffort,omitempty"`
}

func NewBotDefinitionHandler(
	models *BotModelConfigHandler,
	definitions store.BotDefinitionStore,
) *BotDefinitionHandler {
	return &BotDefinitionHandler{models: models, definitions: definitions}
}

// Handle serves the bot-runtime canonical definition. Only the skills field is
// writable here; the model is composed from the existing encrypted cloud-model
// state.
func (h *BotDefinitionHandler) Handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	switch r.Method {
	case http.MethodGet:
		h.handleGet(w, r)
	case http.MethodPut:
		h.handlePut(w, r)
	case http.MethodPatch:
		h.handlePatch(w, r)
	default:
		w.Header().Set("Allow", "GET, PUT, PATCH")
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (h *BotDefinitionHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	botUID, ok := h.botUID(w, r)
	if !ok {
		return
	}
	snapshot, err := h.definitions.GetBotDefinition(botUID)
	if errors.Is(err, store.ErrBotDefinitionNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "bot definition not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load bot definition"})
		return
	}
	h.writeSnapshot(w, http.StatusOK, botUID, snapshot)
}

func (h *BotDefinitionHandler) handlePut(w http.ResponseWriter, r *http.Request) {
	botUID, ok := h.botUID(w, r)
	if !ok {
		return
	}
	if strings.TrimSpace(r.Header.Get("If-None-Match")) == "" {
		writeJSON(w, http.StatusPreconditionRequired, map[string]string{"error": "If-None-Match is required"})
		return
	}
	if strings.TrimSpace(r.Header.Get("If-None-Match")) != "*" {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": "bot definition already exists or precondition is invalid"})
		return
	}
	skills, ok := decodeBotDefinitionSkills(w, r)
	if !ok {
		return
	}
	snapshot, err := h.definitions.CreateBotDefinition(botUID, skills)
	if errors.Is(err, store.ErrBotDefinitionAlreadyExists) {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": "bot definition already exists"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create bot definition"})
		return
	}
	h.writeSnapshot(w, http.StatusCreated, botUID, snapshot)
}

func (h *BotDefinitionHandler) handlePatch(w http.ResponseWriter, r *http.Request) {
	botUID, ok := h.botUID(w, r)
	if !ok {
		return
	}
	ifMatch := strings.TrimSpace(r.Header.Get("If-Match"))
	if ifMatch == "" {
		writeJSON(w, http.StatusPreconditionRequired, map[string]string{"error": "If-Match is required"})
		return
	}
	tagBotUID, modelRevision, skillsRevision, ok := parseBotDefinitionETag(ifMatch)
	if !ok || tagBotUID != botUID {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": "bot definition changed before it was updated"})
		return
	}
	skills, ok := decodeBotDefinitionSkills(w, r)
	if !ok {
		return
	}
	snapshot, err := h.definitions.UpdateBotDefinition(botUID, modelRevision, skillsRevision, skills)
	if errors.Is(err, store.ErrBotDefinitionNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "bot definition not found"})
		return
	}
	if errors.Is(err, store.ErrStaleBotDefinitionRevision) {
		writeJSON(w, http.StatusPreconditionFailed, map[string]string{"error": "bot definition changed before it was updated"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to update bot definition"})
		return
	}
	h.writeSnapshot(w, http.StatusOK, botUID, snapshot)
}

func (h *BotDefinitionHandler) botUID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	if h == nil || h.definitions == nil || h.models == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "bot definition is unavailable"})
		return 0, false
	}
	botUID := UIDFromContext(r.Context())
	if botUID <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return 0, false
	}
	return botUID, true
}

func (h *BotDefinitionHandler) writeSnapshot(
	w http.ResponseWriter,
	status int,
	botUID int64,
	snapshot *types.BotDefinitionSnapshot,
) {
	if snapshot == nil || snapshot.Model == nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "invalid bot definition state"})
		return
	}
	model, err := h.definitionModel(botUID, snapshot.Model)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "bot definition model is unavailable"})
		return
	}
	var skills *[]types.BotSkillRef
	skillsRevision := int64(0)
	if snapshot.Skills != nil {
		value := append([]types.BotSkillRef(nil), snapshot.Skills.Skills...)
		if value == nil {
			value = []types.BotSkillRef{}
		}
		skills = &value
		skillsRevision = snapshot.Skills.Revision
	}
	w.Header().Set("ETag", formatBotDefinitionETag(
		botUID,
		snapshot.Model.Revision,
		skillsRevision,
	))
	writeJSON(w, status, botDefinitionResponse{
		Schema: store.BotDefinitionSchema,
		BotID:  strconv.FormatInt(botUID, 10),
		Model:  model,
		Skills: skills,
	})
}

func (h *BotDefinitionHandler) definitionModel(botUID int64, config *types.BotModelConfig) (interface{}, error) {
	normalized := botModelConfigWithDefaults(config)
	if normalized.Kind != botModelKindCustom {
		return botDefinitionCatalogModel{
			Kind:            botModelKindCatalog,
			ModelID:         normalized.ModelID,
			ReasoningEffort: normalized.ReasoningEffort,
		}, nil
	}
	custom, err := h.models.decryptCustomModel(botUID, normalized.CustomCiphertext)
	if err != nil {
		return nil, err
	}
	return botDefinitionCustomModel{
		Kind:                botModelKindCustom,
		Protocol:            custom.Protocol,
		APIBase:             custom.APIBase,
		Model:               custom.Model,
		APIKey:              custom.APIKey,
		ContextWindowTokens: custom.ContextWindowTokens,
		MaxTokens:           custom.MaxTokens,
		Temperature:         custom.Temperature,
		ReasoningEffort:     custom.ReasoningEffort,
	}, nil
}

func decodeBotDefinitionSkills(w http.ResponseWriter, r *http.Request) ([]types.BotSkillRef, bool) {
	var request botDefinitionSkillsRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBotDefinitionBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return nil, false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return nil, false
	}
	if request.Skills == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "skills is required"})
		return nil, false
	}
	skills := append([]types.BotSkillRef(nil), (*request.Skills)...)
	if err := validateBotSkillRefs(skills); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return nil, false
	}
	if skills == nil {
		skills = []types.BotSkillRef{}
	}
	return skills, true
}

func validateBotSkillRefs(skills []types.BotSkillRef) error {
	if len(skills) > maxBotDefinitionSkills {
		return errors.New("too many skills")
	}
	seen := make(map[string]struct{}, len(skills))
	for _, skill := range skills {
		if !validBotSkillReferencePart(skill.SkillID, maxBotSkillIDBytes) {
			return errors.New("invalid skillId")
		}
		if !validBotSkillReferencePart(skill.Version, maxBotSkillVersionBytes) {
			return errors.New("invalid skill version")
		}
		if _, exists := seen[skill.SkillID]; exists {
			return errors.New("duplicate skillId")
		}
		seen[skill.SkillID] = struct{}{}
	}
	return nil
}

func validBotSkillReferencePart(value string, maxBytes int) bool {
	if value == "" || len(value) > maxBytes || !utf8.ValidString(value) || strings.TrimSpace(value) != value {
		return false
	}
	for _, char := range value {
		if unicode.IsControl(char) {
			return false
		}
	}
	return true
}

func formatBotDefinitionETag(botUID, modelRevision, skillsRevision int64) string {
	return `"bot-definition-` + strconv.FormatInt(botUID, 10) +
		`-m` + strconv.FormatInt(modelRevision, 10) +
		`-s` + strconv.FormatInt(skillsRevision, 10) + `"`
}

func parseBotDefinitionETag(value string) (botUID, modelRevision, skillsRevision int64, ok bool) {
	match := botDefinitionETagPattern.FindStringSubmatch(value)
	if match == nil {
		return 0, 0, 0, false
	}
	botUID, errBot := strconv.ParseInt(match[1], 10, 64)
	modelRevision, errModel := strconv.ParseInt(match[2], 10, 64)
	skillsRevision, errSkills := strconv.ParseInt(match[3], 10, 64)
	if errBot != nil || errModel != nil || errSkills != nil {
		return 0, 0, 0, false
	}
	return botUID, modelRevision, skillsRevision, true
}
