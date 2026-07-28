package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const botDefinitionSchema = "xiaoba.bot-definition.v1"

type BotDefinitionHandler struct {
	owners      botModelOwnershipStore
	definitions store.BotDefinitionStore
	models      store.BotModelConfigStore
	secretCodec *botModelSecretCodec
	secretError error
}

type botDefinitionPatchRequest struct {
	ExpectedRevision *int64                    `json:"expected_revision"`
	Model            *botDefinitionModelInput  `json:"model,omitempty"`
	SavedCustomModel *botDefinitionModelInput  `json:"savedCustomModel,omitempty"`
	Prompt           *botDefinitionPromptInput `json:"prompt,omitempty"`
}

type botDefinitionModelInput struct {
	Kind                string   `json:"kind"`
	ModelID             string   `json:"modelId,omitempty"`
	ReasoningEffort     string   `json:"reasoningEffort,omitempty"`
	ReasoningEffortSet  bool     `json:"-"`
	Protocol            string   `json:"protocol,omitempty"`
	APIBase             string   `json:"apiBase,omitempty"`
	Model               string   `json:"model,omitempty"`
	APIKey              string   `json:"apiKey,omitempty"`
	ClearAPIKey         bool     `json:"clearApiKey,omitempty"`
	ContextWindowTokens int64    `json:"contextWindowTokens,omitempty"`
	MaxTokens           *int64   `json:"maxTokens,omitempty"`
	Temperature         *float64 `json:"temperature,omitempty"`
	TemperatureSet      bool     `json:"-"`
}

func (m *botDefinitionModelInput) UnmarshalJSON(data []byte) error {
	type modelInput botDefinitionModelInput
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var decoded modelInput
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	*m = botDefinitionModelInput(decoded)
	_, m.TemperatureSet = fields["temperature"]
	_, m.ReasoningEffortSet = fields["reasoningEffort"]
	return nil
}

type botDefinitionPromptInput struct {
	Selected           string  `json:"selected"`
	CustomSystemPrompt *string `json:"customSystemPrompt,omitempty"`
}

type botDefinitionAckRequest struct {
	Revision int64  `json:"revision"`
	Error    string `json:"error,omitempty"`
}

func NewBotDefinitionHandler(
	owners botModelOwnershipStore,
	definitions store.BotDefinitionStore,
	models store.BotModelConfigStore,
) *BotDefinitionHandler {
	codec, err := newBotModelSecretCodecFromEnv()
	return &BotDefinitionHandler{
		owners: owners, definitions: definitions, models: models,
		secretCodec: codec, secretError: err,
	}
}

func (h *BotDefinitionHandler) HandleOwnerDefinition(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodPatch {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	ownerUID := UIDFromContext(r.Context())
	botUID, err := strconv.ParseInt(r.URL.Query().Get("uid"), 10, 64)
	if ownerUID <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if err != nil || botUID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid uid"})
		return
	}
	actualOwner, err := h.owners.GetBotOwner(botUID)
	if err != nil {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "bot not found"})
		return
	}
	if actualOwner != ownerUID {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not your bot"})
		return
	}
	if h.definitions == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "bot definition is unavailable"})
		return
	}

	record, apply, err := h.definitions.GetBotDefinition(botUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load bot definition"})
		return
	}
	if r.Method == http.MethodGet {
		if record == nil {
			h.writeMigrationRequired(w, botUID, false)
			return
		}
		response, responseErr := h.definitionResponse(botUID, record, apply, false)
		if responseErr != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": responseErr.Error()})
			return
		}
		writeJSON(w, http.StatusOK, response)
		return
	}

	var req botDefinitionPatchRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024*1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || req.ExpectedRevision == nil || *req.ExpectedRevision < 0 ||
		(req.Model == nil && req.SavedCustomModel == nil && req.Prompt == nil) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	expected := *req.ExpectedRevision
	if record == nil {
		if expected != 0 || req.Model == nil || req.Prompt == nil {
			h.writeMigrationRequired(w, botUID, false)
			return
		}
		record = &types.BotDefinitionRecord{
			Definition: types.BotDefinition{
				Schema: botDefinitionSchema,
				BotID:  strconv.FormatInt(botUID, 10),
			},
		}
	} else if record.Revision != expected {
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"error": "revision_conflict", "current_revision": record.Revision,
		})
		return
	}

	next := record.Definition
	if req.Model != nil {
		model, saved, modelErr := h.prepareDefinitionModel(botUID, req.Model, &record.Definition)
		if modelErr != nil {
			status := http.StatusBadRequest
			if errors.Is(modelErr, errBotModelEncryptionUnavailable) {
				status = http.StatusServiceUnavailable
			}
			writeJSON(w, status, map[string]string{"error": modelErr.Error()})
			return
		}
		next.Model = model
		if saved != nil {
			next.SavedCustomModel = saved
		} else if req.Model.ClearAPIKey {
			next.SavedCustomModel = nil
		}
	}
	if req.SavedCustomModel != nil {
		if strings.ToLower(strings.TrimSpace(req.SavedCustomModel.Kind)) != botModelKindCustom {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "savedCustomModel must be custom"})
			return
		}
		_, savedCustom, savedErr := h.prepareDefinitionModel(botUID, req.SavedCustomModel, &record.Definition)
		if savedErr != nil {
			status := http.StatusBadRequest
			if errors.Is(savedErr, errBotModelEncryptionUnavailable) {
				status = http.StatusServiceUnavailable
			}
			writeJSON(w, status, map[string]string{"error": savedErr.Error()})
			return
		}
		next.SavedCustomModel = savedCustom
	}
	if req.Prompt != nil {
		prompt, promptErr := prepareDefinitionPrompt(req.Prompt, next.Prompt)
		if promptErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": promptErr.Error()})
			return
		}
		next.Prompt = prompt
	}
	next.Schema = botDefinitionSchema
	next.BotID = strconv.FormatInt(botUID, 10)
	if next.Model.Kind == botModelKindCustom {
		if _, decryptErr := h.decryptDefinitionCustom(botUID, next.Model.APIKeyEncrypted); decryptErr != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": decryptErr.Error()})
			return
		}
	}
	saved, apply, err := h.definitions.SaveBotDefinition(botUID, expected, &next)
	if errors.Is(err, store.ErrStaleBotDefinitionRevision) {
		current, _, _ := h.definitions.GetBotDefinition(botUID)
		currentRevision := int64(0)
		if current != nil {
			currentRevision = current.Revision
		}
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"error": "revision_conflict", "current_revision": currentRevision,
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save bot definition"})
		return
	}
	response, responseErr := h.definitionResponse(botUID, saved, apply, false)
	if responseErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": responseErr.Error()})
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
	if _, err := h.owners.GetBotOwner(botUID); err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "bot api key required"})
		return
	}
	if h.definitions == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "bot definition is unavailable"})
		return
	}
	record, apply, err := h.definitions.GetBotDefinition(botUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load bot definition"})
		return
	}
	if record == nil {
		h.writeMigrationRequired(w, botUID, true)
		return
	}
	// A Definition-capable runtime can also apply the model-only compatibility
	// view, so keep the existing WebApp capability indicator accurate.
	if h.models != nil {
		if legacy, legacyErr := h.models.GetBotModelConfig(botUID); legacyErr == nil &&
			!botModelRuntimeSupported(legacy) {
			_, _ = h.models.MarkBotModelRuntimeProtocol(botUID, botModelRuntimeProtocol)
		}
	}
	response, responseErr := h.definitionResponse(botUID, record, apply, true)
	if responseErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": responseErr.Error()})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
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
	if err := decoder.Decode(&req); err != nil || req.Revision <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	record, _, err := h.definitions.GetBotDefinition(botUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load bot definition"})
		return
	}
	if record == nil {
		h.writeMigrationRequired(w, botUID, true)
		return
	}
	applyError := strings.TrimSpace(req.Error)
	if secret := h.definitionSecret(botUID, &record.Definition); secret != "" {
		applyError = strings.ReplaceAll(applyError, secret, "[REDACTED]")
	}
	if len(applyError) > 500 {
		applyError = applyError[:500]
	}
	record, apply, err := h.definitions.AckBotDefinition(botUID, req.Revision, applyError)
	if errors.Is(err, store.ErrStaleBotDefinitionRevision) {
		current, _, _ := h.definitions.GetBotDefinition(botUID)
		writeJSON(w, http.StatusConflict, map[string]interface{}{
			"error": "revision_conflict", "current_revision": recordRevision(current),
		})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to acknowledge bot definition"})
		return
	}
	response, responseErr := h.definitionResponse(botUID, record, apply, true)
	if responseErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": responseErr.Error()})
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, response)
}

func (h *BotDefinitionHandler) writeMigrationRequired(w http.ResponseWriter, botUID int64, runtime bool) {
	response := map[string]interface{}{"error": "migration_required", "uid": botUID}
	if h.models != nil {
		if legacy, err := h.models.GetBotModelConfig(botUID); err == nil && botModelConfigIsConfigured(legacy) {
			var value map[string]interface{}
			if runtime {
				value, err = h.runtimeLegacyModel(botUID, legacy)
			} else {
				value = h.ownerLegacyModel(botUID, legacy)
			}
			if err == nil {
				response["legacy_model"] = value
			}
		}
	}
	writeJSON(w, http.StatusConflict, response)
}

func (h *BotDefinitionHandler) ownerLegacyModel(botUID int64, config *types.BotModelConfig) map[string]interface{} {
	model := definitionModelMap(config.Kind, config.ModelID, config.ReasoningEffort)
	if config.Kind == botModelKindCustom && config.CustomCiphertext != "" {
		if custom, err := h.decryptDefinitionCustom(botUID, config.CustomCiphertext); err == nil {
			model = ownerCustomDefinitionMap(custom)
		}
	}
	return model
}

func (h *BotDefinitionHandler) runtimeLegacyModel(botUID int64, config *types.BotModelConfig) (map[string]interface{}, error) {
	if config.Kind != botModelKindCustom {
		return definitionModelMap(config.Kind, config.ModelID, config.ReasoningEffort), nil
	}
	custom, err := h.decryptDefinitionCustom(botUID, config.CustomCiphertext)
	if err != nil {
		return nil, err
	}
	return runtimeCustomDefinitionMap(custom), nil
}

func (h *BotDefinitionHandler) definitionResponse(
	botUID int64,
	record *types.BotDefinitionRecord,
	apply *types.BotDefinitionApplyState,
	runtime bool,
) (map[string]interface{}, error) {
	if record == nil {
		return nil, errors.New("bot definition is missing")
	}
	definition := record.Definition
	if definition.Schema != botDefinitionSchema || definition.BotID != strconv.FormatInt(botUID, 10) {
		return nil, errors.New("stored bot definition is invalid")
	}
	if definition.Prompt.Selected != "default" && definition.Prompt.Selected != "custom" {
		return nil, errors.New("stored bot prompt selection is invalid")
	}
	if definition.Prompt.Selected == "custom" && strings.TrimSpace(definition.Prompt.CustomSystemPrompt) == "" {
		return nil, errors.New("stored custom system prompt is empty")
	}
	var model map[string]interface{}
	var custom *cloudCustomModelConfig
	if definition.Model.Kind == botModelKindCustom {
		var err error
		custom, err = h.decryptDefinitionCustom(botUID, definition.Model.APIKeyEncrypted)
		if err != nil {
			return nil, err
		}
	}
	if definition.Model.Kind == botModelKindCustom {
		if custom == nil {
			return nil, errors.New("stored custom model is unavailable")
		}
		if runtime {
			model = runtimeCustomDefinitionMap(custom)
		} else {
			model = ownerCustomDefinitionMap(custom)
		}
	} else {
		if _, _, ok := normalizeBotModelSelection(definition.Model.ModelID, definition.Model.ReasoningEffort); !ok {
			return nil, errors.New("stored catalog model is invalid")
		}
		model = definitionModelMap(definition.Model.Kind, definition.Model.ModelID, definition.Model.ReasoningEffort)
	}
	definitionMap := map[string]interface{}{
		"schema": definition.Schema,
		"botId":  definition.BotID,
		"model":  model,
		"prompt": map[string]interface{}{
			"selected": definition.Prompt.Selected,
		},
	}
	if definition.Prompt.CustomSystemPrompt != "" {
		definitionMap["prompt"].(map[string]interface{})["customSystemPrompt"] = definition.Prompt.CustomSystemPrompt
	}
	if definition.Model.Kind != botModelKindCustom && definition.SavedCustomModel != nil &&
		definition.SavedCustomModel.APIKeyEncrypted != "" {
		savedCustom, err := h.decryptDefinitionCustom(botUID, definition.SavedCustomModel.APIKeyEncrypted)
		if err == nil {
			if runtime {
				definitionMap["savedCustomModel"] = runtimeCustomDefinitionMap(savedCustom)
			} else {
				definitionMap["savedCustomModel"] = ownerCustomDefinitionMap(savedCustom)
			}
		} else {
			definitionMap["savedCustomModelUnavailableReason"] = "saved custom model credential is unavailable"
		}
	}
	if apply == nil {
		apply = &types.BotDefinitionApplyState{DesiredRevision: record.Revision}
	}
	return map[string]interface{}{
		"definition": definitionMap,
		"revision":   record.Revision,
		"updatedAt":  record.UpdatedAt,
		"applyState": apply,
	}, nil
}

func (h *BotDefinitionHandler) prepareDefinitionModel(
	botUID int64,
	input *botDefinitionModelInput,
	previous *types.BotDefinition,
) (types.BotDefinitionModel, *types.BotDefinitionCustomModel, error) {
	kind := strings.ToLower(strings.TrimSpace(input.Kind))
	if kind == botModelKindCatalog {
		model, reasoning, ok := normalizeBotModelSelection(input.ModelID, input.ReasoningEffort)
		if !ok {
			return types.BotDefinitionModel{}, nil, errors.New("unsupported model or reasoning effort")
		}
		next := types.BotDefinitionModel{
			Kind: botModelKindCatalog, ModelID: model.ID, ReasoningEffort: reasoning,
			ClearSavedCustom: input.ClearAPIKey,
		}
		if input.ClearAPIKey {
			return next, nil, nil
		}
		return next, previous.SavedCustomModel, nil
	}
	if kind != botModelKindCustom {
		return types.BotDefinitionModel{}, nil, errors.New("invalid model kind")
	}
	if input.ClearAPIKey && strings.TrimSpace(input.APIKey) != "" {
		return types.BotDefinitionModel{}, nil, errors.New("apiKey and clearApiKey cannot be used together")
	}
	var prior *cloudCustomModelConfig
	var priorCiphertext string
	if previous != nil {
		priorCiphertext = previous.Model.APIKeyEncrypted
		if priorCiphertext == "" && previous.SavedCustomModel != nil {
			priorCiphertext = previous.SavedCustomModel.APIKeyEncrypted
		}
		if priorCiphertext != "" {
			prior, _ = h.decryptDefinitionCustom(botUID, priorCiphertext)
		}
	}
	custom := &cloudCustomModelConfig{
		Protocol:            strings.TrimSpace(input.Protocol),
		APIBase:             strings.TrimSpace(input.APIBase),
		Model:               strings.TrimSpace(input.Model),
		APIKey:              strings.TrimSpace(input.APIKey),
		ContextWindowTokens: input.ContextWindowTokens,
		Temperature:         input.Temperature,
		ReasoningEffort:     strings.TrimSpace(input.ReasoningEffort),
	}
	if prior != nil {
		if custom.Protocol == "" {
			custom.Protocol = prior.Protocol
		}
		if custom.APIBase == "" {
			custom.APIBase = prior.APIBase
		}
		if custom.Model == "" {
			custom.Model = prior.Model
		}
		if custom.APIKey == "" && !input.ClearAPIKey {
			custom.APIKey = prior.APIKey
		}
		if custom.ContextWindowTokens == 0 {
			custom.ContextWindowTokens = prior.ContextWindowTokens
		}
		if input.MaxTokens == nil {
			custom.MaxTokens = prior.MaxTokens
		}
		if !input.TemperatureSet {
			custom.Temperature = prior.Temperature
		}
		if !input.ReasoningEffortSet {
			custom.ReasoningEffort = prior.ReasoningEffort
		}
	}
	if input.MaxTokens != nil {
		custom.MaxTokens = *input.MaxTokens
	}
	if input.ClearAPIKey {
		custom.APIKey = ""
	}
	if h.secretCodec == nil {
		return types.BotDefinitionModel{}, nil, fmt.Errorf("%w: %v", errBotModelEncryptionUnavailable, h.secretError)
	}
	custom.Protocol = strings.ToLower(custom.Protocol)
	custom.APIBase = strings.TrimRight(custom.APIBase, "/")
	custom.ReasoningEffort = strings.ToLower(custom.ReasoningEffort)
	if err := validateCloudCustomModel(custom); err != nil {
		return types.BotDefinitionModel{}, nil, err
	}
	plaintext, err := json.Marshal(custom)
	if err != nil {
		return types.BotDefinitionModel{}, nil, errors.New("failed to encode custom model configuration")
	}
	ciphertext, err := h.secretCodec.encrypt(botUID, plaintext)
	if err != nil {
		return types.BotDefinitionModel{}, nil, fmt.Errorf("%w: %v", errBotModelEncryptionUnavailable, err)
	}
	model := types.BotDefinitionModel{
		Kind: botModelKindCustom, Protocol: custom.Protocol, APIBase: custom.APIBase,
		Model: custom.Model, APIKeyEncrypted: ciphertext,
		ContextWindowTokens: custom.ContextWindowTokens, MaxTokens: custom.MaxTokens,
		Temperature: custom.Temperature, ReasoningEffort: custom.ReasoningEffort,
	}
	saved := &types.BotDefinitionCustomModel{
		Kind: botModelKindCustom, Protocol: custom.Protocol, APIBase: custom.APIBase,
		Model: custom.Model, APIKeyEncrypted: ciphertext,
		ContextWindowTokens: custom.ContextWindowTokens, MaxTokens: custom.MaxTokens,
		Temperature: custom.Temperature, ReasoningEffort: custom.ReasoningEffort,
	}
	return model, saved, nil
}

func prepareDefinitionPrompt(
	input *botDefinitionPromptInput,
	previous types.BotDefinitionPrompt,
) (types.BotDefinitionPrompt, error) {
	selected := strings.ToLower(strings.TrimSpace(input.Selected))
	if selected != "default" && selected != "custom" {
		return types.BotDefinitionPrompt{}, errors.New("invalid prompt selection")
	}
	custom := previous.CustomSystemPrompt
	if input.CustomSystemPrompt != nil {
		custom = strings.TrimSpace(*input.CustomSystemPrompt)
	}
	if len(custom) > 512*1024 {
		return types.BotDefinitionPrompt{}, errors.New("custom system prompt is too large")
	}
	if selected == "custom" && custom == "" {
		return types.BotDefinitionPrompt{}, errors.New("custom system prompt is required")
	}
	return types.BotDefinitionPrompt{Selected: selected, CustomSystemPrompt: custom}, nil
}

func (h *BotDefinitionHandler) decryptDefinitionCustom(botUID int64, ciphertext string) (*cloudCustomModelConfig, error) {
	if h.secretCodec == nil {
		return nil, fmt.Errorf("%w: %v", errBotModelEncryptionUnavailable, h.secretError)
	}
	plaintext, err := h.secretCodec.decrypt(botUID, ciphertext)
	if err != nil {
		return nil, err
	}
	var custom cloudCustomModelConfig
	if err := json.Unmarshal(plaintext, &custom); err != nil {
		return nil, errors.New("invalid encrypted custom model configuration")
	}
	if err := validateCloudCustomModel(&custom); err != nil {
		return nil, err
	}
	return &custom, nil
}

func (h *BotDefinitionHandler) definitionSecret(botUID int64, definition *types.BotDefinition) string {
	if definition == nil {
		return ""
	}
	ciphertext := definition.Model.APIKeyEncrypted
	if ciphertext == "" && definition.SavedCustomModel != nil {
		ciphertext = definition.SavedCustomModel.APIKeyEncrypted
	}
	if ciphertext == "" {
		return ""
	}
	custom, err := h.decryptDefinitionCustom(botUID, ciphertext)
	if err != nil {
		return ""
	}
	return custom.APIKey
}

func definitionModelMap(kind, modelID, reasoning string) map[string]interface{} {
	if kind == "" {
		kind = botModelKindCatalog
	}
	value := map[string]interface{}{"kind": kind, "modelId": modelID}
	if reasoning != "" {
		value["reasoningEffort"] = reasoning
	}
	return value
}

func ownerCustomDefinitionMap(custom *cloudCustomModelConfig) map[string]interface{} {
	value := customDefinitionMap(custom)
	value["apiKeyConfigured"] = custom.APIKey != ""
	if custom.APIKey != "" {
		value["apiKeyHint"] = secretHint(custom.APIKey)
	}
	return value
}

func runtimeCustomDefinitionMap(custom *cloudCustomModelConfig) map[string]interface{} {
	value := customDefinitionMap(custom)
	value["apiKey"] = custom.APIKey
	return value
}

func customDefinitionMap(custom *cloudCustomModelConfig) map[string]interface{} {
	value := map[string]interface{}{
		"kind": "custom", "protocol": custom.Protocol, "apiBase": custom.APIBase,
		"model": custom.Model, "contextWindowTokens": custom.ContextWindowTokens,
	}
	if custom.MaxTokens > 0 {
		value["maxTokens"] = custom.MaxTokens
	}
	if custom.Temperature != nil {
		value["temperature"] = *custom.Temperature
	}
	if custom.ReasoningEffort != "" {
		value["reasoningEffort"] = custom.ReasoningEffort
	}
	return value
}

func recordRevision(record *types.BotDefinitionRecord) int64 {
	if record == nil {
		return 0
	}
	return record.Revision
}
