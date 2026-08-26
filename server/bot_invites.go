package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/openchat/openchat/server/store"
)

type BotInviteRequest struct {
	Code string `json:"code"`
}

func generateBotInviteCode() (string, error) {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return strings.ToUpper(hex.EncodeToString(b)), nil
}

func botInviteStore(db store.Store) (store.BotInviteStore, bool) {
	s, ok := db.(store.BotInviteStore)
	return s, ok
}

// HandleBotInviteCode handles GET/POST/DELETE /api/bots/invite-code?uid=...
func (h *BotHandler) HandleBotInviteCode(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())
	botUID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("uid")), 10, 64)
	if err != nil || botUID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid bot uid"})
		return
	}
	ownerUID, err := h.db.GetBotOwner(botUID)
	if err != nil || ownerUID != uid {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "not bot owner"})
		return
	}
	invites, ok := botInviteStore(h.db)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "bot invites unavailable"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		code, err := invites.GetBotInviteCode(botUID, uid)
		if err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "invite code not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"code": code})
	case http.MethodPost:
		var code string
		for attempt := 0; attempt < 3; attempt++ {
			code, err = generateBotInviteCode()
			if err != nil {
				break
			}
			if err = invites.CreateBotInviteCode(botUID, uid, code); err == nil {
				break
			}
		}
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to generate invite code"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"code": code})
	case http.MethodDelete:
		if err := invites.RevokeBotInviteCode(botUID, uid); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to revoke invite code"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]bool{"success": true})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// HandleRedeemBotInvite handles POST /api/bots/invite/redeem.
func (h *BotHandler) HandleRedeemBotInvite(w http.ResponseWriter, r *http.Request) {
	uid := UIDFromContext(r.Context())
	var req BotInviteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	code := strings.ToUpper(strings.TrimSpace(req.Code))
	if code == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invite code required"})
		return
	}
	invites, ok := botInviteStore(h.db)
	if !ok {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"error": "bot invites unavailable"})
		return
	}
	botUID, err := invites.RedeemBotInviteCode(code, uid)
	if err != nil {
		if errors.Is(err, store.ErrBotInviteUnavailable) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to redeem invite code"})
		return
	}
	if h.hub != nil {
		friends := NewFriendHandler(h.db, h.hub)
		friends.notifyFriendEvent("accepted", botUID, uid, "", append(friends.friendEventRecipients(botUID), uid)...)
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"success": true, "bot_uid": botUID, "status": "accepted"})
}

func redeemBotInviteAfterRegistration(db store.Store, code string, uid int64) (int64, error) {
	code = strings.TrimSpace(code)
	if code == "" {
		return 0, nil
	}
	invites, ok := botInviteStore(db)
	if !ok {
		return 0, fmt.Errorf("bot invites unavailable")
	}
	return invites.RedeemBotInviteCode(strings.ToUpper(code), uid)
}
