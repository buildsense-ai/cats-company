package server

import (
	"fmt"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/openchat/openchat/server/store/types"
)

// This is a capability exchange, not a tool. All authority comes from the
// stored grant, never from model-supplied user/session/device/operations.
type MsgDeviceGrantRenewal struct {
	Type      string             `json:"type"`
	RequestID string             `json:"request_id"`
	GrantID   string             `json:"grant_id"`
	Grant     *ScopedDeviceGrant `json:"grant,omitempty"`
	Error     *MsgDeviceRPCError `json:"error,omitempty"`
}

func (h *Hub) handleDeviceGrantRenewal(client *Client, msg *MsgDeviceGrantRenewal) {
	if h == nil || client == nil || msg == nil {
		return
	}
	result := &MsgDeviceGrantRenewal{Type: "result", RequestID: msg.RequestID, GrantID: msg.GrantID}
	grant, err := h.renewDeviceGrant(client, msg)
	if err != nil {
		// Do not reveal stored grant contents or identity details on rejection.
		result.Error = &MsgDeviceRPCError{Code: "device_grant_renewal_denied", Message: "设备授权无法续期；任务未改用其他设备。请检查设备连接或发起新一轮请求。"}
	} else {
		result.Grant = &grant
	}
	h.SendToClient(client, &ServerMessage{DeviceGrantRenewal: result})
}

func (h *Hub) renewDeviceGrant(client *Client, msg *MsgDeviceGrantRenewal) (ScopedDeviceGrant, error) {
	deny := func() (ScopedDeviceGrant, error) {
		return ScopedDeviceGrant{}, fmt.Errorf("device grant renewal denied")
	}
	if h == nil || h.db == nil || h.userDevices == nil || client == nil || msg == nil ||
		client.accountType != types.AccountBot || client.deviceConnector != nil || client.wikiAgentUID != 0 ||
		!h.isClientRegistered(client) || h.bodyLeases == nil || client.bodyID == "" ||
		!h.bodyLeases.isCurrent(client.uid, client.bodyID, client.connectionID) || msg.Type != "request" {
		return deny()
	}
	if _, ok := normalizeDeviceRPCRequestID(msg.RequestID); !ok || strings.TrimSpace(msg.GrantID) == "" {
		return deny()
	}
	if credential := client.botRuntimeCredential; credential != nil {
		if h.botRuntimeCredentials == nil || validateBotRuntimeCredentialClaims(credential, h.botRuntimeCredentials.now().UTC()) != nil ||
			credential.BotUID != client.uid || credential.BodyID != client.bodyID || credential.InstallationID != client.installationID {
			return deny()
		}
	}
	old, ok := h.userDevices.lookupGrant(msg.GrantID)
	if !ok || h.deviceGrantWasRevoked(old) || old.Source != "catscompany" || old.IdentityTrust != "server_canonical" || old.AgentID != formatUID(client.uid) ||
		old.AgentBodyID == "" || old.AgentBodyID != client.bodyID || old.OwnerUserID != old.ActorUserID ||
		(old.TargetScope == "explicit_device" && !xiaobaRuntimeV2EnabledForClient(client)) {
		return deny()
	}
	actor := parseFormattedUID(old.ActorUserID)
	if actor <= 0 || old.SessionKey != buildCatsCoSessionKey(old.TopicID, old.TopicType, old.AgentID, actor) {
		return deny()
	}
	if user, err := h.db.GetUser(actor); err != nil || user == nil || user.State != 0 || user.AccountType != types.AccountHuman {
		return deny()
	}
	if bot, err := h.db.GetUser(client.uid); err != nil || bot == nil || bot.State != 0 || bot.AccountType != types.AccountBot {
		return deny()
	}
	if err := h.validateSkillMutationConversationAccess(old.TopicID, actor, client.uid); err != nil {
		return deny()
	}
	if err := h.authorizeOrdinaryThinToolRPCTarget(actor, old.DeviceID); err != nil {
		return deny()
	}
	device, ok := h.userDevices.activeDevice(actor, old.DeviceID)
	if !ok || device.BodyID != old.DeviceBodyID || device.InstallationID != old.DeviceInstallationID {
		return deny()
	}
	route, _ := h.findDeviceRPCTarget(actor, device)
	if !h.routeConnected(route) {
		return deny()
	}
	for _, operation := range old.Operations {
		found := false
		for _, current := range device.Capabilities {
			if operation == current {
				found = true
				break
			}
		}
		if !found {
			return deny()
		}
	}
	now := unixMillis(h.userDevices.now())
	deadline := old.RenewableUntil
	if deadline == 0 {
		deadline = old.CreatedAt + 24*60*60*1000
	} // pre-upgrade grants remain bounded
	if deadline <= now || old.CreatedAt > now {
		return deny()
	}
	next := old // Preserve all scope/operation/incarnation fields and the original creation time.
	next.GrantID = "device_grant_" + randomDeviceGrantIDSuffix()
	next.ExpiresAt = unixMillis(h.userDevices.now().Add(h.userDevices.grantTT))
	if next.ExpiresAt > deadline {
		next.ExpiresAt = deadline
	}
	next.RenewableUntil = deadline
	// Refresh only the same routing revision. A concurrent switch cannot be overwritten.
	preference, err := h.userDevices.selectionRecord(actor, old.SessionKey)
	if err != nil || preference.Revision != old.SelectionRevision {
		return deny()
	}
	h.userDevices.refreshDeviceSelection(actor, old.SessionKey, preference)
	h.userDevices.rememberGrants([]ScopedDeviceGrant{next})
	// A switch racing persistence invalidates the new grant too (including on Redis).
	if _, ok := h.userDevices.lookupGrant(next.GrantID); !ok {
		return deny()
	}
	if h.deviceGrantWasRevoked(next) {
		return deny()
	}
	return next, nil
}

// Use the server-issued grant's ORIGINAL creation time against the existing
// device logout/revocation watermark. A later re-login of the same computer
// must not resurrect an earlier task's capabilities.
func (h *Hub) deviceGrantWasRevoked(grant ScopedDeviceGrant) bool {
	claims := &DeviceConnectorClaims{UID: parseFormattedUID(grant.OwnerUserID), DeviceID: grant.DeviceID}
	claims.IssuedAt = &jwt.NumericDate{Time: time.UnixMilli(grant.CreatedAt)}
	return h.isDeviceConnectorRevoked(claims)
}
