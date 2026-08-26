package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type skillMutationActivationOwnerStore struct {
	ownerUID int64
}

func (s *skillMutationActivationOwnerStore) GetBotOwner(int64) (int64, error) {
	return s.ownerUID, nil
}

type skillMutationActivationTestStore struct {
	activationInput *types.BotSkillMutationActivationInput
	failureInput    *types.BotSkillMutationActivationFailureInput
	idempotent      bool
}

func (s *skillMutationActivationTestStore) ActivateBotSkillMutation(
	input types.BotSkillMutationActivationInput,
	_ time.Time,
) (*types.BotSkillMutation, *types.BotDefinitionRecord, bool, error) {
	s.activationInput = &input
	return &types.BotSkillMutation{ID: input.MutationID, Status: types.BotSkillMutationActive},
		&types.BotDefinitionRecord{Runtime: types.BotDefinitionRuntime{DesiredRevision: input.AppliedDefinitionRevision}},
		s.idempotent, nil
}

func (s *skillMutationActivationTestStore) RecordBotSkillMutationActivationFailure(
	input types.BotSkillMutationActivationFailureInput,
	_ time.Time,
) (*types.BotSkillMutation, *types.BotDefinitionRecord, bool, error) {
	s.failureInput = &input
	status := types.BotSkillMutationActivationPending
	if input.Permanent {
		status = types.BotSkillMutationCompensationPending
	}
	return &types.BotSkillMutation{ID: input.MutationID, Status: status},
		&types.BotDefinitionRecord{Runtime: types.BotDefinitionRuntime{DesiredRevision: input.AttemptedDefinitionRevision}},
		s.idempotent, nil
}

func newSkillMutationActivationTestHandler(
	t *testing.T,
	scopes []string,
) (*SkillMutationActivationHandler, string, *skillMutationActivationTestStore) {
	t.Helper()
	now := time.Date(2026, 8, 26, 9, 0, 0, 0, time.UTC)
	signer, err := newBotRuntimeCredentialSigner([]byte(skillMutationGrantTestSecret), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	raw, _, err := signer.issue(botRuntimeCredentialInput{
		OwnerUID: 7, BotUID: 42, BodyID: "body-prod-1", InstallationID: "install-prod-1", Scopes: scopes,
	})
	if err != nil {
		t.Fatal(err)
	}
	mutations := &skillMutationActivationTestStore{}
	handler := NewSkillMutationActivationHandler(
		&skillMutationActivationOwnerStore{ownerUID: 7},
		mutations,
		&Hub{botRuntimeCredentials: signer},
	)
	handler.now = func() time.Time { return now }
	return handler, raw, mutations
}

func TestSkillMutationActivationRouteIsClosedByDefault(t *testing.T) {
	handler, raw, mutations := newSkillMutationActivationTestHandler(
		t, []string{botRuntimeSkillMutationScope, botRuntimeSkillActivationScope},
	)
	req := httptest.NewRequest(http.MethodPost, "/api/bot/skill-mutations/101/activation", strings.NewReader("{}"))
	req.Header.Set(botRuntimeCredentialHeader, raw)
	rec := httptest.NewRecorder()
	handler.Handle(rec, req)
	if rec.Code != http.StatusNotFound || mutations.activationInput != nil {
		t.Fatalf("default-closed status=%d input=%#v", rec.Code, mutations.activationInput)
	}
}

func TestSkillMutationActivationRequiresDedicatedCredentialScope(t *testing.T) {
	handler, raw, mutations := newSkillMutationActivationTestHandler(t, nil)
	handler.SetRollout(true, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/bot/skill-mutations/101/activation", strings.NewReader(
		`{"appliedDefinitionRevision":12,"skillSetHash":"`+strings.Repeat("a", 64)+`","result":"applied"}`,
	))
	req.Header.Set(botRuntimeCredentialHeader, raw)
	rec := httptest.NewRecorder()
	handler.Handle(rec, req)
	if rec.Code != http.StatusForbidden || mutations.activationInput != nil {
		t.Fatalf("old credential status=%d input=%#v", rec.Code, mutations.activationInput)
	}
}

func TestSkillMutationActivationUsesCredentialIdentityAndReturnsIdempotency(t *testing.T) {
	handler, raw, mutations := newSkillMutationActivationTestHandler(
		t, []string{botRuntimeSkillMutationScope, botRuntimeSkillActivationScope},
	)
	handler.SetRollout(false, map[int64]bool{42: true})
	mutations.idempotent = true
	body := []byte(`{"appliedDefinitionRevision":12,"skillSetHash":"` + strings.Repeat("a", 64) + `","result":"applied"}`)
	req := httptest.NewRequest(http.MethodPost, "/api/bot/skill-mutations/101/activation", bytes.NewReader(body))
	req.Header.Set(botRuntimeCredentialHeader, raw)
	rec := httptest.NewRecorder()
	handler.Handle(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if mutations.activationInput == nil ||
		mutations.activationInput.BotUID != 42 ||
		mutations.activationInput.MutationID != 101 ||
		mutations.activationInput.RuntimeBodyID != "body-prod-1" ||
		mutations.activationInput.RuntimeInstallationID != "install-prod-1" {
		t.Fatalf("activation did not use credential identity: %#v", mutations.activationInput)
	}
	var response map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response["idempotent"] != true || response["status"] != string(types.BotSkillMutationActive) {
		t.Fatalf("response=%#v", response)
	}
}

func TestSkillMutationActivationFailureAcceptsOnlyStableSanitizedCodes(t *testing.T) {
	handler, raw, mutations := newSkillMutationActivationTestHandler(
		t, []string{botRuntimeSkillMutationScope, botRuntimeSkillActivationScope},
	)
	handler.SetRollout(true, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/bot/skill-mutations/101/activation", strings.NewReader(
		`{"appliedDefinitionRevision":12,"result":"failed","errorCode":"PACKAGE_HASH_MISMATCH"}`,
	))
	req.Header.Set(botRuntimeCredentialHeader, raw)
	rec := httptest.NewRecorder()
	handler.Handle(rec, req)
	if rec.Code != http.StatusOK || mutations.failureInput == nil || !mutations.failureInput.Permanent {
		t.Fatalf("status=%d input=%#v body=%s", rec.Code, mutations.failureInput, rec.Body.String())
	}
	if mutations.failureInput.ErrorSummary != "A Skill package failed integrity verification" ||
		strings.Contains(mutations.failureInput.ErrorSummary, "token") {
		t.Fatalf("unsafe failure summary=%q", mutations.failureInput.ErrorSummary)
	}

	unsupported := httptest.NewRequest(http.MethodPost, "/api/bot/skill-mutations/101/activation", strings.NewReader(
		`{"appliedDefinitionRevision":12,"result":"failed","errorCode":"C:\\secret\\token.txt"}`,
	))
	unsupported.Header.Set(botRuntimeCredentialHeader, raw)
	unsupportedRec := httptest.NewRecorder()
	handler.Handle(unsupportedRec, unsupported)
	if unsupportedRec.Code != http.StatusBadRequest {
		t.Fatalf("unsupported error status=%d body=%s", unsupportedRec.Code, unsupportedRec.Body.String())
	}
}
