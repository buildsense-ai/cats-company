package server

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"
	"strings"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const (
	maxCustomSystemPromptBytes      = 1024 * 1024
	maxBotDefinitionPromptBodyBytes = 6*maxCustomSystemPromptBytes + 4096
)

type BotDefinitionHandler struct {
	owners      botModelOwnershipStore
	definitions store.BotDefinitionStore
	models      store.BotModelConfigStore
	modelConfig *BotModelConfigHandler
}

type botDefinitionModelPatchRequest struct {
	Revision *int64                       `json:"revision,omitempty"`
	Model    botDefinitionModelAPIRequest `json:"model"`
}

type botDefinitionModelAPIRequest struct {
	Kind                string   `json:"kind"`
	ModelID             string   `json:"modelId,omitempty"`
	ReasoningEffort     string   `json:"reasoningEffort,omitempty"`
	Protocol            string   `json:"protocol,omitempty"`
	APIBase             string   `json:"apiBase,omitempty"`
	Model               string   `json:"model,omitempty"`
	APIKey              string   `json:"apiKey,omitempty"`
	ContextWindowTokens int64    `json:"contextWindowTokens,omitempty"`
	MaxTokens           int64    `json:"maxTokens,omitempty"`
	Temperature         *float64 `json:"temperature,omitempty"`
}

type botDefinitionPromptPatchRequest struct {
	Revision *int64                    `json:"revision,omitempty"`
	Prompt   types.BotPromptDefinition `json:"prompt"`
}

type botDefinitionAckRequest struct {
	Revision int64  `json:"revision"`
	Error    string `json:"error,omitempty"`
}

func NewBotDefinitionHandler(
	owners botModelOwnershipStore,
	definitions store.BotDefinitionStore,
	models store.BotModelConfigStore,
	modelConfig *BotModelConfigHandler,
) *BotDefinitionHandler {
	return &BotDefinitionHandler{
		owners: owners, definitions: definitions, models: models, modelConfig: modelConfig,
	}
}

func (h *BotDefinitionHandler) HandleOwnerDefinition(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	ownerUID, botUID, ok := h.authorizeOwner(w, r)
	if !ok {
		return
	}
	record, err := h.loadDefinition(botUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load bot definition"})
		return
	}
	response, err := h.definitionResponse(ownerUID, botUID, record, false)
	if err != nil {
		log.Printf("prepare owner bot definition failed bot_uid=%d: %v", botUID, err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "bot definition could not be prepared"})
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *BotDefinitionHandler) HandleOwnerModel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	ownerUID, botUID, ok := h.authorizeOwner(w, r)
	if !ok {
		return
	}
	var req botDefinitionModelPatchRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 32*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	current, err := h.loadDefinition(botUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load bot definition"})
		return
	}
	model, err := h.prepareStoredModel(botUID, req.Model, current)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, errBotModelEncryptionUnavailable) {
			status = http.StatusServiceUnavailable
		}
		log.Printf("prepare owner bot model definition failed bot_uid=%d: %v", botUID, err)
		writeJSON(w, status, map[string]string{"error": "bot model definition could not be updated"})
		return
	}
	expected := int64(-1)
	if req.Revision != nil {
		expected = *req.Revision
	}
	record, err := h.definitions.UpdateBotDefinitionModel(botUID, expected, model)
	if errors.Is(err, store.ErrStaleBotModelRevision) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "bot definition changed before it was saved"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save bot model definition"})
		return
	}
	response, err := h.definitionResponse(ownerUID, botUID, record, false)
	if err != nil {
		log.Printf("prepare owner bot definition failed bot_uid=%d: %v", botUID, err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "bot definition could not be prepared"})
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *BotDefinitionHandler) HandleOwnerPrompt(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	ownerUID, botUID, ok := h.authorizeOwner(w, r)
	if !ok {
		return
	}
	var req botDefinitionPromptPatchRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBotDefinitionPromptBodyBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if req.Revision == nil || *req.Revision < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "revision is required"})
		return
	}
	req.Prompt.Selected = strings.ToLower(strings.TrimSpace(req.Prompt.Selected))
	if strings.TrimSpace(req.Prompt.CustomSystemPrompt) == "" {
		req.Prompt.CustomSystemPrompt = ""
	}
	if err := validateBotPromptDefinition(req.Prompt); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}
	record, err := h.definitions.UpdateBotDefinitionPrompt(botUID, *req.Revision, req.Prompt)
	if errors.Is(err, store.ErrStaleBotModelRevision) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "bot definition changed before it was saved"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save bot prompt definition"})
		return
	}
	response, err := h.definitionResponse(ownerUID, botUID, record, false)
	if err != nil {
		log.Printf("prepare owner bot definition failed bot_uid=%d: %v", botUID, err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "bot definition could not be prepared"})
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *BotDefinitionHandler) HandleRuntimeDefinition(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	botUID := UIDFromContext(r.Context())
	if botUID <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	ownerUID, err := h.owners.GetBotOwner(botUID)
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "bot api key required"})
		return
	}
	if h.models != nil {
		if config, getErr := h.models.GetBotModelConfig(botUID); getErr == nil && !botModelRuntimeSupported(config) {
			_, _ = h.models.MarkBotModelRuntimeProtocol(botUID, botModelRuntimeProtocol)
		}
	}
	record, err := h.loadDefinition(botUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load bot definition"})
		return
	}
	response, err := h.definitionResponse(ownerUID, botUID, record, true)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *BotDefinitionHandler) HandleRuntimeAck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
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
	var req botDefinitionAckRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || req.Revision < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	applyError := strings.TrimSpace(req.Error)
	if applyError != "" {
		if current, getErr := h.definitions.GetBotDefinition(botUID); getErr == nil &&
			current != nil &&
			current.Exists &&
			current.Definition.Model.Kind == botModelKindCustom &&
			current.Definition.Model.APIKeyCiphertext != "" {
			if custom, decryptErr := h.modelConfig.decryptCustomModel(
				botUID,
				current.Definition.Model.APIKeyCiphertext,
			); decryptErr == nil && custom.APIKey != "" {
				applyError = strings.ReplaceAll(applyError, custom.APIKey, "[REDACTED]")
			}
		}
	}
	if len(applyError) > 500 {
		applyError = applyError[:500]
	}
	record, err := h.definitions.AckBotDefinition(botUID, req.Revision, applyError)
	if errors.Is(err, store.ErrStaleBotModelRevision) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "bot definition changed before it was applied"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to acknowledge bot definition"})
		return
	}
	response, err := h.definitionResponse(0, botUID, record, true)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func (h *BotDefinitionHandler) authorizeOwner(w http.ResponseWriter, r *http.Request) (int64, int64, bool) {
	ownerUID := UIDFromContext(r.Context())
	botUID, err := strconv.ParseInt(r.URL.Query().Get("uid"), 10, 64)
	if ownerUID <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return 0, 0, false
	}
	if err != nil || botUID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid uid"})
		return 0, 0, false
	}
	actualOwner, err := h.owners.GetBotOwner(botUID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "bot not found"})
		return 0, 0, false
	}
	if actualOwner != ownerUID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not your bot"})
		return 0, 0, false
	}
	return ownerUID, botUID, true
}

func (h *BotDefinitionHandler) loadDefinition(botUID int64) (*types.BotDefinitionRecord, error) {
	record, err := h.definitions.GetBotDefinition(botUID)
	if err != nil {
		return nil, err
	}
	if record.Exists {
		return record, nil
	}
	// A legacy cloud_model is a real migration source. Persist it as the
	// canonical definition on first read, but leave a truly empty historical
	// bot unconfigured so XiaoBa can upload its local legacy definition.
	if strings.TrimSpace(record.Definition.Model.Kind) != "" {
		definition := record.Definition
		if definition.Model.Kind == botModelKindCustom &&
			definition.Model.APIKeyCiphertext != "" &&
			(definition.Model.Protocol == "" || definition.Model.APIBase == "") {
			custom, decryptErr := h.modelConfig.decryptCustomModel(
				botUID,
				definition.Model.APIKeyCiphertext,
			)
			if decryptErr != nil {
				return nil, decryptErr
			}
			definition.Model.Protocol = custom.Protocol
			definition.Model.APIBase = custom.APIBase
			definition.Model.Model = custom.Model
			definition.Model.ContextWindowTokens = custom.ContextWindowTokens
			definition.Model.MaxTokens = custom.MaxTokens
			definition.Model.Temperature = custom.Temperature
			definition.Model.ReasoningEffort = custom.ReasoningEffort
		}
		if definition.Prompt == nil {
			definition.Prompt = &types.BotPromptDefinition{Selected: "default"}
		}
		return h.definitions.CreateBotDefinitionIfAbsent(botUID, definition)
	}
	return record, nil
}

func (h *BotDefinitionHandler) prepareStoredModel(
	botUID int64,
	input botDefinitionModelAPIRequest,
	current *types.BotDefinitionRecord,
) (types.BotDefinitionModel, error) {
	kind := strings.ToLower(strings.TrimSpace(input.Kind))
	if kind == "" {
		kind = botModelKindCatalog
	}
	if kind == botModelKindCatalog {
		model, reasoning, ok := normalizeBotModelSelection(input.ModelID, input.ReasoningEffort)
		if !ok {
			return types.BotDefinitionModel{}, errors.New("unsupported model or reasoning effort")
		}
		return types.BotDefinitionModel{
			Kind: botModelKindCatalog, ModelID: model.ID, ReasoningEffort: reasoning,
			// Persist the standard catalog context window so responses can serve
			// it directly instead of depending on a catalog lookup at read time.
			ContextWindowTokens: model.ContextWindowTokens,
		}, nil
	}
	if kind != botModelKindCustom {
		return types.BotDefinitionModel{}, errors.New("invalid model kind")
	}
	custom := &cloudCustomModelConfig{
		Protocol:            input.Protocol,
		APIBase:             input.APIBase,
		Model:               input.Model,
		APIKey:              input.APIKey,
		ContextWindowTokens: input.ContextWindowTokens,
		MaxTokens:           input.MaxTokens,
		Temperature:         input.Temperature,
		ReasoningEffort:     input.ReasoningEffort,
	}
	legacy := legacyConfigForDefinition(current)
	prepared, ciphertext, err := h.modelConfig.prepareCustomModelUpdate(botUID, custom, legacy)
	if err != nil {
		return types.BotDefinitionModel{}, err
	}
	return types.BotDefinitionModel{
		Kind:                botModelKindCustom,
		Protocol:            prepared.Protocol,
		APIBase:             prepared.APIBase,
		Model:               prepared.Model,
		APIKeyCiphertext:    ciphertext,
		ContextWindowTokens: prepared.ContextWindowTokens,
		MaxTokens:           prepared.MaxTokens,
		Temperature:         prepared.Temperature,
		ReasoningEffort:     prepared.ReasoningEffort,
	}, nil
}

func (h *BotDefinitionHandler) definitionResponse(
	ownerUID, botUID int64,
	record *types.BotDefinitionRecord,
	includeSecret bool,
) (map[string]interface{}, error) {
	if record == nil || !record.Exists {
		return map[string]interface{}{
			"uid":        botUID,
			"configured": false,
			"revision":   int64(0),
		}, nil
	}
	model, configured := botDefinitionModelForResponse(record)
	if !configured {
		return map[string]interface{}{
			"uid":        botUID,
			"configured": false,
			"revision":   int64(0),
		}, nil
	}
	modelResponse := map[string]interface{}{"kind": model.Kind}
	if model.Kind == botModelKindCustom {
		modelResponse["protocol"] = model.Protocol
		modelResponse["apiBase"] = model.APIBase
		modelResponse["model"] = model.Model
		if includeSecret {
			modelResponse["contextWindowTokens"] = model.ContextWindowTokens
			if model.MaxTokens > 0 {
				modelResponse["maxTokens"] = model.MaxTokens
			}
		}
		if model.Temperature != nil {
			modelResponse["temperature"] = model.Temperature
		}
		if model.ReasoningEffort != "" {
			modelResponse["reasoningEffort"] = model.ReasoningEffort
		}
		if model.APIKeyCiphertext != "" {
			custom, err := h.modelConfig.decryptCustomModel(botUID, model.APIKeyCiphertext)
			if err != nil {
				return nil, err
			}
			if includeSecret {
				modelResponse["apiKey"] = custom.APIKey
			} else {
				modelResponse["apiKeyConfigured"] = custom.APIKey != ""
				modelResponse["apiKeyHint"] = secretHint(custom.APIKey)
			}
		}
	} else if model.Kind == botModelKindLocal {
		modelResponse["modelId"] = botModelKindLocal
	} else {
		modelResponse["modelId"] = model.ModelID
		if model.ReasoningEffort != "" {
			modelResponse["reasoningEffort"] = model.ReasoningEffort
		}
		// Catalog models ship the authoritative cloud context window so devices
		// follow the catalog even when a stored definition predates the field.
		if model.ContextWindowTokens > 0 {
			modelResponse["contextWindowTokens"] = model.ContextWindowTokens
		} else if tokens, ok := catalogContextWindowTokens(model.ModelID); ok {
			modelResponse["contextWindowTokens"] = tokens
		}
	}
	definition := map[string]interface{}{
		"schema": record.Definition.Schema,
		"botId":  record.Definition.BotID,
		"model":  modelResponse,
		"skills": append([]types.BotSkillRef(nil), record.Definition.Skills...),
	}
	if record.Definition.Prompt != nil {
		definition["prompt"] = record.Definition.Prompt
	}
	runtime := record.Runtime
	if ownerUID > 0 && runtime.LastError != "" {
		runtime.LastError = "Bot 配置应用失败"
	}
	response := map[string]interface{}{
		"uid":        botUID,
		"configured": true,
		"definition": definition,
		"revision":   record.Runtime.DesiredRevision,
		"runtime":    runtime,
	}
	if ownerUID > 0 {
		response["management_enabled"] = h.modelConfig.managementEnabled(ownerUID)
	}
	return response, nil
}

// Historical local handoffs can exist as an empty canonical model with a
// non-zero runtime revision. Normalize them at the API boundary without
// mutating storage; a truly untouched revision-zero record remains
// unconfigured so the device can bootstrap its current local model.
func botDefinitionModelForResponse(record *types.BotDefinitionRecord) (types.BotDefinitionModel, bool) {
	model := record.Definition.Model
	kind := strings.ToLower(strings.TrimSpace(model.Kind))
	modelID := strings.ToLower(strings.TrimSpace(model.ModelID))

	if kind == botModelKindLocal || modelID == botModelKindLocal {
		return types.BotDefinitionModel{Kind: botModelKindLocal, ModelID: botModelKindLocal}, true
	}
	if kind == "" && modelID == "" {
		appliedKind := strings.ToLower(strings.TrimSpace(record.Runtime.AppliedKind))
		appliedModelID := strings.ToLower(strings.TrimSpace(record.Runtime.AppliedModelID))
		if record.Runtime.DesiredRevision > 0 || appliedKind == botModelKindLocal || appliedModelID == botModelKindLocal {
			return types.BotDefinitionModel{Kind: botModelKindLocal, ModelID: botModelKindLocal}, true
		}
		return types.BotDefinitionModel{}, false
	}
	if kind == "" && modelID != "" {
		model.Kind = botModelKindCatalog
	}
	return model, true
}

func validateBotPromptDefinition(prompt types.BotPromptDefinition) error {
	prompt.Selected = strings.ToLower(strings.TrimSpace(prompt.Selected))
	if prompt.Selected != "default" && prompt.Selected != "custom" {
		return errors.New("prompt selection must be default or custom")
	}
	if len([]byte(prompt.CustomSystemPrompt)) > maxCustomSystemPromptBytes {
		return errors.New("custom system prompt is too large")
	}
	if prompt.Selected == "custom" && strings.TrimSpace(prompt.CustomSystemPrompt) == "" {
		return errors.New("custom system prompt is required")
	}
	return nil
}

func legacyConfigForDefinition(record *types.BotDefinitionRecord) *types.BotModelConfig {
	if record == nil {
		return &types.BotModelConfig{}
	}
	customCiphertext := record.Definition.Model.APIKeyCiphertext
	if customCiphertext == "" && record.SavedCustomModel != nil {
		customCiphertext = record.SavedCustomModel.APIKeyCiphertext
	}
	return &types.BotModelConfig{
		Kind:                record.Definition.Model.Kind,
		ModelID:             firstDefinitionModelID(record.Definition.Model),
		ReasoningEffort:     record.Definition.Model.ReasoningEffort,
		CustomCiphertext:    customCiphertext,
		RuntimeProtocol:     record.Runtime.RuntimeProtocol,
		RuntimeProtocolSeen: record.Runtime.RuntimeProtocolSeen,
		Revision:            record.Runtime.DesiredRevision,
		UpdatedAt:           record.Runtime.UpdatedAt,
		AppliedKind:         record.Runtime.AppliedKind,
		AppliedModelID:      record.Runtime.AppliedModelID,
		AppliedReasoning:    record.Runtime.AppliedReasoning,
		AppliedRevision:     record.Runtime.AppliedRevision,
		AppliedAt:           record.Runtime.AppliedAt,
		LastAttemptRevision: record.Runtime.LastAttemptRevision,
		LastAttemptAt:       record.Runtime.LastAttemptAt,
		LastError:           record.Runtime.LastError,
	}
}

func firstDefinitionModelID(model types.BotDefinitionModel) string {
	if strings.TrimSpace(model.ModelID) != "" {
		return model.ModelID
	}
	return model.Model
}
