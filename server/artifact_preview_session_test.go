package server

import (
	"encoding/json"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

func TestArtifactPreviewSessionIsPrivateAndConnectionBound(t *testing.T) {
	hub := NewHub(nil, nil)
	client := &Client{
		uid:          7,
		displayName:  "Alice",
		accountType:  types.AccountHuman,
		connectionID: "preview-connection-a",
		send:         make(chan []byte, 1),
	}
	hub.addClient(client)
	hub.handleHi(client, client.displayName, &MsgClientHi{ID: "hi-1"})

	var response ServerMessage
	decodeQueuedServerMessage(t, client.send, &response)
	encoded, err := json.Marshal(response.Ctrl.Params)
	if err != nil {
		t.Fatalf("encode handshake params: %v", err)
	}
	var params struct {
		PreviewSession artifactPreviewSessionRef `json:"artifact_preview_session"`
	}
	if err := json.Unmarshal(encoded, &params); err != nil {
		t.Fatalf("decode handshake params: %v", err)
	}
	route, ok := hub.artifactPreviewSessions.verify(params.PreviewSession, 7)
	if !ok || !route.matches(hub.clientRoute(client)) {
		t.Fatalf("preview session route = %#v ok=%v", route, ok)
	}
	if _, ok := hub.artifactPreviewSessions.verify(params.PreviewSession, 8); ok {
		t.Fatal("another actor accepted the preview session")
	}
	tampered := params.PreviewSession
	tampered.Token += "x"
	if _, ok := hub.artifactPreviewSessions.verify(tampered, 7); ok {
		t.Fatal("tampered preview session was accepted")
	}
}
