package server

import (
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

// A cloud Agent rebuilds its session from this history and can only fetch by
// absolute address. The stored value and human readers must keep the
// server-relative /uploads path, so the public origin is injected on the
// authorized Agent's read copy only.
func TestAgentContextHistoryCarriesAttachmentAddressForCloudRead(t *testing.T) {
	t.Setenv("CATSCO_PUBLIC_BASE_URL", "https://app.example")

	users := map[int64]*types.User{
		7:  {ID: 7, Username: "alice", DisplayName: "Alice"},
		42: {ID: 42, Username: "dev_agent", DisplayName: "Dev Agent", AccountType: types.AccountBot},
	}
	hub := NewHub(&identityMessageStore{users: users}, nil)
	stored := `{"type":"file","payload":{"url":"/uploads/files/report.txt","name":"report.txt","size":12}}`
	message := &types.Message{ID: 5, TopicID: "grp_80", FromUID: 7, MsgType: "file", Content: stored}

	agentCopy := hub.agentContextHistoryAPIMessageForRecipient(42, message, users, nil)
	if agentCopy == nil {
		t.Fatal("agent context copy missing")
	}
	agentText := richDescription(t, agentCopy["content"])
	for _, fragment := range []string{
		"https://app.example/uploads/files/report.txt",
		"report.txt",
		"read_file",
	} {
		if !strings.Contains(agentText, fragment) {
			t.Fatalf("agent copy omits %q: %q", fragment, agentText)
		}
	}

	// The human reader renders its own attachment UI from content_blocks and
	// must not see the injected address.
	humanCopy := hub.historyAPIMessageForRecipient(7, message, users)
	if humanText := richDescription(t, humanCopy["content"]); strings.Contains(humanText, "app.example") {
		t.Fatalf("human history copy was rewritten: %q", humanText)
	}

	if message.Content != stored {
		t.Fatalf("the stored message was mutated: %q", message.Content)
	}
}

// Without a configured public origin there is no address to give, but the
// attachment must still render by name instead of vanishing.
func TestAgentContextHistoryKeepsAttachmentWithoutPublicOrigin(t *testing.T) {
	t.Setenv("CATSCO_PUBLIC_BASE_URL", "")

	users := map[int64]*types.User{
		7:  {ID: 7, Username: "alice", DisplayName: "Alice"},
		42: {ID: 42, Username: "dev_agent", DisplayName: "Dev Agent", AccountType: types.AccountBot},
	}
	hub := NewHub(&identityMessageStore{users: users}, nil)
	message := &types.Message{
		ID: 6, TopicID: "grp_80", FromUID: 7, MsgType: "file",
		Content: `{"type":"file","payload":{"url":"/uploads/files/report.txt","name":"report.txt"}}`,
	}

	agentCopy := hub.agentContextHistoryAPIMessageForRecipient(42, message, users, nil)
	if agentCopy == nil {
		t.Fatal("agent context copy missing")
	}
	if text := richDescription(t, agentCopy["content"]); strings.Contains(text, "app.example") {
		t.Fatalf("unconfigured origin still produced an address: %q", text)
	}
}

// Text messages are untouched by the attachment rewrite.
func TestAgentContextHistoryLeavesTextMessagesAlone(t *testing.T) {
	t.Setenv("CATSCO_PUBLIC_BASE_URL", "https://app.example")

	users := map[int64]*types.User{
		7:  {ID: 7, Username: "alice", DisplayName: "Alice"},
		42: {ID: 42, Username: "dev_agent", DisplayName: "Dev Agent", AccountType: types.AccountBot},
	}
	hub := NewHub(&identityMessageStore{users: users}, nil)
	message := &types.Message{ID: 7, TopicID: "grp_80", FromUID: 7, MsgType: "text", Content: "看下这个文件"}

	agentCopy := hub.agentContextHistoryAPIMessageForRecipient(42, message, users, nil)
	if agentCopy["content"] != "看下这个文件" {
		t.Fatalf("text content changed: %#v", agentCopy["content"])
	}
}

// richDescription resolves the description the cloud consumer actually renders
// for an object-shaped attachment.
func richDescription(t *testing.T, content interface{}) string {
	t.Helper()
	rich, ok := content.(map[string]interface{})
	if !ok {
		t.Fatalf("content = %#v, want object", content)
	}
	payload, ok := rich["payload"].(map[string]interface{})
	if !ok {
		payload = rich
	}
	if description, ok := payload["description"].(string); ok {
		return description
	}
	if text, ok := payload["text"].(string); ok {
		return text
	}
	return ""
}
