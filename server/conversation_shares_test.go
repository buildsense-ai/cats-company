package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type conversationShareTestStore struct {
	store.Store
	messages []*types.Message
	users    map[int64]*types.User
	shares   map[string]*store.ConversationShare
	items    map[string][]*store.ConversationShareItem
	assets   map[string]*store.ConversationShareAsset
}

func (s *conversationShareTestStore) GetMessagesByIDs(topicID string, ids []int64) ([]*types.Message, error) {
	wanted := make(map[int64]struct{}, len(ids))
	for _, id := range ids {
		wanted[id] = struct{}{}
	}
	var result []*types.Message
	for _, message := range s.messages {
		if message != nil && message.TopicID == topicID {
			if _, ok := wanted[message.ID]; ok {
				result = append(result, message)
			}
		}
	}
	return result, nil
}

func (s *conversationShareTestStore) GetUser(id int64) (*types.User, error) {
	return s.users[id], nil
}

func (s *conversationShareTestStore) CreateConversationShare(share *store.ConversationShare, items []*store.ConversationShareItem, assets []*store.ConversationShareAsset) error {
	s.shares[share.ID] = share
	s.items[share.ID] = append([]*store.ConversationShareItem(nil), items...)
	for _, asset := range assets {
		s.assets[asset.ID] = asset
	}
	return nil
}

func (s *conversationShareTestStore) GetConversationShareByTokenHash(tokenHash string) (*store.ConversationShare, error) {
	for _, share := range s.shares {
		if share.TokenHash == tokenHash {
			return share, nil
		}
	}
	return nil, nil
}

func (s *conversationShareTestStore) GetConversationShareByID(shareID string) (*store.ConversationShare, error) {
	return s.shares[shareID], nil
}

func (s *conversationShareTestStore) ListConversationShares(ownerUID int64, topicID string) ([]*store.ConversationShare, error) {
	shares := make([]*store.ConversationShare, 0)
	for _, share := range s.shares {
		if share != nil && share.OwnerUID == ownerUID && share.TopicID == topicID {
			shares = append(shares, share)
		}
	}
	return shares, nil
}

func (s *conversationShareTestStore) GetConversationShareItems(shareID string) ([]*store.ConversationShareItem, error) {
	return append([]*store.ConversationShareItem(nil), s.items[shareID]...), nil
}

func (s *conversationShareTestStore) GetConversationShareAsset(shareID, assetID string) (*store.ConversationShareAsset, error) {
	asset := s.assets[assetID]
	if asset == nil || asset.ShareID != shareID {
		return nil, nil
	}
	return asset, nil
}

func (s *conversationShareTestStore) RevokeConversationShare(ownerUID int64, shareID string) (bool, error) {
	share := s.shares[shareID]
	if share == nil || share.OwnerUID != ownerUID || share.State != store.ConversationShareStateActive {
		return false, nil
	}
	share.State = store.ConversationShareStateRevoked
	return true, nil
}

func TestConversationSharePlainTextDoesNotSerializeMessageMetadata(t *testing.T) {
	if got := conversationSharePlainText(map[string]interface{}{
		"text":           "只分享这段正文",
		"device_context": "must not be exported",
	}); got != "只分享这段正文" {
		t.Fatalf("plain text = %q", got)
	}
	if got := conversationSharePlainText(map[string]interface{}{
		"device_context": "must not be exported",
	}); got != "" {
		t.Fatalf("metadata-only content = %q, want empty", got)
	}
}

func TestConversationShareSnapshotPreservesLegacyTextAlongsideAttachment(t *testing.T) {
	sourceRoot := t.TempDir()
	shareRoot := t.TempDir()
	const fileKey = "20260817_0123456789abcdef0123456789abcdef.png"
	sourcePath := filepath.Join(sourceRoot, "images", fileKey)
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatalf("create image directory: %v", err)
	}
	if err := os.WriteFile(sourcePath, []byte("legacy image"), 0o644); err != nil {
		t.Fatalf("write image fixture: %v", err)
	}

	handler := NewConversationShareHandler(&conversationShareTestStore{}, nil, sourceRoot, shareRoot)
	snapshot, assets, _, err := handler.makeSnapshot(
		7,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		&types.Message{
			ID:      101,
			TopicID: "p2p_7_99",
			FromUID: 7,
			Content: "旧格式正文仍应保留",
			MsgType: "text",
			ContentBlocks: []types.ContentBlock{{
				Type: "image",
				Payload: map[string]interface{}{
					"name": "legacy.png",
					"url":  "/uploads/images/" + fileKey,
				},
			}},
		},
		conversationShareMaxAssets,
	)
	if err != nil {
		t.Fatalf("make snapshot: %v", err)
	}
	if snapshot.Content != "旧格式正文仍应保留" {
		t.Fatalf("snapshot content = %q", snapshot.Content)
	}
	if len(snapshot.ContentBlocks) != 1 || snapshot.ContentBlocks[0].Type != "image" {
		t.Fatalf("snapshot blocks = %#v", snapshot.ContentBlocks)
	}
	if len(assets) != 1 {
		t.Fatalf("asset count = %d, want 1", len(assets))
	}
}

func TestConversationShareSnapshotsAudioWithoutChangingOrdinaryVisibility(t *testing.T) {
	sourceRoot := t.TempDir()
	shareRoot := t.TempDir()
	const fileKey = "20260817_0123456789abcdef0123456789abcdef.ogg"
	sourcePath := filepath.Join(sourceRoot, "files", fileKey)
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatalf("create audio directory: %v", err)
	}
	if err := os.WriteFile(sourcePath, []byte("audio snapshot"), 0o644); err != nil {
		t.Fatalf("write audio fixture: %v", err)
	}

	if isUserVisibleMessageType("audio") {
		t.Fatal("audio must remain outside ordinary message visibility")
	}
	if isDurableAgentContextMessage(&types.Message{}, "audio") {
		t.Fatal("audio must remain outside durable agent context")
	}

	handler := NewConversationShareHandler(&conversationShareTestStore{}, nil, sourceRoot, shareRoot)
	snapshot, assets, _, err := handler.makeSnapshot(
		7,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		&types.Message{
			ID:      102,
			TopicID: "p2p_7_99",
			FromUID: 7,
			MsgType: "audio",
			ContentBlocks: []types.ContentBlock{{
				Type: "audio",
				Payload: map[string]interface{}{
					"name":      "voice.ogg",
					"mime_type": "audio/ogg",
					"url":       "/uploads/files/" + fileKey,
				},
			}},
		},
		conversationShareMaxAssets,
	)
	if err != nil {
		t.Fatalf("make audio snapshot: %v", err)
	}
	if len(snapshot.ContentBlocks) != 1 || snapshot.ContentBlocks[0].Type != "audio" {
		t.Fatalf("snapshot blocks = %#v", snapshot.ContentBlocks)
	}
	if len(assets) != 1 || assets[0].Kind != "audio" {
		t.Fatalf("snapshot assets = %#v", assets)
	}
}

func TestConversationShareCreatesSanitizedPublicSnapshot(t *testing.T) {
	sourceRoot := t.TempDir()
	shareRoot := t.TempDir()
	const fileKey = "20260817_0123456789abcdef0123456789abcdef.png"
	sourcePath := filepath.Join(sourceRoot, "images", fileKey)
	if err := os.MkdirAll(filepath.Dir(sourcePath), 0o755); err != nil {
		t.Fatalf("create image directory: %v", err)
	}
	if err := os.WriteFile(sourcePath, []byte("shared image bytes"), 0o644); err != nil {
		t.Fatalf("write image fixture: %v", err)
	}

	db := &conversationShareTestStore{
		users: map[int64]*types.User{
			7:  {ID: 7, Username: "owner", AccountType: types.AccountHuman},
			99: {ID: 99, Username: "agent", AccountType: types.AccountBot},
		},
		shares: map[string]*store.ConversationShare{},
		items:  map[string][]*store.ConversationShareItem{},
		assets: map[string]*store.ConversationShareAsset{},
		messages: []*types.Message{{
			ID:        101,
			TopicID:   "p2p_7_99",
			FromUID:   99,
			Content:   "原始会话内容不应作为隐式上下文暴露",
			MsgType:   "text",
			CreatedAt: time.Date(2026, 8, 17, 8, 30, 0, 0, time.UTC),
			ContentBlocks: []types.ContentBlock{
				{Type: "text", Text: "这是已选择的结论。"},
				{Type: "thinking", Thinking: "绝不能分享的推理"},
				{Type: "image", Payload: map[string]interface{}{
					"name":          "proof.png",
					"url":           "/uploads/images/" + fileKey,
					"mime_type":     "image/png",
					"size":          float64(18),
					"device_access": "must not escape",
				}},
			},
		}},
	}
	handler := NewConversationShareHandler(db, nil, sourceRoot, shareRoot)
	handler.now = func() time.Time { return time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC) }

	body := bytes.NewBufferString(`{"topic_id":"p2p_7_99","message_ids":[101],"title":"仅此片段","expires_in":3600}`)
	request := httptest.NewRequest(http.MethodPost, "https://app.example.test/api/conversation-shares", body)
	request = request.WithContext(context.WithValue(request.Context(), uidKey, int64(7)))
	created := httptest.NewRecorder()
	handler.HandleAuthenticated(created, request)

	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var createResponse struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &createResponse); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	parts := strings.Split(strings.TrimRight(createResponse.URL, "/"), "/")
	token := parts[len(parts)-1]
	if token == "" {
		t.Fatalf("share URL has no capability token: %q", createResponse.URL)
	}

	publicRequest := httptest.NewRequest(http.MethodGet, "/api/shared-conversations/"+token, nil)
	publicResponse := httptest.NewRecorder()
	handler.HandlePublic(publicResponse, publicRequest)
	if publicResponse.Code != http.StatusOK {
		t.Fatalf("public status=%d body=%s", publicResponse.Code, publicResponse.Body.String())
	}
	if got := publicResponse.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q, want no-store", got)
	}
	serialized := publicResponse.Body.String()
	for _, forbidden := range []string{"p2p_7_99", "device_access", "绝不能分享的推理", "原始会话内容不应作为隐式上下文暴露", "/uploads/images/"} {
		if strings.Contains(serialized, forbidden) {
			t.Fatalf("public snapshot leaked %q: %s", forbidden, serialized)
		}
	}
	if !strings.Contains(serialized, "这是已选择的结论。") {
		t.Fatalf("public snapshot omitted selected message: %s", serialized)
	}

	var publicBody struct {
		Items []struct {
			Speaker       string `json:"speaker"`
			CreatedAt     string `json:"created_at"`
			ContentBlocks []struct {
				Type    string `json:"type"`
				Payload struct {
					URL string `json:"url"`
				} `json:"payload"`
			} `json:"content_blocks"`
		} `json:"items"`
	}
	if err := json.Unmarshal(publicResponse.Body.Bytes(), &publicBody); err != nil {
		t.Fatalf("decode public response: %v", err)
	}
	if len(publicBody.Items) != 1 || publicBody.Items[0].Speaker != "assistant" {
		t.Fatalf("unexpected public items: %+v", publicBody.Items)
	}
	if publicBody.Items[0].CreatedAt != "2026-08-17T08:30:00Z" {
		t.Fatalf("created_at=%q, want selected message timestamp", publicBody.Items[0].CreatedAt)
	}
	if len(publicBody.Items[0].ContentBlocks) != 2 {
		t.Fatalf("content block count=%d, want 2", len(publicBody.Items[0].ContentBlocks))
	}
	assetURL := publicBody.Items[0].ContentBlocks[1].Payload.URL
	if !strings.Contains(assetURL, "/api/shared-conversations/"+token+"/assets/") {
		t.Fatalf("asset URL=%q is not share-scoped", assetURL)
	}

	assetRequest := httptest.NewRequest(http.MethodGet, assetURL, nil)
	assetResponse := httptest.NewRecorder()
	handler.HandlePublic(assetResponse, assetRequest)
	if assetResponse.Code != http.StatusOK || assetResponse.Body.String() != "shared image bytes" {
		t.Fatalf("asset status=%d body=%q", assetResponse.Code, assetResponse.Body.String())
	}
}

func TestConversationShareOwnerListIsScopedAndOmitsCapabilityToken(t *testing.T) {
	now := time.Date(2026, 8, 17, 9, 0, 0, 0, time.UTC)
	expiresAt := now.Add(-time.Hour)
	db := &conversationShareTestStore{
		shares: map[string]*store.ConversationShare{
			"share-owner-active": {
				ID:        "share-owner-active",
				OwnerUID:  7,
				TopicID:   "p2p_7_99",
				TokenHash: "secret-token-hash",
				Title:     "可管理链接",
				State:     store.ConversationShareStateActive,
				CreatedAt: now,
			},
			"share-owner-expired": {
				ID:        "share-owner-expired",
				OwnerUID:  7,
				TopicID:   "p2p_7_99",
				TokenHash: "expired-token-hash",
				Title:     "已过期链接",
				State:     store.ConversationShareStateActive,
				ExpiresAt: &expiresAt,
				CreatedAt: now.Add(-2 * time.Hour),
			},
			"share-other-topic": {
				ID:        "share-other-topic",
				OwnerUID:  7,
				TopicID:   "p2p_7_100",
				TokenHash: "other-topic-hash",
				State:     store.ConversationShareStateActive,
			},
			"share-other-owner": {
				ID:        "share-other-owner",
				OwnerUID:  8,
				TopicID:   "p2p_7_99",
				TokenHash: "other-owner-hash",
				State:     store.ConversationShareStateActive,
			},
		},
	}
	handler := NewConversationShareHandler(db, nil, t.TempDir(), t.TempDir())
	handler.now = func() time.Time { return now }
	request := httptest.NewRequest(http.MethodGet, "/api/conversation-shares?topic_id=p2p_7_99", nil)
	request = request.WithContext(context.WithValue(request.Context(), uidKey, int64(7)))
	response := httptest.NewRecorder()
	handler.HandleAuthenticated(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "token-hash") || strings.Contains(response.Body.String(), "p2p_7_99") {
		t.Fatalf("owner list leaked private fields: %s", response.Body.String())
	}
	var body struct {
		Shares []struct {
			ID    string `json:"id"`
			State string `json:"state"`
		} `json:"shares"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode owner list: %v", err)
	}
	if len(body.Shares) != 2 {
		t.Fatalf("owner list count=%d, want 2", len(body.Shares))
	}
	states := map[string]string{}
	for _, share := range body.Shares {
		states[share.ID] = share.State
	}
	if states["share-owner-active"] != "active" || states["share-owner-expired"] != "expired" {
		t.Fatalf("owner list states=%v", states)
	}
}

func TestConversationShareRevocationInvalidatesTranscriptAndAssets(t *testing.T) {
	assetRoot := t.TempDir()
	const token = "visitor-capability"
	const shareID = "11111111111111111111111111111111"
	const assetID = "asset-1"
	storageKey := filepath.Join(shareID, assetID+".pdf")
	assetPath := filepath.Join(assetRoot, storageKey)
	if err := os.MkdirAll(filepath.Dir(assetPath), 0o700); err != nil {
		t.Fatalf("create asset directory: %v", err)
	}
	if err := os.WriteFile(assetPath, []byte("private preview"), 0o600); err != nil {
		t.Fatalf("write asset: %v", err)
	}

	db := &conversationShareTestStore{
		shares: map[string]*store.ConversationShare{
			shareID: {
				ID:        shareID,
				OwnerUID:  7,
				TokenHash: conversationShareTokenHash(token),
				State:     store.ConversationShareStateActive,
				CreatedAt: time.Now().UTC(),
			},
		},
		items: map[string][]*store.ConversationShareItem{},
		assets: map[string]*store.ConversationShareAsset{
			assetID: {
				ID:         assetID,
				ShareID:    shareID,
				StorageKey: filepath.ToSlash(storageKey),
				Name:       "report.pdf",
				MimeType:   "application/pdf",
			},
		},
	}
	handler := NewConversationShareHandler(db, nil, t.TempDir(), assetRoot)

	revokeRequest := httptest.NewRequest(http.MethodDelete, "/api/conversation-shares/"+shareID, nil)
	revokeRequest = revokeRequest.WithContext(context.WithValue(revokeRequest.Context(), uidKey, int64(7)))
	revoked := httptest.NewRecorder()
	handler.HandleAuthenticated(revoked, revokeRequest)
	if revoked.Code != http.StatusOK {
		t.Fatalf("revoke status=%d body=%s", revoked.Code, revoked.Body.String())
	}
	if _, err := os.Stat(assetPath); !os.IsNotExist(err) {
		t.Fatalf("revoked asset still exists: %v", err)
	}

	for _, target := range []string{
		"/api/shared-conversations/" + token,
		"/api/shared-conversations/" + token + "/assets/" + assetID,
	} {
		response := httptest.NewRecorder()
		handler.HandlePublic(response, httptest.NewRequest(http.MethodGet, target, nil))
		if response.Code != http.StatusNotFound {
			t.Fatalf("%s status=%d body=%s, want 404", target, response.Code, response.Body.String())
		}
	}
}

func TestConversationShareCleanupRemovesExpiredAndDeletedShareAssets(t *testing.T) {
	assetRoot := t.TempDir()
	expiredAt := time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC)
	const expiredID = "22222222222222222222222222222222"
	const deletedID = "33333333333333333333333333333333"
	const activeID = "44444444444444444444444444444444"
	for _, shareID := range []string{expiredID, deletedID, activeID} {
		path := filepath.Join(assetRoot, shareID, "asset.bin")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatalf("create %s directory: %v", shareID, err)
		}
		if err := os.WriteFile(path, []byte("private asset"), 0o600); err != nil {
			t.Fatalf("write %s fixture: %v", shareID, err)
		}
	}
	db := &conversationShareTestStore{
		shares: map[string]*store.ConversationShare{
			expiredID: {
				ID:        expiredID,
				State:     store.ConversationShareStateActive,
				ExpiresAt: &expiredAt,
			},
			activeID: {
				ID:    activeID,
				State: store.ConversationShareStateActive,
			},
		},
	}
	handler := NewConversationShareHandler(db, nil, t.TempDir(), assetRoot)
	handler.now = func() time.Time { return expiredAt.Add(time.Second) }
	handler.cleanupInactiveAssetDirectories()

	for _, shareID := range []string{expiredID, deletedID} {
		if _, err := os.Stat(filepath.Join(assetRoot, shareID)); !os.IsNotExist(err) {
			t.Fatalf("inactive share directory %s still exists: %v", shareID, err)
		}
	}
	if _, err := os.Stat(filepath.Join(assetRoot, activeID)); err != nil {
		t.Fatalf("active share directory removed: %v", err)
	}
}

func TestConversationShareAssetSandboxesHTML(t *testing.T) {
	assetRoot := t.TempDir()
	const token = "html-preview-capability"
	const shareID = "share-html"
	const assetID = "asset-html"
	storageKey := filepath.Join(shareID, assetID+".html")
	assetPath := filepath.Join(assetRoot, storageKey)
	if err := os.MkdirAll(filepath.Dir(assetPath), 0o700); err != nil {
		t.Fatalf("create asset directory: %v", err)
	}
	if err := os.WriteFile(assetPath, []byte("<!doctype html><script>window.parent.postMessage('x', '*')</script>"), 0o600); err != nil {
		t.Fatalf("write HTML asset: %v", err)
	}

	db := &conversationShareTestStore{
		shares: map[string]*store.ConversationShare{
			shareID: {
				ID:        shareID,
				OwnerUID:  7,
				TokenHash: conversationShareTokenHash(token),
				State:     store.ConversationShareStateActive,
				CreatedAt: time.Now().UTC(),
			},
		},
		items: map[string][]*store.ConversationShareItem{},
		assets: map[string]*store.ConversationShareAsset{
			assetID: {
				ID:         assetID,
				ShareID:    shareID,
				StorageKey: filepath.ToSlash(storageKey),
				Name:       "report.html",
				MimeType:   "text/html",
			},
		},
	}
	handler := NewConversationShareHandler(db, nil, t.TempDir(), assetRoot)
	response := httptest.NewRecorder()
	handler.HandlePublic(response, httptest.NewRequest(http.MethodGet, "/api/shared-conversations/"+token+"/assets/"+assetID, nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	policy := response.Header().Get("Content-Security-Policy")
	if !strings.Contains(policy, "sandbox") || strings.Contains(policy, "allow-same-origin") {
		t.Fatalf("unsafe HTML content security policy: %q", policy)
	}
}

func TestConversationShareAssetSandboxesSVG(t *testing.T) {
	assetRoot := t.TempDir()
	const token = "svg-preview-capability"
	const shareID = "share-svg"
	const assetID = "asset-svg"
	storageKey := filepath.Join(shareID, assetID+".svg")
	assetPath := filepath.Join(assetRoot, storageKey)
	if err := os.MkdirAll(filepath.Dir(assetPath), 0o700); err != nil {
		t.Fatalf("create asset directory: %v", err)
	}
	if err := os.WriteFile(assetPath, []byte(`<svg xmlns="http://www.w3.org/2000/svg"><script>window.top.postMessage('x', '*')</script></svg>`), 0o600); err != nil {
		t.Fatalf("write SVG asset: %v", err)
	}

	db := &conversationShareTestStore{
		shares: map[string]*store.ConversationShare{
			shareID: {
				ID:        shareID,
				OwnerUID:  7,
				TokenHash: conversationShareTokenHash(token),
				State:     store.ConversationShareStateActive,
				CreatedAt: time.Now().UTC(),
			},
		},
		items: map[string][]*store.ConversationShareItem{},
		assets: map[string]*store.ConversationShareAsset{
			assetID: {
				ID:         assetID,
				ShareID:    shareID,
				StorageKey: filepath.ToSlash(storageKey),
				Name:       "diagram.svg",
				MimeType:   "image/svg+xml",
			},
		},
	}
	handler := NewConversationShareHandler(db, nil, t.TempDir(), assetRoot)
	response := httptest.NewRecorder()
	handler.HandlePublic(response, httptest.NewRequest(http.MethodGet, "/api/shared-conversations/"+token+"/assets/"+assetID, nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Security-Policy"); got != "sandbox" {
		t.Fatalf("Content-Security-Policy=%q, want sandbox", got)
	}
}

func TestConversationShareURLDoesNotTrustForwardedHost(t *testing.T) {
	t.Setenv("CATSCO_PUBLIC_BASE_URL", "")
	request := httptest.NewRequest(http.MethodPost, "https://app.example.test/api/conversation-shares", nil)
	request.Header.Set("X-Forwarded-Host", "attacker.example.test")
	request.Header.Set("X-Forwarded-Proto", "https")

	if got := conversationShareURL(request, "visitor-capability"); got != "https://app.example.test/share/visitor-capability" {
		t.Fatalf("share URL=%q, want the request host rather than forwarded host", got)
	}
}

func TestConversationShareURLPrefersConfiguredPublicBaseURL(t *testing.T) {
	t.Setenv("CATSCO_PUBLIC_BASE_URL", "https://public.example.test/catsco/")
	request := httptest.NewRequest(http.MethodPost, "http://internal.example.test/api/conversation-shares", nil)

	if got := conversationShareURL(request, "visitor-capability"); got != "https://public.example.test/catsco/share/visitor-capability" {
		t.Fatalf("share URL=%q, want configured public base URL", got)
	}
}

func TestConversationShareURLIgnoresInvalidConfiguredPublicBaseURL(t *testing.T) {
	t.Setenv("CATSCO_PUBLIC_BASE_URL", "javascript:alert(1)")
	request := httptest.NewRequest(http.MethodPost, "https://app.example.test/api/conversation-shares", nil)

	if got := conversationShareURL(request, "visitor-capability"); got != "https://app.example.test/share/visitor-capability" {
		t.Fatalf("share URL=%q, want request host fallback", got)
	}
}
