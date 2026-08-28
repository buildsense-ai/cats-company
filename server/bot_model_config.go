package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const defaultBotModelID = "minimax-m3"
const botModelRuntimeProtocol = "cloud-model-v1"

const botModelRuntimeUnavailableReason = "当前 CatsCo 版本暂不支持云端切换，请更新桌面端后再试"

const (
	botModelKindCatalog = "catalog"
	botModelKindCustom  = "custom"
	botModelKindLocal   = "local"
)

var errBotModelEncryptionUnavailable = errors.New("custom model encryption is unavailable")

// errBotModelCiphertextUnreadable marks stored ciphertext that cannot be read at
// all (corrupt payload, wrong key, invalid JSON). Unlike a stored configuration
// that decrypts but fails validation, this is recoverable when the owner submits
// a fresh api key, so the update must not be blocked.
var errBotModelCiphertextUnreadable = errors.New("stored custom model ciphertext cannot be read")

type botModelOwnershipStore interface {
	GetBotOwner(botUID int64) (int64, error)
}

type BotModelConfigHandler struct {
	owners                   botModelOwnershipStore
	models                   store.BotModelConfigStore
	relayAdmin               *RelayAdminClient
	secretCodec              *botModelSecretCodec
	secretCodecError         error
	rolloutConfigured        bool
	publicEnabled            bool
	testUIDs                 map[int64]bool
	commercialStore          commercialQuotaSummaryStore
	commercialEnforceEnabled bool
	commercialEnforceUIDs    map[int64]bool
}

type botModelCatalogItem struct {
	ID                     string                     `json:"id"`
	Label                  string                     `json:"label"`
	Description            string                     `json:"description"`
	Provider               string                     `json:"provider"`
	Protocol               string                     `json:"protocol"`
	ContextWindowTokens    int64                      `json:"context_window_tokens"`
	ReasoningEfforts       []string                   `json:"reasoning_efforts,omitempty"`
	DefaultReasoningEffort string                     `json:"default_reasoning_effort,omitempty"`
	Vision                 bool                       `json:"vision,omitempty"`
	Available              bool                       `json:"available"`
	UnavailableReason      string                     `json:"unavailable_reason,omitempty"`
	Runtime                *botModelRuntimeDescriptor `json:"runtime,omitempty"`
	Quota                  *relayUsageSummary         `json:"quota,omitempty"`
	RuntimeModel           string                     `json:"-"`
}

// botModelRuntimeDescriptor is non-secret metadata consumed by XiaoBa. Relay
// endpoints and credentials are deliberately excluded and remain device-local.
type botModelRuntimeDescriptor struct {
	CatalogModelID      string `json:"catalogModelId"`
	Model               string `json:"model"`
	Provider            string `json:"provider"`
	ContextWindowTokens int64  `json:"contextWindowTokens"`
	OpenAIAPIMode       string `json:"openaiApiMode,omitempty"`
	Vision              bool   `json:"vision"`
	ToolCalling         bool   `json:"toolCalling"`
	Streaming           bool   `json:"streaming"`
}

var botModelCatalog = []botModelCatalogItem{
	{
		ID: "minimax-m2.7", Label: "MiniMax M2.7", Description: "标准额度，适合日常任务",
		Provider: "anthropic", Protocol: "Anthropic SDK", ContextWindowTokens: 204800, RuntimeModel: "MiniMax-M2.7",
	},
	{
		ID: "minimax-m3", Label: "MiniMax M3", Description: "支持多模态与长上下文",
		Provider: "anthropic", Protocol: "Anthropic SDK", ContextWindowTokens: 1000000, Vision: true, RuntimeModel: "MiniMax-M3",
	},
	deepSeekModelCatalogItem(),
	{
		ID: "glm-5.3-flash", Label: "GLM 5.3 Flash", Description: "高性价比多模态模型，适合长上下文与工具任务",
		Provider: "anthropic", Protocol: "Anthropic SDK", ContextWindowTokens: 1000000,
		ReasoningEfforts: []string{"max"}, DefaultReasoningEffort: "max",
		Vision: true, RuntimeModel: "glm-5.3-flash",
	},
	{
		ID: "gpt-5.6-terra", Label: "GPT-5.6 Terra", Description: "OpenAI Responses，支持精细推理强度",
		Provider: "openai", Protocol: "OpenAI Responses", ContextWindowTokens: 256000, RuntimeModel: "gpt-5.6-terra",
		ReasoningEfforts: []string{"none", "minimal", "low", "medium", "high", "xhigh"}, DefaultReasoningEffort: "medium",
	},
	{
		ID: "gpt-5.6-sol", Label: "GPT-5.6 Sol", Description: "OpenAI Responses，支持精细推理强度",
		Provider: "openai", Protocol: "OpenAI Responses", ContextWindowTokens: 256000, RuntimeModel: "gpt-5.6-sol",
		ReasoningEfforts: []string{"none", "minimal", "low", "medium", "high", "xhigh"}, DefaultReasoningEffort: "medium",
	},
	{
		ID: "gpt-5.6-luna", Label: "GPT-5.6 Luna", Description: "OpenAI Responses，支持精细推理强度",
		Provider: "openai", Protocol: "OpenAI Responses", ContextWindowTokens: 256000, RuntimeModel: "gpt-5.6-luna",
		ReasoningEfforts: []string{"none", "minimal", "low", "medium", "high", "xhigh"}, DefaultReasoningEffort: "medium",
	},
}

type botModelUpdateRequest struct {
	Kind            string                  `json:"kind,omitempty"`
	ModelID         string                  `json:"model_id"`
	ReasoningEffort string                  `json:"reasoning_effort,omitempty"`
	Custom          *cloudCustomModelConfig `json:"custom,omitempty"`
}

type botModelAckRequest struct {
	Revision        int64  `json:"revision"`
	Kind            string `json:"kind,omitempty"`
	ModelID         string `json:"model_id"`
	ReasoningEffort string `json:"reasoning_effort,omitempty"`
	Error           string `json:"error,omitempty"`
}

type cloudCustomModelConfig struct {
	Protocol            string   `json:"protocol"`
	APIBase             string   `json:"api_base"`
	Model               string   `json:"model"`
	APIKey              string   `json:"api_key,omitempty"`
	ContextWindowTokens int64    `json:"context_window_tokens"`
	MaxTokens           int64    `json:"max_tokens,omitempty"`
	Temperature         *float64 `json:"temperature,omitempty"`
	ReasoningEffort     string   `json:"reasoning_effort,omitempty"`
}

type ownerCustomModelConfig struct {
	Protocol            string   `json:"protocol"`
	APIBase             string   `json:"api_base"`
	Model               string   `json:"model"`
	APIKeyConfigured    bool     `json:"api_key_configured"`
	APIKeyHint          string   `json:"api_key_hint,omitempty"`
	ContextWindowTokens int64    `json:"context_window_tokens"`
	Temperature         *float64 `json:"temperature,omitempty"`
	ReasoningEffort     string   `json:"reasoning_effort,omitempty"`
}

func NewBotModelConfigHandler(owners botModelOwnershipStore, models store.BotModelConfigStore) *BotModelConfigHandler {
	codec, err := newBotModelSecretCodecFromEnv()
	return &BotModelConfigHandler{owners: owners, models: models, secretCodec: codec, secretCodecError: err}
}

func (h *BotModelConfigHandler) SetRelayUsageClient(admin *RelayAdminClient) {
	if h != nil {
		h.relayAdmin = admin
	}
}

func (h *BotModelConfigHandler) SetCommercialQuotaSource(store commercialQuotaSummaryStore, enforceEnabled bool, enforceUIDs map[int64]bool) {
	if h == nil {
		return
	}
	h.commercialStore = store
	h.commercialEnforceEnabled = enforceEnabled
	h.commercialEnforceUIDs = copyCommercialUIDSet(enforceUIDs)
}

func (h *BotModelConfigHandler) commercialQuotaEnforced(uid int64) bool {
	return h != nil && h.commercialStore != nil && uid > 0 && (h.commercialEnforceEnabled || h.commercialEnforceUIDs[uid])
}

// SetRollout supports either a public launch or an owner allowlist.
func (h *BotModelConfigHandler) SetRollout(publicEnabled bool, testUIDs map[int64]bool) {
	if h == nil {
		return
	}
	h.rolloutConfigured = true
	h.publicEnabled = publicEnabled
	h.testUIDs = make(map[int64]bool, len(testUIDs))
	for uid, enabled := range testUIDs {
		if uid > 0 && enabled {
			h.testUIDs[uid] = true
		}
	}
}

func (h *BotModelConfigHandler) managementEnabled(ownerUID int64) bool {
	if h == nil {
		return false
	}
	if !h.rolloutConfigured {
		return true
	}
	return h.publicEnabled || h.testUIDs[ownerUID]
}

// HandleOwnerConfig manages the cloud model selected by a bot owner.
func (h *BotModelConfigHandler) HandleOwnerConfig(w http.ResponseWriter, r *http.Request) {
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
	if h.models == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "bot model configuration is unavailable"})
		return
	}
	managementEnabled := h.managementEnabled(ownerUID)
	if r.Method == http.MethodPatch && !managementEnabled {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cloud bot model management is not enabled for this account"})
		return
	}

	storedConfig, err := h.models.GetBotModelConfig(botUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load bot model configuration"})
		return
	}
	if r.Method == http.MethodPatch && !botModelRuntimeSupported(storedConfig) {
		writeJSON(w, http.StatusConflict, map[string]string{
			"error":   "bot runtime does not support cloud model management",
			"message": botModelRuntimeUnavailableReason,
		})
		return
	}
	configured := botModelConfigIsConfigured(storedConfig)
	config := botModelConfigWithDefaults(storedConfig)
	if r.Method == http.MethodPatch {
		var req botModelUpdateRequest
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
			return
		}
		kind := strings.ToLower(strings.TrimSpace(req.Kind))
		if strings.EqualFold(strings.TrimSpace(req.ModelID), "local") || kind == "local" {
			localApplied := !configured && storedConfig.Revision > 0 &&
				storedConfig.AppliedRevision == storedConfig.Revision &&
				storedConfig.AppliedKind == "local" && storedConfig.AppliedModelID == "local" &&
				!(storedConfig.LastAttemptRevision == storedConfig.Revision && storedConfig.LastError != "")
			if !localApplied && (configured || storedConfig.Revision > 0) {
				config, err = h.models.SaveBotDesiredModelConfig(botUID, botModelKindLocal, botModelKindLocal, "", "")
			} else {
				config = storedConfig
			}
		} else if kind == botModelKindCustom || strings.EqualFold(strings.TrimSpace(req.ModelID), botModelKindCustom) {
			custom, customCiphertext, customErr := h.prepareCustomModelUpdate(botUID, req.Custom, storedConfig)
			if customErr != nil {
				status := http.StatusBadRequest
				if errors.Is(customErr, errBotModelEncryptionUnavailable) {
					status = http.StatusServiceUnavailable
				}
				log.Printf("prepare custom model update failed bot_uid=%d: %v", botUID, customErr)
				writeJSON(w, status, map[string]string{"error": "custom model configuration could not be updated"})
				return
			}
			config, err = h.saveDesiredCustomModel(botUID, custom, customCiphertext)
		} else {
			model, reasoning, ok := normalizeBotModelSelection(req.ModelID, req.ReasoningEffort)
			if !ok {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported model or reasoning effort"})
				return
			}
			allowed, quotaErr := h.catalogModelAllowed(ownerUID, model.ID)
			if quotaErr != nil {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{
					"error": "commercial quota is unavailable",
					"code":  "model_entitlement_unavailable",
				})
				return
			}
			if !allowed {
				writeJSON(w, http.StatusForbidden, map[string]string{
					"error": "model is not included in the current plan",
					"code":  "model_not_in_plan",
				})
				return
			}
			selectionApplied := configured && config.Kind == botModelKindCatalog &&
				config.ModelID == model.ID &&
				config.ReasoningEffort == reasoning &&
				config.AppliedRevision == config.Revision &&
				config.AppliedModelID == model.ID &&
				config.AppliedReasoning == reasoning &&
				!(config.LastAttemptRevision == config.Revision && config.LastError != "")
			if !selectionApplied {
				config, err = h.saveDesiredCatalogModel(botUID, model.ID, reasoning)
			}
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save bot model configuration"})
			return
		}
	}
	if r.Method == http.MethodGet {
		response := h.ownerConfigResponse(r.Context(), ownerUID, botUID, storedConfig, r.URL.Query().Get("include_usage") == "1")
		writeJSON(w, http.StatusOK, response)
		return
	}
	writeJSON(w, http.StatusOK, h.ownerConfigResponse(r.Context(), ownerUID, botUID, config, false))
}

func (h *BotModelConfigHandler) saveDesiredCatalogModel(
	botUID int64,
	modelID, reasoning string,
) (*types.BotModelConfig, error) {
	if definitions, ok := h.models.(store.BotDefinitionStore); ok {
		record, err := definitions.UpdateBotDefinitionModel(botUID, -1, types.BotDefinitionModel{
			Kind: botModelKindCatalog, ModelID: modelID, ReasoningEffort: reasoning,
		})
		if err != nil {
			return nil, err
		}
		return legacyConfigForDefinition(record), nil
	}
	return h.models.SaveBotDesiredModelConfig(botUID, botModelKindCatalog, modelID, reasoning, "")
}

func (h *BotModelConfigHandler) saveDesiredCustomModel(
	botUID int64,
	custom *cloudCustomModelConfig,
	ciphertext string,
) (*types.BotModelConfig, error) {
	if definitions, ok := h.models.(store.BotDefinitionStore); ok {
		record, err := definitions.UpdateBotDefinitionModel(botUID, -1, types.BotDefinitionModel{
			Kind:                botModelKindCustom,
			Protocol:            custom.Protocol,
			APIBase:             custom.APIBase,
			Model:               custom.Model,
			APIKeyCiphertext:    ciphertext,
			ContextWindowTokens: custom.ContextWindowTokens,
			MaxTokens:           custom.MaxTokens,
			Temperature:         custom.Temperature,
			ReasoningEffort:     custom.ReasoningEffort,
		})
		if err != nil {
			return nil, err
		}
		return legacyConfigForDefinition(record), nil
	}
	return h.models.SaveBotDesiredModelConfig(
		botUID, botModelKindCustom, custom.Model, custom.ReasoningEffort, ciphertext,
	)
}

// HandleRuntimeConfig lets an authenticated bot read only its own desired model.
func (h *BotModelConfigHandler) HandleRuntimeConfig(w http.ResponseWriter, r *http.Request) {
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
	if h.models == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "bot model configuration is unavailable"})
		return
	}
	if !h.managementEnabled(ownerUID) {
		response := botModelConfigResponse(botUID, nil)
		response["management_enabled"] = false
		writeJSON(w, http.StatusOK, response)
		return
	}
	config, err := h.models.GetBotModelConfig(botUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load bot model configuration"})
		return
	}
	if !botModelRuntimeSupported(config) {
		config, err = h.models.MarkBotModelRuntimeProtocol(botUID, botModelRuntimeProtocol)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to register bot model runtime"})
			return
		}
	}
	response, responseErr := h.runtimeConfigResponse(botUID, config)
	if responseErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": responseErr.Error()})
		return
	}
	writeJSON(w, http.StatusOK, response)
}

// HandleRuntimeAck records a successful or failed apply for the current revision.
func (h *BotModelConfigHandler) HandleRuntimeAck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
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
	if h.models == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "bot model configuration is unavailable"})
		return
	}
	if !h.managementEnabled(ownerUID) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cloud bot model management is not enabled for this account"})
		return
	}
	var req botModelAckRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil || req.Revision < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	current, err := h.models.GetBotModelConfig(botUID)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load bot model configuration"})
		return
	}
	configured := botModelConfigIsConfigured(current)
	localHandoff := !configured && current.Revision > 0
	if !configured && !localHandoff {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "cloud bot model configuration is not enabled"})
		return
	}
	kind := strings.ToLower(strings.TrimSpace(req.Kind))
	modelID := strings.TrimSpace(req.ModelID)
	reasoning := strings.ToLower(strings.TrimSpace(req.ReasoningEffort))
	if localHandoff {
		kind = strings.ToLower(kind)
		if kind != "local" || modelID != "local" || reasoning != "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid local model acknowledgement"})
			return
		}
	} else {
		current = botModelConfigWithDefaults(current)
		if kind == "" {
			kind = current.Kind
		}
	}
	if !localHandoff && kind == botModelKindCatalog {
		model, normalizedReasoning, ok := normalizeBotModelSelection(modelID, reasoning)
		if !ok {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported model or reasoning effort"})
			return
		}
		modelID = model.ID
		reasoning = normalizedReasoning
	} else if !localHandoff && kind == botModelKindCustom {
		if modelID == "" || !validCustomReasoningEffort(reasoning) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid custom model acknowledgement"})
			return
		}
	} else if !localHandoff {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid model kind"})
		return
	}
	if current.Revision != req.Revision || (!localHandoff &&
		(current.Kind != kind || current.ModelID != modelID || current.ReasoningEffort != reasoning)) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "bot model configuration changed before it was applied"})
		return
	}
	applyError := strings.TrimSpace(req.Error)
	if !localHandoff && kind == botModelKindCustom && applyError != "" {
		if custom, decryptErr := h.decryptCustomModel(botUID, current.CustomCiphertext); decryptErr == nil && custom.APIKey != "" {
			applyError = strings.ReplaceAll(applyError, custom.APIKey, "[REDACTED]")
		}
	}
	if len(applyError) > 500 {
		applyError = applyError[:500]
	}
	config, err := h.models.AckBotModelConfig(botUID, req.Revision, kind, modelID, reasoning, applyError)
	if errors.Is(err, store.ErrStaleBotModelRevision) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "bot model configuration changed before it was applied"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to acknowledge bot model configuration"})
		return
	}
	response, responseErr := h.runtimeConfigResponse(botUID, config)
	if responseErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": responseErr.Error()})
		return
	}
	writeJSON(w, http.StatusOK, response)
}

func botModelConfigWithDefaults(config *types.BotModelConfig) *types.BotModelConfig {
	if config == nil {
		config = &types.BotModelConfig{}
	}
	copy := *config
	if copy.Kind == "" && copy.ModelID != "" {
		copy.Kind = botModelKindCatalog
	}
	if copy.AppliedKind == "" && copy.AppliedModelID != "" {
		copy.AppliedKind = botModelKindCatalog
	}
	if copy.Kind == botModelKindCustom || copy.Kind == botModelKindLocal {
		return &copy
	}
	if copy.ModelID == "" {
		copy.Kind = botModelKindCatalog
		copy.ModelID = defaultBotModelID
	}
	model, reasoning, ok := normalizeBotModelSelection(copy.ModelID, copy.ReasoningEffort)
	if !ok {
		copy.ModelID = defaultBotModelID
		copy.ReasoningEffort = ""
		return &copy
	}
	copy.Kind = botModelKindCatalog
	copy.ModelID = model.ID
	copy.ReasoningEffort = reasoning
	return &copy
}

func normalizeBotModelSelection(modelID, reasoning string) (botModelCatalogItem, string, bool) {
	normalizedModel := strings.ToLower(strings.TrimSpace(modelID))
	for _, model := range botModelCatalog {
		if model.ID != normalizedModel {
			continue
		}
		value := strings.ToLower(strings.TrimSpace(reasoning))
		if len(model.ReasoningEfforts) == 0 {
			if value != "" && value != "default" {
				return botModelCatalogItem{}, "", false
			}
			return model, "", true
		}
		if value == "" || value == "default" {
			value = model.DefaultReasoningEffort
		}
		for _, option := range model.ReasoningEfforts {
			if option == value {
				return model, value, true
			}
		}
		return botModelCatalogItem{}, "", false
	}
	return botModelCatalogItem{}, "", false
}

// catalogContextWindowTokens returns the standard context window for a catalog
// model id. It is the authoritative cloud-side value so devices never need to
// guess it from a local profile; catalog ids that are not in the catalog (e.g.
// legacy aliases) fall back to the callers' defaults.
func catalogContextWindowTokens(modelID string) (int64, bool) {
	normalized := strings.ToLower(strings.TrimSpace(modelID))
	for _, model := range botModelCatalog {
		if model.ID == normalized && model.ContextWindowTokens > 0 {
			return model.ContextWindowTokens, true
		}
	}
	return 0, false
}

func botModelConfigIsConfigured(config *types.BotModelConfig) bool {
	return config != nil &&
		strings.TrimSpace(config.Kind) != botModelKindLocal &&
		strings.TrimSpace(config.ModelID) != "" &&
		strings.TrimSpace(config.ModelID) != botModelKindLocal
}

func botModelRuntimeSupported(config *types.BotModelConfig) bool {
	return config != nil && config.RuntimeProtocol == botModelRuntimeProtocol
}

func botModelConfigResponse(botUID int64, config *types.BotModelConfig) map[string]interface{} {
	configured := botModelConfigIsConfigured(config)
	config = botModelConfigWithDefaults(config)
	desiredKind := config.Kind
	desiredModelID := config.ModelID
	desiredReasoning := config.ReasoningEffort
	if !configured {
		desiredKind = "local"
		desiredModelID = "local"
		desiredReasoning = ""
	}
	status := "local"
	localHandoff := !configured && config.Revision > 0 &&
		!(config.AppliedRevision == config.Revision && config.AppliedKind == "local" && config.AppliedModelID == "local")
	if (configured || localHandoff) && config.LastAttemptRevision == config.Revision && config.LastError != "" {
		status = "failed"
	} else if configured && config.AppliedRevision == config.Revision && config.AppliedKind == config.Kind && config.AppliedModelID == config.ModelID {
		status = "applied"
	} else if configured || localHandoff {
		status = "pending"
	}
	response := map[string]interface{}{
		"uid":        botUID,
		"configured": configured,
		"desired":    desiredModelConfigResponse(desiredKind, desiredModelID, desiredReasoning, config),
		"applied": map[string]interface{}{
			"kind": config.AppliedKind, "model_id": config.AppliedModelID, "reasoning_effort": config.AppliedReasoning,
			"revision": config.AppliedRevision, "applied_at": config.AppliedAt,
		},
		"status":     status,
		"last_error": config.LastError,
		"apply_mode": "runtime_reload",
	}
	return response
}

// desiredModelConfigResponse builds the desired model selection payload. For
// catalog models it includes the authoritative cloud context window so the
// device does not rely on a local profile that can drift from the catalog.
func desiredModelConfigResponse(kind, modelID, reasoning string, config *types.BotModelConfig) map[string]interface{} {
	desired := map[string]interface{}{
		"kind": kind, "model_id": modelID, "reasoning_effort": reasoning,
		"revision": config.Revision, "updated_at": config.UpdatedAt,
	}
	if kind == botModelKindCatalog {
		if tokens, ok := catalogContextWindowTokens(modelID); ok {
			desired["context_window_tokens"] = tokens
		}
	}
	return desired
}

func (h *BotModelConfigHandler) ownerConfigResponse(
	ctx context.Context,
	ownerUID, botUID int64,
	config *types.BotModelConfig,
	includeUsage bool,
) map[string]interface{} {
	response := botModelConfigResponse(botUID, config)
	if lastError, _ := response["last_error"].(string); strings.TrimSpace(lastError) != "" {
		response["last_error"] = "模型配置应用失败"
	}
	response["management_enabled"] = h.managementEnabled(ownerUID)
	response["runtime_supported"] = botModelRuntimeSupported(config)
	if !botModelRuntimeSupported(config) {
		response["runtime_unavailable_reason"] = botModelRuntimeUnavailableReason
	}
	currentCatalogModelID := ""
	normalized := botModelConfigWithDefaults(config)
	if botModelConfigIsConfigured(config) && normalized.Kind == botModelKindCatalog {
		currentCatalogModelID = normalized.ModelID
	}
	catalog, quotaError := h.catalogWithUsageForCurrent(ctx, ownerUID, includeUsage, currentCatalogModelID)
	response["models"] = catalog
	response["custom_supported"] = h.secretCodec != nil
	if quotaError != "" {
		response["quota_error"] = quotaError
	}
	if normalized.CustomCiphertext != "" {
		custom, err := h.decryptCustomModel(botUID, normalized.CustomCiphertext)
		if err != nil {
			response["custom_unavailable_reason"] = "已保存的自定义模型凭证暂时无法读取，请重新填写后保存"
		} else {
			response["custom"] = ownerCustomModelConfig{
				Protocol:            custom.Protocol,
				APIBase:             custom.APIBase,
				Model:               custom.Model,
				APIKeyConfigured:    custom.APIKey != "",
				APIKeyHint:          secretHint(custom.APIKey),
				ContextWindowTokens: custom.ContextWindowTokens,
				Temperature:         custom.Temperature,
				ReasoningEffort:     custom.ReasoningEffort,
			}
		}
	}
	if h.secretCodecError != nil {
		response["custom_unavailable_reason"] = "服务端尚未配置自定义模型密钥加密"
	}
	return response
}

func (h *BotModelConfigHandler) runtimeConfigResponse(botUID int64, config *types.BotModelConfig) (map[string]interface{}, error) {
	response := botModelConfigResponse(botUID, config)
	normalized := botModelConfigWithDefaults(config)
	if !botModelConfigIsConfigured(config) || normalized.Kind != botModelKindCustom {
		return response, nil
	}
	custom, err := h.decryptCustomModel(botUID, normalized.CustomCiphertext)
	if err != nil {
		return nil, err
	}
	desired, _ := response["desired"].(map[string]interface{})
	desired["custom"] = custom
	return response, nil
}

func (h *BotModelConfigHandler) catalogWithUsage(ctx context.Context, ownerUID int64, includeUsage bool) ([]botModelCatalogItem, string) {
	return h.catalogWithUsageForCurrent(ctx, ownerUID, includeUsage, "")
}

func (h *BotModelConfigHandler) catalogWithUsageForCurrent(
	ctx context.Context,
	ownerUID int64,
	includeUsage bool,
	currentModelID string,
) ([]botModelCatalogItem, string) {
	catalog := make([]botModelCatalogItem, len(botModelCatalog))
	copy(catalog, botModelCatalog)
	for i := range catalog {
		catalog[i].Available = true
		catalog[i].UnavailableReason = ""
		catalog[i].Quota = nil
		catalog[i].Runtime = catalogRuntimeDescriptor(catalog[i])
	}

	var commercialSummary *types.CommercialSummary
	if h.commercialQuotaEnforced(ownerUID) {
		var summaryErr error
		commercialSummary, summaryErr = h.commercialStore.GetCommercialSummary(ownerUID)
		if summaryErr != nil {
			return catalogWithUnavailableCurrent(catalog, currentModelID, "套餐额度暂时无法确认"), "套餐共享额度暂时无法同步"
		}
		if commercialSummaryHasQuota(commercialSummary) {
			catalog = catalogForCommercialSummary(catalog, commercialSummary, currentModelID)
		}
	}
	if !includeUsage {
		return catalog, ""
	}
	if h.relayAdmin == nil {
		return catalog, "额度暂时无法同步"
	}
	user, err := fetchRelayUsageForUID(ctx, h.relayAdmin, ownerUID)
	if err != nil {
		return catalog, "额度暂时无法同步"
	}
	if h.commercialQuotaEnforced(ownerUID) {
		if !commercialSummaryHasQuota(commercialSummary) {
			for i := range catalog {
				usage := buildRelayUsageResponse(user, catalog[i].ID)
				catalog[i].Quota = usage.Summary
			}
			return catalog, "套餐共享额度迁移中，暂按原额度显示"
		}
		if user == nil || user.Limits.MonthlyBudget.MaxLimit <= 0 {
			return catalog, "套餐共享额度同步中"
		}
		for i := range catalog {
			if catalog[i].Available {
				catalog[i].Quota = buildRelaySharedUsageResponse(user, catalog[i].ID).Summary
			}
		}
		return catalog, ""
	}
	for i := range catalog {
		usage := buildRelayUsageResponse(user, catalog[i].ID)
		catalog[i].Quota = usage.Summary
	}
	return catalog, ""
}

func catalogRuntimeDescriptor(model botModelCatalogItem) *botModelRuntimeDescriptor {
	provider := strings.ToLower(strings.TrimSpace(model.Provider))
	if provider != "anthropic" && provider != "openai" || model.ID == "" || model.ContextWindowTokens <= 0 {
		return nil
	}
	// Relay-facing names are explicit because some providers use case-sensitive
	// names (for example MiniMax-M3) even when catalog IDs are stable aliases.
	modelName := strings.TrimSpace(model.RuntimeModel)
	if modelName == "" {
		modelName = model.ID
	}
	d := &botModelRuntimeDescriptor{
		CatalogModelID: model.ID, Model: modelName, Provider: provider, ContextWindowTokens: model.ContextWindowTokens,
		Vision: model.Vision, ToolCalling: true, Streaming: true,
	}
	if provider == "openai" {
		d.OpenAIAPIMode = "responses"
	}
	return d
}

func (h *BotModelConfigHandler) catalogModelAllowed(ownerUID int64, modelID string) (bool, error) {
	if !h.commercialQuotaEnforced(ownerUID) {
		return true, nil
	}
	summary, err := h.commercialStore.GetCommercialSummary(ownerUID)
	if err != nil {
		return false, err
	}
	if !commercialSummaryHasQuota(summary) {
		return true, nil
	}
	return commercialQuotaModelAllowed(summary, modelID), nil
}

func catalogForCommercialSummary(
	catalog []botModelCatalogItem,
	summary *types.CommercialSummary,
	currentModelID string,
) []botModelCatalogItem {
	current := normalizeRelayModelName(currentModelID)
	filtered := make([]botModelCatalogItem, 0, len(catalog))
	for _, item := range catalog {
		if commercialQuotaModelAllowed(summary, item.ID) {
			filtered = append(filtered, item)
			continue
		}
		if current != "" && normalizeRelayModelName(item.ID) == current {
			item.Available = false
			item.UnavailableReason = "当前套餐已不包含该模型，切换后不可再选"
			filtered = append(filtered, item)
		}
	}
	return filtered
}

func catalogWithUnavailableCurrent(
	catalog []botModelCatalogItem,
	currentModelID, reason string,
) []botModelCatalogItem {
	current := normalizeRelayModelName(currentModelID)
	if current == "" {
		return nil
	}
	for _, item := range catalog {
		if normalizeRelayModelName(item.ID) != current {
			continue
		}
		item.Available = false
		item.UnavailableReason = reason
		return []botModelCatalogItem{item}
	}
	return nil
}

func (h *BotModelConfigHandler) prepareCustomModelUpdate(
	botUID int64,
	input *cloudCustomModelConfig,
	stored *types.BotModelConfig,
) (*cloudCustomModelConfig, string, error) {
	if h.secretCodec == nil {
		return nil, "", fmt.Errorf("%w: %v", errBotModelEncryptionUnavailable, h.secretCodecError)
	}
	if input == nil {
		return nil, "", errors.New("custom model configuration is required")
	}
	custom := *input
	custom.Protocol = strings.ToLower(strings.TrimSpace(custom.Protocol))
	custom.APIBase = strings.TrimRight(strings.TrimSpace(custom.APIBase), "/")
	custom.Model = strings.TrimSpace(custom.Model)
	custom.APIKey = strings.TrimSpace(custom.APIKey)
	custom.ReasoningEffort = strings.ToLower(strings.TrimSpace(custom.ReasoningEffort))
	// Token limits are server-managed: max_tokens is never accepted from the
	// owner-facing API. The owner may set the context window explicitly
	// (validated below); otherwise default to 128K or preserve the stored value.
	custom.MaxTokens = 0
	if stored != nil && stored.CustomCiphertext != "" {
		previous, err := h.decryptCustomModel(botUID, stored.CustomCiphertext)
		if err != nil {
			// 只有“密文完全无法读取”（损坏/密钥不匹配）时才允许用全新的
			// api key 覆盖恢复；stored 配置能解密但校验失败属于数据完整性问题，
			// 仍然拒绝更新。
			if custom.APIKey == "" || !errors.Is(err, errBotModelCiphertextUnreadable) {
				return nil, "", err
			}
			log.Printf("bot model: stored custom ciphertext could not be read; using the provided fresh key: %v", err)
		} else {
			if custom.APIKey == "" {
				custom.APIKey = previous.APIKey
			}
			if custom.ContextWindowTokens <= 0 {
				custom.ContextWindowTokens = previous.ContextWindowTokens
			}
			if custom.Temperature == nil {
				custom.Temperature = previous.Temperature
			}
			custom.MaxTokens = previous.MaxTokens
		}
	}
	if custom.ContextWindowTokens <= 0 {
		custom.ContextWindowTokens = 128000
	}
	if err := validateCloudCustomModel(&custom); err != nil {
		return nil, "", err
	}
	plaintext, err := json.Marshal(custom)
	if err != nil {
		return nil, "", errors.New("failed to encode custom model configuration")
	}
	ciphertext, err := h.secretCodec.encrypt(botUID, plaintext)
	if err != nil {
		return nil, "", fmt.Errorf("%w: %v", errBotModelEncryptionUnavailable, err)
	}
	return &custom, ciphertext, nil
}

func (h *BotModelConfigHandler) decryptCustomModel(botUID int64, ciphertext string) (*cloudCustomModelConfig, error) {
	if h.secretCodec == nil {
		return nil, fmt.Errorf("%w: %v", errBotModelEncryptionUnavailable, h.secretCodecError)
	}
	plaintext, err := h.secretCodec.decrypt(botUID, ciphertext)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errBotModelCiphertextUnreadable, err)
	}
	var custom cloudCustomModelConfig
	if err := json.Unmarshal(plaintext, &custom); err != nil {
		return nil, fmt.Errorf("%w: invalid encrypted custom model configuration", errBotModelCiphertextUnreadable)
	}
	if err := validateCloudCustomModel(&custom); err != nil {
		return nil, fmt.Errorf("stored custom model configuration is invalid: %w", err)
	}
	return &custom, nil
}

func validateCloudCustomModel(custom *cloudCustomModelConfig) error {
	if custom == nil {
		return errors.New("custom model configuration is required")
	}
	switch custom.Protocol {
	case "anthropic", "openai-chat-completions", "openai-responses":
	default:
		return errors.New("unsupported custom model protocol")
	}
	parsed, err := url.Parse(custom.APIBase)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil || parsed.Fragment != "" {
		return errors.New("custom model API base must be a valid HTTP(S) URL")
	}
	if len(custom.APIBase) > 2048 || custom.Model == "" || len(custom.Model) > 200 {
		return errors.New("custom model API base or model name is invalid")
	}
	if custom.APIKey == "" || len(custom.APIKey) > 4096 {
		return errors.New("custom model API key is required")
	}
	if custom.ContextWindowTokens < 1024 || custom.ContextWindowTokens > 4000000 {
		return errors.New("custom model context window must be between 1024 and 4000000 tokens")
	}
	if custom.MaxTokens < 0 || custom.MaxTokens > 1000000 {
		return errors.New("custom model max tokens is invalid")
	}
	if custom.Temperature != nil && (*custom.Temperature < 0 || *custom.Temperature > 2) {
		return errors.New("custom model temperature must be between 0 and 2")
	}
	if !validCustomReasoningEffort(custom.ReasoningEffort) {
		return errors.New("unsupported custom model reasoning effort")
	}
	return nil
}

func validCustomReasoningEffort(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "default", "none", "minimal", "low", "medium", "high", "xhigh", "max", "disabled":
		return true
	default:
		return false
	}
}

func secretHint(secret string) string {
	secret = strings.TrimSpace(secret)
	if len(secret) <= 4 {
		return "****"
	}
	return "****" + secret[len(secret)-4:]
}
