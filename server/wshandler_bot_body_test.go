package server

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type wsBotBodyStore struct {
	store.Store
	mu     sync.Mutex
	apiKey string
	botUID int64
	bodyID string
}

func (s *wsBotBodyStore) GetBotByAPIKey(apiKey string) (int64, error) {
	if apiKey == s.apiKey {
		return s.botUID, nil
	}
	return 0, errors.New("not found")
}

func (s *wsBotBodyStore) EnsureBotBodyBinding(botUID int64, bodyID string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if botUID != s.botUID {
		return "", false, errors.New("bot not found")
	}
	if s.bodyID == "" {
		s.bodyID = bodyID
	}
	return s.bodyID, s.bodyID == bodyID, nil
}

func (s *wsBotBodyStore) GetBotBodyID(botUID int64) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if botUID != s.botUID {
		return "", errors.New("bot not found")
	}
	return s.bodyID, nil
}

func (s *wsBotBodyStore) GetUser(id int64) (*types.User, error) {
	if id != s.botUID {
		return nil, errors.New("not found")
	}
	return &types.User{
		ID:          id,
		Username:    "bot",
		DisplayName: "Bot",
		AccountType: types.AccountBot,
	}, nil
}

func (s *wsBotBodyStore) GetFriends(uid int64) ([]*types.User, error) {
	return nil, nil
}

func (s *wsBotBodyStore) GetBotOwner(botUID int64) (int64, error) {
	return 0, errors.New("not found")
}

func TestServeWSRequiresBotBodyIDForAPIKeyConnections(t *testing.T) {
	t.Setenv("CATSCO_REQUIRE_BOT_BODY_ID", "1")
	botUID := int64(42)
	apiKey := GenerateAPIKey(botUID)
	wsURL, _, cleanup := newBotBodyTestServer(apiKey, botUID)
	defer cleanup()

	headers := http.Header{}
	headers.Set("X-API-Key", apiKey)
	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, headers)
	if conn != nil {
		conn.Close()
	}
	closeResponse(resp)

	if err == nil {
		t.Fatal("expected missing body id to reject the websocket upgrade")
	}
	if resp == nil || resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("missing body id status = %v, want 400", responseStatus(resp))
	}
}

func TestServeWSAllowsLegacyBotBodyWithoutHeaderTemporarily(t *testing.T) {
	botUID := int64(40)
	apiKey := GenerateAPIKey(botUID)
	wsURL, hub, cleanup := newBotBodyTestServer(apiKey, botUID)
	defer cleanup()

	first, resp, err := dialBotWithoutBody(wsURL, apiKey)
	closeResponse(resp)
	if err != nil {
		t.Fatalf("legacy bot body dial failed: %v", err)
	}
	waitForClientCount(t, hub, botUID, 1)

	reconnect, resp, err := dialBotWithoutBody(wsURL, apiKey)
	closeResponse(resp)
	if err != nil {
		first.Close()
		t.Fatalf("legacy bot reconnect failed: %v", err)
	}
	defer reconnect.Close()
	waitForClientCount(t, hub, botUID, 1)
	first.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := first.ReadMessage(); err == nil {
		t.Fatal("expected stale legacy bot connection to be closed")
	}
}

func TestServeWSRejectsLegacyBotAfterPersistentBodyBinding(t *testing.T) {
	botUID := int64(46)
	apiKey := GenerateAPIKey(botUID)
	wsURL, _, cleanup := newBotBodyTestServer(apiKey, botUID)
	defer cleanup()

	current, resp, err := dialBotBody(wsURL, apiKey, "body-a")
	closeResponse(resp)
	if err != nil {
		t.Fatalf("body-bound bot dial failed: %v", err)
	}
	current.Close()

	legacy, resp, err := dialBotWithoutBody(wsURL, apiKey)
	if legacy != nil {
		legacy.Close()
	}
	closeResponse(resp)
	if err == nil {
		t.Fatal("expected legacy bot to be rejected after persistent body binding")
	}
	if resp == nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("legacy after binding status = %v, want 409", responseStatus(resp))
	}
}

func TestServeWSExplicitBodyReplacesActiveLegacyBody(t *testing.T) {
	botUID := int64(47)
	apiKey := GenerateAPIKey(botUID)
	wsURL, hub, cleanup := newBotBodyTestServer(apiKey, botUID)
	defer cleanup()

	legacy, resp, err := dialBotWithoutBody(wsURL, apiKey)
	closeResponse(resp)
	if err != nil {
		t.Fatalf("legacy bot body dial failed: %v", err)
	}
	waitForClientCount(t, hub, botUID, 1)

	current, resp, err := dialBotBody(wsURL, apiKey, "body-a")
	closeResponse(resp)
	if err != nil {
		legacy.Close()
		t.Fatalf("explicit body should replace active legacy body: %v", err)
	}
	defer current.Close()
	waitForClientCount(t, hub, botUID, 1)
	legacy.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := legacy.ReadMessage(); err == nil {
		t.Fatal("expected active legacy bot connection to be closed")
	}
}

func TestServeWSRejectsBotJWTConnections(t *testing.T) {
	botUID := int64(41)
	apiKey := GenerateAPIKey(botUID)
	wsURL, _, cleanup := newBotBodyTestServer(apiKey, botUID)
	defer cleanup()
	token, err := GenerateToken(botUID, "bot", "bot@example.test")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL+"?token="+token, nil)
	if conn != nil {
		conn.Close()
	}
	closeResponse(resp)
	if err == nil {
		t.Fatal("expected bot JWT websocket connection to be rejected")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("bot JWT status = %v, want 403", responseStatus(resp))
	}
}

func TestServeWSRejectsDifferentActiveBotBody(t *testing.T) {
	botUID := int64(43)
	apiKey := GenerateAPIKey(botUID)
	wsURL, _, cleanup := newBotBodyTestServer(apiKey, botUID)
	defer cleanup()

	first, resp, err := dialBotBody(wsURL, apiKey, "body-a")
	closeResponse(resp)
	if err != nil {
		t.Fatalf("first bot body dial failed: %v", err)
	}
	defer first.Close()

	second, resp, err := dialBotBody(wsURL, apiKey, "body-b")
	if second != nil {
		second.Close()
	}
	closeResponse(resp)
	if err == nil {
		t.Fatal("expected different body to reject while first body is active")
	}
	if resp == nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("different body status = %v, want 409", responseStatus(resp))
	}
}

func TestServeWSAllowsSameBodyReconnectAndRejectsDifferentBodyAfterDisconnect(t *testing.T) {
	botUID := int64(44)
	apiKey := GenerateAPIKey(botUID)
	wsURL, hub, cleanup := newBotBodyTestServer(apiKey, botUID)
	defer cleanup()

	first, resp, err := dialBotBody(wsURL, apiKey, "body-a")
	closeResponse(resp)
	if err != nil {
		t.Fatalf("first bot body dial failed: %v", err)
	}
	waitForClientCount(t, hub, botUID, 1)

	reconnect, resp, err := dialBotBody(wsURL, apiKey, "body-a")
	closeResponse(resp)
	if err != nil {
		first.Close()
		t.Fatalf("same body reconnect failed: %v", err)
	}
	defer reconnect.Close()
	waitForClientCount(t, hub, botUID, 1)
	first.SetReadDeadline(time.Now().Add(time.Second))
	if _, _, err := first.ReadMessage(); err == nil {
		t.Fatal("expected stale same-body connection to be closed")
	}

	reconnect.Close()
	waitForClientCount(t, hub, botUID, 0)

	next, resp, err := dialBotBody(wsURL, apiKey, "body-b")
	closeResponse(resp)
	if next != nil {
		next.Close()
	}
	if err == nil {
		t.Fatal("expected persistent body binding to reject a different body after disconnect")
	}
	if resp == nil || resp.StatusCode != http.StatusConflict {
		t.Fatalf("different body after disconnect status = %v, want 409", responseStatus(resp))
	}
}

func TestServeWSConcurrentSameBodyDialKeepsOneActiveClient(t *testing.T) {
	botUID := int64(45)
	apiKey := GenerateAPIKey(botUID)
	wsURL, hub, cleanup := newBotBodyTestServer(apiKey, botUID)
	defer cleanup()

	const attempts = 3
	var wg sync.WaitGroup
	conns := make(chan *websocket.Conn, attempts)
	errs := make(chan error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			conn, resp, err := dialBotBody(wsURL, apiKey, "body-a")
			closeResponse(resp)
			if err != nil {
				errs <- err
				return
			}
			conns <- conn
		}()
	}
	wg.Wait()
	close(conns)
	close(errs)
	if len(errs) != 0 {
		t.Fatalf("same body concurrent dials should not be rejected: %v", <-errs)
	}

	waitForClientCount(t, hub, botUID, 1)
	for conn := range conns {
		conn.Close()
	}
	waitForClientCount(t, hub, botUID, 0)
}

func newBotBodyTestServer(apiKey string, botUID int64) (string, *Hub, func()) {
	store := &wsBotBodyStore{apiKey: apiKey, botUID: botUID}
	hub := NewHub(store, nil)
	go hub.Run()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ServeWS(hub, w, r)
	}))

	return "ws" + strings.TrimPrefix(server.URL, "http"), hub, server.Close
}

func dialBotBody(wsURL string, apiKey string, bodyID string) (*websocket.Conn, *http.Response, error) {
	headers := http.Header{}
	headers.Set("X-API-Key", apiKey)
	headers.Set(botBodyIDHeader, bodyID)
	return websocket.DefaultDialer.Dial(wsURL, headers)
}

func dialBotWithoutBody(wsURL string, apiKey string) (*websocket.Conn, *http.Response, error) {
	headers := http.Header{}
	headers.Set("X-API-Key", apiKey)
	return websocket.DefaultDialer.Dial(wsURL, headers)
}

func waitForClientCount(t *testing.T, hub *Hub, uid int64, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(hub.getClients(uid)) == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	got := len(hub.getClients(uid))
	t.Fatalf("client count for uid=%d = %d, want %d", uid, got, want)
}

func closeResponse(resp *http.Response) {
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
}

func responseStatus(resp *http.Response) string {
	if resp == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%d", resp.StatusCode)
}
