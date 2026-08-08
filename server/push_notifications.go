package server

import (
	"context"
	"crypto/elliptic"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const (
	maxPushRequestBody            = 8 << 10
	maxPushEndpointLen            = 512
	maxPushPayloadLen             = 4096
	maxPushSubscriptionsPerUser   = 10
	maxConcurrentPushDeliveries   = 8
	maxQueuedPushDeliveries       = 128
	maxPushProviderAttempts       = 2
	pushRequestTimeout            = 15 * time.Second
	pushDeliveryTimeout           = 20 * time.Second
	pushProviderRetryBackoff      = 100 * time.Millisecond
	pushRelayEndpointHeader       = "X-Catsco-Push-Endpoint"
	pushRelayTokenHeader          = "X-Catsco-Relay-Token"
	pushRelayProviderStatusHeader = "X-Catsco-Relay-Provider-Status"
)

var nonPublicPushPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.31.196.0/24"),
	netip.MustParsePrefix("192.52.193.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.175.48.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/3"),
	netip.MustParsePrefix("::/96"),
	netip.MustParsePrefix("::ffff:0:0:0/96"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("3fff::/20"),
	netip.MustParsePrefix("5f00::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

// PushNotification is the complete payload sent to a browser. Keep this type
// deliberately small: notification payloads must not contain message IDs,
// sender identities, tokens, or any other sensitive metadata.
type PushNotification struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	URL   string `json:"url,omitempty"`
	Tag   string `json:"tag,omitempty"`
}

// PushNotificationConfig contains the VAPID credentials used for Web Push.
type PushNotificationConfig struct {
	PublicKey  string
	PrivateKey string
	Subject    string
	// RelayURL and RelayToken are optional. When both are present, the server
	// encrypts and VAPID-signs for the browser endpoint locally, then sends that
	// exact request through the constrained egress relay.
	RelayURL   string
	RelayToken string
}

type pushSendFunc func(context.Context, []byte, *webpush.Subscription, *webpush.Options) (*http.Response, error)
type pushLookupIPFunc func(context.Context, string) ([]net.IPAddr, error)
type pushDialContextFunc func(context.Context, string, string) (net.Conn, error)

type pushDeliveryJob struct {
	uid                int64
	notification       PushNotification
	shouldSendToDevice func(*types.PushSubscription) bool
	queuedAt           time.Time
}

type pushDeliveryResult struct {
	Attempted int
	Accepted  int
	Expired   int
}

// PushNotificationService owns the Web Push API and delivery behavior. The
// service is disabled unless a subscription store, all VAPID values, and any
// configured relay values are valid.
type PushNotificationService struct {
	store         store.PushSubscriptionStore
	config        PushNotificationConfig
	send          pushSendFunc
	client        webpush.HTTPClient
	configErr     error
	logf          func(string, ...interface{})
	deliveryQueue chan pushDeliveryJob
	startWorkers  sync.Once
}

// NewPushNotificationService reads VAPID_PUBLIC_KEY, VAPID_PRIVATE_KEY, and
// VAPID_SUBJECT from the environment.
func NewPushNotificationService(subscriptionStore store.PushSubscriptionStore) *PushNotificationService {
	return NewPushNotificationServiceWithConfig(subscriptionStore, PushNotificationConfig{
		PublicKey:  os.Getenv("VAPID_PUBLIC_KEY"),
		PrivateKey: os.Getenv("VAPID_PRIVATE_KEY"),
		Subject:    os.Getenv("VAPID_SUBJECT"),
		RelayURL:   os.Getenv("CATSCO_PUSH_RELAY_URL"),
		RelayToken: os.Getenv("CATSCO_PUSH_RELAY_TOKEN"),
	})
}

// NewPushNotificationServiceWithConfig is useful for explicit wiring and tests.
func NewPushNotificationServiceWithConfig(subscriptionStore store.PushSubscriptionStore, config PushNotificationConfig) *PushNotificationService {
	config.PublicKey = strings.TrimSpace(config.PublicKey)
	config.PrivateKey = strings.TrimSpace(config.PrivateKey)
	config.Subject = strings.TrimSpace(config.Subject)
	config.RelayURL = strings.TrimSpace(config.RelayURL)
	config.RelayToken = strings.TrimSpace(config.RelayToken)
	client, configErr := newPushHTTPClientWithRelay(config.RelayURL, config.RelayToken)
	return &PushNotificationService{
		store:         subscriptionStore,
		config:        config,
		send:          webpush.SendNotificationWithContext,
		client:        client,
		configErr:     configErr,
		logf:          log.Printf,
		deliveryQueue: make(chan pushDeliveryJob, maxQueuedPushDeliveries),
	}
}

func newPushHTTPClient() *http.Client {
	dialer := &net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}
	return newPushHTTPClientWithNetwork(net.DefaultResolver.LookupIPAddr, dialer.DialContext, pushRequestTimeout)
}

func newPushHTTPClientWithRelay(relayURL, relayToken string) (*http.Client, error) {
	relayURL = strings.TrimSpace(relayURL)
	relayToken = strings.TrimSpace(relayToken)
	if relayURL == "" && relayToken == "" {
		return newPushHTTPClient(), nil
	}
	if relayURL == "" || relayToken == "" {
		return nil, errors.New("CATSCO_PUSH_RELAY_URL and CATSCO_PUSH_RELAY_TOKEN must be configured together")
	}
	if strings.ContainsAny(relayToken, "\r\n") {
		return nil, errors.New("CATSCO_PUSH_RELAY_TOKEN must be a single-line value")
	}

	parsedRelayURL, err := validatePushRelayURL(relayURL)
	if err != nil {
		return nil, fmt.Errorf("invalid CATSCO_PUSH_RELAY_URL: %w", err)
	}

	client := newPushHTTPClient()
	client.Transport = &pushRelayRoundTripper{
		base:     client.Transport,
		relayURL: parsedRelayURL,
		token:    relayToken,
	}
	return client, nil
}

type pushRelayRoundTripper struct {
	base     http.RoundTripper
	relayURL *url.URL
	token    string
}

func (t *pushRelayRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if t == nil || t.relayURL == nil || strings.TrimSpace(t.token) == "" {
		return nil, errors.New("push relay transport is not configured")
	}
	if request == nil || request.URL == nil {
		return nil, errors.New("push relay received an invalid provider request")
	}

	endpoint := request.URL.String()
	relayRequest := request.Clone(request.Context())
	relayTarget := *t.relayURL
	relayRequest.URL = &relayTarget
	relayRequest.Host = ""
	relayRequest.RequestURI = ""
	relayRequest.Header = request.Header.Clone()
	relayRequest.Header.Set(pushRelayEndpointHeader, endpoint)
	relayRequest.Header.Set(pushRelayTokenHeader, t.token)

	base := t.base
	if base == nil {
		base = http.DefaultTransport
	}
	return base.RoundTrip(relayRequest)
}

func newPushHTTPClientWithNetwork(lookup pushLookupIPFunc, dial pushDialContextFunc, timeout time.Duration) *http.Client {
	if timeout <= 0 {
		timeout = pushRequestTimeout
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Resolve and dial the endpoint directly so proxies and DNS rebinding cannot
	// bypass the public-address check.
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("parse push endpoint address: %w", err)
		}
		addresses, err := lookup(ctx, host)
		if err != nil {
			return nil, fmt.Errorf("resolve push endpoint %q: %w", host, err)
		}
		if len(addresses) == 0 {
			return nil, fmt.Errorf("resolve push endpoint %q: no addresses", host)
		}
		for _, resolved := range addresses {
			if !isPublicPushIP(resolved.IP) {
				return nil, fmt.Errorf("push endpoint %q resolved to non-publicly routable address %s", host, resolved.IP)
			}
		}

		var dialErrors []error
		for _, resolved := range addresses {
			connection, dialErr := dial(ctx, network, net.JoinHostPort(resolved.IP.String(), port))
			if dialErr == nil {
				return connection, nil
			}
			dialErrors = append(dialErrors, dialErr)
		}
		return nil, fmt.Errorf("dial push endpoint %q: %w", host, errors.Join(dialErrors...))
	}
	return &http.Client{
		Transport: transport,
		Timeout:   timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func validatePushRelayURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxPushEndpointLen {
		return nil, errors.New("relay URL is empty or too long")
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || strings.Contains(raw, "#") || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, errors.New("relay URL must be an absolute HTTPS URL")
	}
	if parsed.Port() != "" && parsed.Port() != "443" {
		return nil, errors.New("relay URL must use the standard HTTPS port")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return nil, errors.New("relay URL host is local")
	}
	if ip := net.ParseIP(host); ip != nil && !isPublicPushIP(ip) {
		return nil, errors.New("relay URL IP is not publicly routable")
	}
	return parsed, nil
}

func isPublicPushIP(ip net.IP) bool {
	address, ok := netip.AddrFromSlice(ip)
	if !ok {
		return false
	}
	address = address.Unmap()
	if !address.IsGlobalUnicast() || address.IsPrivate() || address.IsLoopback() || address.IsLinkLocalUnicast() {
		return false
	}
	for _, prefix := range nonPublicPushPrefixes {
		if prefix.Contains(address) {
			return false
		}
	}
	return true
}

// Enabled reports whether delivery and subscription mutation are available.
func (s *PushNotificationService) Enabled() bool {
	return s != nil && s.store != nil && s.client != nil && s.configErr == nil && s.config.PublicKey != "" && s.config.PrivateKey != "" && s.config.Subject != ""
}

// ConfigError returns a relay configuration error without exposing any secret
// values. It is primarily used by startup logging.
func (s *PushNotificationService) ConfigError() error {
	if s == nil {
		return errors.New("push notification service is not configured")
	}
	return s.configErr
}

// isAuthoritativePushProviderResponse reports whether a terminal status can
// safely be attributed to the browser push provider. Direct delivery receives
// provider responses directly. Relay delivery requires the Worker marker so a
// relay-owned route or authentication error cannot delete a valid subscription.
func (s *PushNotificationService) isAuthoritativePushProviderResponse(response *http.Response) bool {
	if s == nil {
		return false
	}
	if s.config.RelayURL == "" {
		return true
	}
	return response != nil && strings.TrimSpace(response.Header.Get(pushRelayProviderStatusHeader)) == strconv.Itoa(response.StatusCode)
}

// HandleStatus serves the public GET status endpoint.
func (s *PushNotificationService) HandleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	enabled := s.Enabled()
	publicKey := ""
	if enabled {
		publicKey = s.config.PublicKey
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"enabled":    enabled,
		"public_key": publicKey,
	})
}

type pushSubscriptionRequest struct {
	Endpoint       string                `json:"endpoint"`
	Keys           *pushSubscriptionKeys `json:"keys"`
	RegistrationID string                `json:"registration_id"`
}

type pushSubscriptionKeys struct {
	P256DH string `json:"p256dh"`
	Auth   string `json:"auth"`
}

type deletePushSubscriptionRequest struct {
	Endpoint         string `json:"endpoint"`
	RegistrationID   string `json:"registration_id"`
	AllRegistrations bool   `json:"all_registrations"`
}

type testPushNotificationRequest struct {
	RegistrationID string `json:"registration_id"`
}

// HandleSubscription serves POST and DELETE for the authenticated user. Mount
// this handler behind AuthMiddleware (JWT only); it intentionally trusts only
// the uid established in request context and never accepts a uid in the body.
func (s *PushNotificationService) HandleSubscription(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		w.Header().Set("Allow", http.MethodPost+", "+http.MethodDelete)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	uid, ok := r.Context().Value(uidKey).(int64)
	if !ok || uid <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if !s.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "push notifications are disabled"})
		return
	}

	switch r.Method {
	case http.MethodPost:
		s.handleSubscribe(w, r, uid)
	case http.MethodDelete:
		s.handleUnsubscribe(w, r, uid)
	}
}

// HandleTest asks the push provider to deliver a test notification to the
// authenticated user's current browser. Acceptance does not guarantee display.
func (s *PushNotificationService) HandleTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	uid, ok := r.Context().Value(uidKey).(int64)
	if !ok || uid <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if !s.Enabled() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "push notifications are disabled"})
		return
	}

	var req testPushNotificationRequest
	if err := decodeStrictPushJSON(w, r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	registrationID := strings.TrimSpace(req.RegistrationID)
	if registrationID == "" || len(registrationID) > 64 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid registration id"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), pushRequestTimeout)
	defer cancel()
	result, err := s.sendToUserFiltered(ctx, uid, PushNotification{
		Title: "CatsCo 测试通知",
		Body:  "如果你看到这条通知，说明当前设备与浏览器可以接收 CatsCo 消息通知。",
		URL:   "/",
		Tag:   fmt.Sprintf("catsco-push-test-%d", time.Now().UnixNano()),
	}, func(subscription *types.PushSubscription) bool {
		return subscription.RegistrationID == registrationID
	})
	if result.Accepted > 0 {
		if err != nil {
			s.logf("web push: partial test delivery for uid %d: %v", uid, err)
		}
		writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
		return
	}
	if result.Expired > 0 && result.Expired == result.Attempted {
		writeJSON(w, http.StatusConflict, map[string]string{
			"code":  "push_subscription_expired",
			"error": "push subscription for this device has expired",
		})
		return
	}
	if err != nil {
		s.logf("web push: test delivery for uid %d: %v", uid, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{
			"code":  "push_provider_rejected",
			"error": "push provider rejected the test notification",
		})
		return
	}
	if result.Attempted == 0 {
		writeJSON(w, http.StatusConflict, map[string]string{
			"code":  "push_subscription_missing",
			"error": "no active push subscription for this device",
		})
		return
	}
	writeJSON(w, http.StatusConflict, map[string]string{
		"code":  "push_subscription_missing",
		"error": "no active push subscription for this device",
	})
}

func (s *PushNotificationService) handleSubscribe(w http.ResponseWriter, r *http.Request, uid int64) {
	var req pushSubscriptionRequest
	if err := decodeStrictPushJSON(w, r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if req.Keys == nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "keys are required"})
		return
	}

	endpoint, err := validatePushEndpoint(req.Endpoint)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid endpoint"})
		return
	}
	p256dh, auth, err := validatePushKeys(req.Keys.P256DH, req.Keys.Auth)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid subscription keys"})
		return
	}
	registrationID := strings.TrimSpace(req.RegistrationID)
	if len(registrationID) > 64 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid registration id"})
		return
	}

	stored, err := s.store.UpsertPushSubscription(r.Context(), &types.PushSubscription{
		UID:            uid,
		Endpoint:       endpoint,
		P256DH:         p256dh,
		Auth:           auth,
		RegistrationID: registrationID,
	}, maxPushSubscriptionsPerUser)
	if err != nil {
		s.logf("web push: save subscription for uid %d: %v", uid, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save subscription"})
		return
	}
	if !stored {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "push subscription limit reached"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]bool{"subscribed": true})
}

func (s *PushNotificationService) handleUnsubscribe(w http.ResponseWriter, r *http.Request, uid int64) {
	var req deletePushSubscriptionRequest
	if err := decodeStrictPushJSON(w, r, &req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	registrationID := strings.TrimSpace(req.RegistrationID)
	if len(registrationID) > 64 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid registration id"})
		return
	}
	if strings.TrimSpace(req.Endpoint) == "" {
		if registrationID == "" || req.AllRegistrations {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid endpoint"})
			return
		}
		if err := s.store.DeletePushSubscriptionsByRegistrationID(r.Context(), uid, registrationID); err != nil {
			s.logf("web push: delete registration subscriptions for uid %d: %v", uid, err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to delete subscriptions"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"subscribed": false})
		return
	}
	endpoint, err := validatePushEndpoint(req.Endpoint)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid endpoint"})
		return
	}
	if req.AllRegistrations {
		if err := s.store.DeletePushSubscriptionsByEndpoint(r.Context(), uid, endpoint); err != nil {
			s.logf("web push: delete endpoint subscriptions for uid %d: %v", uid, err)
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to delete subscriptions"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"subscribed": false})
		return
	}
	if err := s.store.DeletePushSubscription(r.Context(), uid, endpoint, registrationID); err != nil {
		s.logf("web push: delete subscription for uid %d: %v", uid, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to delete subscription"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"subscribed": false})
}

func decodeStrictPushJSON(w http.ResponseWriter, r *http.Request, dst interface{}) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxPushRequestBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON object")
	}
	return nil
}

func validatePushEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" || len(raw) > maxPushEndpointLen {
		return "", errors.New("endpoint is empty or too long")
	}
	parsed, err := url.ParseRequestURI(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
		return "", errors.New("endpoint must be an absolute HTTPS URL")
	}
	if parsed.Port() != "" && parsed.Port() != "443" {
		return "", errors.New("endpoint must use the standard HTTPS port")
	}
	host := strings.TrimSuffix(strings.ToLower(parsed.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") {
		return "", errors.New("endpoint host is local")
	}
	if ip := net.ParseIP(host); ip != nil && !ip.IsGlobalUnicast() {
		return "", errors.New("endpoint IP is not globally routable")
	} else if ip != nil && (ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()) {
		return "", errors.New("endpoint IP is private")
	}
	return parsed.String(), nil
}

func validatePushKeys(rawP256DH, rawAuth string) (string, string, error) {
	rawP256DH = strings.TrimSpace(rawP256DH)
	rawAuth = strings.TrimSpace(rawAuth)
	p256dh, err := decodePushKey(rawP256DH)
	if err != nil || len(p256dh) != 65 || p256dh[0] != 4 {
		return "", "", errors.New("invalid p256dh key")
	}
	x, y := elliptic.Unmarshal(elliptic.P256(), p256dh)
	if x == nil || y == nil {
		return "", "", errors.New("p256dh key is not a P-256 point")
	}
	auth, err := decodePushKey(rawAuth)
	if err != nil || len(auth) < 16 || len(auth) > 64 {
		return "", "", errors.New("invalid auth key")
	}
	return rawP256DH, rawAuth, nil
}

func decodePushKey(value string) ([]byte, error) {
	if value == "" || len(value) > 256 {
		return nil, errors.New("key is empty or too long")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err == nil {
		return decoded, nil
	}
	return base64.URLEncoding.DecodeString(value)
}

func pushEndpointLogID(endpoint string) string {
	digest := sha256.Sum256([]byte(endpoint))
	host := "unknown"
	if parsed, err := url.Parse(endpoint); err == nil && parsed.Hostname() != "" {
		host = strings.ToLower(parsed.Hostname())
	}
	return fmt.Sprintf("%s#%x", host, digest[:6])
}

// pushSubscriptionID is a stable, non-reversible identity for the browser
// Push subscription shared by tabs in the same browser profile.
func pushSubscriptionID(endpoint string) string {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(endpoint))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

// webpush-go expects an email address rather than a mailto URI and adds the
// scheme itself. Keep accepting the standard URI form used by our deployment
// configuration without producing an invalid "mailto:mailto:" VAPID subject.
func webPushSubscriber(subject string) string {
	subject = strings.TrimSpace(subject)
	const mailtoPrefix = "mailto:"
	if len(subject) >= len(mailtoPrefix) && strings.EqualFold(subject[:len(mailtoPrefix)], mailtoPrefix) {
		return strings.TrimSpace(subject[len(mailtoPrefix):])
	}
	return subject
}

func redactPushEndpointError(err error, endpoint string) error {
	if err == nil {
		return nil
	}
	return errors.New(strings.ReplaceAll(err.Error(), endpoint, pushEndpointLogID(endpoint)))
}

// SendToUser sends one privacy-minimized notification to every subscription
// belonging to uid. Disabled service is a no-op, so Hub callers do not need
// configuration checks.
func (s *PushNotificationService) SendToUser(ctx context.Context, uid int64, notification PushNotification) error {
	_, err := s.sendToUserFiltered(ctx, uid, notification, nil)
	return err
}

func (s *PushNotificationService) sendToUserFiltered(ctx context.Context, uid int64, notification PushNotification, shouldSendToDevice func(*types.PushSubscription) bool) (pushDeliveryResult, error) {
	var result pushDeliveryResult
	if !s.Enabled() {
		return result, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if uid <= 0 {
		return result, errors.New("invalid push notification uid")
	}

	payload, err := json.Marshal(notification)
	if err != nil {
		return result, fmt.Errorf("marshal push notification: %w", err)
	}
	if len(payload) > maxPushPayloadLen {
		return result, errors.New("push notification payload is too large")
	}

	subscriptions, err := s.store.ListPushSubscriptions(ctx, uid)
	if err != nil {
		return result, fmt.Errorf("list push subscriptions: %w", err)
	}

	var deliveryErrors []error
	deliveries := 0
	for _, subscription := range subscriptions {
		if subscription == nil {
			continue
		}
		if shouldSendToDevice != nil && !shouldSendToDevice(subscription) {
			continue
		}
		if deliveries >= maxPushSubscriptionsPerUser {
			break
		}
		if err := ctx.Err(); err != nil {
			deliveryErrors = append(deliveryErrors, err)
			break
		}
		deliveries++
		result.Attempted++
		endpointID := pushEndpointLogID(subscription.Endpoint)
		var response *http.Response
		var sendErr error
		status := 0
		for attempt := 1; attempt <= maxPushProviderAttempts; attempt++ {
			response, sendErr = s.send(ctx, payload, &webpush.Subscription{
				Endpoint: subscription.Endpoint,
				Keys: webpush.Keys{
					P256dh: subscription.P256DH,
					Auth:   subscription.Auth,
				},
			}, &webpush.Options{
				HTTPClient:      s.client,
				Subscriber:      webPushSubscriber(s.config.Subject),
				VAPIDPublicKey:  s.config.PublicKey,
				VAPIDPrivateKey: s.config.PrivateKey,
				TTL:             60,
			})

			status = 0
			if response != nil {
				status = response.StatusCode
				if response.Body != nil {
					if closeErr := response.Body.Close(); closeErr != nil {
						s.logf("web push: close response for provider %q: %v", endpointID, closeErr)
					}
				}
			}
			if attempt == maxPushProviderAttempts || !shouldRetryPushProviderResponse(status, sendErr) {
				break
			}
			timer := time.NewTimer(pushProviderRetryBackoff)
			select {
			case <-ctx.Done():
				timer.Stop()
				sendErr = ctx.Err()
				attempt = maxPushProviderAttempts
			case <-timer.C:
			}
		}

		if sendErr != nil {
			deliveryErr := fmt.Errorf("send to provider %q: %w", endpointID, redactPushEndpointError(sendErr, subscription.Endpoint))
			s.logf("web push: %v", deliveryErr)
			deliveryErrors = append(deliveryErrors, deliveryErr)
			continue
		}
		if status == http.StatusNotFound || status == http.StatusGone {
			if !s.isAuthoritativePushProviderResponse(response) {
				deliveryErr := fmt.Errorf("relay returned HTTP %d without a provider response marker for %q", status, endpointID)
				s.logf("web push: %v", deliveryErr)
				deliveryErrors = append(deliveryErrors, deliveryErr)
				continue
			}
			if deleteErr := s.store.DeletePushSubscription(ctx, uid, subscription.Endpoint, subscription.RegistrationID); deleteErr != nil {
				cleanupErr := fmt.Errorf("remove expired provider %q: %w", endpointID, redactPushEndpointError(deleteErr, subscription.Endpoint))
				s.logf("web push: %v", cleanupErr)
				deliveryErrors = append(deliveryErrors, cleanupErr)
			}
			result.Expired++
			continue
		}
		if status < http.StatusOK || status >= http.StatusMultipleChoices {
			deliveryErr := fmt.Errorf("provider %q returned HTTP %d", endpointID, status)
			s.logf("web push: %v", deliveryErr)
			deliveryErrors = append(deliveryErrors, deliveryErr)
			continue
		}
		result.Accepted++
	}
	return result, errors.Join(deliveryErrors...)
}

func shouldRetryPushProviderResponse(status int, sendErr error) bool {
	if sendErr != nil {
		return true
	}
	return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError
}

func (s *PushNotificationService) runDeliveryWorkers() {
	for range maxConcurrentPushDeliveries {
		go func() {
			for job := range s.deliveryQueue {
				ctx, cancel := context.WithDeadline(context.Background(), job.queuedAt.Add(pushDeliveryTimeout))
				if _, err := s.sendToUserFiltered(ctx, job.uid, job.notification, job.shouldSendToDevice); err != nil {
					s.logf("send offline push: uid=%d err=%v", job.uid, err)
				}
				cancel()
			}
		}()
	}
}

// EnqueueToUser queues a best-effort delivery without adding backpressure to
// chat fanout. A fixed worker pool bounds concurrency while the bounded queue
// absorbs ordinary message bursts.
func (s *PushNotificationService) EnqueueToUser(uid int64, notification PushNotification) bool {
	return s.EnqueueToUserFiltered(uid, notification, nil)
}

// EnqueueToUserFiltered queues delivery to subscriptions accepted by shouldSendToDevice.
// The filter runs in the delivery worker so it observes current device visibility.
func (s *PushNotificationService) EnqueueToUserFiltered(uid int64, notification PushNotification, shouldSendToDevice func(*types.PushSubscription) bool) bool {
	if !s.Enabled() || uid <= 0 {
		return false
	}
	s.startWorkers.Do(s.runDeliveryWorkers)
	select {
	case s.deliveryQueue <- pushDeliveryJob{
		uid:                uid,
		notification:       notification,
		shouldSendToDevice: shouldSendToDevice,
		queuedAt:           time.Now(),
	}:
		return true
	default:
		return false
	}
}
