package server

// Native APNs registrations share the existing durable subscription store and
// its atomic account transfer/limit semantics. The apns:// namespace is never
// accepted by the browser API or passed to a Web Push transport. No DDL change.
import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

const nativePushPrefix = "apns://"

type NativePushConfig struct {
	TeamID, KeyID, BundleID, KeyFile string
}
type NativePushService struct {
	store     store.PushSubscriptionStore
	config    NativePushConfig
	key       *ecdsa.PrivateKey
	client    *http.Client
	configErr error
	mu        sync.Mutex
	jwt       string
	issued    time.Time
	queue     chan pushDeliveryJob
	workers   sync.Once
}

func NewNativePushService(db store.PushSubscriptionStore) *NativePushService {
	return NewNativePushServiceWithConfig(db, NativePushConfig{
		TeamID: os.Getenv("APNS_TEAM_ID"), KeyID: os.Getenv("APNS_KEY_ID"),
		BundleID: os.Getenv("APNS_BUNDLE_ID"), KeyFile: os.Getenv("APNS_KEY_FILE"),
	})
}
func NewNativePushServiceWithConfig(db store.PushSubscriptionStore, config NativePushConfig) *NativePushService {
	if config.BundleID == "" {
		config.BundleID = "cc.catsco.ios"
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = true
	s := &NativePushService{store: db, config: config, client: &http.Client{Transport: transport, Timeout: pushRequestTimeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, queue: make(chan pushDeliveryJob, maxQueuedPushDeliveries)}
	if config.KeyFile != "" {
		data, err := os.ReadFile(config.KeyFile)
		if err == nil {
			s.key, err = jwt.ParseECPrivateKeyFromPEM(data)
		}
		if err != nil {
			s.configErr = errors.New("APNs signing key could not be loaded")
		}
		if s.key != nil && s.key.Curve.Params().BitSize != 256 {
			s.key = nil
			s.configErr = errors.New("APNs requires a P-256 key")
		}
	}
	return s
}
func (s *NativePushService) Enabled() bool {
	return s != nil && s.store != nil && s.key != nil && s.config.TeamID != "" && s.config.KeyID != "" && s.config.BundleID != "" && s.configErr == nil
}
func (s *NativePushService) ConfigError() error { return s.configErr }
func (s *NativePushService) HandleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, 200, map[string]interface{}{"enabled": s.Enabled(), "bundle_id": s.config.BundleID})
}

type nativePushRegistration struct {
	Token          string `json:"token"`
	Environment    string `json:"environment"`
	RegistrationID string `json:"registration_id"`
	ShowPreviews   bool   `json:"show_previews"`
}

func (s *NativePushService) endpoint(r nativePushRegistration) (string, error) {
	token := strings.ToLower(strings.TrimSpace(r.Token))
	if len(token) < 32 || len(token) > 256 || len(token)%2 != 0 {
		return "", errors.New("invalid device token")
	}
	if _, err := hex.DecodeString(token); err != nil {
		return "", errors.New("invalid device token")
	}
	if r.Environment != "production" && r.Environment != "sandbox" {
		return "", errors.New("invalid APNs environment")
	}
	if !validNativeRegistrationID(r.RegistrationID) {
		return "", errors.New("invalid registration id")
	}
	return nativePushPrefix + r.Environment + "/" + s.config.BundleID + "/" + token, nil
}
func validNativeRegistrationID(id string) bool {
	if len(id) < 16 || len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}
func (s *NativePushService) HandleRegistration(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())
	if uid <= 0 {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var input nativePushRegistration
	if decodeStrictPushJSON(w, r, &input) != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid registration"})
		return
	}
	endpoint, err := s.endpoint(input)
	if err != nil {
		writeJSON(w, 400, map[string]string{"error": err.Error()})
		return
	}
	// Revocation must still work when APNs credentials are temporarily disabled.
	if r.Method == http.MethodDelete {
		if err := s.store.DeletePushSubscription(r.Context(), uid, endpoint, input.RegistrationID); err != nil {
			writeJSON(w, 500, map[string]string{"error": "could not revoke device"})
			return
		}
		writeJSON(w, 200, map[string]bool{"registered": false})
		return
	}
	if !s.Enabled() {
		writeJSON(w, 503, map[string]string{"error": "iOS 推送服务尚未就绪，请稍后重试。"})
		return
	}
	previews := "generic"
	if input.ShowPreviews {
		previews = "preview"
	}
	stored, err := s.store.UpsertPushSubscription(r.Context(), &types.PushSubscription{UID: uid, Endpoint: endpoint, P256DH: previews, Auth: "apns-v1", RegistrationID: input.RegistrationID}, maxPushSubscriptionsPerUser)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "could not register device"})
		return
	}
	if !stored {
		writeJSON(w, 409, map[string]string{"error": "通知设备数量已达上限，请先停用其他设备的通知。"})
		return
	}
	writeJSON(w, 201, map[string]bool{"registered": true})
}
func (s *NativePushService) HandleTest(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())
	if uid <= 0 {
		w.WriteHeader(401)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	if !s.Enabled() {
		writeJSON(w, 503, map[string]string{"error": "iOS 推送服务尚未就绪，请稍后重试。"})
		return
	}
	var input testPushNotificationRequest
	if decodeStrictPushJSON(w, r, &input) != nil || !validNativeRegistrationID(input.RegistrationID) {
		w.WriteHeader(400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), pushDeliveryTimeout)
	defer cancel()
	result, err := s.send(ctx, uid, PushNotification{Title: "CatsCo 测试通知", Body: "这台设备已连接 CatsCo 消息通知。", Tag: "catsco-test"}, nil, input.RegistrationID)
	if result.Accepted > 0 {
		writeJSON(w, 202, map[string]bool{"accepted": true})
		return
	}
	if err != nil {
		log.Printf("native push: test rejected uid=%d", uid)
		writeJSON(w, 502, map[string]string{"error": "Apple 未接受测试通知，请稍后重试。"})
		return
	}
	writeJSON(w, 409, map[string]string{"error": "设备注册已失效，请重新开启通知。"})
}
func (s *NativePushService) bearer() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.jwt != "" && time.Since(s.issued) < 45*time.Minute {
		return s.jwt, nil
	}
	token := jwt.NewWithClaims(jwt.SigningMethodES256, jwt.MapClaims{"iss": s.config.TeamID, "iat": time.Now().Unix()})
	token.Header["kid"] = s.config.KeyID
	signed, err := token.SignedString(s.key)
	if err != nil {
		return "", errors.New("could not sign APNs provider token")
	}
	s.jwt = signed
	s.issued = time.Now()
	return signed, nil
}
func (s *NativePushService) send(ctx context.Context, uid int64, n PushNotification, allowed func(context.Context) bool, registration string) (pushDeliveryResult, error) {
	var result pushDeliveryResult
	if !s.Enabled() {
		return result, nil
	}
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if allowed != nil && !allowed(ctx) {
		return result, nil
	}
	subscriptions, err := s.store.ListPushSubscriptions(ctx, uid)
	if err != nil {
		return result, errors.New("could not load APNs registrations")
	}
	bearer, err := s.bearer()
	if err != nil {
		return result, err
	}
	var failures []error
	for _, sub := range subscriptions {
		if sub == nil || !strings.HasPrefix(sub.Endpoint, nativePushPrefix) || sub.Auth != "apns-v1" || (registration != "" && sub.RegistrationID != registration) {
			continue
		}
		if result.Attempted >= maxPushSubscriptionsPerUser {
			break
		}
		parts := strings.Split(strings.TrimPrefix(sub.Endpoint, nativePushPrefix), "/")
		if len(parts) != 3 || parts[1] != s.config.BundleID {
			continue
		}
		input := nativePushRegistration{Token: parts[2], Environment: parts[0], RegistrationID: sub.RegistrationID}
		if _, err := s.endpoint(input); err != nil {
			continue
		}
		title, body := n.Title, n.Body
		if sub.P256DH != "preview" && n.Tag != "catsco-test" {
			title = "CatsCo"
			body = "你有新的消息或任务进展，点按查看。"
		}
		payload := map[string]interface{}{"aps": map[string]interface{}{"alert": map[string]string{"title": title, "body": body}, "sound": "default", "thread-id": nativeCollapseID(n.URL)}, "registration_id": sub.RegistrationID, "topic_id": n.URL}
		data, err := json.Marshal(payload)
		if err != nil || len(data) > maxPushPayloadLen {
			return result, errors.New("APNs payload too large")
		}
		host := "https://api.push.apple.com"
		if input.Environment == "sandbox" {
			host = "https://api.sandbox.push.apple.com"
		}
		result.Attempted++
		for attempt := 0; attempt < maxPushProviderAttempts; attempt++ {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, host+"/3/device/"+input.Token, bytes.NewReader(data))
			if err != nil {
				return result, err
			}
			req.Header.Set("authorization", "bearer "+bearer)
			req.Header.Set("apns-topic", s.config.BundleID)
			req.Header.Set("apns-push-type", "alert")
			req.Header.Set("apns-priority", "10")
			req.Header.Set("apns-expiration", fmt.Sprint(time.Now().Add(time.Hour).Unix()))
			req.Header.Set("apns-collapse-id", nativeCollapseID(n.URL+"/"+n.Tag))
			req.Header.Set("content-type", "application/json")
			response, sendErr := s.client.Do(req)
			status := 0
			reason := ""
			if response != nil {
				status = response.StatusCode
				var detail struct {
					Reason string `json:"reason"`
				}
				_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&detail)
				_ = response.Body.Close()
				reason = detail.Reason
			}
			if sendErr == nil && status == 200 {
				result.Accepted++
				break
			}
			if sendErr == nil && (status == 410 || status == 400 && (reason == "BadDeviceToken" || reason == "DeviceTokenNotForTopic")) {
				// Generation-bound deletion cannot remove a newer registration/account.
				if err := s.store.DeletePushSubscription(ctx, uid, sub.Endpoint, sub.RegistrationID); err != nil {
					failures = append(failures, errors.New("could not retire APNs token"))
				}
				result.Expired++
				break
			}
			if attempt+1 < maxPushProviderAttempts && shouldRetryPushProviderResponse(status, sendErr) {
				timer := time.NewTimer(pushProviderRetryBackoff)
				select {
				case <-ctx.Done():
					timer.Stop()
					return result, ctx.Err()
				case <-timer.C:
				}
				continue
			}
			// Do not log request errors: net/http embeds the secret device token URL.
			failures = append(failures, fmt.Errorf("APNs delivery failed (HTTP %d)", status))
			break
		}
	}
	return result, errors.Join(failures...)
}
func nativeCollapseID(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:16])
}
func (s *NativePushService) Enqueue(uid int64, n PushNotification, allowed func(context.Context) bool) bool {
	if !s.Enabled() || uid <= 0 {
		return false
	}
	s.workers.Do(func() {
		for range maxConcurrentPushDeliveries {
			go func() {
				for job := range s.queue {
					ctx, cancel := context.WithDeadline(context.Background(), job.queuedAt.Add(pushDeliveryTimeout))
					_, err := s.send(ctx, job.uid, job.notification, job.shouldDeliver, "")
					cancel()
					if err != nil {
						log.Printf("native push: delivery uid=%d: %v", job.uid, err)
					}
				}
			}()
		}
	})
	select {
	case s.queue <- pushDeliveryJob{uid: uid, notification: n, shouldDeliver: allowed, queuedAt: time.Now()}:
		return true
	default:
		return false
	}
}
