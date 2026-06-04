package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

// decodeCtrl reads one queued frame from a client's send channel and decodes it.
func decodeCtrl(t *testing.T, ch <-chan []byte) *ServerMessage {
	t.Helper()
	select {
	case data := <-ch:
		var msg ServerMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			t.Fatalf("unmarshal queued frame: %v", err)
		}
		return &msg
	default:
		t.Fatal("expected a queued frame, channel was empty")
		return nil
	}
}

func TestDisconnectUserClosesAllConnectionsAndNotifies(t *testing.T) {
	hub := NewHub(nil, nil)
	connA := &Client{uid: 42, send: make(chan []byte, 4)}
	connB := &Client{uid: 42, send: make(chan []byte, 4)}
	other := &Client{uid: 99, send: make(chan []byte, 4)}

	hub.addClient(connA)
	hub.addClient(connB)
	hub.addClient(other)

	n := hub.DisconnectUser(42, "account_disabled")
	if n != 2 {
		t.Fatalf("DisconnectUser returned %d, want 2", n)
	}

	if hub.IsOnline(42) {
		t.Fatal("expected uid 42 offline after DisconnectUser")
	}
	if !hub.IsOnline(99) {
		t.Fatal("expected uid 99 to remain online")
	}

	for _, c := range []*Client{connA, connB} {
		msg := decodeCtrl(t, c.send)
		if msg.Ctrl == nil || msg.Ctrl.Text != "force_logout" {
			t.Fatalf("expected force_logout ctrl, got %+v", msg.Ctrl)
		}
		if !c.sendClosed {
			t.Fatal("expected send channel to be closed after disconnect")
		}
	}
}

func TestDisconnectUserNoConnectionsIsNoop(t *testing.T) {
	hub := NewHub(nil, nil)
	if n := hub.DisconnectUser(7, "account_disabled"); n != 0 {
		t.Fatalf("DisconnectUser on offline user returned %d, want 0", n)
	}
}

func TestHandleUserStateDisablesAndDisconnects(t *testing.T) {
	users := map[int64]*types.User{
		7: {ID: 7, Username: "erin", AccountType: types.AccountHuman, State: 0},
	}
	hub := NewHub(nil, nil)
	conn := &Client{uid: 7, send: make(chan []byte, 4)}
	hub.addClient(conn)

	handler := NewAccountAdminHandler(accountTestUserLookup{users: users}, nil, nil)
	handler.SetHub(hub)

	req := httptest.NewRequest(http.MethodPost, "/local/account-admin/users/state", strings.NewReader(`{"uid":7,"state":1}`))
	req.RemoteAddr = "127.0.0.1:40200"
	rec := httptest.NewRecorder()
	handler.HandleUserState(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", rec.Code, rec.Body.String())
	}
	if hub.IsOnline(7) {
		t.Fatal("expected disabled user to be force-disconnected")
	}
	msg := decodeCtrl(t, conn.send)
	if msg.Ctrl == nil || msg.Ctrl.Text != "force_logout" {
		t.Fatalf("expected force_logout ctrl, got %+v", msg.Ctrl)
	}
}

func TestHandleUserStateRestoreDoesNotDisconnect(t *testing.T) {
	users := map[int64]*types.User{
		8: {ID: 8, Username: "frank", AccountType: types.AccountHuman, State: 1},
	}
	hub := NewHub(nil, nil)
	conn := &Client{uid: 8, send: make(chan []byte, 4)}
	hub.addClient(conn)

	handler := NewAccountAdminHandler(accountTestUserLookup{users: users}, nil, nil)
	handler.SetHub(hub)

	req := httptest.NewRequest(http.MethodPost, "/local/account-admin/users/state", strings.NewReader(`{"uid":8,"state":0}`))
	req.RemoteAddr = "127.0.0.1:40200"
	rec := httptest.NewRecorder()
	handler.HandleUserState(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("restore status=%d body=%s", rec.Code, rec.Body.String())
	}
	if !hub.IsOnline(8) {
		t.Fatal("expected re-enabled user to stay connected")
	}
}
