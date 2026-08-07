package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
)

type pushKeys struct {
	P256DH string `json:"p256dh"`
	Auth   string `json:"auth"`
}

type pushSubscriptionRequest struct {
	Endpoint       string   `json:"endpoint"`
	Keys           pushKeys `json:"keys"`
	RegistrationID string   `json:"registration_id"`
}

type registrationRequest struct {
	Endpoint       string `json:"endpoint"`
	RegistrationID string `json:"registration_id"`
}

type localPushProxy struct {
	publicKey  string
	privateKey string
	proxy      *httputil.ReverseProxy
	client     *http.Client
	relay      bool
	mu         sync.RWMutex
	byID       map[string]pushSubscriptionRequest
}

func newLocalPushProxy(upstream *url.URL) (*localPushProxy, error) {
	privateKey, publicKey, err := webpush.GenerateVAPIDKeys()
	if err != nil {
		return nil, fmt.Errorf("generate VAPID keys: %w", err)
	}
	client, relay, err := pushHTTPClientFromEnv()
	if err != nil {
		return nil, err
	}
	return &localPushProxy{
		publicKey:  publicKey,
		privateKey: privateKey,
		proxy:      httputil.NewSingleHostReverseProxy(upstream),
		client:     client,
		relay:      relay,
		byID:       make(map[string]pushSubscriptionRequest),
	}, nil
}

type relayRoundTripper struct {
	base     http.RoundTripper
	relayURL *url.URL
	token    string
}

func (t *relayRoundTripper) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil {
		return nil, fmt.Errorf("invalid push provider request")
	}
	relayRequest := request.Clone(request.Context())
	relayTarget := *t.relayURL
	relayRequest.URL = &relayTarget
	relayRequest.Host = ""
	relayRequest.RequestURI = ""
	relayRequest.Header = request.Header.Clone()
	relayRequest.Header.Set("X-Catsco-Push-Endpoint", request.URL.String())
	relayRequest.Header.Set("X-Catsco-Relay-Token", t.token)
	return t.base.RoundTrip(relayRequest)
}

func pushHTTPClientFromEnv() (*http.Client, bool, error) {
	relayURL := strings.TrimSpace(os.Getenv("LOCAL_PUSH_RELAY_URL"))
	tokenFile := strings.TrimSpace(os.Getenv("LOCAL_PUSH_RELAY_TOKEN_FILE"))
	if relayURL == "" && tokenFile == "" {
		return &http.Client{Timeout: 20 * time.Second}, false, nil
	}
	if relayURL == "" || tokenFile == "" {
		return nil, false, fmt.Errorf("LOCAL_PUSH_RELAY_URL and LOCAL_PUSH_RELAY_TOKEN_FILE must be configured together")
	}
	parsedRelayURL, err := url.ParseRequestURI(relayURL)
	if err != nil || parsedRelayURL.Scheme != "https" || parsedRelayURL.Host == "" || parsedRelayURL.User != nil || parsedRelayURL.Fragment != "" {
		return nil, false, fmt.Errorf("LOCAL_PUSH_RELAY_URL must be an absolute HTTPS URL")
	}
	tokenBytes, err := os.ReadFile(tokenFile)
	if err != nil {
		return nil, false, fmt.Errorf("read relay token file: %w", err)
	}
	token := strings.TrimSpace(string(tokenBytes))
	if token == "" || strings.ContainsAny(token, "\r\n") {
		return nil, false, fmt.Errorf("relay token file must contain one non-empty line")
	}
	transport := &relayRoundTripper{
		base:     http.DefaultTransport,
		relayURL: parsedRelayURL,
		token:    token,
	}
	return &http.Client{Transport: transport, Timeout: 20 * time.Second}, true, nil
}

func (p *localPushProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/push/config":
		p.handleConfig(w, r)
	case "/api/push/subscriptions":
		p.handleSubscription(w, r)
	case "/api/push/test":
		p.handleTest(w, r)
	default:
		p.proxy.ServeHTTP(w, r)
	}
}

func (p *localPushProxy) handleConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{
		"enabled":    true,
		"public_key": p.publicKey,
		"mock":       true,
		"relay":      p.relay,
	})
}

func (p *localPushProxy) handleSubscription(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		var request pushSubscriptionRequest
		if !decodeJSON(w, r, &request) {
			return
		}
		request.Endpoint = strings.TrimSpace(request.Endpoint)
		request.RegistrationID = strings.TrimSpace(request.RegistrationID)
		if request.Endpoint == "" || request.RegistrationID == "" || request.Keys.P256DH == "" || request.Keys.Auth == "" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid subscription"})
			return
		}
		p.mu.Lock()
		p.byID[request.RegistrationID] = request
		p.mu.Unlock()
		log.Printf("registered local push subscription: provider=%s relay=%t", endpointHost(request.Endpoint), p.relay)
		writeJSON(w, http.StatusCreated, map[string]bool{"subscribed": true})
	case http.MethodDelete:
		var request registrationRequest
		if !decodeJSON(w, r, &request) {
			return
		}
		p.mu.Lock()
		delete(p.byID, strings.TrimSpace(request.RegistrationID))
		p.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]bool{"subscribed": false})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (p *localPushProxy) handleTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var request registrationRequest
	if !decodeJSON(w, r, &request) {
		return
	}
	registrationID := strings.TrimSpace(request.RegistrationID)
	p.mu.RLock()
	subscription, found := p.byID[registrationID]
	p.mu.RUnlock()
	if !found {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "no active push subscription for this device"})
		return
	}
	providerHost := endpointHost(subscription.Endpoint)
	log.Printf("sending local push test: provider=%s relay=%t", providerHost, p.relay)

	payload, _ := json.Marshal(map[string]string{
		"title": "CatsCo 本地通知测试",
		"body":  "本地 VAPID mock 已成功发送通知。",
		"url":   "/",
		"tag":   fmt.Sprintf("catsco-local-push-test-%d", time.Now().UnixNano()),
	})
	response, err := webpush.SendNotificationWithContext(r.Context(), payload, &webpush.Subscription{
		Endpoint: subscription.Endpoint,
		Keys: webpush.Keys{
			P256dh: subscription.Keys.P256DH,
			Auth:   subscription.Keys.Auth,
		},
	}, &webpush.Options{
		HTTPClient:      p.client,
		Subscriber:      "mailto:local-push@catsco.test",
		VAPIDPublicKey:  p.publicKey,
		VAPIDPrivateKey: p.privateKey,
		TTL:             60,
	})
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		log.Printf("local push delivery failed: provider=%s relay=%t error=%v", providerHost, p.relay, err)
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "push provider rejected the test notification"})
		return
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		log.Printf("local push provider returned HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "push provider rejected the test notification"})
		return
	}
	log.Printf("local push provider accepted request: provider=%s relay=%t status=%d", providerHost, p.relay, response.StatusCode)
	writeJSON(w, http.StatusAccepted, map[string]bool{"accepted": true})
}

func endpointHost(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Hostname() == "" {
		return "unknown"
	}
	return strings.ToLower(parsed.Hostname())
}

func decodeJSON(w http.ResponseWriter, r *http.Request, destination interface{}) bool {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16<<10))
	if err := decoder.Decode(destination); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, payload interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func envOr(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func main() {
	listenAddress := envOr("LOCAL_PUSH_LISTEN", "127.0.0.1:6063")
	upstream, err := url.Parse(envOr("LOCAL_PUSH_UPSTREAM", "http://127.0.0.1:6064"))
	if err != nil {
		log.Fatal(err)
	}
	proxy, err := newLocalPushProxy(upstream)
	if err != nil {
		log.Fatal(err)
	}
	server := &http.Server{
		Addr:              listenAddress,
		Handler:           proxy,
		ReadHeaderTimeout: 10 * time.Second,
	}
	log.Printf("local Web Push mock listening on http://%s (upstream %s)", listenAddress, upstream)
	log.Printf("Cloudflare Push Relay enabled: %t", proxy.relay)
	log.Printf("generated ephemeral VAPID public key: %s", proxy.publicKey)
	log.Fatal(server.ListenAndServe())
}
