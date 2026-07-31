package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type artifactRuntimeConfigTestStore struct {
	store.Store
	users   map[int64]*types.User
	botKeys map[string]int64
}

func (s artifactRuntimeConfigTestStore) GetUser(id int64) (*types.User, error) {
	return s.users[id], nil
}

func (s artifactRuntimeConfigTestStore) GetBotByAPIKey(apiKey string) (int64, error) {
	return s.botKeys[apiKey], nil
}

func TestArtifactRuntimeConfigReturnsAuthenticatedBotConfig(t *testing.T) {
	t.Setenv("CATSCO_ARTIFACT_DNS_ACCESS_KEY", "test-access")
	t.Setenv("CATSCO_ARTIFACT_DNS_SECRET_KEY", "test-secret")
	t.Setenv("CATSCO_ARTIFACT_DNS_ZONE", "catsco.fun")
	t.Setenv("CATSCO_ARTIFACT_HOST_SUFFIX", "artifacts.catsco.fun")
	const apiKey = "cc_217_runtime"
	db := artifactRuntimeConfigTestStore{
		users: map[int64]*types.User{
			535: {ID: 535, Username: "runtime-bot", AccountType: types.AccountBot, State: 0},
		},
		botKeys: map[string]int64{apiKey: 535},
	}
	handler := BotAPIKeyMiddlewareWithDB(db)(NewArtifactRuntimeConfigHandlerFromEnv().Handle)
	req := httptest.NewRequest(http.MethodGet, "/api/bot/artifact-runtime-config", nil)
	req.Header.Set("Authorization", "ApiKey "+apiKey)
	rec := httptest.NewRecorder()

	handler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control=%q want=no-store", got)
	}
	var payload artifactRuntimeConfigResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if payload.ContractVersion != artifactRuntimeConfigContract ||
		payload.AgentUID != "535" ||
		payload.DNSProvider != "volcengine" ||
		payload.DNSZone != "catsco.fun" ||
		payload.HostSuffix != "artifacts.catsco.fun" {
		t.Fatalf("unexpected runtime config: %+v", payload)
	}
	if payload.Credentials.AccessKey != "test-access" || payload.Credentials.SecretKey != "test-secret" {
		t.Fatalf("unexpected credentials: %+v", payload.Credentials)
	}
}

func TestArtifactRuntimeConfigRequiresServerCredentials(t *testing.T) {
	t.Setenv("CATSCO_ARTIFACT_DNS_ACCESS_KEY", "")
	t.Setenv("CATSCO_ARTIFACT_DNS_SECRET_KEY", "")
	t.Setenv("VOLC_ACCESSKEY", "")
	t.Setenv("VOLC_SECRETKEY", "")
	handler := NewArtifactRuntimeConfigHandlerFromEnv()
	req := httptest.NewRequest(http.MethodGet, "/api/bot/artifact-runtime-config", nil)
	req = req.WithContext(context.WithValue(req.Context(), uidKey, int64(535)))
	rec := httptest.NewRecorder()

	handler.Handle(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=%d body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "access") || strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("configuration error exposed secret details: %s", rec.Body.String())
	}
}

func TestArtifactRuntimeConfigRejectsUntrustedOrWrongMethod(t *testing.T) {
	t.Setenv("CATSCO_ARTIFACT_DNS_ACCESS_KEY", "test-access")
	t.Setenv("CATSCO_ARTIFACT_DNS_SECRET_KEY", "test-secret")
	handler := NewArtifactRuntimeConfigHandlerFromEnv()

	missingContext := httptest.NewRequest(http.MethodGet, "/api/bot/artifact-runtime-config", nil)
	missingContextRec := httptest.NewRecorder()
	handler.Handle(missingContextRec, missingContext)
	if missingContextRec.Code != http.StatusUnauthorized {
		t.Fatalf("missing context status=%d want=%d", missingContextRec.Code, http.StatusUnauthorized)
	}

	wrongMethod := httptest.NewRequest(http.MethodPost, "/api/bot/artifact-runtime-config", nil)
	wrongMethod = wrongMethod.WithContext(context.WithValue(wrongMethod.Context(), uidKey, int64(535)))
	wrongMethodRec := httptest.NewRecorder()
	handler.Handle(wrongMethodRec, wrongMethod)
	if wrongMethodRec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("wrong method status=%d want=%d", wrongMethodRec.Code, http.StatusMethodNotAllowed)
	}
	if got := wrongMethodRec.Header().Get("Allow"); got != http.MethodGet {
		t.Fatalf("Allow=%q want=%q", got, http.MethodGet)
	}
}
