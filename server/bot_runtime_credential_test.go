package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

type botRuntimeCredentialTestStore struct {
	store.Store
	ownerUID int64
}

func (s *botRuntimeCredentialTestStore) GetBotOwner(botUID int64) (int64, error) {
	if botUID != 42 {
		return 0, errors.New("not found")
	}
	return s.ownerUID, nil
}

func TestBotRuntimeCredentialRoundTripBindsOwnerBotAndInstallation(t *testing.T) {
	now := time.Date(2026, 8, 16, 14, 0, 0, 0, time.UTC)
	signer, err := newBotRuntimeCredentialSigner([]byte(skillMutationGrantTestSecret), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	raw, issued, err := signer.issue(botRuntimeCredentialInput{
		OwnerUID: 7, BotUID: 42, BodyID: "body-prod-1", InstallationID: "install-prod-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	verified, err := signer.verify(raw)
	if err != nil {
		t.Fatal(err)
	}
	if verified.OwnerUID != 7 || verified.BotUID != 42 || verified.BodyID != "body-prod-1" ||
		verified.InstallationID != "install-prod-1" || !botRuntimeCredentialHasScope(verified, botRuntimeSkillMutationScope) ||
		verified.ID != issued.ID {
		t.Fatalf("unexpected Bot Runtime credential: %#v", verified)
	}
}

func TestBotRuntimeCredentialRejectsWrongBindingAndLifetime(t *testing.T) {
	now := time.Date(2026, 8, 16, 14, 0, 0, 0, time.UTC)
	signer, _ := newBotRuntimeCredentialSigner([]byte(skillMutationGrantTestSecret), func() time.Time { return now })
	if _, _, err := signer.issue(botRuntimeCredentialInput{
		OwnerUID: 7, BotUID: 42, BodyID: legacyBotBodyID(42), InstallationID: "install-prod-1",
	}); err == nil {
		t.Fatal("legacy body received a Bot Runtime credential")
	}
	if _, _, err := signer.issue(botRuntimeCredentialInput{
		OwnerUID: 7, BotUID: 42, BodyID: "body-prod-1", InstallationID: "invalid installation", TTL: time.Hour,
	}); err == nil {
		t.Fatal("invalid installation received a Bot Runtime credential")
	}
	if _, _, err := signer.issue(botRuntimeCredentialInput{
		OwnerUID: 7, BotUID: 42, BodyID: "body-prod-1", InstallationID: "install-prod-1",
		TTL: maxBotRuntimeCredentialTTL + time.Second,
	}); err == nil {
		t.Fatal("oversized Bot Runtime credential lifetime was accepted")
	}
}

func TestBotRuntimeCredentialUsesDedicatedSigningKey(t *testing.T) {
	now := time.Date(2026, 8, 16, 14, 0, 0, 0, time.UTC)
	signer, err := newBotRuntimeCredentialSigner([]byte(skillMutationGrantTestSecret), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if string(signer.key) == skillMutationGrantTestSecret {
		t.Fatal("Bot Runtime credential reused the root JWT key directly")
	}
	raw, _, err := signer.issue(botRuntimeCredentialInput{
		OwnerUID: 7, BotUID: 42, BodyID: "body-prod-1", InstallationID: "install-prod-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	other, _ := newBotRuntimeCredentialSigner([]byte("abcdef0123456789abcdef0123456789"), func() time.Time { return now })
	if _, err := other.verify(raw); err == nil {
		t.Fatal("credential signed by another root key was accepted")
	}
	if _, err := newBotRuntimeCredentialSigner([]byte("too-short"), time.Now); err == nil {
		t.Fatal("short root secret was accepted")
	}
}

func TestIssueBotRuntimeCredentialRequiresActualOwner(t *testing.T) {
	now := time.Date(2026, 8, 16, 14, 0, 0, 0, time.UTC)
	db := &botRuntimeCredentialTestStore{ownerUID: 7}
	hub := NewHub(db, nil)
	signer, _ := newBotRuntimeCredentialSigner([]byte(skillMutationGrantTestSecret), func() time.Time { return now })
	hub.botRuntimeCredentials = signer
	handler := NewBotHandler(db)
	handler.SetHub(hub)

	body := []byte(`{"bot_uid":42,"body_id":"body-prod-1","installation_id":"install-prod-1"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/bots/runtime-credential", bytes.NewReader(body))
	req = req.WithContext(contextWithClaims(req.Context(), &JWTClaims{UID: 7, Username: "owner"}))
	rec := httptest.NewRecorder()
	handler.HandleIssueRuntimeCredential(rec, req)
	if rec.Code != http.StatusCreated || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("owner issue status=%d headers=%v body=%s", rec.Code, rec.Header(), rec.Body.String())
	}
	var response struct {
		Credential string `json:"credential"`
		ExpiresAt  int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	claims, err := signer.verify(response.Credential)
	if err != nil {
		t.Fatal(err)
	}
	if claims.OwnerUID != 7 || claims.BotUID != 42 || response.ExpiresAt != claims.ExpiresAt.Time.UnixMilli() {
		t.Fatalf("unexpected issued credential response=%+v claims=%#v", response, claims)
	}

	deniedReq := httptest.NewRequest(http.MethodPost, "/api/bots/runtime-credential", bytes.NewReader(body))
	deniedReq = deniedReq.WithContext(context.WithValue(deniedReq.Context(), uidKey, int64(8)))
	denied := httptest.NewRecorder()
	handler.HandleIssueRuntimeCredential(denied, deniedReq)
	if denied.Code != http.StatusForbidden {
		t.Fatalf("non-owner issue status=%d body=%s", denied.Code, denied.Body.String())
	}
}
