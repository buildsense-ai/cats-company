package server

import (
	"log"
	"time"

	"github.com/gorilla/websocket"
)

// wsCloseAccountDisabled is a private-use WebSocket close code (4000-4999).
// Browsers surface it on the onclose event, letting clients distinguish
// "account revoked" from network drops without exposing HTTP handshake codes.
const wsCloseAccountDisabled = 4403

const wsCloseAccountDisabledReason = "account_disabled"

// KickUser force-disconnects every live connection of uid and returns the
// number of closed connections. Call it whenever an account is disabled or
// deleted so an established socket cannot outlive its owner's state.
func (h *Hub) KickUser(uid int64, reason string) int {
	clients := h.getClients(uid)
	for _, client := range clients {
		h.kickClient(client, reason)
	}
	if len(clients) > 0 {
		log.Printf("session revoke: kicked %d connection(s) of uid=%d (%s)", len(clients), uid, reason)
	}
	return len(clients)
}

func (h *Hub) kickClient(client *Client, reason string) {
	if client == nil {
		return
	}
	if client.conn != nil {
		deadline := time.Now().Add(writeWait)
		msg := websocket.FormatCloseMessage(wsCloseAccountDisabled, wsCloseAccountDisabledReason)
		_ = client.conn.WriteControl(websocket.CloseMessage, msg, deadline)
	}
	h.disconnectClient(client, "session revoked: "+reason)
}
