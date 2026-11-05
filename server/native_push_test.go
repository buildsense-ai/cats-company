package server

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/openchat/openchat/server/store/types"
)

type apnsRoundTripper func(*http.Request) (*http.Response, error)

func (f apnsRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func nativePushFixture(t *testing.T) (*NativePushService, *memoryPushSubscriptionStore) {
	t.Helper()
	db := &memoryPushSubscriptionStore{}
	s := NewNativePushServiceWithConfig(db, NativePushConfig{TeamID: "TEAM123", KeyID: "KEY123", BundleID: "cc.catsco.ios"})
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	s.key = key
	db.subscriptions = []*types.PushSubscription{{UID: 7, Endpoint: "apns://production/cc.catsco.ios/" + strings.Repeat("ab", 32), Auth: "apns-v1", P256DH: "generic", RegistrationID: "registration-current"}}
	return s, db
}
func TestNativePushProviderContract(t *testing.T) {
	s, db := nativePushFixture(t)
	calls := 0
	s.client = &http.Client{Transport: apnsRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host != "api.push.apple.com" || r.Header.Get("apns-topic") != "cc.catsco.ios" || r.Header.Get("apns-push-type") != "alert" || r.Header.Get("apns-priority") != "10" {
			t.Fatalf("incorrect APNs routing/headers")
		}
		bearer := strings.TrimPrefix(r.Header.Get("authorization"), "bearer ")
		token, err := jwt.Parse(bearer, func(token *jwt.Token) (interface{}, error) {
			if token.Header["kid"] != "KEY123" {
				t.Error("missing key ID")
			}
			return &s.key.PublicKey, nil
		}, jwt.WithValidMethods([]string{"ES256"}))
		if err != nil || !token.Valid {
			t.Fatal("invalid provider signature", err)
		}
		claims := token.Claims.(jwt.MapClaims)
		if claims["iss"] != "TEAM123" {
			t.Error("wrong team")
		}
		var payload map[string]interface{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["aps"].(map[string]interface{})["content-available"] != float64(1) {
			t.Fatal("ordinary alert cannot request widget background refresh")
		}
		alert := payload["aps"].(map[string]interface{})["alert"].(map[string]interface{})
		if strings.Contains(fmt.Sprint(alert), "private") {
			t.Fatal("default preview leaked content")
		}
		if payload["topic_id"] != "p2p_7_8" || payload["registration_id"] != db.subscriptions[0].RegistrationID {
			t.Fatal("missing account-bound routing")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	result, err := s.send(context.Background(), 7, PushNotification{Title: "private agent", Body: "private message", URL: "p2p_7_8"}, nil, "")
	if err != nil || result.Accepted != 1 || calls != 1 {
		t.Fatalf("delivery: %+v %v calls=%d", result, err, calls)
	}
}
func TestNativePushDeliveryIsolationAndPreviews(t *testing.T) {
	s, db := nativePushFixture(t)
	db.subscriptions = append(db.subscriptions, &types.PushSubscription{UID: 7, Endpoint: "https://web-push.example/device", RegistrationID: "web-id"})
	db.subscriptions = append(db.subscriptions,
		&types.PushSubscription{UID: 7, Endpoint: "surface://7/widget-registration", Auth: "surface-v1:hash"},
		&types.PushSubscription{UID: 7, Endpoint: "activitykit://production/cc.catsco.ios/" + strings.Repeat("cd", 32), Auth: "activity-v1"},
	)
	calls := 0
	s.client = &http.Client{Transport: apnsRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		var payload map[string]interface{}
		_ = json.NewDecoder(r.Body).Decode(&payload)
		alert := payload["aps"].(map[string]interface{})["alert"].(map[string]interface{})
		if alert["body"] != "visible reply" {
			t.Error("explicit previews ignored")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	db.subscriptions[0].P256DH = "preview"
	n := PushNotification{Body: "visible reply"}
	_, _ = s.send(context.Background(), 7, n, func(context.Context) bool { return false }, "")
	_, _ = s.send(context.Background(), 7, n, nil, "registration-from-old-account")
	if calls != 0 {
		t.Fatal("muted/stale registrations were delivered")
	}
	result, err := s.send(context.Background(), 7, n, nil, "")
	if err != nil || result.Accepted != 1 || calls != 1 {
		t.Fatalf("native/web isolation failed: %+v %v", result, err)
	}
}
func TestNativePushProviderErrors(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		reason   string
		attempts int
		expired  bool
	}{
		{"uninstalled", 410, "Unregistered", 1, true},
		{"bad-token", 400, "BadDeviceToken", 1, true},
		{"wrong-topic", 400, "DeviceTokenNotForTopic", 1, true},
		{"bad-credentials", 403, "InvalidProviderToken", 1, false},
		{"throttled", 429, "TooManyRequests", 2, false},
		{"server-error", 503, "ServiceUnavailable", 2, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, db := nativePushFixture(t)
			calls := 0
			s.client = &http.Client{Transport: apnsRoundTripper(func(*http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"reason":%q}`, tc.reason)))}, nil
			})}
			result, err := s.send(context.Background(), 7, PushNotification{}, nil, "")
			if calls != tc.attempts || result.Accepted != 0 {
				t.Fatalf("bad retry count/result: calls=%d result=%+v", calls, result)
			}
			if tc.expired {
				if result.Expired != 1 || db.deletedUID != 7 || db.deletedRegistrationID != "registration-current" {
					t.Fatal("expired registration not retired by generation")
				}
			} else if err == nil || db.deleted != "" {
				t.Fatal("transient/auth failure removed subscription or lost error")
			}
		})
	}
}
func TestNativePushRegistrationAuthorizationAndValidation(t *testing.T) {
	s, db := nativePushFixture(t)
	body := func(token, env, registration string) string {
		return fmt.Sprintf(`{"token":%q,"environment":%q,"registration_id":%q}`, token, env, registration)
	}
	for _, tc := range []struct {
		name, method, body string
		uid                int64
		status             int
	}{
		{"unauthorized", "POST", body(strings.Repeat("ab", 32), "production", "registration-current"), 0, 401},
		{"valid", "POST", body(strings.Repeat("ab", 32), "production", "registration-current"), 7, 201},
		{"bad-token", "POST", body("https://attacker.invalid", "production", "registration-current"), 7, 400},
		{"bad-env", "POST", body(strings.Repeat("ab", 32), "arbitrary-host", "registration-current"), 7, 400},
		{"bad-registration", "POST", body(strings.Repeat("ab", 32), "production", "bad"), 7, 400},
		{"untrusted-topic", "POST", `{"token":"abc","topic":"another.bundle"}`, 7, 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(tc.method, "/api/push/ios/registrations", bytes.NewBufferString(tc.body))
			if tc.uid > 0 {
				r = r.WithContext(context.WithValue(r.Context(), uidKey, tc.uid))
			}
			w := httptest.NewRecorder()
			s.HandleRegistration(w, r)
			if w.Code != tc.status {
				t.Fatalf("HTTP %d expected %d: %s", w.Code, tc.status, w.Body.String())
			}
		})
	}
	if db.upserted == nil || db.upserted.UID != 7 || db.upserted.P256DH != "generic" {
		t.Fatal("incorrect registration")
	}
	s.key = nil
	r := httptest.NewRequest("DELETE", "/", strings.NewReader(body(strings.Repeat("ab", 32), "production", "registration-current")))
	r = r.WithContext(context.WithValue(r.Context(), uidKey, int64(7)))
	w := httptest.NewRecorder()
	s.HandleRegistration(w, r)
	if w.Code != 200 || db.deletedRegistrationID != "registration-current" {
		t.Fatal("disabled provider prevented logout revocation")
	}
}
func TestNativeTaskFallbackUsesExistingTurnDedup(t *testing.T) {
	c := newAgentPushTurnCoordinator()
	c.terminalSettleDelay = time.Hour
	status := &types.ConversationTaskStatus{TopicID: "p2p_7_8", SourceUID: 8, RunID: "run", State: "completed", UpdatedAt: time.Now()}
	c.observeStatus(status)
	scope := agentPushScope(8, status.TopicID)
	c.addNativeStatusFallback(7, scope, status.RunID, func(string) bool { return true })
	c.mu.Lock()
	turn := c.trackedTurns[newAgentPushTrackedTurnKey(scope, status.RunID)]
	first := turn.candidates[7].id
	c.mu.Unlock()
	c.addNativeStatusFallback(7, scope, status.RunID, func(string) bool { return true })
	c.mu.Lock()
	defer c.mu.Unlock()
	if turn.candidates[7].id != first || len(turn.candidates) != 1 {
		t.Fatal("status heartbeat replaced or duplicated the candidate")
	}
	if turn.timer != nil {
		turn.timer.Stop()
	}
	if turn.deliveryTimer != nil {
		turn.deliveryTimer.Stop()
	}
}
