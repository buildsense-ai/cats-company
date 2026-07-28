package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

const testSkillHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func newBotSkillDefinitionTestHandler() (*BotDefinitionHandler, *botDefinitionTestStore) {
	db := &botDefinitionTestStore{
		owners: map[int64]int64{43: 7},
		records: map[int64]*types.BotDefinitionRecord{
			43: {
				Definition: types.BotDefinition{
					Schema: types.BotDefinitionSchema,
					BotID:  "43",
					Model:  types.BotDefinitionModel{Kind: "catalog", ModelID: "minimax-m3"},
					Prompt: &types.BotPromptDefinition{Selected: "default"},
					Skills: []types.BotSkillRef{},
				},
				Runtime: types.BotDefinitionRuntime{DesiredRevision: 2},
				Exists:  true,
			},
		},
	}
	models := &botModelConfigTestStore{owners: db.owners, models: map[int64]*types.BotModelConfig{}}
	return NewBotDefinitionHandler(db, db, models, NewBotModelConfigHandler(db, models)), db
}

func TestBotDefinitionSkillsUseUnifiedRevisionAndCanonicalOrder(t *testing.T) {
	handler, db := newBotSkillDefinitionTestHandler()
	req := httptest.NewRequest(http.MethodPatch, "/api/bot/definition/skills", strings.NewReader(
		`{"revision":2,"skills":[`+
			`{"source":"skillhub","skillId":" z-skill ","version":" private-2 ","contentHash":"`+testSkillHash+`"},`+
			`{"source":"SKILLHUB","skillId":"a-skill","version":"1.0.0","contentHash":"`+testSkillHash+`"}]}`,
	))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(43)))
	rec := httptest.NewRecorder()
	handler.HandleRuntimeSkills(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"revision":3`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	want := []types.BotSkillRef{
		{Source: "skillhub", SkillID: "a-skill", Version: "1.0.0", ContentHash: testSkillHash},
		{Source: "skillhub", SkillID: "z-skill", Version: "private-2", ContentHash: testSkillHash},
	}
	if !reflect.DeepEqual(db.records[43].Definition.Skills, want) {
		t.Fatalf("skills=%+v want=%+v", db.records[43].Definition.Skills, want)
	}
	if db.records[43].Definition.Model.ModelID != "minimax-m3" ||
		db.records[43].Definition.Prompt == nil {
		t.Fatalf("non-skill fields changed: %+v", db.records[43].Definition)
	}
}

func TestBotDefinitionSkillsRejectStaleRevisionAndInvalidRefs(t *testing.T) {
	handler, _ := newBotSkillDefinitionTestHandler()
	cases := []struct {
		name   string
		body   string
		status int
	}{
		{name: "missing revision", body: `{"skills":[]}`, status: http.StatusBadRequest},
		{name: "stale revision", body: `{"revision":1,"skills":[]}`, status: http.StatusConflict},
		{name: "missing skills", body: `{"revision":2}`, status: http.StatusBadRequest},
		{name: "unknown field", body: `{"revision":2,"skills":[],"model":{}}`, status: http.StatusBadRequest},
		{name: "unsafe id", body: `{"revision":2,"skills":[{"source":"skillhub","skillId":"../a","version":"1","contentHash":"` + testSkillHash + `"}]}`, status: http.StatusBadRequest},
		{name: "bad hash", body: `{"revision":2,"skills":[{"source":"skillhub","skillId":"a","version":"1","contentHash":"sha256:` + testSkillHash + `"}]}`, status: http.StatusBadRequest},
		{name: "duplicate id", body: `{"revision":2,"skills":[{"source":"skillhub","skillId":"a","version":"1","contentHash":"` + testSkillHash + `"},{"source":"skillhub","skillId":"a","version":"2","contentHash":"` + testSkillHash + `"}]}`, status: http.StatusBadRequest},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPatch, "/api/bot/definition/skills", strings.NewReader(tc.body))
			req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(43)))
			rec := httptest.NewRecorder()
			handler.HandleRuntimeSkills(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status=%d body=%s want=%d", rec.Code, rec.Body.String(), tc.status)
			}
		})
	}
}

func TestBotDefinitionSkillsOwnerAndRuntimeScope(t *testing.T) {
	handler, db := newBotSkillDefinitionTestHandler()
	friend := httptest.NewRequest(http.MethodGet, "/api/bots/definition/skills?uid=43", nil)
	friend = friend.WithContext(context.WithValue(friend.Context(), uidKey, int64(8)))
	friendRec := httptest.NewRecorder()
	handler.HandleOwnerSkills(friendRec, friend)
	if friendRec.Code != http.StatusForbidden {
		t.Fatalf("friend status=%d body=%s", friendRec.Code, friendRec.Body.String())
	}

	runtime := httptest.NewRequest(http.MethodPatch, "/api/bot/definition/skills?uid=99", strings.NewReader(
		`{"revision":2,"skills":[]}`,
	))
	runtime = runtime.WithContext(context.WithValue(runtime.Context(), uidKey, int64(43)))
	runtimeRec := httptest.NewRecorder()
	handler.HandleRuntimeSkills(runtimeRec, runtime)
	if runtimeRec.Code != http.StatusOK {
		t.Fatalf("runtime status=%d body=%s", runtimeRec.Code, runtimeRec.Body.String())
	}
	if db.records[99] != nil || db.records[43].Runtime.DesiredRevision != 2 {
		t.Fatalf("runtime query uid escaped scope: records=%+v", db.records)
	}
}

func TestFullBotDefinitionResponseIncludesSkills(t *testing.T) {
	handler, db := newBotSkillDefinitionTestHandler()
	db.records[43].Definition.Skills = []types.BotSkillRef{{
		Source: "skillhub", SkillID: "a", Version: "1", ContentHash: testSkillHash,
	}}
	req := httptest.NewRequest(http.MethodGet, "/api/bot/definition", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(43)))
	rec := httptest.NewRecorder()
	handler.HandleRuntimeDefinition(rec, req)
	if rec.Code != http.StatusOK ||
		!strings.Contains(rec.Body.String(), `"skills":[{"source":"skillhub"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestBotDefinitionSkillsNoopKeepsUnifiedRevision(t *testing.T) {
	handler, db := newBotSkillDefinitionTestHandler()
	db.records[43].Definition.Skills = []types.BotSkillRef{{
		Source: "skillhub", SkillID: "a", Version: "1", ContentHash: testSkillHash,
	}}
	req := httptest.NewRequest(http.MethodPatch, "/api/bot/definition/skills", strings.NewReader(
		`{"revision":2,"skills":[{"source":"skillhub","skillId":"a","version":"1","contentHash":"`+
			testSkillHash+`"}]}`,
	))
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(43)))
	rec := httptest.NewRecorder()
	handler.HandleRuntimeSkills(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"revision":2`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}
