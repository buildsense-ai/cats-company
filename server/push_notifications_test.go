package server

import (
	"bytes"
	"context"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type memoryPushSubscriptionStore struct {
	subscriptions           []*types.PushSubscription
	upserted                *types.PushSubscription
	deletedUID              int64
	deleted                 string
	deletedRegistrationID   string
	deletedAll              bool
	deletedRegistrationOnly string
	deletedScoped           []string
	upsertErr               error
	listErr                 error
	listBlock               <-chan struct{}
	deleteErr               error
	beforeDelete            func()
}

type pushHubUserStore struct {
	store.Store
	users map[int64]*types.User
}

type pushHubConversationTitleStore struct {
	pushHubUserStore
	titles map[string]string
}

type mutedConversationPushStore struct {
	pushHubUserStore
	mutedTopics               map[string]bool
	notificationPreferenceErr error
	preferenceChecked         chan struct{}
}

func (s *mutedConversationPushStore) ListMutedConversationTopics(_ context.Context, _ int64, topicIDs []string) (map[string]bool, error) {
	if s.notificationPreferenceErr != nil {
		return nil, s.notificationPreferenceErr
	}
	muted := make(map[string]bool)
	for _, topicID := range topicIDs {
		if s.mutedTopics[topicID] {
			muted[topicID] = true
		}
	}
	return muted, nil
}

func (s *mutedConversationPushStore) SetConversationNotificationsMuted(_ context.Context, _ int64, topicID string, muted bool) error {
	if s.notificationPreferenceErr != nil {
		return s.notificationPreferenceErr
	}
	if s.mutedTopics == nil {
		s.mutedTopics = make(map[string]bool)
	}
	if muted {
		s.mutedTopics[topicID] = true
	} else {
		delete(s.mutedTopics, topicID)
	}
	return nil
}

func (s *mutedConversationPushStore) IsConversationNotificationsMuted(_ context.Context, _ int64, topicID string) (bool, error) {
	if s.preferenceChecked != nil {
		select {
		case s.preferenceChecked <- struct{}{}:
		default:
		}
	}
	if s.notificationPreferenceErr != nil {
		return false, s.notificationPreferenceErr
	}
	return s.mutedTopics[topicID], nil
}

func (s pushHubUserStore) GetUser(uid int64) (*types.User, error) {
	return s.users[uid], nil
}

func (s pushHubUserStore) GetGroup(int64) (*types.Group, error) {
	return nil, nil
}

func (s pushHubConversationTitleStore) GetConversationTitles(_ int64, topicIDs []string) (map[string]string, error) {
	titles := make(map[string]string, len(topicIDs))
	for _, topicID := range topicIDs {
		if title := s.titles[topicID]; title != "" {
			titles[topicID] = title
		}
	}
	return titles, nil
}

func (s pushHubConversationTitleStore) UpdateConversationTitle(_ int64, _, _ string) (bool, error) {
	return false, nil
}

func (m *memoryPushSubscriptionStore) UpsertPushSubscription(_ context.Context, subscription *types.PushSubscription, maxSubscriptions int) (bool, error) {
	if m.upsertErr != nil {
		return false, m.upsertErr
	}
	existingEndpoint := false
	for _, existing := range m.subscriptions {
		if existing != nil && existing.Endpoint == subscription.Endpoint {
			existingEndpoint = true
			break
		}
	}
	if !existingEndpoint && len(m.subscriptions) >= maxSubscriptions {
		return false, nil
	}
	copy := *subscription
	m.upserted = &copy
	return true, nil
}

func (m *memoryPushSubscriptionStore) ListPushSubscriptions(ctx context.Context, uid int64) ([]*types.PushSubscription, error) {
	if m.listBlock != nil {
		select {
		case <-m.listBlock:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if m.listErr != nil {
		return nil, m.listErr
	}
	return m.subscriptions, nil
}

func (m *memoryPushSubscriptionStore) DeletePushSubscription(_ context.Context, uid int64, endpoint, registrationID string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	if m.beforeDelete != nil {
		m.beforeDelete()
	}
	m.deletedUID = uid
	m.deleted = endpoint
	m.deletedRegistrationID = registrationID
	m.deletedScoped = append(m.deletedScoped, fmt.Sprintf("%d:%s", uid, endpoint))
	for index, subscription := range m.subscriptions {
		if subscription != nil && subscription.UID == uid && subscription.Endpoint == endpoint && subscription.RegistrationID == registrationID {
			m.subscriptions = append(m.subscriptions[:index], m.subscriptions[index+1:]...)
			break
		}
	}
	return nil
}

func (m *memoryPushSubscriptionStore) DeletePushSubscriptionsByEndpoint(_ context.Context, uid int64, endpoint string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.deletedUID = uid
	m.deleted = endpoint
	m.deletedAll = true
	for index := len(m.subscriptions) - 1; index >= 0; index-- {
		subscription := m.subscriptions[index]
		if subscription != nil && subscription.UID == uid && subscription.Endpoint == endpoint {
			m.subscriptions = append(m.subscriptions[:index], m.subscriptions[index+1:]...)
		}
	}
	return nil
}

func (m *memoryPushSubscriptionStore) DeletePushSubscriptionsByRegistrationID(_ context.Context, uid int64, registrationID string) error {
	if m.deleteErr != nil {
		return m.deleteErr
	}
	m.deletedUID = uid
	m.deletedRegistrationOnly = registrationID
	for index := len(m.subscriptions) - 1; index >= 0; index-- {
		subscription := m.subscriptions[index]
		if subscription != nil && subscription.UID == uid && subscription.RegistrationID == registrationID {
			m.subscriptions = append(m.subscriptions[:index], m.subscriptions[index+1:]...)
		}
	}
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

func TestValidatePushRelayURLRejectsUnsafeOrMalformedValues(t *testing.T) {
	tests := []string{
		"http://relay.example.test/v1/push/relay",
		"https://localhost/v1/push/relay",
		"https://relay.local/v1/push/relay",
		"https://127.0.0.1/v1/push/relay",
		"https://relay.example.test:8443/v1/push/relay",
		"https://user:password@relay.example.test/v1/push/relay",
		"https://relay.example.test/v1/push/relay#fragment",
		"https://" + strings.Repeat("a", maxPushEndpointLen),
	}
	for _, raw := range tests {
		t.Run(raw, func(t *testing.T) {
			if _, err := validatePushRelayURL(raw); err == nil {
				t.Fatalf("validatePushRelayURL(%q) error = nil", raw)
			}
		})
	}

	const valid = "https://relay.example.test/v1/push/relay"
	if parsed, err := validatePushRelayURL(valid); err != nil || parsed.String() != valid {
		t.Fatalf("validatePushRelayURL(%q) = %v, %v", valid, parsed, err)
	}
}

func TestPushHTTPClientRejectsPrivateDNSResolution(t *testing.T) {
	dialed := false
	client := newPushHTTPClientWithNetwork(
		func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("127.0.0.1")}}, nil
		},
		func(context.Context, string, string) (net.Conn, error) {
			dialed = true
			return nil, errors.New("unexpected dial")
		},
		pushRequestTimeout,
	)

	_, err := client.Get("https://push.example.test/subscription")
	if err == nil || !strings.Contains(err.Error(), "non-publicly routable") {
		t.Fatalf("private DNS resolution error = %v", err)
	}
	if dialed {
		t.Fatal("private DNS resolution reached the network dialer")
	}
}

func TestPublicPushIPAddressPolicy(t *testing.T) {
	tests := []struct {
		address string
		public  bool
	}{
		{address: "0.0.0.1"},
		{address: "10.0.0.1"},
		{address: "100.64.0.1"},
		{address: "127.0.0.1"},
		{address: "169.254.1.1"},
		{address: "192.0.2.1"},
		{address: "198.18.0.1"},
		{address: "240.0.0.1"},
		{address: "::1"},
		{address: "::192.0.2.1"},
		{address: "::ffff:0:127.0.0.1"},
		{address: "64:ff9b::a00:1"},
		{address: "100::1"},
		{address: "2001:db8::1"},
		{address: "fc00::1"},
		{address: "fe80::1"},
		{address: "fec0::1"},
		{address: "8.8.8.8", public: true},
		{address: "2606:4700:4700::1111", public: true},
	}
	for _, test := range tests {
		t.Run(test.address, func(t *testing.T) {
			if got := isPublicPushIP(net.ParseIP(test.address)); got != test.public {
				t.Fatalf("isPublicPushIP(%q) = %t, want %t", test.address, got, test.public)
			}
		})
	}
}

func TestPushHTTPClientDoesNotFollowRedirects(t *testing.T) {
	targetHit := false
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHit = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer target.Close()

	start := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL)
		w.WriteHeader(http.StatusTemporaryRedirect)
	}))
	defer start.Close()

	var dialedAddress string
	client := newPushHTTPClientWithNetwork(
		func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		},
		func(ctx context.Context, network, address string) (net.Conn, error) {
			dialedAddress = address
			var dialer net.Dialer
			return dialer.DialContext(ctx, network, start.Listener.Addr().String())
		},
		pushRequestTimeout,
	)

	response, err := client.Get("http://push.example.test/subscription")
	if err != nil {
		t.Fatalf("redirect request: %v", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusTemporaryRedirect)
	}
	if targetHit {
		t.Fatal("push HTTP client followed a redirect")
	}
	if dialedAddress != "93.184.216.34:80" {
		t.Fatalf("dialed address = %q, want resolved public IP", dialedAddress)
	}
	if client.Timeout != 15*time.Second {
		t.Fatalf("timeout = %v, want 15s", client.Timeout)
	}
}

func TestPushHTTPClientTimesOutStalledConnections(t *testing.T) {
	client := newPushHTTPClientWithNetwork(
		func(context.Context, string) ([]net.IPAddr, error) {
			return []net.IPAddr{{IP: net.ParseIP("93.184.216.34")}}, nil
		},
		func(ctx context.Context, _, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		},
		20*time.Millisecond,
	)

	started := time.Now()
	_, err := client.Get("https://push.example.test/subscription")
	if err == nil || !strings.Contains(err.Error(), "Client.Timeout") {
		t.Fatalf("stalled request error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("stalled request took %v, want under 1s", elapsed)
	}
}

func TestPushRelayTransportForwardsSignedRequestWithoutChangingItsTarget(t *testing.T) {
	relayURL, err := validatePushRelayURL("https://relay.example.test/v1/push/relay")
	if err != nil {
		t.Fatalf("validatePushRelayURL: %v", err)
	}
	var received *http.Request
	transport := &pushRelayRoundTripper{
		relayURL: relayURL,
		token:    "relay-token",
		base: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			received = request.Clone(request.Context())
			return &http.Response{
				StatusCode: http.StatusCreated,
				Body:       io.NopCloser(strings.NewReader("")),
			}, nil
		}),
	}
	providerEndpoint := "https://fcm.googleapis.com/fcm/send/subscription-token"
	request := httptest.NewRequest(http.MethodPost, providerEndpoint, strings.NewReader("encrypted-payload"))
	request.RequestURI = ""
	request.Header.Set("Authorization", "vapid signed-for-fcm")
	request.Header.Set("Content-Encoding", "aes128gcm")

	response, err := transport.RoundTrip(request)
	if err != nil {
		t.Fatalf("relay RoundTrip: %v", err)
	}
	defer response.Body.Close()
	if received == nil {
		t.Fatal("relay transport did not call its base transport")
	}
	if got, want := received.URL.String(), "https://relay.example.test/v1/push/relay"; got != want {
		t.Fatalf("relay request URL = %q, want %q", got, want)
	}
	if got := received.Header.Get(pushRelayEndpointHeader); got != providerEndpoint {
		t.Fatalf("relay target header = %q, want original endpoint %q", got, providerEndpoint)
	}
	if got := received.Header.Get(pushRelayTokenHeader); got != "relay-token" {
		t.Fatalf("relay token header = %q, want configured token", got)
	}
	if got := received.Header.Get("Authorization"); got != "vapid signed-for-fcm" {
		t.Fatalf("VAPID authorization header = %q, want original request header", got)
	}
	if got := received.Header.Get("Content-Encoding"); got != "aes128gcm" {
		t.Fatalf("content encoding = %q, want aes128gcm", got)
	}
}

func TestPushRelayConfigIsAllOrNothing(t *testing.T) {
	partial := NewPushNotificationServiceWithConfig(&memoryPushSubscriptionStore{}, PushNotificationConfig{
		PublicKey:  "public-key",
		PrivateKey: "private-key",
		Subject:    "mailto:push@example.com",
		RelayURL:   "https://relay.example.test/v1/push/relay",
	})
	if partial.Enabled() {
		t.Fatal("partially configured relay unexpectedly enabled push delivery")
	}
	if err := partial.ConfigError(); err == nil || !strings.Contains(err.Error(), "configured together") {
		t.Fatalf("partial relay config error = %v", err)
	}

	client, err := newPushHTTPClientWithRelay("https://relay.example.test/v1/push/relay", "relay-token")
	if err != nil {
		t.Fatalf("newPushHTTPClientWithRelay: %v", err)
	}
	if _, ok := client.Transport.(*pushRelayRoundTripper); !ok {
		t.Fatalf("relay client transport = %T, want *pushRelayRoundTripper", client.Transport)
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

func TestPushNotificationSubscriptionLimitRejectsOnlyNewEndpoints(t *testing.T) {
	store := &memoryPushSubscriptionStore{}
	for index := 0; index < 10; index++ {
		store.subscriptions = append(store.subscriptions, &types.PushSubscription{
			UID:      73,
			Endpoint: fmt.Sprintf("https://push.example.test/subscription/%d", index),
		})
	}
	service := enabledPushService(store)
	p256dh, auth := validPushKeys(t)

	recorder := httptest.NewRecorder()
	body := `{"endpoint":"https://push.example.test/subscription/new","keys":{"p256dh":"` + p256dh + `","auth":"` + auth + `"}}`
	service.HandleSubscription(recorder, pushRequest(t, http.MethodPost, body, 73))
	if recorder.Code != http.StatusConflict {
		t.Fatalf("new endpoint status = %d, want %d; body=%s", recorder.Code, http.StatusConflict, recorder.Body.String())
	}
	if store.upserted != nil {
		t.Fatalf("subscription beyond the limit was stored: %+v", store.upserted)
	}

	recorder = httptest.NewRecorder()
	body = `{"endpoint":"https://push.example.test/subscription/0","keys":{"p256dh":"` + p256dh + `","auth":"` + auth + `"}}`
	service.HandleSubscription(recorder, pushRequest(t, http.MethodPost, body, 73))
	if recorder.Code != http.StatusCreated {
		t.Fatalf("existing endpoint status = %d, want %d; body=%s", recorder.Code, http.StatusCreated, recorder.Body.String())
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

func TestPushNotificationDeleteSubscription(t *testing.T) {
	store := &memoryPushSubscriptionStore{}
	service := enabledPushService(store)
	recorder := httptest.NewRecorder()
	service.HandleSubscription(recorder, pushRequest(t, http.MethodDelete, `{"endpoint":"https://push.example.test/subscription/delete","registration_id":"session-old"}`, 104))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if store.deletedUID != 104 || store.deleted != "https://push.example.test/subscription/delete" {
		t.Fatalf("delete called with uid=%d endpoint=%q", store.deletedUID, store.deleted)
	}
	if store.deletedRegistrationID != "session-old" {
		t.Fatalf("delete registration id = %q", store.deletedRegistrationID)
	}
}

func TestPushNotificationDeleteSubscriptionAllowsLegacyEmptyRegistrationID(t *testing.T) {
	store := &memoryPushSubscriptionStore{}
	service := enabledPushService(store)
	recorder := httptest.NewRecorder()
	service.HandleSubscription(recorder, pushRequest(t, http.MethodDelete, `{"endpoint":"https://push.example.test/subscription/delete","registration_id":" "}`, 104))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if store.deleted != "https://push.example.test/subscription/delete" || store.deletedRegistrationID != "" {
		t.Fatalf("legacy delete called with endpoint=%q registration_id=%q", store.deleted, store.deletedRegistrationID)
	}
}

func TestPushNotificationDeleteAllRegistrationsForCurrentUserEndpoint(t *testing.T) {
	store := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{
		{UID: 104, Endpoint: "https://push.example.test/subscription/shared", RegistrationID: "other-tab"},
		{UID: 105, Endpoint: "https://push.example.test/subscription/other-user", RegistrationID: "other-user"},
	}}
	service := enabledPushService(store)
	recorder := httptest.NewRecorder()
	service.HandleSubscription(recorder, pushRequest(t, http.MethodDelete, `{"endpoint":"https://push.example.test/subscription/shared","all_registrations":true}`, 104))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if !store.deletedAll || store.deletedUID != 104 || store.deleted != "https://push.example.test/subscription/shared" {
		t.Fatalf("delete all called with all=%v uid=%d endpoint=%q", store.deletedAll, store.deletedUID, store.deleted)
	}
	if len(store.subscriptions) != 1 || store.subscriptions[0].UID != 105 {
		t.Fatalf("delete all removed another user's subscription: %+v", store.subscriptions)
	}
}

func TestPushNotificationDeleteOrphanedRegistrationWithoutEndpoint(t *testing.T) {
	store := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{
		{UID: 104, Endpoint: "https://push.example.test/subscription/orphan", RegistrationID: "registration-current"},
		{UID: 104, Endpoint: "https://push.example.test/subscription/other", RegistrationID: "registration-other"},
	}}
	service := enabledPushService(store)
	recorder := httptest.NewRecorder()
	service.HandleSubscription(recorder, pushRequest(t, http.MethodDelete, `{"registration_id":"registration-current"}`, 104))

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusOK, recorder.Body.String())
	}
	if store.deletedRegistrationOnly != "registration-current" || len(store.subscriptions) != 1 {
		t.Fatalf("registration cleanup = %q subscriptions=%+v", store.deletedRegistrationOnly, store.subscriptions)
	}
}

func TestPushNotificationTestTargetsCurrentBrowserRegistration(t *testing.T) {
	store := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{
		{UID: 104, Endpoint: "https://push.example.test/current", P256DH: "p256dh", Auth: "auth", RegistrationID: "registration-current"},
		{UID: 104, Endpoint: "https://push.example.test/other", P256DH: "p256dh", Auth: "auth", RegistrationID: "registration-other"},
	}}
	service := enabledPushService(store)
	var delivered []string
	service.send = func(ctx context.Context, payload []byte, subscription *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > pushRequestTimeout {
			t.Fatalf("test delivery deadline = %v, want within %v", deadline, pushRequestTimeout)
		}
		delivered = append(delivered, subscription.Endpoint)
		var notification PushNotification
		if err := json.Unmarshal(payload, &notification); err != nil {
			t.Fatalf("decode test payload %s: %v", payload, err)
		}
		if notification.Title != "CatsCo 测试通知" {
			t.Fatalf("test payload = %s", payload)
		}
		if !strings.HasPrefix(notification.Tag, "catsco-push-test-") || notification.Tag == "catsco-push-test" {
			t.Fatalf("test notification tag = %q, want unique tag", notification.Tag)
		}
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
	}

	recorder := httptest.NewRecorder()
	service.HandleTest(recorder, pushRequest(t, http.MethodPost, `{"registration_id":"registration-current"}`, 104))

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
	want := []string{"https://push.example.test/current"}
	if fmt.Sprint(delivered) != fmt.Sprint(want) {
		t.Fatalf("delivered endpoints = %#v, want %#v", delivered, want)
	}
}

func TestPushNotificationTestRejectsMissingBrowserRegistration(t *testing.T) {
	store := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{{
		UID: 104, Endpoint: "https://push.example.test/current", RegistrationID: "registration-current",
	}}}
	service := enabledPushService(store)
	service.send = func(_ context.Context, _ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		t.Fatal("test notification unexpectedly sent")
		return nil, nil
	}

	recorder := httptest.NewRecorder()
	service.HandleTest(recorder, pushRequest(t, http.MethodPost, `{"registration_id":"registration-stale"}`, 104))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusConflict, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"code":"push_subscription_missing"`) {
		t.Fatalf("body = %s, want stable missing-subscription code", recorder.Body.String())
	}
}

func TestPushNotificationTestReportsExpiredSubscription(t *testing.T) {
	const endpoint = "https://push.example.test/expired"
	store := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{{
		UID: 104, Endpoint: endpoint, P256DH: "p256dh", Auth: "auth", RegistrationID: "registration-current",
	}}}
	service := enabledPushService(store)
	service.send = func(_ context.Context, _ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusGone, Body: io.NopCloser(strings.NewReader(""))}, nil
	}

	recorder := httptest.NewRecorder()
	service.HandleTest(recorder, pushRequest(t, http.MethodPost, `{"registration_id":"registration-current"}`, 104))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusConflict, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"code":"push_subscription_expired"`) {
		t.Fatalf("body = %s, want stable expired-subscription code", recorder.Body.String())
	}
	if store.deleted != endpoint {
		t.Fatalf("expired endpoint was not removed: deleted=%q", store.deleted)
	}
}

func TestPushNotificationTestReportsProviderRejection(t *testing.T) {
	store := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{{
		UID: 104, Endpoint: "https://push.example.test/rejected", P256DH: "p256dh", Auth: "auth", RegistrationID: "registration-current",
	}}}
	service := enabledPushService(store)
	service.send = func(_ context.Context, _ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadGateway, Body: io.NopCloser(strings.NewReader(""))}, nil
	}

	recorder := httptest.NewRecorder()
	service.HandleTest(recorder, pushRequest(t, http.MethodPost, `{"registration_id":"registration-current"}`, 104))

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadGateway, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"code":"push_provider_rejected"`) {
		t.Fatalf("body = %s, want stable provider-rejection code", recorder.Body.String())
	}
}

func TestPushNotificationTestAcceptsPartialProviderSuccess(t *testing.T) {
	store := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{
		{UID: 104, Endpoint: "https://push.example.test/accepted", P256DH: "p256dh", Auth: "auth", RegistrationID: "registration-current"},
		{UID: 104, Endpoint: "https://push.example.test/rejected", P256DH: "p256dh", Auth: "auth", RegistrationID: "registration-current"},
	}}
	service := enabledPushService(store)
	service.send = func(_ context.Context, _ []byte, subscription *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		status := http.StatusCreated
		if strings.HasSuffix(subscription.Endpoint, "/rejected") {
			status = http.StatusBadGateway
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
	}

	recorder := httptest.NewRecorder()
	service.HandleTest(recorder, pushRequest(t, http.MethodPost, `{"registration_id":"registration-current"}`, 104))

	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusAccepted, recorder.Body.String())
	}
}

func TestPushNotificationTestReportsMixedExpiredAndRejectedAsRejection(t *testing.T) {
	store := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{
		{UID: 104, Endpoint: "https://push.example.test/rejected", P256DH: "p256dh", Auth: "auth", RegistrationID: "registration-current"},
		{UID: 104, Endpoint: "https://push.example.test/expired", P256DH: "p256dh", Auth: "auth", RegistrationID: "registration-current"},
	}}
	service := enabledPushService(store)
	service.send = func(_ context.Context, _ []byte, subscription *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		status := http.StatusBadGateway
		if strings.HasSuffix(subscription.Endpoint, "/expired") {
			status = http.StatusGone
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
	}

	recorder := httptest.NewRecorder()
	service.HandleTest(recorder, pushRequest(t, http.MethodPost, `{"registration_id":"registration-current"}`, 104))

	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want %d; body=%s", recorder.Code, http.StatusBadGateway, recorder.Body.String())
	}
	if !strings.Contains(recorder.Body.String(), `"code":"push_provider_rejected"`) {
		t.Fatalf("body = %s, want stable provider-rejection code", recorder.Body.String())
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
	service.send = func(_ context.Context, payload []byte, subscription *webpush.Subscription, options *webpush.Options) (*http.Response, error) {
		if options.HTTPClient == nil {
			t.Fatal("push delivery omitted the restricted HTTP client")
		}
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
		URL:   "/conversations/active",
		Tag:   "message",
	})
	if err != nil {
		t.Fatalf("SendToUser returned error: %v", err)
	}
	wantDeleted := []string{
		"15:https://push.example.test/gone",
		"15:https://push.example.test/missing",
	}
	if fmt.Sprint(store.deletedScoped) != fmt.Sprint(wantDeleted) {
		t.Fatalf("deleted scoped endpoints = %#v, want %#v", store.deletedScoped, wantDeleted)
	}
	if len(payloads) != 3 {
		t.Fatalf("sent payload count = %d, want 3", len(payloads))
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(payloads[0], &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if len(payload) != 4 {
		t.Fatalf("payload has unexpected metadata: %s", payloads[0])
	}
	for _, key := range []string{"title", "body", "url", "tag"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("payload missing %q: %s", key, payloads[0])
		}
	}
}

func TestPushNotificationNormalizesMailtoSubjectForWebPushLibrary(t *testing.T) {
	store := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{{
		UID: 21, Endpoint: "https://push.example.test/current", P256DH: "p256dh", Auth: "auth",
	}}}
	service := enabledPushService(store)
	var subscriber string
	service.send = func(_ context.Context, _ []byte, _ *webpush.Subscription, options *webpush.Options) (*http.Response, error) {
		subscriber = options.Subscriber
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
	}

	if err := service.SendToUser(context.Background(), 21, PushNotification{Title: "test"}); err != nil {
		t.Fatalf("SendToUser returned error: %v", err)
	}
	if subscriber != "push@example.com" {
		t.Fatalf("web push subscriber = %q, want %q", subscriber, "push@example.com")
	}
}

func TestPushNotificationRelayExpiredCleanupRequiresProviderMarker(t *testing.T) {
	tests := []struct {
		name        string
		status      int
		marker      string
		wantDeleted bool
	}{
		{name: "unmarked relay 404", status: http.StatusNotFound},
		{name: "mismatched relay marker", status: http.StatusNotFound, marker: "410"},
		{name: "marked provider 404", status: http.StatusNotFound, marker: "404", wantDeleted: true},
		{name: "marked provider 410", status: http.StatusGone, marker: "410", wantDeleted: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const endpoint = "https://push.example.test/subscription/relay-cleanup"
			store := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{{
				Endpoint: endpoint,
				P256DH:   "p256dh",
				Auth:     "auth",
			}}}
			service := NewPushNotificationServiceWithConfig(store, PushNotificationConfig{
				PublicKey:  "public-key",
				PrivateKey: "private-key",
				Subject:    "mailto:push@example.com",
				RelayURL:   "https://relay.example.test/v1/push/relay",
				RelayToken: "relay-token",
			})
			service.logf = func(string, ...interface{}) {}
			service.send = func(context.Context, []byte, *webpush.Subscription, *webpush.Options) (*http.Response, error) {
				header := make(http.Header)
				if test.marker != "" {
					header.Set(pushRelayProviderStatusHeader, test.marker)
				}
				return &http.Response{
					StatusCode: test.status,
					Header:     header,
					Body:       io.NopCloser(strings.NewReader("")),
				}, nil
			}

			err := service.SendToUser(context.Background(), 15, PushNotification{Title: "title"})
			if test.wantDeleted {
				if err != nil {
					t.Fatalf("SendToUser returned error: %v", err)
				}
				if got := store.deleted; got != endpoint {
					t.Fatalf("deleted endpoint = %q, want %q", got, endpoint)
				}
				return
			}

			if err == nil || !strings.Contains(err.Error(), "without a provider response marker") {
				t.Fatalf("SendToUser error = %v, want relay marker error", err)
			}
			if len(store.deletedScoped) != 0 {
				t.Fatalf("relay-owned response deleted a subscription: %#v", store.deletedScoped)
			}
		})
	}
}

func TestPushNotificationExpiredCleanupDoesNotDeleteUpgradedLegacySubscription(t *testing.T) {
	const endpoint = "https://push.example.test/legacy-upgraded"
	store := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{{
		UID:            15,
		Endpoint:       endpoint,
		P256DH:         "p256dh",
		Auth:           "auth",
		RegistrationID: "",
	}}}
	store.beforeDelete = func() {
		store.subscriptions[0].RegistrationID = "session-new"
	}
	service := enabledPushService(store)
	service.send = func(_ context.Context, _ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusGone, Body: io.NopCloser(strings.NewReader(""))}, nil
	}

	err := service.SendToUser(context.Background(), 15, PushNotification{Title: "New message"})
	if err != nil {
		t.Fatalf("SendToUser returned error: %v", err)
	}
	if len(store.subscriptions) != 1 || store.subscriptions[0].RegistrationID != "session-new" {
		t.Fatalf("stale cleanup removed upgraded subscription: %+v", store.subscriptions)
	}
	if store.deletedRegistrationID != "" {
		t.Fatalf("cleanup registration id = %q, want legacy empty generation", store.deletedRegistrationID)
	}
}

func TestPushNotificationSendCapsDeliveriesPerUser(t *testing.T) {
	store := &memoryPushSubscriptionStore{}
	for index := 0; index < maxPushSubscriptionsPerUser+1; index++ {
		store.subscriptions = append(store.subscriptions, &types.PushSubscription{
			Endpoint: fmt.Sprintf("https://push.example.test/subscription/%d", index),
			P256DH:   "p256dh",
			Auth:     "auth",
		})
	}
	service := enabledPushService(store)
	deliveries := 0
	service.send = func(_ context.Context, _ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		deliveries++
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
	}

	if err := service.SendToUser(context.Background(), 15, PushNotification{Title: "title"}); err != nil {
		t.Fatalf("SendToUser returned error: %v", err)
	}
	if deliveries != maxPushSubscriptionsPerUser {
		t.Fatalf("deliveries = %d, want %d", deliveries, maxPushSubscriptionsPerUser)
	}
}

func TestPushNotificationEnqueueBoundsConcurrencyAndSetsDeadline(t *testing.T) {
	store := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{{
		Endpoint: "https://push.example.test/subscription/slow",
		P256DH:   "p256dh",
		Auth:     "auth",
	}}}
	service := enabledPushService(store)
	started := make(chan struct{}, 9)
	release := make(chan struct{})
	completed := make(chan struct{}, 9)
	service.send = func(ctx context.Context, _ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > 21*time.Second {
			t.Errorf("delivery deadline = %v, ok=%t", deadline, ok)
		}
		started <- struct{}{}
		<-release
		completed <- struct{}{}
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
	}

	for index := 0; index < 8; index++ {
		if !service.EnqueueToUser(15, PushNotification{Title: "title"}) {
			t.Fatalf("enqueue %d was rejected", index)
		}
	}
	for index := 0; index < 8; index++ {
		<-started
	}
	if !service.EnqueueToUser(15, PushNotification{Title: "queued"}) {
		t.Fatal("enqueue was rejected while workers were busy")
	}
	select {
	case <-started:
		t.Fatal("delivery exceeded the worker concurrency bound")
	case <-time.After(20 * time.Millisecond):
	}

	close(release)
	for index := 0; index < 9; index++ {
		<-completed
	}
}

func TestPushNotificationQueueIsBounded(t *testing.T) {
	store := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{{
		Endpoint: "https://push.example.test/subscription/slow",
		P256DH:   "p256dh",
		Auth:     "auth",
	}}}
	service := enabledPushService(store)
	totalAccepted := maxConcurrentPushDeliveries + maxQueuedPushDeliveries
	started := make(chan struct{}, totalAccepted)
	completed := make(chan struct{}, totalAccepted)
	release := make(chan struct{})
	service.send = func(_ context.Context, _ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		started <- struct{}{}
		<-release
		completed <- struct{}{}
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
	}

	for index := 0; index < maxConcurrentPushDeliveries; index++ {
		if !service.EnqueueToUser(15, PushNotification{Title: "running"}) {
			t.Fatalf("running delivery %d was rejected", index)
		}
	}
	for index := 0; index < maxConcurrentPushDeliveries; index++ {
		<-started
	}
	for index := 0; index < maxQueuedPushDeliveries; index++ {
		if !service.EnqueueToUser(15, PushNotification{Title: "queued"}) {
			t.Fatalf("queued delivery %d was rejected", index)
		}
	}
	if service.EnqueueToUser(15, PushNotification{Title: "overflow"}) {
		t.Fatal("delivery beyond the queue bound was accepted")
	}

	close(release)
	for index := 0; index < totalAccepted; index++ {
		<-completed
	}
}

func TestPushNotificationDeadlineCoversSubscriptionLookup(t *testing.T) {
	release := make(chan struct{})
	store := &memoryPushSubscriptionStore{listBlock: release}
	service := enabledPushService(store)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	go func() {
		time.Sleep(100 * time.Millisecond)
		close(release)
	}()

	started := time.Now()
	err := service.SendToUser(ctx, 15, PushNotification{Title: "title"})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("SendToUser error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 75*time.Millisecond {
		t.Fatalf("subscription lookup ignored deadline for %v", elapsed)
	}
}

func TestPushSubscriptionIDMatchesBrowserGoldenVector(t *testing.T) {
	const endpoint = "https://push.example.test/subscription/browser-profile"
	const want = "WUIrC4yppUY8v9TxFnhjVvwOgkISFt0ZOdGvyL0nals"
	if got := pushSubscriptionID(endpoint); got != want {
		t.Fatalf("pushSubscriptionID() = %q, want %q", got, want)
	}
}

func TestEnqueueUserPushQueuesOnlyWhenNoVisibleHumanClient(t *testing.T) {
	tests := []struct {
		name         string
		accountType  types.AccountType
		state        int
		clientStates []string
		wantPush     bool
	}{
		{name: "offline human", accountType: types.AccountHuman, wantPush: true},
		{name: "visible human without subscription identity", accountType: types.AccountHuman, clientStates: []string{"visible"}, wantPush: true},
		{name: "legacy connected human", accountType: types.AccountHuman, clientStates: []string{""}, wantPush: true},
		{name: "hidden human", accountType: types.AccountHuman, clientStates: []string{"hidden"}, wantPush: true},
		{name: "visible and hidden human without identity", accountType: types.AccountHuman, clientStates: []string{"visible", "hidden"}, wantPush: true},
		{name: "offline bot", accountType: types.AccountBot},
		{name: "disabled human", accountType: types.AccountHuman, state: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const uid int64 = 42
			pushStore := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{{
				Endpoint: "https://push.example.test/subscription/hub",
				P256DH:   "p256dh",
				Auth:     "auth",
			}}}
			service := enabledPushService(pushStore)
			delivered := make(chan struct{}, 1)
			service.send = func(_ context.Context, _ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
				delivered <- struct{}{}
				return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
			}
			hub := NewHub(pushHubUserStore{users: map[int64]*types.User{
				uid: {ID: uid, AccountType: test.accountType, State: test.state},
			}}, nil)
			hub.SetPushNotificationService(service)
			for _, pageVisibility := range test.clientStates {
				client := &Client{uid: uid, send: make(chan []byte, 1)}
				hub.addClient(client)
				hub.setClientPageVisibility(client, pageVisibility)
			}

			hub.enqueueOfflineUserPush(uid, "grp_7", "")

			select {
			case <-delivered:
				if !test.wantPush {
					t.Fatal("unexpected push delivery")
				}
			case <-time.After(100 * time.Millisecond):
				if test.wantPush {
					t.Fatal("expected push delivery")
				}
			}
		})
	}
}

func TestConversationNotificationMuteControlsOfflinePushDelivery(t *testing.T) {
	tests := []struct {
		name          string
		muted         bool
		preferenceErr error
		wantDelivery  bool
	}{
		{name: "muted conversation", muted: true},
		{name: "unmuted conversation", wantDelivery: true},
		{name: "preference lookup fails closed", preferenceErr: errors.New("database unavailable")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const (
				uid     = int64(42)
				topicID = "p2p_7_42"
			)
			pushStore := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{{
				Endpoint: "https://push.example.test/subscription/muted-conversation",
				P256DH:   "p256dh",
				Auth:     "auth",
			}}}
			service := enabledPushService(pushStore)
			delivered := make(chan struct{}, 1)
			service.send = func(_ context.Context, _ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
				delivered <- struct{}{}
				return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
			}
			db := &mutedConversationPushStore{
				pushHubUserStore: pushHubUserStore{users: map[int64]*types.User{
					uid: {ID: uid, AccountType: types.AccountHuman},
				}},
				mutedTopics:               map[string]bool{topicID: test.muted},
				notificationPreferenceErr: test.preferenceErr,
				preferenceChecked:         make(chan struct{}, 1),
			}
			hub := NewHub(db, nil)
			hub.SetPushNotificationService(service)

			if !hub.enqueueOfflineUserPush(uid, topicID, "task finished") {
				t.Fatal("offline push was not queued")
			}
			select {
			case <-db.preferenceChecked:
			case <-time.After(time.Second):
				t.Fatal("push worker did not check the conversation notification preference")
			}
			select {
			case <-delivered:
				if !test.wantDelivery {
					t.Fatal("muted conversation delivered a push notification")
				}
			case <-time.After(100 * time.Millisecond):
				if test.wantDelivery {
					t.Fatal("unmuted conversation did not deliver a push notification")
				}
			}
		})
	}
}

func TestEnqueueUserPushSuppressesOnlyFocusedSubscriptionOnTargetTopic(t *testing.T) {
	const uid int64 = 42
	pushStore := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{
		{
			UID:            uid,
			Endpoint:       "https://push.example.test/subscription/visible-device",
			P256DH:         "p256dh",
			Auth:           "auth",
			RegistrationID: "registration-visible",
		},
		{
			UID:            uid,
			Endpoint:       "https://push.example.test/subscription/other-device",
			P256DH:         "p256dh",
			Auth:           "auth",
			RegistrationID: "registration-other",
		},
	}}
	service := enabledPushService(pushStore)
	delivered := make(chan string, 2)
	service.send = func(_ context.Context, _ []byte, subscription *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		delivered <- subscription.Endpoint
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	hub := NewHub(pushHubUserStore{users: map[int64]*types.User{
		uid: {ID: uid, AccountType: types.AccountHuman},
	}}, nil)
	hub.SetPushNotificationService(service)
	visibleEndpoint := "https://push.example.test/subscription/visible-device"
	hub.addClient(&Client{uid: uid, messagingAttention: messagingClientAttention{
		SubscriptionID: pushSubscriptionID(visibleEndpoint),
		ActiveTopic:    "grp_7",
		Visible:        true,
		Focused:        true,
	}, send: make(chan []byte, 1)})

	hub.enqueueOfflineUserPush(uid, "grp_7", "")

	select {
	case endpoint := <-delivered:
		if endpoint != "https://push.example.test/subscription/other-device" {
			t.Fatalf("delivered endpoint = %q, want other device", endpoint)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("expected push delivery to other device")
	}
	select {
	case endpoint := <-delivered:
		t.Fatalf("unexpected additional push delivery to %q", endpoint)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestEnqueueUserPushDoesNotSuppressLegacyClientWithoutSubscriptionIdentity(t *testing.T) {
	const uid int64 = 42
	pushStore := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{
		{
			UID:            uid,
			Endpoint:       "https://push.example.test/subscription/device-a",
			P256DH:         "p256dh",
			Auth:           "auth",
			RegistrationID: "registration-a",
		},
		{
			UID:            uid,
			Endpoint:       "https://push.example.test/subscription/device-b",
			P256DH:         "p256dh",
			Auth:           "auth",
			RegistrationID: "registration-b",
		},
	}}
	service := enabledPushService(pushStore)
	delivered := make(chan string, 2)
	service.send = func(_ context.Context, _ []byte, subscription *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		delivered <- subscription.Endpoint
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	hub := NewHub(pushHubUserStore{users: map[int64]*types.User{
		uid: {ID: uid, AccountType: types.AccountHuman},
	}}, nil)
	hub.SetPushNotificationService(service)
	hub.addClient(&Client{uid: uid, messagingAttention: messagingClientAttention{
		ActiveTopic: "grp_7",
		Visible:     true,
		Focused:     true,
	}, send: make(chan []byte, 1)})

	hub.enqueueOfflineUserPush(uid, "grp_7", "")

	seen := map[string]bool{}
	for range 2 {
		select {
		case endpoint := <-delivered:
			seen[endpoint] = true
		case <-time.After(100 * time.Millisecond):
			t.Fatal("expected push delivery to every subscription")
		}
	}
	if len(seen) != 2 {
		t.Fatalf("delivered endpoints = %#v, want both subscriptions", seen)
	}
}

func TestShouldNotifyOfflineForFinalUserVisibleMessagesOnly(t *testing.T) {
	tests := []struct {
		name                     string
		data                     *MsgServerData
		suppressPushNotification bool
		want                     bool
	}{
		{name: "missing message"},
		{name: "transient text", data: &MsgServerData{SeqID: 0, Type: "text"}},
		{name: "final text", data: &MsgServerData{SeqID: 1, Type: "text"}, want: true},
		{name: "final image", data: &MsgServerData{SeqID: 1, Type: "image"}, want: true},
		{name: "final voice", data: &MsgServerData{SeqID: 1, Type: "voice"}, want: true},
		{name: "final file", data: &MsgServerData{SeqID: 1, Type: "file"}, want: true},
		{name: "future user-visible type", data: &MsgServerData{SeqID: 1, Type: "video"}, want: true},
		{name: "suppressed provider message", data: &MsgServerData{SeqID: 1, Type: "text"}, suppressPushNotification: true},
		{name: "runtime plan", data: &MsgServerData{SeqID: 1, Type: "runtime_plan"}},
		{name: "thinking", data: &MsgServerData{SeqID: 1, Type: "thinking"}},
		{name: "tool use", data: &MsgServerData{SeqID: 1, Type: "tool_use"}},
		{name: "tool result", data: &MsgServerData{SeqID: 1, Type: "tool_result"}},
		{name: "debug", data: &MsgServerData{SeqID: 1, Type: "debug"}},
		{name: "stream delta", data: &MsgServerData{SeqID: 1, Type: "stream_delta"}},
		{name: "stream cancel", data: &MsgServerData{SeqID: 1, Type: "stream_cancel"}},
		{name: "task status", data: &MsgServerData{SeqID: 1, Type: taskStatusType}},
		{name: "unknown progress type", data: &MsgServerData{SeqID: 1, Type: "progress"}},
		{name: "legacy working text", data: &MsgServerData{SeqID: 1, Type: "text", Content: "AI文本: 正在工作"}},
		{
			name: "internal blocks only",
			data: &MsgServerData{
				SeqID:         1,
				Type:          "text",
				ContentBlocks: []types.ContentBlock{{Type: "tool_result", Content: "private output"}},
			},
		},
		{
			name: "final answer block with thinking",
			data: &MsgServerData{
				SeqID: 1,
				Type:  "text",
				ContentBlocks: []types.ContentBlock{
					{Type: "thinking", Thinking: "working"},
					{Type: "assistant_text", Text: "final answer"},
				},
			},
			want: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var msg *ServerMessage
			if test.data != nil {
				msg = &ServerMessage{Data: test.data, suppressPushNotification: test.suppressPushNotification}
			}
			if got := shouldNotifyOfflineForMessage(msg); got != test.want {
				t.Fatalf("shouldNotifyOfflineForMessage() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestPushNotificationMessageBodyUsesVisibleContent(t *testing.T) {
	tests := []struct {
		name string
		data *MsgServerData
		want string
	}{
		{
			name: "plain text",
			data: &MsgServerData{Content: "  final\nanswer  "},
			want: "final answer",
		},
		{
			name: "structured text",
			data: &MsgServerData{Content: map[string]interface{}{"text": "deployment complete"}},
			want: "deployment complete",
		},
		{
			name: "assistant text block",
			data: &MsgServerData{ContentBlocks: []types.ContentBlock{
				{Type: "thinking", Thinking: "private reasoning"},
				{Type: "assistant_text", Text: "report is ready"},
			}},
			want: "report is ready",
		},
		{
			name: "visible block overrides internal raw content",
			data: &MsgServerData{
				Content: "private tool output",
				ContentBlocks: []types.ContentBlock{
					{Type: "tool_result", Content: "private tool output"},
					{Type: "assistant_text", Text: "safe final answer"},
				},
			},
			want: "safe final answer",
		},
		{
			name: "assistant content block",
			data: &MsgServerData{ContentBlocks: []types.ContentBlock{
				{Type: "assistant_text", Content: "content field answer"},
			}},
			want: "content field answer",
		},
		{
			name: "array content",
			data: &MsgServerData{Content: []interface{}{"first paragraph", "second paragraph"}},
			want: "first paragraph second paragraph",
		},
		{
			name: "nested content",
			data: &MsgServerData{Content: map[string]interface{}{
				"content": []interface{}{map[string]interface{}{"text": "nested answer"}},
			}},
			want: "nested answer",
		},
		{
			name: "internal raw content is excluded",
			data: &MsgServerData{
				Content:       "private tool output",
				ContentBlocks: []types.ContentBlock{{Type: "tool_result", Content: "private tool output"}},
			},
			want: "",
		},
		{
			name: "image fallback",
			data: &MsgServerData{Type: "image", ContentBlocks: []types.ContentBlock{{Type: "image"}}},
			want: "发来了一张图片",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := pushNotificationMessageBody(&ServerMessage{Data: test.data})
			if got != test.want {
				t.Fatalf("pushNotificationMessageBody() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPushNotificationTitleUsesSessionName(t *testing.T) {
	hub := NewHub(pushHubConversationTitleStore{
		pushHubUserStore: pushHubUserStore{users: map[int64]*types.User{
			42: {ID: 42, DisplayName: "小明", Username: "xiaoming"},
		}},
		titles: map[string]string{"p2p_7_42": "项目 Alpha"},
	}, nil)
	if got := hub.pushNotificationTitle(7, "p2p_7_42"); got != "项目 Alpha" {
		t.Fatalf("pushNotificationTitle() = %q, want %q", got, "项目 Alpha")
	}
}

func TestPushNotificationMessageBodyTruncatesLongContent(t *testing.T) {
	got := pushNotificationMessageBody(&ServerMessage{Data: &MsgServerData{
		Content: strings.Repeat("猫", maxPushNotificationBodyRunes+20),
	}})
	if utf8.RuneCountInString(got) != maxPushNotificationBodyRunes || !strings.HasSuffix(got, "…") {
		t.Fatalf("truncated body length=%d body=%q", utf8.RuneCountInString(got), got)
	}
}

func TestTaskStatusPublisherRejectedMessageDoesNotFailOpen(t *testing.T) {
	const (
		senderUID    int64 = 7
		recipientUID int64 = 8
	)
	db := &identityMessageStore{users: map[int64]*types.User{
		senderUID:    {ID: senderUID, AccountType: types.AccountBot},
		recipientUID: {ID: recipientUID, AccountType: types.AccountHuman},
	}}
	pushStore := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{{
		Endpoint: "https://push.example.test/subscription/no-fail-open",
		P256DH:   "p256dh",
		Auth:     "auth",
	}}}
	service := enabledPushService(pushStore)
	delivered := make(chan struct{}, 1)
	service.send = func(_ context.Context, _ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		delivered <- struct{}{}
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	hub := NewHub(db, nil)
	hub.SetPushNotificationService(service)

	hub.notifyOfflineUserForMessage(recipientUID, senderUID, &ServerMessage{Data: &MsgServerData{
		Topic: "p2p_7_8", SeqID: 0, Type: "text", Content: "transient agent output",
	}}, true)

	select {
	case <-delivered:
		t.Fatal("rejected task-status publisher message failed open to an immediate push")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestThirdPartyProviderGroupMessagePushEligibility(t *testing.T) {
	providerMetadata := withChannelBindingDeliveryMetadata(nil, &types.ChannelAgentBinding{
		Channel:       "feishu",
		ChannelUserID: "ou_provider_sender",
	})
	tests := []struct {
		name     string
		metadata map[string]interface{}
		wantPush bool
	}{
		{
			name:     "trusted provider source is suppressed",
			metadata: providerMetadata,
		},
		{
			name: "untrusted provider-looking metadata does not suppress",
			metadata: map[string]interface{}{
				"source_channel":  "feishu",
				"channel_user_id": "ou_provider_sender",
			},
			wantPush: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			const (
				groupID    int64 = 83
				senderUID  int64 = 7
				offlineUID int64 = 8
			)
			db := &identityMessageStore{
				users: map[int64]*types.User{
					senderUID:  {ID: senderUID, AccountType: types.AccountHuman},
					offlineUID: {ID: offlineUID, AccountType: types.AccountHuman},
				},
				groupMembers: []*types.GroupMember{
					{GroupID: groupID, UserID: senderUID},
					{GroupID: groupID, UserID: offlineUID},
				},
			}
			pushStore := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{{
				Endpoint: "https://push.example.test/subscription/provider-group",
				P256DH:   "p256dh",
				Auth:     "auth",
			}}}
			service := enabledPushService(pushStore)
			delivered := make(chan struct{}, 1)
			service.send = func(_ context.Context, _ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
				delivered <- struct{}{}
				return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
			}
			hub := NewHub(db, nil)
			hub.SetPushNotificationService(service)

			hub.fanoutNormalizedMessage(senderUID, "grp_83", 0, &normalizedMessagePayload{
				DisplayContent: "来自第三方平台的消息",
				DisplayType:    "text",
				StoredType:     "text",
				Metadata:       test.metadata,
			}, 1, nil)

			select {
			case <-delivered:
				if !test.wantPush {
					t.Fatal("trusted third-party provider message unexpectedly delivered a web push")
				}
			case <-time.After(time.Second):
				if test.wantPush {
					t.Fatal("untrusted provider-looking metadata did not deliver a web push")
				}
			}
		})
	}
}

func newImmediateAgentPushTurnCoordinator() *agentPushTurnCoordinator {
	coordinator := newAgentPushTurnCoordinator()
	coordinator.terminalSettleDelay = 0
	return coordinator
}

func TestPushSuppressionProvenanceIsNotSerialized(t *testing.T) {
	encoded, err := json.Marshal(&ServerMessage{
		Data:                     &MsgServerData{Topic: "grp_83", SeqID: 1, Type: "text", Content: "hello"},
		suppressPushNotification: true,
	})
	if err != nil {
		t.Fatalf("marshal server message: %v", err)
	}
	if strings.Contains(string(encoded), "suppress") {
		t.Fatalf("serialized server message leaked internal push suppression provenance: %s", encoded)
	}
}

func TestAgentPushCoordinatorBoundsDedupKeys(t *testing.T) {
	coordinator := newImmediateAgentPushTurnCoordinator()
	expiresAt := time.Now().Add(agentPushTurnDedupTTL)
	for index := 0; index < maxTrackedAgentPushTurns; index++ {
		coordinator.delivered[agentPushDeliveryKey{
			recipientUID: int64(index + 1),
			scope:        agentPushScope(7, "p2p_7_8"),
			runID:        "capacity",
		}] = expiresAt
	}

	if coordinator.deliverOnce(agentPushTurnDeliveryKey(9999, agentPushScope(7, "p2p_7_8"), "over-capacity"), func() bool { return true }) {
		t.Fatal("new turn was accepted after dedup capacity was exhausted")
	}
}

func TestAgentPushTypedDeliveryKeysDoNotCollideOnDelimiters(t *testing.T) {
	coordinator := newImmediateAgentPushTurnCoordinator()
	deliveries := 0
	keys := []agentPushDeliveryKey{
		agentPushTurnDeliveryKey(8, agentPushScope(7, "topic:a"), "b"),
		agentPushTurnDeliveryKey(8, agentPushScope(7, "topic"), "a:b"),
	}
	for _, key := range keys {
		if !coordinator.deliverOnce(key, func() bool { deliveries++; return true }) {
			t.Fatalf("delivery key %+v was incorrectly deduplicated", key)
		}
	}
	if deliveries != len(keys) {
		t.Fatalf("deliveries = %d, want %d", deliveries, len(keys))
	}
}

func TestAgentPushRunningHeartbeatExtendsActiveTurn(t *testing.T) {
	coordinator := newImmediateAgentPushTurnCoordinator()
	firstExpiry := time.Now().Add(20 * time.Millisecond)
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-1", State: "running", SourceUID: 7, ExpiresAt: &firstExpiry,
	})
	refreshedExpiry := time.Now().Add(time.Hour)
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-1", State: "waiting", SourceUID: 7, ExpiresAt: &refreshedExpiry,
	})
	time.Sleep(30 * time.Millisecond)

	scope := agentPushScope(7, "p2p_7_8")
	turnKey := newAgentPushTrackedTurnKey(scope, "run-1")
	coordinator.mu.Lock()
	_, turnStillActive := coordinator.trackedTurns[turnKey]
	currentRun := coordinator.currentRuns[scope].runID
	coordinator.mu.Unlock()
	if !turnStillActive || currentRun != "run-1" {
		t.Fatal("running heartbeat did not preserve the original active turn")
	}

	delivered := make(chan struct{}, 1)
	msg := &ServerMessage{Data: &MsgServerData{Topic: "p2p_7_8", SeqID: 1, Type: "text", Content: "done"}}
	if !coordinator.observeVisibleMessage(8, 7, msg, func() bool {
		delivered <- struct{}{}
		return true
	}) {
		t.Fatal("refreshed active turn expired at its original deadline")
	}
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-1", State: "completed", SourceUID: 7,
	})
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("refreshed active turn did not deliver after completion")
	}
}

func TestAgentPushSeparateRunsWaitForTheirOwnTerminalStatus(t *testing.T) {
	coordinator := newImmediateAgentPushTurnCoordinator()
	delivered := make(chan string, 2)
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-1", State: "running", SourceUID: 7,
	})
	coordinator.observeVisibleMessage(8, 7, &ServerMessage{Data: &MsgServerData{
		Topic: "p2p_7_8", SeqID: 1, Type: "text", Content: "first run",
		Metadata: map[string]interface{}{"run_id": "run-1"},
	}}, func() bool { delivered <- "run-1"; return true })

	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-2", State: "running", SourceUID: 7,
	})
	coordinator.observeVisibleMessage(8, 7, &ServerMessage{Data: &MsgServerData{
		Topic: "p2p_7_8", SeqID: 2, Type: "text", Content: "second run",
		Metadata: map[string]interface{}{"run_id": "run-2"},
	}}, func() bool { delivered <- "run-2"; return true })
	select {
	case got := <-delivered:
		t.Fatalf("run %s notified before authoritative terminal status", got)
	case <-time.After(25 * time.Millisecond):
	}

	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-1", State: "completed", SourceUID: 7,
	})
	if got := <-delivered; got != "run-1" {
		t.Fatalf("first terminal status delivered %q, want run-1", got)
	}
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-2", State: "completed", SourceUID: 7,
	})
	if got := <-delivered; got != "run-2" {
		t.Fatalf("second terminal status delivered %q, want run-2", got)
	}
}

func TestAgentPushIgnoresExpiredRunningStatus(t *testing.T) {
	coordinator := newImmediateAgentPushTurnCoordinator()
	expired := time.Now().Add(-time.Second)
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-expired", State: "running", SourceUID: 7, ExpiresAt: &expired,
	})
	msg := &ServerMessage{Data: &MsgServerData{Topic: "p2p_7_8", SeqID: 1, Type: "text", Content: "late"}}
	deliveries := 0
	if !coordinator.observeVisibleMessage(8, 7, msg, func() bool { deliveries++; return true }) {
		t.Fatal("message after an expired task status was not suppressed")
	}
	if deliveries != 0 {
		t.Fatal("expired task status authorized a notification")
	}
}

func TestAgentPushMessageBeforeStatusWaitsForMatchingTerminal(t *testing.T) {
	coordinator := newImmediateAgentPushTurnCoordinator()
	msg := &ServerMessage{Data: &MsgServerData{
		Topic: "p2p_7_8", SeqID: 1, Type: "text", Content: "answer before status",
		Metadata: map[string]interface{}{"run_id": "run-before-status"},
	}}
	deliveries := 0
	if !coordinator.observeVisibleMessage(8, 7, msg, func() bool { deliveries++; return true }) {
		t.Fatal("out-of-order message was not retained by the coordinator")
	}
	if deliveries != 0 {
		t.Fatal("out-of-order message notified before terminal status")
	}
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-before-status", State: "completed", SourceUID: 7,
	})
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-before-status", State: "completed", SourceUID: 7,
	})
	if deliveries != 1 {
		t.Fatalf("deliveries = %d, want 1", deliveries)
	}
}

func TestAgentPushNotificationAggregatesVisibleTailAfterWorkingBoundary(t *testing.T) {
	coordinator := newImmediateAgentPushTurnCoordinator()
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-visible-tail", State: "running", SourceUID: 7,
	})

	deliveredBodies := make([]string, 0, 1)
	observe := func(seq int, body string) {
		msg := &ServerMessage{Data: &MsgServerData{
			Topic: "p2p_7_8", SeqID: seq, Type: "text", Content: body,
		}}
		if !coordinator.observeVisibleMessageBody(8, 7, msg, body, func(notificationBody string) bool {
			deliveredBodies = append(deliveredBodies, notificationBody)
			return true
		}) {
			t.Fatalf("message %d was not retained by the coordinator", seq)
		}
	}

	observe(1, "我先检查一下现有实现。")
	coordinator.resetVisibleTailAtWorkingBoundary(7, &ServerMessage{Data: &MsgServerData{
		Topic: "p2p_7_8", SeqID: 2, Type: "tool_use", Content: "execute_shell",
	}})
	for index, body := range []string{"第一部分结论。", "第二部分证据。", "第三部分建议。"} {
		observe(index+3, body)
	}

	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-visible-tail", State: "completed", SourceUID: 7,
	})

	if len(deliveredBodies) != 1 {
		t.Fatalf("delivered bodies = %q, want exactly one notification", deliveredBodies)
	}
	if got, want := deliveredBodies[0], "第一部分结论。 第二部分证据。 第三部分建议。"; got != want {
		t.Fatalf("notification body = %q, want %q", got, want)
	}
}

func TestAgentPushWorkingBoundaryClearsPendingTextBeforeRunningStatus(t *testing.T) {
	coordinator := newImmediateAgentPushTurnCoordinator()
	deliveredBodies := make([]string, 0, 1)
	observe := func(seq int, body string) {
		msg := &ServerMessage{Data: &MsgServerData{
			Topic: "p2p_7_8", SeqID: seq, Type: "text", Content: body,
		}}
		coordinator.observeVisibleMessageBody(8, 7, msg, body, func(notificationBody string) bool {
			deliveredBodies = append(deliveredBodies, notificationBody)
			return true
		})
	}

	observe(1, "状态到达前的工具前文字。")
	coordinator.resetVisibleTailAtWorkingBoundary(7, &ServerMessage{Data: &MsgServerData{
		Topic: "p2p_7_8", SeqID: 2, Type: "tool_result", Content: "内部工具结果",
	}})
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-pending-boundary", State: "running", SourceUID: 7,
	})
	observe(3, "最终回复。")
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-pending-boundary", State: "completed", SourceUID: 7,
	})

	if len(deliveredBodies) != 1 || deliveredBodies[0] != "最终回复。" {
		t.Fatalf("delivered bodies = %q, want only text after the working boundary", deliveredBodies)
	}
}

func TestAgentPushAsynchronousSubagentEventDoesNotClearVisibleReply(t *testing.T) {
	metadataShapes := map[string]map[string]interface{}{
		"kind":                {"kind": "subagent_event"},
		"subagent ID":         {"subagent_id": "child-1"},
		"subagent event type": {"subagent_event_type": "agent_progress"},
	}
	for name, metadata := range metadataShapes {
		t.Run(name, func(t *testing.T) {
			coordinator := newImmediateAgentPushTurnCoordinator()
			coordinator.observeStatus(&types.ConversationTaskStatus{
				TopicID: "p2p_7_8", RunID: "run-with-background-subagent", State: "running", SourceUID: 7,
			})

			deliveredBodies := make([]string, 0, 1)
			msg := &ServerMessage{Data: &MsgServerData{
				Topic: "p2p_7_8", SeqID: 1, Type: "text", Content: "最终回复。",
			}}
			coordinator.observeVisibleMessageBody(8, 7, msg, "最终回复。", func(body string) bool {
				deliveredBodies = append(deliveredBodies, body)
				return true
			})
			coordinator.resetVisibleTailAtWorkingBoundary(7, &ServerMessage{Data: &MsgServerData{
				Topic: "p2p_7_8", SeqID: 2, Type: "thinking", Content: "后台子智能体仍在工作",
				Metadata: metadata,
			}})
			coordinator.observeStatus(&types.ConversationTaskStatus{
				TopicID: "p2p_7_8", RunID: "run-with-background-subagent", State: "completed", SourceUID: 7,
			})

			if len(deliveredBodies) != 1 || deliveredBodies[0] != "最终回复。" {
				t.Fatalf("delivered bodies = %q, want the visible reply preserved across an asynchronous subagent event", deliveredBodies)
			}
		})
	}
}

func TestAgentPushAmbiguousConsecutiveVisibleTextIsPreserved(t *testing.T) {
	coordinator := newImmediateAgentPushTurnCoordinator()
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-text-only", State: "running", SourceUID: 7,
	})
	deliveredBodies := make([]string, 0, 1)
	for seq, body := range []string{"我先处理一下。", "已经处理完成。"} {
		msg := &ServerMessage{Data: &MsgServerData{
			Topic: "p2p_7_8", SeqID: seq + 1, Type: "text", Content: body,
		}}
		coordinator.observeVisibleMessageBody(8, 7, msg, body, func(notificationBody string) bool {
			deliveredBodies = append(deliveredBodies, notificationBody)
			return true
		})
	}
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-text-only", State: "completed", SourceUID: 7,
	})

	if len(deliveredBodies) != 1 || deliveredBodies[0] != "我先处理一下。 已经处理完成。" {
		t.Fatalf("delivered bodies = %q, want all indistinguishable visible text preserved", deliveredBodies)
	}
}

func TestAgentPushVisibleReplyIsBoundedByNotificationBodyInsteadOfSegmentCount(t *testing.T) {
	coordinator := newImmediateAgentPushTurnCoordinator()
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-many-short-segments", State: "running", SourceUID: 7,
	})

	segments := make([]string, 33)
	deliveredBodies := make([]string, 0, 1)
	for index := range segments {
		segments[index] = fmt.Sprintf("%02d", index+1)
		msg := &ServerMessage{Data: &MsgServerData{
			Topic: "p2p_7_8", SeqID: index + 1, Type: "text", Content: segments[index],
		}}
		coordinator.observeVisibleMessageBody(8, 7, msg, segments[index], func(body string) bool {
			deliveredBodies = append(deliveredBodies, body)
			return true
		})
	}
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-many-short-segments", State: "completed", SourceUID: 7,
	})

	want := strings.Join(segments, " ")
	if len(deliveredBodies) != 1 || deliveredBodies[0] != want {
		t.Fatalf("delivered bodies = %q, want all short segments %q", deliveredBodies, want)
	}
}

func TestAgentPushTerminalBeforeSegmentedMessageWithinSettleWindowDeliversCompleteReplyOnce(t *testing.T) {
	coordinator := newAgentPushTurnCoordinator()
	coordinator.terminalSettleDelay = 20 * time.Millisecond
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "terminal-first", State: "completed", SourceUID: 7,
	})
	deliveredBodies := make(chan string, 2)
	for seq, content := range []string{"late final answer part one", "late final answer part two"} {
		msg := &ServerMessage{Data: &MsgServerData{
			Topic: "p2p_7_8", SeqID: seq + 1, Type: "text", Content: content,
			Metadata: map[string]interface{}{"run_id": "terminal-first"},
		}}
		if !coordinator.observeVisibleMessageBody(8, 7, msg, content, func(body string) bool {
			deliveredBodies <- body
			return true
		}) {
			t.Fatal("message after terminal status was not handled by the coordinator")
		}
	}
	select {
	case body := <-deliveredBodies:
		if body != "late final answer part one late final answer part two" {
			t.Fatalf("delivered body = %q, want the complete segmented reply", body)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the terminal notification")
	}
	select {
	case body := <-deliveredBodies:
		t.Fatalf("unexpected duplicate notification body %q", body)
	case <-time.After(50 * time.Millisecond):
	}
}

func TestAgentPushStaleTerminalSettleCallbackCannotDeliver(t *testing.T) {
	coordinator := newAgentPushTurnCoordinator()
	coordinator.terminalSettleDelay = time.Hour
	scope := agentPushScope(7, "p2p_7_8")
	turnKey := newAgentPushTrackedTurnKey(scope, "run-stale-settle")
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: scope.topicID, RunID: "run-stale-settle", State: "running", SourceUID: scope.senderUID,
	})

	deliveredBodies := make([]string, 0, 1)
	observe := func(seq int, body string) {
		msg := &ServerMessage{Data: &MsgServerData{
			Topic: scope.topicID, SeqID: seq, Type: "text", Content: body,
		}}
		coordinator.observeVisibleMessageBody(8, scope.senderUID, msg, body, func(notificationBody string) bool {
			deliveredBodies = append(deliveredBodies, notificationBody)
			return true
		})
	}
	observe(1, "first segment")
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: scope.topicID, RunID: "run-stale-settle", State: "completed", SourceUID: scope.senderUID,
	})
	staleGeneration := coordinator.trackedTurns[turnKey].deliveryGeneration
	observe(2, "second segment")
	currentGeneration := coordinator.trackedTurns[turnKey].deliveryGeneration

	coordinator.deliverSettledTurn(turnKey, "run-stale-settle", staleGeneration)
	if len(deliveredBodies) != 0 {
		t.Fatalf("stale settle callback delivered bodies %q", deliveredBodies)
	}
	coordinator.deliverSettledTurn(turnKey, "run-stale-settle", currentGeneration)
	if len(deliveredBodies) != 1 || deliveredBodies[0] != "first segment second segment" {
		t.Fatalf("delivered bodies = %q, want the latest settled reply once", deliveredBodies)
	}
}

func TestAgentPushUncorrelatedMessageHandlesTerminalOnlyOrdering(t *testing.T) {
	for _, terminalFirst := range []bool{false, true} {
		name := "message_before_terminal"
		if terminalFirst {
			name = "terminal_before_message"
		}
		t.Run(name, func(t *testing.T) {
			coordinator := newImmediateAgentPushTurnCoordinator()
			terminal := &types.ConversationTaskStatus{
				TopicID: "p2p_7_8", RunID: "terminal-only", State: "completed", SourceUID: 7,
			}
			msg := &ServerMessage{Data: &MsgServerData{
				Topic: "p2p_7_8", SeqID: 1, Type: "text", Content: "uncorrelated final answer",
			}}
			deliveries := 0
			if terminalFirst {
				coordinator.observeStatus(terminal)
			}
			if !coordinator.observeVisibleMessage(8, 7, msg, func() bool { deliveries++; return true }) {
				t.Fatal("uncorrelated message was not retained by the coordinator")
			}
			if !terminalFirst {
				coordinator.observeStatus(terminal)
			}
			if deliveries != 1 {
				t.Fatalf("deliveries = %d, want 1", deliveries)
			}
		})
	}
}

func TestAgentPushCompletedRunDoesNotConsumeNextUncorrelatedMessage(t *testing.T) {
	coordinator := newImmediateAgentPushTurnCoordinator()
	deliveries := 0
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-1", State: "running", SourceUID: 7,
	})
	coordinator.observeVisibleMessage(8, 7, &ServerMessage{Data: &MsgServerData{
		Topic: "p2p_7_8", SeqID: 1, Type: "text", Content: "run-1 answer",
	}}, func() bool { deliveries++; return true })
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-1", State: "completed", SourceUID: 7,
	})
	if deliveries != 1 {
		t.Fatalf("run-1 deliveries = %d, want 1", deliveries)
	}

	coordinator.observeVisibleMessage(8, 7, &ServerMessage{Data: &MsgServerData{
		Topic: "p2p_7_8", SeqID: 2, Type: "text", Content: "run-2 answer before status",
	}}, func() bool { deliveries++; return true })
	if deliveries != 1 {
		t.Fatal("completed run consumed the next run's uncorrelated message")
	}
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-2", State: "running", SourceUID: 7,
	})
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-2", State: "completed", SourceUID: 7,
	})
	if deliveries != 2 {
		t.Fatalf("deliveries = %d, want 2", deliveries)
	}
}

func TestAgentPushOutOfOrderRunDoesNotCompleteWithStaleStatus(t *testing.T) {
	coordinator := newImmediateAgentPushTurnCoordinator()
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "stale-run", State: "running", SourceUID: 7,
	})
	msg := &ServerMessage{Data: &MsgServerData{
		Topic: "p2p_7_8", SeqID: 1, Type: "text", Content: "new run answer",
		Metadata: map[string]interface{}{"run_id": "new-run"},
	}}
	deliveries := 0
	if !coordinator.observeVisibleMessage(8, 7, msg, func() bool { deliveries++; return true }) {
		t.Fatal("message for a different run was not retained")
	}
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "stale-run", State: "completed", SourceUID: 7,
	})
	if deliveries != 0 {
		t.Fatal("stale terminal status completed a different run")
	}
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "new-run", State: "completed", SourceUID: 7,
	})
	if deliveries != 1 {
		t.Fatalf("deliveries = %d, want 1", deliveries)
	}
}

func TestAgentPushExplicitRunIDTakesPriorityForStatusCorrelation(t *testing.T) {
	coordinator := newImmediateAgentPushTurnCoordinator()
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-1", State: "running", SourceUID: 7,
	})
	deliveries := 0
	msg := &ServerMessage{Data: &MsgServerData{
		Topic: "p2p_7_8", SeqID: 1, Type: "text", Content: "final answer",
		Metadata: map[string]interface{}{
			"turn_id":     "response-7",
			"response_id": "response-8",
			"run_id":      "run-1",
			"stream_id":   "stream-9",
		},
	}}
	if !coordinator.observeVisibleMessage(8, 7, msg, func() bool { deliveries++; return true }) {
		t.Fatal("message with a matching explicit run_id failed open because another metadata identifier differed")
	}
	if deliveries != 0 {
		t.Fatal("message notified before the matching task status became terminal")
	}
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-1", State: "completed", SourceUID: 7,
	})
	if deliveries != 1 {
		t.Fatalf("deliveries = %d, want 1", deliveries)
	}
}

func TestAgentPushResponseAndStreamIDsDoNotOverrideActiveRun(t *testing.T) {
	for _, metadata := range []map[string]interface{}{
		{"response_id": "response-7"},
		{"stream_id": "stream-9"},
	} {
		coordinator := newImmediateAgentPushTurnCoordinator()
		coordinator.observeStatus(&types.ConversationTaskStatus{
			TopicID: "p2p_7_8", RunID: "run-1", State: "running", SourceUID: 7,
		})
		msg := &ServerMessage{Data: &MsgServerData{
			Topic: "p2p_7_8", SeqID: 1, Type: "text", Content: "final answer", Metadata: metadata,
		}}
		deliveries := 0
		if !coordinator.observeVisibleMessage(8, 7, msg, func() bool { deliveries++; return true }) {
			t.Fatalf("metadata %v was incorrectly treated as a task-status run ID", metadata)
		}
		coordinator.observeStatus(&types.ConversationTaskStatus{
			TopicID: "p2p_7_8", RunID: "run-1", State: "completed", SourceUID: 7,
		})
		if deliveries != 1 {
			t.Fatalf("metadata %v deliveries = %d, want 1", metadata, deliveries)
		}
	}
}

func TestAgentPushStaleStatusesDoNotRebindUntaggedMessage(t *testing.T) {
	coordinator := newImmediateAgentPushTurnCoordinator()
	baseTime := time.Now()
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-1", State: "running", SourceUID: 7, UpdatedAt: baseTime,
	})
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-2", State: "running", SourceUID: 7, UpdatedAt: baseTime.Add(time.Second),
	})
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-1", State: "waiting", SourceUID: 7, UpdatedAt: baseTime.Add(-time.Second),
	})

	deliveries := 0
	msg := &ServerMessage{Data: &MsgServerData{
		Topic: "p2p_7_8", SeqID: 1, Type: "text", Content: "untagged run-2 progress",
	}}
	if !coordinator.observeVisibleMessage(8, 7, msg, func() bool { deliveries++; return true }) {
		t.Fatal("untagged message was not retained")
	}
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-1", State: "completed", SourceUID: 7, UpdatedAt: baseTime,
	})
	if deliveries != 0 {
		t.Fatal("late terminal status rebound an untagged message to the stale run")
	}
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-2", State: "completed", SourceUID: 7, UpdatedAt: baseTime.Add(2 * time.Second),
	})
	if deliveries != 1 {
		t.Fatalf("deliveries = %d, want 1", deliveries)
	}
}

func TestAgentPushFirstSeenStaleRunCannotReclaimCurrentAfterProductionNormalization(t *testing.T) {
	coordinator := newImmediateAgentPushTurnCoordinator()
	baseTime := time.Date(2026, 8, 8, 3, 0, 0, 0, time.UTC)
	normalize := func(runID, state string, updatedAt time.Time) *types.ConversationTaskStatus {
		status, err := normalizeConversationTaskStatus(7, "p2p_7_8", &normalizedMessagePayload{
			DisplayContent: map[string]interface{}{
				"run_id": runID, "state": state, "updated_at": updatedAt.Format(time.RFC3339),
			},
		})
		if err != nil {
			t.Fatalf("normalize %s %s status: %v", runID, state, err)
		}
		return status
	}

	coordinator.observeStatus(normalize("run-2", "running", baseTime.Add(2*time.Second)))
	coordinator.observeStatus(normalize("run-1", "waiting", baseTime))

	deliveries := 0
	msg := &ServerMessage{Data: &MsgServerData{
		Topic: "p2p_7_8", SeqID: 1, Type: "text", Content: "untagged run-2 answer",
	}}
	coordinator.observeVisibleMessage(8, 7, msg, func() bool { deliveries++; return true })
	coordinator.observeStatus(normalize("run-1", "completed", baseTime.Add(time.Second)))
	if deliveries != 0 {
		t.Fatal("first-seen stale run reclaimed the current untagged message")
	}
	coordinator.observeStatus(normalize("run-2", "completed", baseTime.Add(3*time.Second)))
	if deliveries != 1 {
		t.Fatalf("deliveries = %d, want 1", deliveries)
	}
}

func TestAgentPushStaleTerminalStatusDoesNotCompleteCurrentRun(t *testing.T) {
	coordinator := newImmediateAgentPushTurnCoordinator()
	baseTime := time.Now()
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-1", State: "running", SourceUID: 7,
		UpdatedAt: baseTime.Add(time.Second),
	})

	deliveries := 0
	msg := &ServerMessage{Data: &MsgServerData{
		Topic: "p2p_7_8", SeqID: 1, Type: "text", Content: "final answer",
		Metadata: map[string]interface{}{"run_id": "run-1"},
	}}
	coordinator.observeVisibleMessage(8, 7, msg, func() bool { deliveries++; return true })

	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-1", State: "completed", SourceUID: 7,
		UpdatedAt: baseTime,
	})
	if deliveries != 0 {
		t.Fatalf("stale terminal status delivered a push; deliveries = %d", deliveries)
	}
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-1", State: "completed", SourceUID: 7,
	})
	if deliveries != 0 {
		t.Fatalf("timestamp-less terminal status delivered a push; deliveries = %d", deliveries)
	}

	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-1", State: "completed", SourceUID: 7,
		UpdatedAt: baseTime.Add(2 * time.Second),
	})
	if deliveries != 1 {
		t.Fatalf("fresh terminal status deliveries = %d, want 1", deliveries)
	}
}

func TestAgentPushFailedDeliveryRetriesOnDuplicateTerminal(t *testing.T) {
	coordinator := newImmediateAgentPushTurnCoordinator()
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "retry-run", State: "running", SourceUID: 7,
	})
	attempts := 0
	msg := &ServerMessage{Data: &MsgServerData{
		Topic: "p2p_7_8", SeqID: 1, Type: "text", Content: "final answer",
		Metadata: map[string]interface{}{"run_id": "retry-run"},
	}}
	coordinator.observeVisibleMessage(8, 7, msg, func() bool {
		attempts++
		return attempts > 1
	})
	terminal := &types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "retry-run", State: "completed", SourceUID: 7,
	}
	coordinator.observeStatus(terminal)
	if attempts != 1 {
		t.Fatalf("attempts after first terminal = %d, want 1", attempts)
	}
	coordinator.observeStatus(terminal)
	if attempts != 2 {
		t.Fatalf("attempts after retry terminal = %d, want 2", attempts)
	}
	coordinator.observeStatus(terminal)
	if attempts != 2 {
		t.Fatalf("successful delivery retried again; attempts = %d", attempts)
	}
}

func TestAgentPushMissingTerminalNeverFallsBack(t *testing.T) {
	coordinator := newImmediateAgentPushTurnCoordinator()
	expiresAt := time.Now().Add(25 * time.Millisecond)
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "missing-terminal", State: "running", SourceUID: 7, ExpiresAt: &expiresAt,
	})
	delivered := make(chan struct{}, 1)
	msg := &ServerMessage{Data: &MsgServerData{
		Topic: "p2p_7_8", SeqID: 1, Type: "text", Content: "intermediate answer",
		Metadata: map[string]interface{}{"run_id": "missing-terminal"},
	}}
	if !coordinator.observeVisibleMessage(8, 7, msg, func() bool {
		delivered <- struct{}{}
		return true
	}) {
		t.Fatal("active turn did not retain the notification candidate")
	}
	select {
	case <-delivered:
		t.Fatal("message notified without an authoritative terminal status")
	case <-time.After(75 * time.Millisecond):
	}
}

func TestAgentPushHeartbeatsRemainSilentUntilTerminal(t *testing.T) {
	coordinator := newImmediateAgentPushTurnCoordinator()
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "heartbeat-run", State: "running", SourceUID: 7,
	})
	deliveries := 0
	msg := &ServerMessage{Data: &MsgServerData{
		Topic: "p2p_7_8", SeqID: 1, Type: "text", Content: "intermediate answer",
		Metadata: map[string]interface{}{"run_id": "heartbeat-run"},
	}}
	if !coordinator.observeVisibleMessage(8, 7, msg, func() bool { deliveries++; return true }) {
		t.Fatal("active turn did not retain the notification candidate")
	}
	for index := 0; index < 3; index++ {
		coordinator.observeStatus(&types.ConversationTaskStatus{
			TopicID: "p2p_7_8", RunID: "heartbeat-run", State: "waiting", SourceUID: 7,
		})
	}
	if deliveries != 0 {
		t.Fatal("heartbeat delivered the retained candidate")
	}
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "heartbeat-run", State: "completed", SourceUID: 7,
	})
	if deliveries != 1 {
		t.Fatalf("deliveries = %d, want 1", deliveries)
	}
}

type aggregateTaskStatusPushStore struct {
	*identityMessageStore
	source    *types.ConversationTaskStatus
	aggregate *types.ConversationTaskStatus
}

type mutedAggregateTaskStatusPushStore struct {
	*aggregateTaskStatusPushStore
	mutedTopics       map[string]bool
	preferenceChecked chan struct{}
}

func (s *mutedAggregateTaskStatusPushStore) ListMutedConversationTopics(_ context.Context, _ int64, topicIDs []string) (map[string]bool, error) {
	muted := make(map[string]bool)
	for _, topicID := range topicIDs {
		if s.mutedTopics[topicID] {
			muted[topicID] = true
		}
	}
	return muted, nil
}

func (s *mutedAggregateTaskStatusPushStore) SetConversationNotificationsMuted(_ context.Context, _ int64, topicID string, muted bool) error {
	if s.mutedTopics == nil {
		s.mutedTopics = make(map[string]bool)
	}
	if muted {
		s.mutedTopics[topicID] = true
	} else {
		delete(s.mutedTopics, topicID)
	}
	return nil
}

func (s *mutedAggregateTaskStatusPushStore) IsConversationNotificationsMuted(_ context.Context, _ int64, topicID string) (bool, error) {
	if s.preferenceChecked != nil {
		select {
		case s.preferenceChecked <- struct{}{}:
		default:
		}
	}
	return s.mutedTopics[topicID], nil
}

func (s *aggregateTaskStatusPushStore) CreateTopic(string, string, int64) error {
	return nil
}

func (s *aggregateTaskStatusPushStore) GetConversationTaskStatusForSource(topicID string, sourceUID int64) (*types.ConversationTaskStatus, error) {
	if s.source == nil || s.source.TopicID != topicID || s.source.SourceUID != sourceUID {
		return nil, nil
	}
	copyOfStatus := *s.source
	return &copyOfStatus, nil
}

func (s *aggregateTaskStatusPushStore) UpsertConversationTaskStatus(status *types.ConversationTaskStatus) (*types.ConversationTaskStatus, error) {
	s.source = prepareTestConversationTaskStatus(status)
	aggregate := *s.aggregate
	return &aggregate, nil
}

func (s *aggregateTaskStatusPushStore) GetConversationTaskStatuses([]string) (map[string]*types.ConversationTaskStatus, error) {
	return map[string]*types.ConversationTaskStatus{}, nil
}

func TestAgentPushWaitsForMatchingTerminalTaskStatus(t *testing.T) {
	const (
		senderUID  int64 = 7
		offlineUID int64 = 8
	)
	db := &aggregateTaskStatusPushStore{
		identityMessageStore: &identityMessageStore{users: map[int64]*types.User{
			senderUID:  {ID: senderUID, AccountType: types.AccountBot},
			offlineUID: {ID: offlineUID, AccountType: types.AccountHuman},
		}},
		aggregate: &types.ConversationTaskStatus{
			TopicID: "p2p_7_8", RunID: "other-agent-run", State: "running", SourceUID: 99,
		},
	}
	pushStore := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{{
		Endpoint: "https://push.example.test/subscription/task-status",
		P256DH:   "p256dh",
		Auth:     "auth",
	}}}
	service := enabledPushService(pushStore)
	delivered := make(chan PushNotification, 1)
	service.send = func(_ context.Context, payload []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		var notification PushNotification
		if err := json.Unmarshal(payload, &notification); err != nil {
			t.Errorf("decode push notification: %v", err)
		}
		delivered <- notification
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	hub := NewHub(db, nil)
	hub.SetPushNotificationService(service)
	handler := NewMessageHandler(db, hub)

	if _, err := handler.handleTaskStatus(senderUID, "p2p_7_8", &normalizedMessagePayload{
		DisplayType:         taskStatusType,
		ExplicitDisplayType: true,
		DisplayContent:      map[string]interface{}{"run_id": "run-1", "state": "running"},
	}); err != nil {
		t.Fatalf("publish running task status: %v", err)
	}
	hub.fanoutNormalizedMessage(senderUID, "p2p_7_8", 0, &normalizedMessagePayload{
		DisplayContent: "final answer",
		DisplayType:    "text",
		StoredType:     "text",
	}, 1, nil)
	select {
	case <-delivered:
		t.Fatal("agent message notified before the task reached a terminal state")
	case <-time.After(100 * time.Millisecond):
	}

	hub.observeAgentPushTaskStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "old-run", State: "completed", SourceUID: senderUID,
	})
	select {
	case <-delivered:
		t.Fatal("a stale terminal status completed the active agent turn")
	case <-time.After(100 * time.Millisecond):
	}

	if _, err := handler.handleTaskStatus(senderUID, "p2p_7_8", &normalizedMessagePayload{
		DisplayType:         taskStatusType,
		ExplicitDisplayType: true,
		DisplayContent:      map[string]interface{}{"run_id": "run-1", "state": "completed"},
	}); err != nil {
		t.Fatalf("publish completed task status: %v", err)
	}
	select {
	case notification := <-delivered:
		if notification.Body != "final answer" {
			t.Fatalf("notification body = %q, want detailed message content", notification.Body)
		}
	case <-time.After(time.Second):
		t.Fatal("matching terminal task status did not notify the offline recipient")
	}
}

func TestMutedAgentTaskTerminalStatusDoesNotDeliverPush(t *testing.T) {
	for _, terminalState := range []string{"completed", "failed"} {
		t.Run(terminalState, func(t *testing.T) {
			const (
				senderUID  int64 = 7
				offlineUID int64 = 8
				topicID          = "p2p_7_8"
			)
			db := &mutedAggregateTaskStatusPushStore{
				aggregateTaskStatusPushStore: &aggregateTaskStatusPushStore{
					identityMessageStore: &identityMessageStore{users: map[int64]*types.User{
						senderUID:  {ID: senderUID, AccountType: types.AccountBot},
						offlineUID: {ID: offlineUID, AccountType: types.AccountHuman},
					}},
					aggregate: &types.ConversationTaskStatus{
						TopicID: topicID, RunID: "other-agent-run", State: "running", SourceUID: 99,
					},
				},
				mutedTopics:       map[string]bool{topicID: true},
				preferenceChecked: make(chan struct{}, 1),
			}
			pushStore := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{{
				Endpoint: "https://push.example.test/subscription/muted-agent-task",
				P256DH:   "p256dh",
				Auth:     "auth",
			}}}
			service := enabledPushService(pushStore)
			delivered := make(chan struct{}, 1)
			service.send = func(_ context.Context, _ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
				delivered <- struct{}{}
				return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
			}
			hub := NewHub(db, nil)
			hub.SetPushNotificationService(service)
			handler := NewMessageHandler(db, hub)

			if _, err := handler.handleTaskStatus(senderUID, topicID, &normalizedMessagePayload{
				DisplayType:         taskStatusType,
				ExplicitDisplayType: true,
				DisplayContent:      map[string]interface{}{"run_id": "run-1", "state": "running"},
			}); err != nil {
				t.Fatalf("publish running task status: %v", err)
			}
			hub.fanoutNormalizedMessage(senderUID, topicID, 0, &normalizedMessagePayload{
				DisplayContent: "final answer",
				DisplayType:    "text",
				StoredType:     "text",
			}, 1, nil)
			if _, err := handler.handleTaskStatus(senderUID, topicID, &normalizedMessagePayload{
				DisplayType:         taskStatusType,
				ExplicitDisplayType: true,
				DisplayContent:      map[string]interface{}{"run_id": "run-1", "state": terminalState},
			}); err != nil {
				t.Fatalf("publish %s task status: %v", terminalState, err)
			}

			select {
			case <-db.preferenceChecked:
			case <-time.After(time.Second):
				t.Fatal("terminal task status did not check the conversation notification preference")
			}
			select {
			case <-delivered:
				t.Fatalf("muted task %s delivered a push notification", terminalState)
			case <-time.After(100 * time.Millisecond):
			}
		})
	}
}

func TestServiceAccountPushWaitsForMatchingTerminalTaskStatus(t *testing.T) {
	const (
		senderUID  int64 = 17
		offlineUID int64 = 18
	)
	db := &aggregateTaskStatusPushStore{
		identityMessageStore: &identityMessageStore{users: map[int64]*types.User{
			senderUID:  {ID: senderUID, AccountType: types.AccountService},
			offlineUID: {ID: offlineUID, AccountType: types.AccountHuman},
		}},
		aggregate: &types.ConversationTaskStatus{
			TopicID: "p2p_17_18", RunID: "other-service-run", State: "running", SourceUID: 99,
		},
	}
	pushStore := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{{
		Endpoint: "https://push.example.test/subscription/service-task-status",
		P256DH:   "p256dh",
		Auth:     "auth",
	}}}
	service := enabledPushService(pushStore)
	delivered := make(chan struct{}, 1)
	service.send = func(_ context.Context, _ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		delivered <- struct{}{}
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	hub := NewHub(db, nil)
	hub.SetPushNotificationService(service)
	handler := NewMessageHandler(db, hub)

	if _, err := handler.handleTaskStatus(senderUID, "p2p_17_18", &normalizedMessagePayload{
		DisplayType:         taskStatusType,
		ExplicitDisplayType: true,
		DisplayContent:      map[string]interface{}{"run_id": "run-1", "state": "running"},
	}); err != nil {
		t.Fatalf("publish running service task status: %v", err)
	}
	hub.fanoutNormalizedMessage(senderUID, "p2p_17_18", 0, &normalizedMessagePayload{
		DisplayContent: "final answer",
		DisplayType:    "text",
		StoredType:     "text",
	}, 1, nil)
	select {
	case <-delivered:
		t.Fatal("service account message notified before the task reached a terminal state")
	case <-time.After(100 * time.Millisecond):
	}

	if _, err := handler.handleTaskStatus(senderUID, "p2p_17_18", &normalizedMessagePayload{
		DisplayType:         taskStatusType,
		ExplicitDisplayType: true,
		DisplayContent:      map[string]interface{}{"run_id": "run-1", "state": "completed"},
	}); err != nil {
		t.Fatalf("publish completed service task status: %v", err)
	}
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("matching terminal service task status did not notify the offline recipient")
	}
}

func TestServiceAccountGroupPushWaitsForMatchingTerminalTaskStatus(t *testing.T) {
	const (
		groupID    int64 = 82
		senderUID  int64 = 17
		offlineUID int64 = 18
	)
	db := &aggregateTaskStatusPushStore{
		identityMessageStore: &identityMessageStore{
			users: map[int64]*types.User{
				senderUID:  {ID: senderUID, AccountType: types.AccountService},
				offlineUID: {ID: offlineUID, AccountType: types.AccountHuman},
			},
			groupMembers: []*types.GroupMember{
				{GroupID: groupID, UserID: senderUID},
				{GroupID: groupID, UserID: offlineUID},
			},
		},
		aggregate: &types.ConversationTaskStatus{
			TopicID: "grp_82", RunID: "other-service-run", State: "running", SourceUID: 99,
		},
	}
	pushStore := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{{
		Endpoint: "https://push.example.test/subscription/service-group-task-status",
		P256DH:   "p256dh",
		Auth:     "auth",
	}}}
	service := enabledPushService(pushStore)
	delivered := make(chan struct{}, 1)
	service.send = func(_ context.Context, _ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		delivered <- struct{}{}
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	hub := NewHub(db, nil)
	hub.SetPushNotificationService(service)
	handler := NewMessageHandler(db, hub)

	if _, err := handler.handleTaskStatus(senderUID, "grp_82", &normalizedMessagePayload{
		DisplayType:         taskStatusType,
		ExplicitDisplayType: true,
		DisplayContent:      map[string]interface{}{"run_id": "run-1", "state": "running"},
	}); err != nil {
		t.Fatalf("publish running service task status: %v", err)
	}
	hub.broadcastToGroupWithMentions(groupID, &ServerMessage{Data: &MsgServerData{
		Topic:   "grp_82",
		From:    formatUID(senderUID),
		SeqID:   1,
		Content: "final answer",
		Type:    "text",
		MsgType: "text",
	}}, senderUID, nil, senderUID, false)
	select {
	case <-delivered:
		t.Fatal("service account group message notified before the task reached a terminal state")
	case <-time.After(100 * time.Millisecond):
	}

	if _, err := handler.handleTaskStatus(senderUID, "grp_82", &normalizedMessagePayload{
		DisplayType:         taskStatusType,
		ExplicitDisplayType: true,
		DisplayContent:      map[string]interface{}{"run_id": "run-1", "state": "completed"},
	}); err != nil {
		t.Fatalf("publish completed service task status: %v", err)
	}
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("matching terminal service task status did not notify the offline group recipient")
	}
}

func TestOrdinaryP2PMessageStillNotifiesWithoutTurnMetadata(t *testing.T) {
	const (
		senderUID  int64 = 7
		offlineUID int64 = 8
	)
	db := &identityMessageStore{users: map[int64]*types.User{
		senderUID:  {ID: senderUID, AccountType: types.AccountHuman},
		offlineUID: {ID: offlineUID, AccountType: types.AccountHuman},
	}}
	pushStore := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{{
		Endpoint: "https://push.example.test/subscription/ordinary-message",
		P256DH:   "p256dh",
		Auth:     "auth",
	}}}
	service := enabledPushService(pushStore)
	delivered := make(chan struct{}, 1)
	service.send = func(_ context.Context, _ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		delivered <- struct{}{}
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	hub := NewHub(db, nil)
	hub.SetPushNotificationService(service)

	hub.fanoutNormalizedMessage(senderUID, "p2p_7_8", 0, &normalizedMessagePayload{
		DisplayContent: "ordinary message",
		DisplayType:    "text",
		StoredType:     "text",
	}, 1, nil)

	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("ordinary non-agent message did not notify the offline recipient")
	}
	select {
	case <-delivered:
		t.Fatal("one ordinary message delivered more than one push")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestP2PAgentMultipleIntermediateMessagesWaitForTerminalStatus(t *testing.T) {
	const (
		senderUID  int64 = 7
		offlineUID int64 = 8
	)
	db := &identityMessageStore{users: map[int64]*types.User{
		senderUID:  {ID: senderUID, AccountType: types.AccountBot},
		offlineUID: {ID: offlineUID, AccountType: types.AccountHuman},
	}}
	pushStore := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{{
		Endpoint: "https://push.example.test/subscription/p2p-agent",
		P256DH:   "p256dh",
		Auth:     "auth",
	}}}
	service := enabledPushService(pushStore)
	delivered := make(chan PushNotification, 3)
	service.send = func(_ context.Context, payload []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		var notification PushNotification
		if err := json.Unmarshal(payload, &notification); err != nil {
			t.Errorf("decode push notification: %v", err)
		}
		delivered <- notification
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	hub := NewHub(db, nil)
	hub.SetPushNotificationService(service)
	hub.agentPush.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-1", State: "running", SourceUID: senderUID,
	})

	hub.fanoutNormalizedMessage(senderUID, "p2p_7_8", 0, &normalizedMessagePayload{
		DisplayContent: "first progress update",
		DisplayType:    "text",
		StoredType:     "text",
	}, 1, nil)
	hub.fanoutNormalizedMessage(senderUID, "p2p_7_8", 0, &normalizedMessagePayload{
		DisplayContent: "execute_shell",
		DisplayType:    "tool_use",
		StoredType:     "tool_use",
	}, 2, nil)
	hub.fanoutNormalizedMessage(senderUID, "p2p_7_8", 0, &normalizedMessagePayload{
		DisplayContent: "first final segment",
		DisplayType:    "text",
		StoredType:     "text",
	}, 3, nil)
	hub.fanoutNormalizedMessage(senderUID, "p2p_7_8", 0, &normalizedMessagePayload{
		DisplayContent: "background child progress",
		DisplayType:    "thinking",
		StoredType:     "thinking",
		Metadata:       map[string]interface{}{"kind": "subagent_event", "subagent_id": "child-1"},
	}, 4, nil)
	hub.fanoutNormalizedMessage(senderUID, "p2p_7_8", 0, &normalizedMessagePayload{
		DisplayContent: "second final segment",
		DisplayType:    "text",
		StoredType:     "text",
	}, 5, nil)
	select {
	case notification := <-delivered:
		if notification.Body != "first final segment second final segment" {
			t.Fatalf("notification body = %q, want complete visible tail", notification.Body)
		}
		t.Fatal("intermediate agent text notified before terminal status")
	case <-time.After(100 * time.Millisecond):
	}

	hub.agentPush.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-1", State: "completed", SourceUID: senderUID,
	})
	select {
	case notification := <-delivered:
		if notification.Body != "first final segment second final segment" {
			t.Fatalf("notification body = %q, want complete visible tail across an asynchronous subagent event", notification.Body)
		}
	case <-time.After(time.Second):
		t.Fatal("terminal status did not notify the offline recipient")
	}

	hub.agentPush.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-1", State: "completed", SourceUID: senderUID,
	})
	hub.fanoutNormalizedMessage(senderUID, "p2p_7_8", 0, &normalizedMessagePayload{
		DisplayContent: "out-of-order late chunk",
		DisplayType:    "text",
		StoredType:     "text",
		Metadata:       map[string]interface{}{"run_id": "run-1"},
	}, 6, nil)
	select {
	case <-delivered:
		t.Fatal("duplicate terminal or late chunk delivered another push")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestGroupBroadcastQueuesPushOnlyForOfflineHumans(t *testing.T) {
	const (
		groupID    int64 = 80
		senderUID  int64 = 7
		offlineUID int64 = 8
		onlineUID  int64 = 9
	)
	db := &identityMessageStore{
		users: map[int64]*types.User{
			senderUID:  {ID: senderUID, AccountType: types.AccountBot},
			offlineUID: {ID: offlineUID, AccountType: types.AccountHuman},
			onlineUID:  {ID: onlineUID, AccountType: types.AccountHuman},
		},
		groupMembers: []*types.GroupMember{
			{GroupID: groupID, UserID: senderUID, IsBot: true},
			{GroupID: groupID, UserID: offlineUID},
			{GroupID: groupID, UserID: onlineUID},
		},
	}
	pushStore := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{{
		Endpoint: "https://push.example.test/subscription/group",
		P256DH:   "p256dh",
		Auth:     "auth",
	}}}
	service := enabledPushService(pushStore)
	delivered := make(chan struct{}, 2)
	service.send = func(_ context.Context, _ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		delivered <- struct{}{}
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	hub := NewHub(db, nil)
	hub.SetPushNotificationService(service)
	hub.addClient(&Client{
		uid:         onlineUID,
		accountType: types.AccountHuman,
		messagingAttention: messagingClientAttention{
			SubscriptionID: pushSubscriptionID("https://push.example.test/subscription/group"),
			ActiveTopic:    "grp_80",
			Visible:        true,
			Focused:        true,
		},
		send: make(chan []byte, 2),
	})
	hub.agentPush.observeStatus(&types.ConversationTaskStatus{
		TopicID: "grp_80", RunID: "group-turn-1", State: "running", SourceUID: senderUID,
	})

	hub.broadcastToGroupWithMentions(groupID, &ServerMessage{Data: &MsgServerData{
		Topic:   "grp_80",
		From:    formatUID(senderUID),
		SeqID:   1,
		Content: "working",
		Type:    "thinking",
		MsgType: "text",
	}}, senderUID, nil, senderUID, false)

	select {
	case <-delivered:
		t.Fatal("agent working message unexpectedly delivered a group push")
	case <-time.After(100 * time.Millisecond):
	}

	hub.broadcastToGroupWithMentions(groupID, &ServerMessage{Data: &MsgServerData{
		Topic:   "grp_80",
		From:    formatUID(senderUID),
		SeqID:   2,
		Content: "final answer",
		Type:    "text",
		MsgType: "text",
	}}, senderUID, nil, senderUID, false)

	select {
	case <-delivered:
		t.Fatal("group agent message notified before task completion")
	case <-time.After(100 * time.Millisecond):
	}
	hub.agentPush.observeStatus(&types.ConversationTaskStatus{
		TopicID: "grp_80", RunID: "group-turn-1", State: "completed", SourceUID: senderUID,
	})
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("offline group member did not receive push")
	}
	select {
	case <-delivered:
		t.Fatal("group broadcast queued more than the offline member push")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestGroupBroadcastQueuesBurstBeyondWorkerCount(t *testing.T) {
	const (
		groupID   int64 = 81
		senderUID int64 = 7
	)
	db := &identityMessageStore{
		users: map[int64]*types.User{
			senderUID: {ID: senderUID, AccountType: types.AccountHuman},
		},
		groupMembers: []*types.GroupMember{
			{GroupID: groupID, UserID: senderUID},
		},
	}
	for index := 0; index < maxConcurrentPushDeliveries+1; index++ {
		uid := int64(100 + index)
		db.users[uid] = &types.User{ID: uid, AccountType: types.AccountHuman}
		db.groupMembers = append(db.groupMembers, &types.GroupMember{GroupID: groupID, UserID: uid})
	}

	pushStore := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{{
		Endpoint: "https://push.example.test/subscription/group-burst",
		P256DH:   "p256dh",
		Auth:     "auth",
	}}}
	service := enabledPushService(pushStore)
	wantDeliveries := maxConcurrentPushDeliveries + 1
	delivered := make(chan struct{}, wantDeliveries)
	release := make(chan struct{})
	service.send = func(_ context.Context, _ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		<-release
		delivered <- struct{}{}
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	hub := NewHub(db, nil)
	hub.SetPushNotificationService(service)

	hub.broadcastToGroupWithMentions(groupID, &ServerMessage{Data: &MsgServerData{
		Topic:   "grp_81",
		From:    formatUID(senderUID),
		SeqID:   1,
		Content: "final answer",
		Type:    "text",
		MsgType: "text",
	}}, senderUID, nil, senderUID, false)
	close(release)

	for index := 0; index < wantDeliveries; index++ {
		select {
		case <-delivered:
		case <-time.After(time.Second):
			t.Fatalf("group burst delivered %d pushes, want %d", index, wantDeliveries)
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
	if len(store.deletedScoped) != 0 {
		t.Fatalf("non-expired subscriptions were deleted: %#v", store.deletedScoped)
	}
}

func TestPushNotificationRetriesTransientProviderRejection(t *testing.T) {
	store := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{{
		Endpoint: "https://push.example.test/transient", P256DH: "p256dh", Auth: "auth",
	}}}
	service := enabledPushService(store)
	attempts := 0
	service.send = func(_ context.Context, _ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		attempts++
		status := http.StatusServiceUnavailable
		if attempts == maxPushProviderAttempts {
			status = http.StatusCreated
		}
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(""))}, nil
	}

	if err := service.SendToUser(context.Background(), 15, PushNotification{Title: "title"}); err != nil {
		t.Fatalf("SendToUser returned error after transient retry: %v", err)
	}
	if attempts != maxPushProviderAttempts {
		t.Fatalf("provider attempts = %d, want %d", attempts, maxPushProviderAttempts)
	}
}

func TestPushNotificationDeliveryErrorsRedactEndpointCapability(t *testing.T) {
	const endpoint = "https://push.example.test/subscription/secret-capability-token"
	store := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{
		{Endpoint: endpoint, P256DH: "p256dh", Auth: "auth"},
	}}
	service := enabledPushService(store)
	var logs []string
	service.logf = func(format string, args ...interface{}) {
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	service.send = func(_ context.Context, _ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		return nil, fmt.Errorf("POST %s: network failure", endpoint)
	}

	err := service.SendToUser(context.Background(), 15, PushNotification{Title: "title"})
	if err == nil {
		t.Fatal("SendToUser error = nil, want provider error")
	}
	combined := err.Error() + "\n" + strings.Join(logs, "\n")
	if strings.Contains(combined, endpoint) || strings.Contains(combined, "secret-capability-token") {
		t.Fatalf("delivery diagnostics exposed push endpoint capability: %s", combined)
	}
	if !strings.Contains(combined, "push.example.test#") {
		t.Fatalf("delivery diagnostics omitted redacted provider identity: %s", combined)
	}
}
