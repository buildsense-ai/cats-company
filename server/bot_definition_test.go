package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type botDefinitionMemoryStore struct {
	snapshot *types.BotDefinitionSnapshot
}

func (s *botDefinitionMemoryStore) GetBotDefinition(_ int64) (*types.BotDefinitionSnapshot, error) {
	if s.snapshot == nil {
		return nil, store.ErrBotDefinitionNotFound
	}
	return cloneBotDefinitionSnapshot(s.snapshot), nil
}

func (s *botDefinitionMemoryStore) CreateBotDefinition(
	_ int64,
	skills []types.BotSkillRef,
) (*types.BotDefinitionSnapshot, error) {
	if s.snapshot != nil && s.snapshot.Skills != nil {
		return nil, store.ErrBotDefinitionAlreadyExists
	}
	model := &types.BotModelConfig{Kind: "catalog", ModelID: "minimax-m3", Revision: 3}
	if s.snapshot != nil && s.snapshot.Model != nil {
		model = s.snapshot.Model
	}
	s.snapshot = &types.BotDefinitionSnapshot{
		Model: model,
		Skills: &types.BotDefinitionSkillsState{
			Schema: store.BotDefinitionSchema, Skills: append([]types.BotSkillRef{}, skills...),
			Revision: 1, UpdatedAt: "2026-07-24T00:00:00Z",
		},
	}
	return cloneBotDefinitionSnapshot(s.snapshot), nil
}

func (s *botDefinitionMemoryStore) UpdateBotDefinition(
	_ int64,
	expectedModelRevision, expectedSkillsRevision int64,
	skills []types.BotSkillRef,
) (*types.BotDefinitionSnapshot, error) {
	if s.snapshot == nil {
		return nil, store.ErrBotDefinitionNotFound
	}
	skillsRevision := int64(0)
	if s.snapshot.Skills != nil {
		skillsRevision = s.snapshot.Skills.Revision
	}
	if s.snapshot.Model.Revision != expectedModelRevision || skillsRevision != expectedSkillsRevision {
		return nil, store.ErrStaleBotDefinitionRevision
	}
	if s.snapshot.Skills == nil {
		s.snapshot.Skills = &types.BotDefinitionSkillsState{
			Schema: store.BotDefinitionSchema, Revision: 1, UpdatedAt: "2026-07-24T00:00:01Z",
		}
	} else {
		s.snapshot.Skills.Revision++
		s.snapshot.Skills.UpdatedAt = "2026-07-24T00:00:01Z"
	}
	s.snapshot.Skills.Skills = append([]types.BotSkillRef{}, skills...)
	return cloneBotDefinitionSnapshot(s.snapshot), nil
}

func cloneBotDefinitionSnapshot(input *types.BotDefinitionSnapshot) *types.BotDefinitionSnapshot {
	if input == nil {
		return nil
	}
	output := &types.BotDefinitionSnapshot{}
	if input.Model != nil {
		model := *input.Model
		output.Model = &model
	}
	if input.Skills != nil {
		skills := *input.Skills
		skills.Skills = append([]types.BotSkillRef(nil), input.Skills.Skills...)
		output.Skills = &skills
	}
	return output
}

func TestBotDefinitionHandlerLifecycleUsesStrongETag(t *testing.T) {
	definitions := &botDefinitionMemoryStore{
		snapshot: &types.BotDefinitionSnapshot{
			Model: &types.BotModelConfig{Kind: "catalog", ModelID: "minimax-m3", Revision: 3},
		},
	}
	handler := NewBotDefinitionHandler(NewBotModelConfigHandler(nil, nil), definitions)

	rec := performBotDefinitionRequest(handler, http.MethodGet, "", "", "")
	if rec.Code != http.StatusOK || rec.Header().Get("Cache-Control") != "no-store" ||
		rec.Header().Get("ETag") != `"bot-definition-42-m3-s0"` ||
		strings.Contains(rec.Body.String(), `"skills"`) {
		t.Fatalf("initial GET status=%d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}

	rec = performBotDefinitionRequest(handler, http.MethodPut, `{"skills":[]}`, "", "")
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("PUT without precondition status=%d body=%s", rec.Code, rec.Body.String())
	}

	body := `{"skills":[{"skillId":"lin/agent-browser","version":"1.0.3"}]}`
	rec = performBotDefinitionRequest(handler, http.MethodPut, body, "", "*")
	if rec.Code != http.StatusCreated {
		t.Fatalf("PUT status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("ETag"); got != `"bot-definition-42-m3-s1"` {
		t.Fatalf("PUT ETag=%q", got)
	}
	var created botDefinitionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	model, ok := created.Model.(map[string]interface{})
	if !ok || created.BotID != "42" || created.Schema != store.BotDefinitionSchema ||
		model["modelId"] != "minimax-m3" || created.Skills == nil || len(*created.Skills) != 1 {
		t.Fatalf("created=%+v", created)
	}

	rec = performBotDefinitionRequest(handler, http.MethodPut, body, "", "*")
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("second PUT status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = performBotDefinitionRequest(handler, http.MethodPatch, `{"skills":[]}`, "", "")
	if rec.Code != http.StatusPreconditionRequired {
		t.Fatalf("PATCH without precondition status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = performBotDefinitionRequest(
		handler,
		http.MethodPatch,
		`{"skills":[],"model":{"kind":"catalog"}}`,
		`"bot-definition-42-m3-s1"`,
		"",
	)
	if rec.Code != http.StatusBadRequest || definitions.snapshot.Skills.Revision != 1 {
		t.Fatalf("PATCH with model status=%d revision=%d body=%s", rec.Code, definitions.snapshot.Skills.Revision, rec.Body.String())
	}

	rec = performBotDefinitionRequest(handler, http.MethodPatch, `{"skills":[]}`, `W/"bot-definition-42-m3-s1"`, "")
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("weak PATCH status=%d body=%s", rec.Code, rec.Body.String())
	}

	definitions.snapshot.Model.Revision = 4
	rec = performBotDefinitionRequest(handler, http.MethodPatch, `{"skills":[]}`, `"bot-definition-42-m3-s1"`, "")
	if rec.Code != http.StatusPreconditionFailed {
		t.Fatalf("stale model PATCH status=%d body=%s", rec.Code, rec.Body.String())
	}

	rec = performBotDefinitionRequest(handler, http.MethodPatch, `{"skills":[]}`, `"bot-definition-42-m4-s1"`, "")
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"bot-definition-42-m4-s2"` {
		t.Fatalf("PATCH status=%d etag=%q body=%s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}

	rec = performBotDefinitionRequest(handler, http.MethodGet, "", "", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"skills":[]`) {
		t.Fatalf("final GET status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPatchMigratesLegacyBotDefinitionFromRevisionZero(t *testing.T) {
	definitions := &botDefinitionMemoryStore{
		snapshot: &types.BotDefinitionSnapshot{
			Model: &types.BotModelConfig{Kind: "catalog", ModelID: "minimax-m3", Revision: 3},
		},
	}
	handler := NewBotDefinitionHandler(NewBotModelConfigHandler(nil, nil), definitions)
	rec := performBotDefinitionRequest(
		handler,
		http.MethodPatch,
		`{"skills":[{"skillId":"lin/agent-browser","version":"1.0.3"}]}`,
		`"bot-definition-42-m3-s0"`,
		"",
	)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"bot-definition-42-m3-s1"` {
		t.Fatalf("PATCH status=%d etag=%q body=%s", rec.Code, rec.Header().Get("ETag"), rec.Body.String())
	}
	if definitions.snapshot.Skills == nil || definitions.snapshot.Skills.Revision != 1 ||
		len(definitions.snapshot.Skills.Skills) != 1 {
		t.Fatalf("snapshot=%+v", definitions.snapshot)
	}
}

func TestBotDefinitionHandlerRejectsInvalidSkillsRequests(t *testing.T) {
	tooMany := make([]types.BotSkillRef, maxBotDefinitionSkills+1)
	for index := range tooMany {
		tooMany[index] = types.BotSkillRef{SkillID: "skill-" + strconv.Itoa(index), Version: "1"}
	}
	tooManyBody, err := json.Marshal(map[string]interface{}{"skills": tooMany})
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]string{
		"missing":        `{}`,
		"null":           `{"skills":null}`,
		"unknown field":  `{"skills":[],"model":{}}`,
		"trailing value": `{"skills":[]} {}`,
		"duplicate":      `{"skills":[{"skillId":"a","version":"1"},{"skillId":"a","version":"2"}]}`,
		"blank id":       `{"skills":[{"skillId":" ","version":"1"}]}`,
		"control":        "{\"skills\":[{\"skillId\":\"bad\\u0000id\",\"version\":\"1\"}]}",
		"blank version":  `{"skills":[{"skillId":"a","version":""}]}`,
		"too many":       string(tooManyBody),
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			store := &botDefinitionMemoryStore{
				snapshot: &types.BotDefinitionSnapshot{
					Model: &types.BotModelConfig{Kind: "catalog", ModelID: "minimax-m3"},
				},
			}
			handler := NewBotDefinitionHandler(NewBotModelConfigHandler(nil, nil), store)
			rec := performBotDefinitionRequest(handler, http.MethodPut, body, "", "*")
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if store.snapshot.Skills != nil {
				t.Fatal("invalid request mutated the store")
			}
		})
	}
}

func TestBotDefinitionHandlerComposesEncryptedCustomModel(t *testing.T) {
	t.Setenv(botModelEncryptionKeyEnv, strings.Repeat("11", 32))
	models := NewBotModelConfigHandler(nil, nil)
	custom := cloudCustomModelConfig{
		Protocol: "anthropic", APIBase: "https://models.example.test", Model: "private-model",
		APIKey: "secret", ContextWindowTokens: 128000,
	}
	raw, err := json.Marshal(custom)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, err := models.secretCodec.encrypt(42, raw)
	if err != nil {
		t.Fatal(err)
	}
	definitions := &botDefinitionMemoryStore{
		snapshot: &types.BotDefinitionSnapshot{
			Model: &types.BotModelConfig{
				Kind: "custom", ModelID: "private-model", CustomCiphertext: ciphertext, Revision: 5,
			},
			Skills: &types.BotDefinitionSkillsState{
				Schema: store.BotDefinitionSchema, Skills: []types.BotSkillRef{}, Revision: 2,
			},
		},
	}
	rec := performBotDefinitionRequest(NewBotDefinitionHandler(models, definitions), http.MethodGet, "", "", "")
	if rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"apiKey":"secret"`) ||
		!strings.Contains(rec.Body.String(), `"contextWindowTokens":128000`) {
		t.Fatalf("GET status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(string(mustJSONMarshal(t, definitions.snapshot)), "secret") {
		t.Fatal("custom model secret was copied into the definition skills state")
	}
}

func TestBotDefinitionHandlerUsesAuthenticatedBotIdentity(t *testing.T) {
	const apiKey = "cc_2a_test"
	authStore := authStateTestStore{
		users: map[int64]*types.User{
			42: {ID: 42, Username: "definition-bot", AccountType: types.AccountBot, State: 0},
		},
		botKeys: map[string]int64{apiKey: 42},
	}
	definitions := &botDefinitionMemoryStore{
		snapshot: &types.BotDefinitionSnapshot{
			Model: &types.BotModelConfig{Kind: "catalog", ModelID: "minimax-m3", Revision: 1},
			Skills: &types.BotDefinitionSkillsState{
				Schema: store.BotDefinitionSchema, Skills: []types.BotSkillRef{}, Revision: 1,
			},
		},
	}
	wrapped := BotAPIKeyMiddlewareWithDB(authStore)(
		NewBotDefinitionHandler(NewBotModelConfigHandler(nil, nil), definitions).Handle,
	)

	unauthorized := httptest.NewRecorder()
	wrapped(unauthorized, httptest.NewRequest(http.MethodGet, "/api/bot/definition", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}

	authorizedRequest := httptest.NewRequest(http.MethodGet, "/api/bot/definition", nil)
	authorizedRequest.Header.Set("Authorization", "ApiKey "+apiKey)
	authorized := httptest.NewRecorder()
	wrapped(authorized, authorizedRequest)
	if authorized.Code != http.StatusOK || !strings.Contains(authorized.Body.String(), `"botId":"42"`) {
		t.Fatalf("authorized status=%d body=%s", authorized.Code, authorized.Body.String())
	}
}

func performBotDefinitionRequest(
	handler *BotDefinitionHandler,
	method, body, ifMatch, ifNoneMatch string,
) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, "/api/bot/definition", strings.NewReader(body))
	request = request.WithContext(context.WithValue(request.Context(), uidKey, int64(42)))
	if ifMatch != "" {
		request.Header.Set("If-Match", ifMatch)
	}
	if ifNoneMatch != "" {
		request.Header.Set("If-None-Match", ifNoneMatch)
	}
	recorder := httptest.NewRecorder()
	handler.Handle(recorder, request)
	return recorder
}

func mustJSONMarshal(t *testing.T, value interface{}) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

var _ store.BotDefinitionStore = (*botDefinitionMemoryStore)(nil)
