package server

import (
	"context"
	"errors"
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
		configs: map[int64]*types.BotConfig{
			43: {UserID: 43, OwnerID: 7, SkillsVisibility: types.BotSkillsOwner},
		},
		friends: map[[2]int64]bool{},
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

func TestBotDefinitionViewerSkillsRespectVisibilityAndRedactDefinition(t *testing.T) {
	tests := []struct {
		name       string
		viewerUID  int64
		visibility types.BotSkillsVisibility
		friend     bool
		wantStatus int
	}{
		{name: "owner", viewerUID: 7, visibility: types.BotSkillsOwner, wantStatus: http.StatusOK},
		{name: "authorized friend", viewerUID: 8, visibility: types.BotSkillsAuthorized, friend: true, wantStatus: http.StatusOK},
		{name: "authorized non-friend", viewerUID: 8, visibility: types.BotSkillsAuthorized, wantStatus: http.StatusForbidden},
		{name: "public", viewerUID: 8, visibility: types.BotSkillsPublic, wantStatus: http.StatusOK},
		{name: "owner only friend still sees safe metadata", viewerUID: 8, visibility: types.BotSkillsOwner, friend: true, wantStatus: http.StatusOK},
		{name: "owner only stranger", viewerUID: 8, visibility: types.BotSkillsOwner, wantStatus: http.StatusForbidden},
		{name: "missing legacy value stranger", viewerUID: 8, visibility: "", wantStatus: http.StatusForbidden},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			handler, db := newBotSkillDefinitionTestHandler()
			db.configs[43].SkillsVisibility = tc.visibility
			db.records[43].Definition.Skills = []types.BotSkillRef{{
				Source: "skillhub", SkillID: "catsco/example", Version: "1.0.0", ContentHash: testSkillHash,
			}}
			if tc.friend {
				db.friends[[2]int64{tc.viewerUID, 43}] = true
			}
			req := httptest.NewRequest(http.MethodGet, "/api/agents/skills?uid=43", nil)
			req = req.WithContext(context.WithValue(req.Context(), uidKey, tc.viewerUID))
			rec := httptest.NewRecorder()

			handler.HandleViewerSkills(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if tc.wantStatus == http.StatusOK {
				body := rec.Body.String()
				if !strings.Contains(body, `"skillId":"catsco/example"`) || strings.Contains(body, "contentHash") || strings.Contains(body, "revision") {
					t.Fatalf("viewer response was not redacted: %s", body)
				}
			} else if !strings.Contains(rec.Body.String(), "Agent 所有者未公开技能列表") {
				t.Fatalf("missing private-state message: %s", rec.Body.String())
			}
		})
	}
}

func TestBotDefinitionSkillsResolvePrivateDisplayNamesWithoutExposingPackageData(t *testing.T) {
	handler, db := newBotSkillDefinitionTestHandler()
	db.records[43].Definition.Skills = []types.BotSkillRef{{
		Source: "skillhub", SkillID: "priv_owned", Version: "v_1", ContentHash: testSkillHash,
	}}
	handler.SetSkillMetadataResolver(func(_ context.Context, botUID int64, skills []types.BotSkillRef) (map[string]BotSkillDisplayMetadata, error) {
		if botUID != 43 || len(skills) != 1 || skills[0].SkillID != "priv_owned" {
			t.Fatalf("unexpected metadata scope bot=%d skills=%+v", botUID, skills)
		}
		return map[string]BotSkillDisplayMetadata{botSkillMetadataKey("priv_owned", "v_1"): {
			DisplayName: "cloud-html-artifact", RevisionNumber: 3, LastChangedBy: "lin",
			LastChangedAt: "2026-08-22T02:03:04Z", ChangeSource: "conversation_mutation",
		}}, nil
	})

	owner := httptest.NewRequest(http.MethodGet, "/api/bots/definition/skills?uid=43", nil)
	owner = owner.WithContext(context.WithValue(owner.Context(), uidKey, int64(7)))
	ownerRec := httptest.NewRecorder()
	handler.HandleOwnerSkills(ownerRec, owner)
	if ownerRec.Code != http.StatusOK || !strings.Contains(ownerRec.Body.String(), `"displayName":"cloud-html-artifact"`) {
		t.Fatalf("owner status=%d body=%s", ownerRec.Code, ownerRec.Body.String())
	}

	db.friends[[2]int64{8, 43}] = true
	viewer := httptest.NewRequest(http.MethodGet, "/api/agents/skills?uid=43", nil)
	viewer = viewer.WithContext(context.WithValue(viewer.Context(), uidKey, int64(8)))
	viewerRec := httptest.NewRecorder()
	handler.HandleViewerSkills(viewerRec, viewer)
	if viewerRec.Code != http.StatusOK || !strings.Contains(viewerRec.Body.String(), `"displayName":"cloud-html-artifact"`) {
		t.Fatalf("viewer status=%d body=%s", viewerRec.Code, viewerRec.Body.String())
	}
	for _, field := range []string{`"revisionNumber":3`, `"lastChangedBy":"lin"`, `"lastChangedAt":"2026-08-22T02:03:04Z"`, `"changeSource":"conversation_mutation"`} {
		if !strings.Contains(viewerRec.Body.String(), field) {
			t.Fatalf("viewer response missing safe metadata %s: %s", field, viewerRec.Body.String())
		}
	}
	if strings.Contains(viewerRec.Body.String(), "contentHash") || strings.Contains(viewerRec.Body.String(), testSkillHash) {
		t.Fatalf("viewer response leaked package identity: %s", viewerRec.Body.String())
	}
	if strings.Contains(viewerRec.Body.String(), "lastChangedByUserUid") {
		t.Fatalf("viewer response leaked the raw actor uid: %s", viewerRec.Body.String())
	}

	handler.SetSkillMetadataResolver(func(context.Context, int64, []types.BotSkillRef) (map[string]BotSkillDisplayMetadata, error) {
		return nil, errors.New("temporary upstream outage")
	})
	fallbackRec := httptest.NewRecorder()
	handler.HandleOwnerSkills(fallbackRec, owner)
	if fallbackRec.Code != http.StatusOK || strings.Contains(fallbackRec.Body.String(), "displayName") {
		t.Fatalf("metadata outage should degrade gracefully: status=%d body=%s", fallbackRec.Code, fallbackRec.Body.String())
	}

	runtimeMetadataCalls := 0
	handler.SetSkillMetadataResolver(func(context.Context, int64, []types.BotSkillRef) (map[string]BotSkillDisplayMetadata, error) {
		runtimeMetadataCalls++
		return nil, nil
	})
	runtime := httptest.NewRequest(http.MethodGet, "/api/bot/definition/skills", nil)
	runtime = runtime.WithContext(context.WithValue(runtime.Context(), uidKey, int64(43)))
	runtimeRec := httptest.NewRecorder()
	handler.HandleRuntimeSkills(runtimeRec, runtime)
	if runtimeRec.Code != http.StatusOK || runtimeMetadataCalls != 0 {
		t.Fatalf("runtime sync must not depend on display metadata: status=%d calls=%d", runtimeRec.Code, runtimeMetadataCalls)
	}
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
