package server

import (
	"bytes"
	"context"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/openchat/openchat/server/store/types"
)

type memoryPushSubscriptionStore struct {
	subscriptions  []*types.PushSubscription
	upserted       *types.PushSubscription
	deletedUID     int64
	deleted        string
	deletedStale   []string
	upsertErr      error
	listErr        error
	deleteErr      error
	staleDeleteErr error
}

func (m *memoryPushSubscriptionStore) UpsertPushSubscription(subscription *types.PushSubscription) error {
	if m.upsertErr != nil {
		return m.upsertErr
	}
	copy := *subscription
	m.upserted = &copy
	return nil
}

func (m *memoryPushSubscriptionStore) ListPushSubscriptions(uid int64) ([]*types.PushSubscription, error) {
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.subscriptions, nil
}

func (m *memoryPushSubscriptionStore) DeletePushSubscription(uid int64, endpoint string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.deletedUID = uid
	m.deleted = endpoint
	return nil
}

func (m *memoryPushSubscriptionStore) DeletePushSubscriptionByEndpoint(endpoint string) error {
	if m.staleDeleteErr != nil {
		return m.staleDeleteErr
	}
	m.deletedStale = append(m.deletedStale, endpoint)
	return nil
}

func enabledPushService(subscriptionStore *memoryPushSubscriptionStore) *PushNotificationService {
	service := NewPushNotificationServiceWithConfig(subscriptionStore, PushNotificationConfig{
		PublicKey:  "public-key",
		PrivateKey: "private-key",
		Subject:    "mailto:push@example.com",
	})
	service.logf = func(string, ...interface{}) {}
	return service
}

func validPushKeys(t *testing.T) (string, string) {
	t.Helper()
	_, x, y, err := elliptic.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate P-256 key: %v", err)
	}
	p256dh := base64.RawURLEncoding.EncodeToString(elliptic.Marshal(elliptic.P256(), x, y))
	authBytes := make([]byte, 16)
	if _, err := rand.Read(authBytes); err != nil {
		t.Fatalf("generate auth key: %v", err)
	}
	return p256dh, base64.RawURLEncoding.EncodeToString(authBytes)
}

func pushRequest(t *testing.T, method, body string, uid int64) *http.Request {
	t.Helper()
	req := httptest.NewRequest(method, "/api/push/subscription", strings.NewReader(body))
	if uid > 0 {
		req = req.WithContext(context.WithValue(req.Context(), uidKey, uid))
	}
	return req
}

func TestPushNotificationStatusDisabled(t *testing.T) {
	service := NewPushNotificationServiceWithConfig(nil, PushNotificationConfig{})
	recorder := httptest.NewRecorder()
	service.HandleStatus(recorder, httptest.NewRequest(http.MethodGet, "/api/push/status", nil))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	var response struct {
		Enabled   bool   `json:"enabled"`
		PublicKey string `json:"public_key"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response.Enabled || response.PublicKey != "" {
		t.Fatalf("disabled response = %+v", response)
	}

	recorder = httptest.NewRecorder()
	service.HandleSubscription(recorder, pushRequest(t, http.MethodPost, `{}`, 41))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("disabled subscription status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}

func TestPushNotificationDisabledSendIsNoOp(t *testing.T) {
	service := NewPushNotificationServiceWithConfig(nil, PushNotificationConfig{})
	service.send = func(context.Context, []byte, *webpush.Subscription, *webpush.Options) (*http.Response, error) {
		t.Fatal("disabled service attempted delivery")
		return nil, nil
	}
	if err := service.SendToUser(context.Background(), 41, PushNotification{Title: "title"}); err != nil {
		t.Fatalf("disabled SendToUser returned error: %v", err)
	}
}

func TestValidatePushEndpointRejectsLocalAndPrivateTargets(t *testing.T) {
	tests := []string{
		"https://localhost/push",
		"https://service.local/push",
		"https://127.0.0.1/push",
		"https://10.0.0.1/push",
		"https://192.168.1.1/push",
		"https://[::1]/push",
		"https://push.example.test:8443/push",
	}
	for _, endpoint := range tests {
		t.Run(endpoint, func(t *testing.T) {
			if _, err := validatePushEndpoint(endpoint); err == nil {
				t.Fatalf("validatePushEndpoint(%q) error = nil", endpoint)
			}
		})
	}

	const valid = "https://push.example.test/subscription/one"
	if endpoint, err := validatePushEndpoint(valid); err != nil || endpoint != valid {
		t.Fatalf("validatePushEndpoint(%q) = %q, %v", valid, endpoint, err)
	}
}

func TestPushNotificationSubscribeUsesAuthenticatedUID(t *testing.T) {
	store := &memoryPushSubscriptionStore{}
	service := enabledPushService(store)
	p256dh, auth := validPushKeys(t)
	body := `{"endpoint":"https://push.example.test/subscription/one","keys":{"p256dh":"` + p256dh + `","auth":"` + auth + `"}}`

	recorder := httptest.NewRecorder()
	service.HandleSubscription(recorder, pushRequest(t, http.MethodPost, body, 73))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}
	if store.upserted == nil {
		t.Fatal("subscription was not stored")
	}
	if store.upserted.UID != 73 || store.upserted.Endpoint != "https://push.example.test/subscription/one" || store.upserted.P256DH != p256dh || store.upserted.Auth != auth {
		t.Fatalf("stored subscription = %+v", store.upserted)
	}
}

func TestPushNotificationSubscriptionRequiresJWTContextAndStrictBody(t *testing.T) {
	store := &memoryPushSubscriptionStore{}
	service := enabledPushService(store)
	p256dh, auth := validPushKeys(t)

	tests := []struct {
		name string
		body string
		uid  int64
	}{
		{name: "missing auth", body: `{}`, uid: 0},
		{name: "non HTTPS endpoint", body: `{"endpoint":"http://push.example.test/a","keys":{"p256dh":"` + p256dh + `","auth":"` + auth + `"}}`, uid: 1},
		{name: "invalid p256dh", body: `{"endpoint":"https://push.example.test/a","keys":{"p256dh":"bad","auth":"` + auth + `"}}`, uid: 1},
		{name: "unknown field", body: `{"endpoint":"https://push.example.test/a","keys":{"p256dh":"` + p256dh + `","auth":"` + auth + `"},"uid":99}`, uid: 1},
		{name: "trailing JSON", body: `{"endpoint":"https://push.example.test/a","keys":{"p256dh":"` + p256dh + `","auth":"` + auth + `"}} {}`, uid: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			service.HandleSubscription(recorder, pushRequest(t, http.MethodPost, test.body, test.uid))
			want := http.StatusBadRequest
			if test.uid == 0 {
				want = http.StatusUnauthorized
			}
			if recorder.Code != want {
				t.Fatalf("status = %d, want %d; body=%s", recorder.Code, want, recorder.Body.String())
			}
		})
	}
	if store.upserted != nil {
		t.Fatalf("invalid request stored subscription: %+v", store.upserted)
	}
}

func TestPushNotificationSubscriptionHandlerAcceptsJWT(t *testing.T) {
	store := &memoryPushSubscriptionStore{}
	service := enabledPushService(store)
	p256dh, auth := validPushKeys(t)
	token, err := GenerateToken(88, "push-user", "push@example.com")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	body := `{"endpoint":"https://push.example.test/jwt","keys":{"p256dh":"` + p256dh + `","auth":"` + auth + `"}}`
	req := httptest.NewRequest(http.MethodPost, "/api/push/subscription", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()

	service.SubscriptionHandler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusCreated, recorder.Body.String())
	}
	if store.upserted == nil || store.upserted.UID != 88 {
		t.Fatalf("stored subscription = %+v", store.upserted)
	}
}

func TestPushNotificationDeleteSubscription(t *testing.T) {
	store := &memoryPushSubscriptionStore{}
	service := enabledPushService(store)
	recorder := httptest.NewRecorder()
	service.HandleSubscription(recorder, pushRequest(t, http.MethodDelete, `{"endpoint":"https://push.example.test/subscription/delete"}`, 104))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if store.deletedUID != 104 || store.deleted != "https://push.example.test/subscription/delete" {
		t.Fatalf("delete called with uid=%d endpoint=%q", store.deletedUID, store.deleted)
	}
}

func TestPushNotificationSendCleansExpiredSubscriptions(t *testing.T) {
	store := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{
		{Endpoint: "https://push.example.test/gone", P256DH: "p256dh", Auth: "auth"},
		{Endpoint: "https://push.example.test/missing", P256DH: "p256dh", Auth: "auth"},
		{Endpoint: "https://push.example.test/ok", P256DH: "p256dh", Auth: "auth"},
	}}
	service := enabledPushService(store)
	var payloads [][]byte
	service.send = func(_ context.Context, payload []byte, subscription *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		payloads = append(payloads, bytes.Clone(payload))
		status := http.StatusCreated
		switch subscription.Endpoint {
		case "https://push.example.test/gone":
			status = http.StatusGone
		case "https://push.example.test/missing":
			status = http.StatusNotFound
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("provider body"))}, nil
	}

	err := service.SendToUser(context.Background(), 15, PushNotification{
		Title: "New message",
		Body:  "Open Cats Company to read it",
		Topic: "conversation",
		URL:   "/conversations/active",
		Tag:   "message",
	})
	if err != nil {
		t.Fatalf("SendToUser returned error: %v", err)
	}
	if len(store.deletedStale) != 2 || store.deletedStale[0] != "https://push.example.test/gone" || store.deletedStale[1] != "https://push.example.test/missing" {
		t.Fatalf("deleted stale endpoints = %#v", store.deletedStale)
	}
	if len(payloads) != 3 {
		t.Fatalf("sent payload count = %d, want 3", len(payloads))
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(payloads[0], &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload) != 5 {
		t.Fatalf("payload has unexpected metadata: %s", payloads[0])
	}
	for _, key := range []string{"title", "body", "topic", "url", "tag"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("payload missing %q: %s", key, payloads[0])
		}
	}
}

func TestPushNotificationSendReportsProviderErrors(t *testing.T) {
	store := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{
		{Endpoint: "https://push.example.test/error", P256DH: "p256dh", Auth: "auth"},
		{Endpoint: "https://push.example.test/unavailable", P256DH: "p256dh", Auth: "auth"},
	}}
	service := enabledPushService(store)
	service.send = func(_ context.Context, _ []byte, subscription *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		if strings.HasSuffix(subscription.Endpoint, "/error") {
			return nil, errors.New("network failure")
		}
		return &http.Response{StatusCode: http.StatusServiceUnavailable, Body: io.NopCloser(strings.NewReader(""))}, nil
	}

	if err := service.SendToUser(context.Background(), 15, PushNotification{Title: "title"}); err == nil {
		t.Fatal("SendToUser error = nil, want provider errors")
	}
	if len(store.deletedStale) != 0 {
		t.Fatalf("non-expired subscriptions were deleted: %#v", store.deletedStale)
	}
}
