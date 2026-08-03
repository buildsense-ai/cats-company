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

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type memoryPushSubscriptionStore struct {
	subscriptions         []*types.PushSubscription
	upserted              *types.PushSubscription
	deletedUID            int64
	deleted               string
	deletedRegistrationID string
	deletedScoped         []string
	upsertErr             error
	listErr               error
	listBlock             <-chan struct{}
	deleteErr             error
	beforeDelete          func()
}

type pushHubUserStore struct {
	store.Store
	users map[int64]*types.User
}

func (s pushHubUserStore) GetUser(uid int64) (*types.User, error) {
	return s.users[uid], nil
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

func TestEnqueueUserPushQueuesOnlyWhenNoVisibleHumanClient(t *testing.T) {
	tests := []struct {
		name         string
		accountType  types.AccountType
		state        int
		clientStates []string
		wantPush     bool
	}{
		{name: "offline human", accountType: types.AccountHuman, wantPush: true},
		{name: "visible human", accountType: types.AccountHuman, clientStates: []string{"visible"}},
		{name: "legacy connected human", accountType: types.AccountHuman, clientStates: []string{""}},
		{name: "hidden human", accountType: types.AccountHuman, clientStates: []string{"hidden"}, wantPush: true},
		{name: "visible and hidden human", accountType: types.AccountHuman, clientStates: []string{"visible", "hidden"}},
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
				hub.addClient(&Client{
					uid:            uid,
					pageVisibility: pageVisibility,
					send:           make(chan []byte, 1),
				})
			}

			hub.enqueueOfflineUserPush(uid)

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

func TestEnqueueUserPushSuppressesOnlyVisibleRegistration(t *testing.T) {
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
	hub.addClient(&Client{
		uid:                uid,
		pageVisibility:     pageVisibilityVisible,
		pushRegistrationID: "registration-visible",
		send:               make(chan []byte, 1),
	})

	hub.enqueueOfflineUserPush(uid)

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

func TestEnqueueUserPushSuppressesAllRegistrationsForVisibleLegacyClient(t *testing.T) {
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
	hub.addClient(&Client{
		uid:            uid,
		pageVisibility: pageVisibilityVisible,
		send:           make(chan []byte, 1),
	})

	hub.enqueueOfflineUserPush(uid)

	select {
	case endpoint := <-delivered:
		t.Fatalf("unexpected push delivery to %q", endpoint)
	case <-time.After(100 * time.Millisecond):
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

func TestAgentPushCompletionRequiresExplicitFinalSignal(t *testing.T) {
	working := &ServerMessage{Data: &MsgServerData{
		Topic:    "p2p_7_8",
		From:     formatUID(7),
		SeqID:    1,
		Type:     "text",
		Content:  "still working",
		Metadata: map[string]interface{}{"turn_id": "turn-1"},
	}}
	if isCompletedAgentMessage(working) {
		t.Fatal("a persisted visible segment without an explicit final signal was treated as complete")
	}

	final := &ServerMessage{Data: &MsgServerData{
		Topic:    "p2p_7_8",
		From:     formatUID(7),
		SeqID:    2,
		Type:     "text",
		Content:  "final answer",
		Metadata: map[string]interface{}{"turn_id": "turn-1", "turn_complete": true},
	}}
	if !isCompletedAgentMessage(final) {
		t.Fatal("an explicitly completed visible agent message was not treated as complete")
	}
}

func TestAgentPushCoordinatorBoundsDedupKeys(t *testing.T) {
	coordinator := newAgentPushTurnCoordinator()
	expiresAt := time.Now().Add(agentPushTurnDedupTTL)
	for index := 0; index < maxTrackedAgentPushTurns; index++ {
		coordinator.delivered[fmt.Sprintf("delivered-%d", index)] = expiresAt
	}

	if coordinator.deliverOnce("over-capacity", func() bool { return true }) {
		t.Fatal("new turn was accepted after dedup capacity was exhausted")
	}
}

func TestAgentPushRunningHeartbeatExtendsActiveTurn(t *testing.T) {
	coordinator := newAgentPushTurnCoordinator()
	firstExpiry := time.Now().Add(20 * time.Millisecond)
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-1", State: "running", SourceUID: 7, ExpiresAt: &firstExpiry,
	})
	refreshedExpiry := time.Now().Add(time.Hour)
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-1", State: "waiting", SourceUID: 7, ExpiresAt: &refreshedExpiry,
	})
	time.Sleep(30 * time.Millisecond)

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

func TestAgentPushNewRunReplacesAbandonedTurn(t *testing.T) {
	coordinator := newAgentPushTurnCoordinator()
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-abandoned", State: "running", SourceUID: 7,
	})
	coordinator.observeVisibleMessage(8, 7, &ServerMessage{Data: &MsgServerData{
		Topic: "p2p_7_8", SeqID: 1, Type: "text", Content: "old",
	}}, func() bool {
		t.Error("abandoned run delivered")
		return true
	})

	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-restarted", State: "running", SourceUID: 7,
	})
	delivered := make(chan struct{}, 1)
	coordinator.observeVisibleMessage(8, 7, &ServerMessage{Data: &MsgServerData{
		Topic: "p2p_7_8", SeqID: 2, Type: "text", Content: "new",
	}}, func() bool {
		delivered <- struct{}{}
		return true
	})
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-restarted", State: "completed", SourceUID: 7,
	})

	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("restarted run did not deliver after completion")
	}
}

func TestAgentPushIgnoresExpiredRunningStatus(t *testing.T) {
	coordinator := newAgentPushTurnCoordinator()
	expired := time.Now().Add(-time.Second)
	coordinator.observeStatus(&types.ConversationTaskStatus{
		TopicID: "p2p_7_8", RunID: "run-expired", State: "running", SourceUID: 7, ExpiresAt: &expired,
	})
	msg := &ServerMessage{Data: &MsgServerData{Topic: "p2p_7_8", SeqID: 1, Type: "text", Content: "late"}}
	if coordinator.observeVisibleMessage(8, 7, msg, func() bool { return true }) {
		t.Fatal("an expired task status opened an active notification turn")
	}
}

type aggregateTaskStatusPushStore struct {
	*identityMessageStore
	source    *types.ConversationTaskStatus
	aggregate *types.ConversationTaskStatus
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
	copyOfStatus := *status
	s.source = &copyOfStatus
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
	delivered := make(chan struct{}, 1)
	service.send = func(_ context.Context, _ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		delivered <- struct{}{}
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
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("matching terminal task status did not notify the offline recipient")
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

func TestLegacyP2PAgentReplyNotifiesWithoutTurnMetadata(t *testing.T) {
	const (
		senderUID  int64 = 7
		offlineUID int64 = 8
	)
	db := &identityMessageStore{users: map[int64]*types.User{
		senderUID:  {ID: senderUID, AccountType: types.AccountBot},
		offlineUID: {ID: offlineUID, AccountType: types.AccountHuman},
	}}
	pushStore := &memoryPushSubscriptionStore{subscriptions: []*types.PushSubscription{{
		Endpoint: "https://push.example.test/subscription/legacy-agent",
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
		DisplayContent: "legacy final answer",
		DisplayType:    "text",
		StoredType:     "text",
	}, 1, nil)

	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("legacy agent reply without turn metadata did not notify the offline recipient")
	}
	select {
	case <-delivered:
		t.Fatal("one legacy agent reply delivered more than one push")
	case <-time.After(100 * time.Millisecond):
	}
}

func TestP2PAgentWorkingMessagesNotifyOnlyOnFinalAnswer(t *testing.T) {
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
	delivered := make(chan struct{}, 3)
	service.send = func(_ context.Context, _ []byte, _ *webpush.Subscription, _ *webpush.Options) (*http.Response, error) {
		delivered <- struct{}{}
		return &http.Response{StatusCode: http.StatusCreated, Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	hub := NewHub(db, nil)
	hub.SetPushNotificationService(service)

	hub.fanoutNormalizedMessage(senderUID, "p2p_7_8", 0, &normalizedMessagePayload{
		DisplayContent: "working",
		DisplayType:    "thinking",
		StoredType:     "text",
		ContentBlocks:  []types.ContentBlock{{Type: "thinking", Thinking: "working"}},
	}, 1, nil)

	select {
	case <-delivered:
		t.Fatal("agent working message unexpectedly delivered a push")
	case <-time.After(100 * time.Millisecond):
	}

	hub.fanoutNormalizedMessage(senderUID, "p2p_7_8", 0, &normalizedMessagePayload{
		DisplayContent: "first final segment",
		DisplayType:    "text",
		StoredType:     "text",
		Metadata:       map[string]interface{}{"turn_id": "turn-1"},
	}, 2, nil)
	select {
	case <-delivered:
		t.Fatal("an incomplete agent segment delivered a push")
	case <-time.After(100 * time.Millisecond):
	}
	hub.fanoutNormalizedMessage(senderUID, "p2p_7_8", 0, &normalizedMessagePayload{
		DisplayContent: "second final segment",
		DisplayType:    "text",
		StoredType:     "text",
		Metadata:       map[string]interface{}{"turn_id": "turn-1", "turn_complete": true},
	}, 3, nil)

	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("agent final answer did not deliver a push")
	}
	select {
	case <-delivered:
		t.Fatal("one agent turn delivered more than one push")
	case <-time.After(100 * time.Millisecond):
	}

	hub.fanoutNormalizedMessage(senderUID, "p2p_7_8", 0, &normalizedMessagePayload{
		DisplayContent: "next turn final answer",
		DisplayType:    "text",
		StoredType:     "text",
		Metadata:       map[string]interface{}{"turn_id": "turn-2", "turn_complete": true},
	}, 4, nil)
	select {
	case <-delivered:
	case <-time.After(time.Second):
		t.Fatal("a different agent turn did not deliver a push")
	}

	hub.fanoutNormalizedMessage(senderUID, "p2p_7_8", 0, &normalizedMessagePayload{
		DisplayContent: "duplicate completion",
		DisplayType:    "text",
		StoredType:     "text",
		Metadata:       map[string]interface{}{"turn_id": "turn-2", "turn_complete": true},
	}, 5, nil)
	select {
	case <-delivered:
		t.Fatal("duplicate completion for one agent turn delivered another push")
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
	hub.addClient(&Client{uid: onlineUID, accountType: types.AccountHuman, send: make(chan []byte, 2)})
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
