package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"

	"github.com/openchat/openchat/server/store/types"
)

const (
	artifactPreviewSessionContract = "catsco.artifact-preview-session.v1"
	artifactPreviewSessionKeyLabel = "catscompany/artifact-preview-session/v1"
)

type artifactPreviewSessionRef struct {
	ContractVersion string `json:"contract_version"`
	Token           string `json:"token"`
}

func (h *Hub) artifactPreviewRouteConnected(actorUID int64, route runtimeRoute) bool {
	if h == nil || actorUID <= 0 || route.NodeID == "" || route.ConnectionID == "" {
		return false
	}
	if route.NodeID == h.nodeID {
		client := h.getClientByConnectionID(route.ConnectionID)
		return client != nil && client.uid == actorUID && client.accountType == types.AccountHuman &&
			client.deviceConnector == nil
	}
	return h.sharedRuntime != nil && h.sharedRuntime.routeConnected(route, nowForRoute(h))
}

type artifactPreviewSessionClaims struct {
	ActorUID     int64  `json:"actor_uid"`
	NodeID       string `json:"node_id"`
	ConnectionID string `json:"connection_id"`
}

type artifactPreviewSessionSigner struct {
	key []byte
}

func newArtifactPreviewSessionSigner(rootSecret []byte) (*artifactPreviewSessionSigner, error) {
	if len(rootSecret) == 0 {
		return nil, errors.New("Artifact preview session secret is not configured")
	}
	mac := hmac.New(sha256.New, rootSecret)
	_, _ = mac.Write([]byte(artifactPreviewSessionKeyLabel))
	return &artifactPreviewSessionSigner{key: mac.Sum(nil)}, nil
}

func (s *artifactPreviewSessionSigner) issue(actorUID int64, route runtimeRoute) (artifactPreviewSessionRef, error) {
	if s == nil || len(s.key) == 0 || actorUID <= 0 ||
		!artifactRuntimeNodePattern.MatchString(route.NodeID) ||
		strings.TrimSpace(route.ConnectionID) == "" || len(route.ConnectionID) > 128 {
		return artifactPreviewSessionRef{}, errors.New("invalid Artifact preview session")
	}
	payload, err := json.Marshal(artifactPreviewSessionClaims{
		ActorUID:     actorUID,
		NodeID:       route.NodeID,
		ConnectionID: route.ConnectionID,
	})
	if err != nil {
		return artifactPreviewSessionRef{}, err
	}
	encoded := base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte(encoded))
	token := encoded + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return artifactPreviewSessionRef{
		ContractVersion: artifactPreviewSessionContract,
		Token:           token,
	}, nil
}

func (s *artifactPreviewSessionSigner) verify(ref artifactPreviewSessionRef, actorUID int64) (runtimeRoute, bool) {
	if s == nil || len(s.key) == 0 || actorUID <= 0 ||
		ref.ContractVersion != artifactPreviewSessionContract ||
		ref.Token == "" || ref.Token != strings.TrimSpace(ref.Token) || len(ref.Token) > 1024 {
		return runtimeRoute{}, false
	}
	encoded, signature, found := strings.Cut(ref.Token, ".")
	if !found || encoded == "" || signature == "" {
		return runtimeRoute{}, false
	}
	provided, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return runtimeRoute{}, false
	}
	mac := hmac.New(sha256.New, s.key)
	_, _ = mac.Write([]byte(encoded))
	if !hmac.Equal(provided, mac.Sum(nil)) {
		return runtimeRoute{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return runtimeRoute{}, false
	}
	var claims artifactPreviewSessionClaims
	if err := json.Unmarshal(payload, &claims); err != nil || claims.ActorUID != actorUID ||
		!artifactRuntimeNodePattern.MatchString(claims.NodeID) ||
		strings.TrimSpace(claims.ConnectionID) == "" || len(claims.ConnectionID) > 128 {
		return runtimeRoute{}, false
	}
	return runtimeRoute{NodeID: claims.NodeID, ConnectionID: claims.ConnectionID}, true
}
