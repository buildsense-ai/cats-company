package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

func promptRequest(method, path, body string, uid int64) *http.Request {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	return req.WithContext(context.WithValue(req.Context(), uidKey, uid))
}

func TestHandleViewerPromptOwnerAndFriendOnlySeeActivePrompt(t *testing.T) {
	db := &botDefinitionTestStore{
		owners:  map[int64]int64{43: 7},
		friends: map[[2]int64]bool{{8, 43}: true},
		records: map[int64]*types.BotDefinitionRecord{43: {
			Definition: types.BotDefinition{
				Schema: types.BotDefinitionSchema, BotID: "43",
				Model:  types.BotDefinitionModel{Kind: "custom", APIBase: "https://secret.invalid"},
				Prompt: &types.BotPromptDefinition{Selected: "custom", CustomSystemPrompt: "active"},
				Skills: []types.BotSkillRef{{SkillID: "private-skill"}},
			},
			DefaultPrompt:    &types.BotDefaultPromptSnapshot{Content: "default", ContentHash: strings.Repeat("a", 64), ReportedAt: "now"},
			PromptVisibility: types.BotPromptFriends,
			Runtime:          types.BotDefinitionRuntime{DesiredRevision: 4, LastError: "secret runtime error"},
			Exists:           true,
		}},
	}
	handler := NewBotDefinitionHandler(db, db, nil, nil)
	for _, tc := range []struct {
		name string
		uid  int64
		edit bool
	}{
		{"owner", 7, true}, {"friend", 8, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.HandleViewerPrompt(rec, promptRequest(http.MethodGet, "/api/agents/prompt?uid=43", "", tc.uid))
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var body map[string]interface{}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body["content"] != "active" || body["can_edit"] != tc.edit || body["relation"] == nil {
				t.Fatalf("response=%s", rec.Body.String())
			}
			if tc.edit && (body["default_content"] != "default" || body["default_content_available"] != true) {
				t.Fatalf("owner default snapshot missing from response=%s", rec.Body.String())
			}
			for _, forbidden := range []string{"secret.invalid", "private-skill", "secret runtime error"} {
				if strings.Contains(rec.Body.String(), forbidden) {
					t.Fatalf("response leaked %q: %s", forbidden, rec.Body.String())
				}
			}
			if !tc.edit && (body["default_content"] != nil || body["default_snapshot"] != nil) {
				t.Fatalf("friend received inactive default prompt: %s", rec.Body.String())
			}
		})
	}
}

func TestHandleViewerPromptDoesNotRequireLegacyCustomModelDecryption(t *testing.T) {
	db := &botDefinitionTestStore{
		owners:  map[int64]int64{43: 7},
		friends: map[[2]int64]bool{{8, 43}: true},
		records: map[int64]*types.BotDefinitionRecord{43: {
			Definition: types.BotDefinition{
				Model: types.BotDefinitionModel{
					Kind: "custom", APIKeyCiphertext: "legacy-ciphertext",
				},
				Prompt: &types.BotPromptDefinition{
					Selected: "custom", CustomSystemPrompt: "shared prompt",
				},
			},
			PromptVisibility: types.BotPromptFriends,
			Exists:           false,
		}},
	}

	rec := httptest.NewRecorder()
	// A nil model handler would panic if HandleViewerPrompt called the owner
	// migration path and attempted to decrypt this legacy custom model.
	NewBotDefinitionHandler(db, db, nil, nil).HandleViewerPrompt(
		rec,
		promptRequest(http.MethodGet, "/api/agents/prompt?uid=43", "", 8),
	)

	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"content":"shared prompt"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "legacy-ciphertext") {
		t.Fatalf("viewer response leaked model ciphertext: %s", rec.Body.String())
	}
}

func TestHandleViewerPromptIncludesSafeApplicationStatus(t *testing.T) {
	db := &botDefinitionTestStore{
		owners: map[int64]int64{40: 7, 41: 7, 42: 7, 43: 7, 44: 7, 45: 7},
		records: map[int64]*types.BotDefinitionRecord{
			40: {Definition: types.BotDefinition{Prompt: &types.BotPromptDefinition{Selected: "default"}}, Runtime: types.BotDefinitionRuntime{LastError: "stale legacy error"}, Exists: true},
			41: {Definition: types.BotDefinition{Prompt: &types.BotPromptDefinition{Selected: "default"}}, Runtime: types.BotDefinitionRuntime{DesiredRevision: 4}, Exists: true},
			42: {Definition: types.BotDefinition{Prompt: &types.BotPromptDefinition{Selected: "default"}}, Runtime: types.BotDefinitionRuntime{DesiredRevision: 4, AppliedRevision: 3, LastAttemptRevision: 3, LastAttemptAt: "2026-08-13T00:00:00Z"}, Exists: true},
			43: {Definition: types.BotDefinition{Prompt: &types.BotPromptDefinition{Selected: "default"}}, Runtime: types.BotDefinitionRuntime{DesiredRevision: 4, AppliedRevision: 4, AppliedAt: "2026-08-13T00:00:00Z"}, Exists: true},
			44: {Definition: types.BotDefinition{Prompt: &types.BotPromptDefinition{Selected: "default"}}, Runtime: types.BotDefinitionRuntime{DesiredRevision: 4, AppliedRevision: 3, LastAttemptRevision: 4, LastAttemptAt: "2026-08-13T00:00:00Z", LastError: "provider secret leaked"}, Exists: true},
			45: {Definition: types.BotDefinition{Prompt: &types.BotPromptDefinition{Selected: "default"}}, Runtime: types.BotDefinitionRuntime{DesiredRevision: 4, AppliedRevision: 4, AppliedAt: "2026-08-13T00:00:00Z", LastAttemptRevision: 4, LastAttemptAt: "2026-08-13T00:01:00Z", LastError: "transient retry failure"}, Exists: true},
		},
	}
	handler := NewBotDefinitionHandler(db, db, nil, nil)
	handler.SetPromptOnlineResolver(func(uid int64) bool { return uid == 40 || uid == 42 })
	for _, tc := range []struct {
		uid       int64
		status    string
		online    bool
		desired   int64
		applied   int64
		lastError string
	}{
		{40, "saved", true, 0, 0, ""},
		{41, "saved", false, 4, 0, ""},
		{42, "pending", true, 4, 3, ""},
		{43, "applied", false, 4, 4, ""},
		{45, "applied", false, 4, 4, ""},
		{44, "failed", false, 4, 3, "Bot 配置应用失败"},
	} {
		t.Run(strconv.FormatInt(tc.uid, 10), func(t *testing.T) {
			rec := httptest.NewRecorder()
			handler.HandleViewerPrompt(rec, promptRequest(http.MethodGet, "/api/agents/prompt?uid="+strconv.FormatInt(tc.uid, 10), "", 7))
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var body struct {
				Application botPromptApplicationStatus `json:"application"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Application.Status != tc.status || body.Application.IsOnline != tc.online || body.Application.AppliedRevision != tc.applied || body.Application.LastError != tc.lastError {
				t.Fatalf("application=%+v, want status=%s online=%v applied=%d error=%q", body.Application, tc.status, tc.online, tc.applied, tc.lastError)
			}
			if body.Application.DesiredRevision != tc.desired {
				t.Fatalf("desired revision=%d, want=%d", body.Application.DesiredRevision, tc.desired)
			}
			if strings.Contains(rec.Body.String(), "provider secret leaked") {
				t.Fatalf("raw runtime error leaked: %s", rec.Body.String())
			}
		})
	}
}

func TestHandleViewerPromptInitialRevisionIsPendingWhenRuntimeIsOnlineOnAnotherNode(t *testing.T) {
	shared := newSharedMemoryRuntimeState()
	hubA := NewHubWithRuntime(nil, nil, shared, "node-a")
	hubB := NewHubWithRuntime(nil, nil, shared, "node-b")

	if _, err := hubB.bodyLeases.acquire(42, "body-a", "conn-b"); err != nil {
		t.Fatalf("node-b acquire failed: %v", err)
	}
	hubB.addRegisteredClient(&Client{
		uid: 42, accountType: types.AccountBot, bodyID: "body-a",
		connectionID: "conn-b", send: make(chan []byte, 1),
	})

	db := &botDefinitionTestStore{
		owners: map[int64]int64{42: 7},
		records: map[int64]*types.BotDefinitionRecord{42: {
			Definition: types.BotDefinition{
				Prompt: &types.BotPromptDefinition{Selected: "default"},
			},
			Runtime: types.BotDefinitionRuntime{DesiredRevision: 1},
			Exists:  true,
		}},
	}
	handler := NewBotDefinitionHandler(db, db, nil, nil)
	handler.SetPromptOnlineResolver(hubA.BotRuntimeOnline)

	rec := httptest.NewRecorder()
	handler.HandleViewerPrompt(
		rec,
		promptRequest(http.MethodGet, "/api/agents/prompt?uid=42", "", 7),
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}

	var body struct {
		Application botPromptApplicationStatus `json:"application"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Application.Status != "pending" || !body.Application.IsOnline {
		t.Fatalf("application=%+v, want pending status from node-b runtime", body.Application)
	}
	if body.Application.DesiredRevision != 1 || body.Application.AppliedRevision != 0 {
		t.Fatalf("application revisions=%+v, want desired=1 applied=0", body.Application)
	}
}

func TestHandleViewerPromptFriendReceivesOnlySanitizedApplicationFailure(t *testing.T) {
	db := &botDefinitionTestStore{
		owners:  map[int64]int64{43: 7},
		friends: map[[2]int64]bool{{8, 43}: true},
		records: map[int64]*types.BotDefinitionRecord{43: {
			Definition:       types.BotDefinition{Prompt: &types.BotPromptDefinition{Selected: "custom", CustomSystemPrompt: "shared"}},
			PromptVisibility: types.BotPromptFriends,
			Runtime: types.BotDefinitionRuntime{
				DesiredRevision: 4, LastAttemptRevision: 4,
				LastAttemptAt: "2026-08-13T00:00:00Z", LastError: "credential sk-private rejected",
			},
			Exists: true,
		}},
	}
	rec := httptest.NewRecorder()
	NewBotDefinitionHandler(db, db, nil, nil).HandleViewerPrompt(
		rec,
		promptRequest(http.MethodGet, "/api/agents/prompt?uid=43", "", 8),
	)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"status":"failed"`) ||
		!strings.Contains(rec.Body.String(), `"last_error":"Bot 配置应用失败"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "sk-private") || strings.Contains(rec.Body.String(), "credential") {
		t.Fatalf("friend response leaked runtime failure detail: %s", rec.Body.String())
	}
}

func TestHandleViewerPromptRequiresSharedVisibilityAndFriendship(t *testing.T) {
	db := &botDefinitionTestStore{
		owners: map[int64]int64{43: 7}, friends: map[[2]int64]bool{},
		records: map[int64]*types.BotDefinitionRecord{43: {
			Definition: types.BotDefinition{Prompt: &types.BotPromptDefinition{Selected: "default"}}, Exists: true,
		}},
	}
	handler := NewBotDefinitionHandler(db, db, nil, nil)
	for _, tc := range []struct {
		uid    int64
		status int
	}{{8, http.StatusNotFound}, {9, http.StatusNotFound}} {
		rec := httptest.NewRecorder()
		handler.HandleViewerPrompt(rec, promptRequest(http.MethodGet, "/api/agents/prompt?uid=43", "", tc.uid))
		if rec.Code != tc.status {
			t.Fatalf("uid=%d status=%d body=%s", tc.uid, rec.Code, rec.Body.String())
		}
	}
}

func TestHandleViewerPromptDefaultSnapshotAndMissingSnapshot(t *testing.T) {
	db := &botDefinitionTestStore{
		owners: map[int64]int64{43: 7, 44: 7},
		records: map[int64]*types.BotDefinitionRecord{
			43: {
				Definition:    types.BotDefinition{Prompt: &types.BotPromptDefinition{Selected: "default"}},
				DefaultPrompt: &types.BotDefaultPromptSnapshot{Content: "bundled", ContentHash: strings.Repeat("a", 64), RuntimeVersion: "node-24", ReportedAt: "now"},
				Exists:        true,
			},
			44: {Definition: types.BotDefinition{Prompt: &types.BotPromptDefinition{Selected: "default"}}, Exists: true},
		},
	}
	handler := NewBotDefinitionHandler(db, db, nil, nil)
	for _, tc := range []struct {
		uid       int64
		available bool
		content   string
	}{
		{43, true, "bundled"}, {44, false, ""},
	} {
		rec := httptest.NewRecorder()
		handler.HandleViewerPrompt(rec, promptRequest(http.MethodGet, "/api/agents/prompt?uid="+strconv.FormatInt(tc.uid, 10), "", 7))
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		var body botViewerPromptResponse
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.ContentAvailable != tc.available || body.Content != tc.content {
			t.Fatalf("uid=%d response=%+v", tc.uid, body)
		}
	}
}

func TestHandleViewerPromptEmptyCustomContentIsUnavailable(t *testing.T) {
	db := &botDefinitionTestStore{
		owners: map[int64]int64{43: 7},
		records: map[int64]*types.BotDefinitionRecord{43: {
			Definition: types.BotDefinition{
				Prompt: &types.BotPromptDefinition{Selected: "custom"},
			},
			Exists: true,
		}},
	}
	rec := httptest.NewRecorder()
	NewBotDefinitionHandler(db, db, nil, nil).HandleViewerPrompt(
		rec,
		promptRequest(http.MethodGet, "/api/agents/prompt?uid=43", "", 7),
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var body botViewerPromptResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.ContentAvailable || body.Content != "" {
		t.Fatalf("empty custom prompt was marked available: %+v", body)
	}
}

func TestHandleViewerPromptFriendSeesDefaultSnapshotWhenItIsActive(t *testing.T) {
	db := &botDefinitionTestStore{
		owners:  map[int64]int64{43: 7},
		friends: map[[2]int64]bool{{8, 43}: true},
		records: map[int64]*types.BotDefinitionRecord{43: {
			Definition:       types.BotDefinition{Prompt: &types.BotPromptDefinition{Selected: "default"}},
			DefaultPrompt:    &types.BotDefaultPromptSnapshot{Content: "bundled", ContentHash: strings.Repeat("a", 64), ReportedAt: "now"},
			PromptVisibility: types.BotPromptFriends,
			Exists:           true,
		}},
	}
	rec := httptest.NewRecorder()
	NewBotDefinitionHandler(db, db, nil, nil).HandleViewerPrompt(
		rec,
		promptRequest(http.MethodGet, "/api/agents/prompt?uid=43", "", 8),
	)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"content":"bundled"`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleRuntimeDefaultPromptValidatesHashAndDeduplicates(t *testing.T) {
	content := "bundled prompt"
	digest := sha256.Sum256([]byte(content))
	body := `{"content":"` + content + `","contentHash":"` + hex.EncodeToString(digest[:]) + `","xiaobaVersion":"1.2.3"}`
	db := &botDefinitionTestStore{owners: map[int64]int64{43: 7}, records: map[int64]*types.BotDefinitionRecord{43: {
		Definition: types.BotDefinition{Prompt: &types.BotPromptDefinition{Selected: "default"}}, Runtime: types.BotDefinitionRuntime{DesiredRevision: 5}, Exists: true,
	}}}
	handler := NewBotDefinitionHandler(db, db, nil, nil)
	first := httptest.NewRecorder()
	handler.HandleRuntimeDefaultPrompt(first, promptRequest(http.MethodPost, "/api/bot/definition/default-prompt", body, 43))
	if first.Code != http.StatusOK || !strings.Contains(first.Body.String(), `"changed":true`) {
		t.Fatalf("first status=%d body=%s", first.Code, first.Body.String())
	}
	if !strings.Contains(first.Body.String(), `"contentHash":"`+hex.EncodeToString(digest[:])+`"`) {
		t.Fatalf("response omitted snapshot hash: %s", first.Body.String())
	}
	if db.records[43].Runtime.DesiredRevision != 5 {
		t.Fatalf("snapshot report changed desired revision: %+v", db.records[43].Runtime)
	}
	second := httptest.NewRecorder()
	handler.HandleRuntimeDefaultPrompt(second, promptRequest(http.MethodPut, "/api/bot/definition/default-prompt", body, 43))
	if second.Code != http.StatusOK || !strings.Contains(second.Body.String(), `"changed":false`) {
		t.Fatalf("second status=%d body=%s", second.Code, second.Body.String())
	}
	bad := httptest.NewRecorder()
	handler.HandleRuntimeDefaultPrompt(bad, promptRequest(http.MethodPost, "/api/bot/definition/default-prompt", strings.Replace(body, hex.EncodeToString(digest[:]), strings.Repeat("0", 64), 1), 43))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad hash status=%d body=%s", bad.Code, bad.Body.String())
	}
}

func TestHandleRuntimeDefaultPromptRejectsOversizedJSONBody(t *testing.T) {
	content := strings.Repeat("p", 6*maxCustomSystemPromptBytes+8192)
	digest := sha256.Sum256([]byte(content))
	body := `{"content":"` + content + `","contentHash":"` + hex.EncodeToString(digest[:]) + `","xiaobaVersion":"1.2.3","runtimeVersion":"` + strings.Repeat("v", maxPromptVersionBytes) + `"}`
	if len(body) <= maxBotDefaultPromptBodyBytes {
		t.Fatalf("test body=%d bytes, want more than limit=%d", len(body), maxBotDefaultPromptBodyBytes)
	}
	db := &botDefinitionTestStore{
		owners:  map[int64]int64{43: 7},
		records: map[int64]*types.BotDefinitionRecord{43: {Exists: true}},
	}
	rec := httptest.NewRecorder()
	NewBotDefinitionHandler(db, db, nil, nil).HandleRuntimeDefaultPrompt(
		rec,
		promptRequest(http.MethodPost, "/api/bot/definition/default-prompt", body, 43),
	)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if db.records[43].DefaultPrompt != nil {
		t.Fatalf("oversized body was persisted: %+v", db.records[43].DefaultPrompt)
	}
}

func TestHandleRuntimeDefaultPromptAcceptsEscapedPromptAtDecodedLimit(t *testing.T) {
	content := strings.Repeat("<", maxCustomSystemPromptBytes)
	digest := sha256.Sum256([]byte(content))
	body := `{"content":"` + strings.Repeat(`\u003c`, maxCustomSystemPromptBytes) + `","contentHash":"` + hex.EncodeToString(digest[:]) + `"}`
	if len(body) <= maxCustomSystemPromptBytes+4096 {
		t.Fatalf("escaped body=%d bytes, want it to exceed the old wire limit=%d", len(body), maxCustomSystemPromptBytes+4096)
	}
	if len(body) >= maxBotDefaultPromptBodyBytes {
		t.Fatalf("escaped body=%d bytes, want it to fit the expanded wire limit=%d", len(body), maxBotDefaultPromptBodyBytes)
	}
	db := &botDefinitionTestStore{owners: map[int64]int64{43: 7}, records: map[int64]*types.BotDefinitionRecord{43: {Exists: true}}}
	rec := httptest.NewRecorder()
	NewBotDefinitionHandler(db, db, nil, nil).HandleRuntimeDefaultPrompt(
		rec,
		promptRequest(http.MethodPost, "/api/bot/definition/default-prompt", body, 43),
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := db.records[43].DefaultPrompt; got == nil || got.Content != content {
		t.Fatalf("escaped prompt was not decoded and persisted: %+v", got)
	}
}

func TestHandleRuntimeDefaultPromptTreatsVersionRegressionAsUnchanged(t *testing.T) {
	content := "older bundled prompt"
	digest := sha256.Sum256([]byte(content))
	body := `{"content":"` + content + `","contentHash":"` + hex.EncodeToString(digest[:]) + `","xiaobaVersion":"1.4.9"}`
	db := &botDefinitionTestStore{owners: map[int64]int64{43: 7}, records: map[int64]*types.BotDefinitionRecord{43: {
		Definition: types.BotDefinition{Prompt: &types.BotPromptDefinition{Selected: "default"}},
		DefaultPrompt: &types.BotDefaultPromptSnapshot{
			Content: "newer bundled prompt", ContentHash: strings.Repeat("a", 64),
			XiaoBaVersion: "1.5.0", ReportedAt: "2026-08-13T00:00:00Z",
		},
		Runtime: types.BotDefinitionRuntime{DesiredRevision: 5}, Exists: true,
	}}}
	handler := NewBotDefinitionHandler(db, db, nil, nil)
	rec := httptest.NewRecorder()
	handler.HandleRuntimeDefaultPrompt(rec, promptRequest(http.MethodPut, "/api/bot/definition/default-prompt", body, 43))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"changed":false`) {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if db.records[43].DefaultPrompt.Content != "newer bundled prompt" ||
		db.records[43].Runtime.DesiredRevision != 5 {
		t.Fatalf("stale report mutated record: %+v", db.records[43])
	}
}

func TestHandleOwnerPromptVisibilityRejectsNonOwner(t *testing.T) {
	db := &botDefinitionTestStore{owners: map[int64]int64{43: 7}, records: map[int64]*types.BotDefinitionRecord{43: {Exists: true}}}
	handler := NewBotDefinitionHandler(db, db, nil, nil)
	rec := httptest.NewRecorder()
	handler.HandleOwnerPromptVisibility(rec, promptRequest(http.MethodPatch, "/api/bots/definition/prompt-visibility?uid=43", `{"prompt_visibility":"friends"}`, 8))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestHandleOwnerPromptVisibilityUpdatesPolicyWithoutRevision(t *testing.T) {
	db := &botDefinitionTestStore{owners: map[int64]int64{43: 7}, records: map[int64]*types.BotDefinitionRecord{43: {
		Runtime: types.BotDefinitionRuntime{DesiredRevision: 6}, Exists: true,
	}}}
	handler := NewBotDefinitionHandler(db, db, nil, nil)
	rec := httptest.NewRecorder()
	handler.HandleOwnerPromptVisibility(rec, promptRequest(http.MethodPatch, "/api/bots/definition/prompt-visibility?uid=43", `{"prompt_visibility":"friends"}`, 7))
	if rec.Code != http.StatusOK || db.records[43].PromptVisibility != types.BotPromptFriends || db.records[43].Runtime.DesiredRevision != 6 {
		t.Fatalf("status=%d body=%s record=%+v", rec.Code, rec.Body.String(), db.records[43])
	}
	bad := httptest.NewRecorder()
	handler.HandleOwnerPromptVisibility(bad, promptRequest(http.MethodPatch, "/api/bots/definition/prompt-visibility?uid=43", `{"prompt_visibility":"public"}`, 7))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("bad status=%d body=%s", bad.Code, bad.Body.String())
	}
}
