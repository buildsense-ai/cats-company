package server

import (
	"context"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const (
	maxPushRequestBody = 8 << 10
	maxPushEndpointLen = 512
	maxPushPayloadLen  = 4096
)

// PushNotification is the complete payload sent to a browser. Keep this type
// deliberately small: notification payloads must not contain message IDs,
// sender identities, tokens, or any other sensitive metadata.
type PushNotification struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	Topic string `json:"topic,omitempty"`
	URL   string `json:"url,omitempty"`
	Tag   string `json:"tag,omitempty"`
}

// PushNotificationConfig contains the VAPID credentials used for Web Push.
type PushNotificationConfig struct {
	PublicKey  string
	PrivateKey string
	Subject    string
}

type pushSendFunc func(context.Context, []byte, *webpush.Subscription, *webpush.Options) (*http.Response, error)

// PushNotificationService owns the Web Push API and delivery behavior. The
// service is disabled unless a subscription store and all VAPID values exist.
type PushNotificationService struct {
	store  store.PushSubscriptionStore
	config PushNotificationConfig
	send   pushSendFunc
	logf   func(string, ...interface{})
}

// NewPushNotificationService reads VAPID_PUBLIC_KEY, VAPID_PRIVATE_KEY, and
// VAPID_SUBJECT from the environment.
func NewPushNotificationService(subscriptionStore store.PushSubscriptionStore) *PushNotificationService {
	return NewPushNotificationServiceWithConfig(subscriptionStore, PushNotificationConfig{
		PublicKey:  os.Getenv("VAPID_PUBLIC_KEY"),
		PrivateKey: os.Getenv("VAPID_PRIVATE_KEY"),
		Subject:    os.Getenv("VAPID_SUBJECT"),
	})
}

// NewPushNotificationServiceWithConfig is useful for explicit wiring and tests.
func NewPushNotificationServiceWithConfig(subscriptionStore store.PushSubscriptionStore, config PushNotificationConfig) *PushNotificationService {
	config.PublicKey = strings.TrimSpace(config.PublicKey)
	config.PrivateKey = strings.TrimSpace(config.PrivateKey)
	config.Subject = strings.TrimSpace(config.Subject)
	return &PushNotificationService{
		store:  subscriptionStore,
		config: config,
		send:   webpush.SendNotificationWithContext,
		logf:   log.Printf,
	}
}

// Enabled reports whether delivery and subscription mutation are available.
func (s *PushNotificationService) Enabled() bool {
	return s != nil && s.store != nil && s.config.PublicKey != "" && s.config.PrivateKey != "" && s.config.Subject != ""
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
	Endpoint string                `json:"endpoint"`
	Keys     *pushSubscriptionKeys `json:"keys"`
}

type pushSubscriptionKeys struct {
	P256DH string `json:"p256dh"`
	Auth   string `json:"auth"`
}

type deletePushSubscriptionRequest struct {
	Endpoint string `json:"endpoint"`
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

// SubscriptionHandler is a JWT-protected HTTP handler suitable for direct
// registration on a ServeMux.
func (s *PushNotificationService) SubscriptionHandler() http.Handler {
	return http.HandlerFunc(AuthMiddleware(s.HandleSubscription))
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

	if err := s.store.UpsertPushSubscription(&types.PushSubscription{
		UID:      uid,
		Endpoint: endpoint,
		P256DH:   p256dh,
		Auth:     auth,
	}); err != nil {
		s.logf("web push: save subscription for uid %d: %v", uid, err)
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save subscription"})
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
	endpoint, err := validatePushEndpoint(req.Endpoint)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid endpoint"})
		return
	}
	if err := s.store.DeletePushSubscription(uid, endpoint); err != nil {
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

// SendToUser sends one privacy-minimized notification to every subscription
// belonging to uid. Disabled service is a no-op, so Hub callers do not need
// configuration checks.
func (s *PushNotificationService) SendToUser(ctx context.Context, uid int64, notification PushNotification) error {
	if !s.Enabled() {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if uid <= 0 {
		return errors.New("invalid push notification uid")
	}

	payload, err := json.Marshal(notification)
	if err != nil {
		return fmt.Errorf("marshal push notification: %w", err)
	}
	if len(payload) > maxPushPayloadLen {
		return errors.New("push notification payload is too large")
	}

	subscriptions, err := s.store.ListPushSubscriptions(uid)
	if err != nil {
		return fmt.Errorf("list push subscriptions: %w", err)
	}

	var deliveryErrors []error
	for _, subscription := range subscriptions {
		if subscription == nil {
			continue
		}
		response, sendErr := s.send(ctx, payload, &webpush.Subscription{
			Endpoint: subscription.Endpoint,
			Keys: webpush.Keys{
				P256dh: subscription.P256DH,
				Auth:   subscription.Auth,
			},
		}, &webpush.Options{
			Subscriber:      s.config.Subject,
			VAPIDPublicKey:  s.config.PublicKey,
			VAPIDPrivateKey: s.config.PrivateKey,
			Topic:           notification.Topic,
			TTL:             60,
		})

		status := 0
		if response != nil {
			status = response.StatusCode
			if response.Body != nil {
				if closeErr := response.Body.Close(); closeErr != nil {
					s.logf("web push: close response for endpoint %q: %v", subscription.Endpoint, closeErr)
				}
			}
		}

		if sendErr != nil {
			deliveryErr := fmt.Errorf("send to endpoint %q: %w", subscription.Endpoint, sendErr)
			s.logf("web push: %v", deliveryErr)
			deliveryErrors = append(deliveryErrors, deliveryErr)
			continue
		}
		if status == http.StatusNotFound || status == http.StatusGone {
			if deleteErr := s.store.DeletePushSubscriptionByEndpoint(subscription.Endpoint); deleteErr != nil {
				cleanupErr := fmt.Errorf("remove expired endpoint %q: %w", subscription.Endpoint, deleteErr)
				s.logf("web push: %v", cleanupErr)
				deliveryErrors = append(deliveryErrors, cleanupErr)
			}
			continue
		}
		if status < http.StatusOK || status >= http.StatusMultipleChoices {
			deliveryErr := fmt.Errorf("endpoint %q returned HTTP %d", subscription.Endpoint, status)
			s.logf("web push: %v", deliveryErr)
			deliveryErrors = append(deliveryErrors, deliveryErr)
		}
	}
	return errors.Join(deliveryErrors...)
}

// SendToUserBackground is a convenience for callers without a request context.
func (s *PushNotificationService) SendToUserBackground(uid int64, notification PushNotification) error {
	return s.SendToUser(context.Background(), uid, notification)
}
