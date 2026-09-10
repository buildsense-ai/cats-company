package server

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/openchat/openchat/server/store/types"
)

func TestShimoLoginResumeIsTransientAndCarriesFreshSkillGrant(t *testing.T) {
	t.Setenv("CATSCO_SHIMO_ACTOR_SECRET", string(shimoTestSecret))
	t.Setenv("CATSCO_SHIMO_SKILL_ID", "catsco/shimo-reader")
	t.Setenv("CATSCO_SHIMO_CONNECTOR_URL", "https://app.catsco.test")
	data := &agentIdentityE2EStore{users: map[int64]*types.User{
		7:  {ID: 7, AccountType: types.AccountHuman},
		43: {ID: 43, AccountType: types.AccountBot},
	}}
	hub := NewHub(data, nil)
	client := &Client{uid: 43, accountType: types.AccountBot, send: make(chan []byte, 1)}
	hub.clients[43] = map[*Client]struct{}{client: {}}

	if !hub.DeliverShimoLoginResume(ShimoLoginResume{AgentUID: 43, ActorUID: 7, TopicID: "p2p_7_43", MessageID: 91}) {
		t.Fatal("expected live bot resume delivery")
	}
	var message ServerMessage
	if err := json.Unmarshal(<-client.send, &message); err != nil {
		t.Fatal(err)
	}
	if message.Data == nil || message.Data.SeqID != 0 || message.Data.From != "usr7" || message.Data.Topic != "p2p_7_43" {
		t.Fatalf("unexpected transient resume envelope: %#v", message.Data)
	}
	if message.Data.Metadata["catsco_transient"] != true || message.Data.Metadata["catsco_skill_login_resume"] != true {
		t.Fatalf("missing trusted resume metadata: %#v", message.Data.Metadata)
	}
	if message.Data.Metadata["catsco_event"] != "provider_connection_ready" || message.Data.Metadata["provider"] != "shimo" {
		t.Fatalf("unexpected resume event metadata: %#v", message.Data.Metadata)
	}
	connectors := metadataMapFromServerMessage(t, &message, "catsco_skill_connectors")
	if connectors["schema"] != "catsco.skill_connectors.v1" {
		t.Fatalf("resume did not carry a fresh connector grant: %#v", connectors)
	}
}

func TestLiveBotMessageCarriesBoundShimoSkillGrantButHistoryDoesNot(t *testing.T) {
	t.Setenv("CATSCO_SHIMO_ACTOR_SECRET", string(shimoTestSecret))
	t.Setenv("CATSCO_SHIMO_SKILL_ID", "catsco/shimo-reader")
	t.Setenv("CATSCO_SHIMO_CONNECTOR_URL", "https://app.catsco.test")
	data := &agentIdentityE2EStore{users: map[int64]*types.User{
		7:  {ID: 7, AccountType: types.AccountHuman},
		43: {ID: 43, AccountType: types.AccountBot},
	}}
	hub := NewHub(data, nil)
	payload := &normalizedMessagePayload{
		DisplayContent: "读取石墨表格",
		DisplayType:    "text",
		StoredType:     "text",
		Metadata: map[string]interface{}{
			"catsco_skill_connectors": map[string]interface{}{"spoofed": true},
		},
	}

	message := hub.messageForRecipient(7, 43, "p2p_7_43", 0, payload, 91)
	connectors := metadataMapFromServerMessage(t, message, "catsco_skill_connectors")
	if connectors["schema"] != "catsco.skill_connectors.v1" {
		t.Fatalf("connector schema = %#v", connectors["schema"])
	}
	grants, ok := connectors["grants"].([]map[string]interface{})
	if !ok || len(grants) != 1 {
		t.Fatalf("connector grants = %#v", connectors["grants"])
	}
	grant := grants[0]
	if grant["provider"] != "shimo" || grant["skill_id"] != "catsco/shimo-reader" || grant["connector_url"] != "https://app.catsco.test" {
		t.Fatalf("unexpected connector grant: %#v", grant)
	}
	tokenText, _ := grant["actor_token"].(string)
	claims := &ShimoActorClaims{}
	token, err := jwt.ParseWithClaims(tokenText, claims, func(token *jwt.Token) (interface{}, error) {
		return shimoTestSecret, nil
	})
	if err != nil || !token.Valid {
		t.Fatalf("parse actor token: token=%v err=%v", token.Valid, err)
	}
	if claims.AgentUID != "usr43" || claims.ActorUID != "usr7" || claims.SkillID != "catsco/shimo-reader" || claims.TaskRef != "catsco:p2p_7_43:91" {
		t.Fatalf("unexpected claims: %#v", claims)
	}
	if claims.ExpiresAt == nil || time.Until(claims.ExpiresAt.Time) <= 0 {
		t.Fatalf("grant already expired: %#v", claims.ExpiresAt)
	}

	history := hub.historyMessageDataForRecipient(43, &types.Message{
		ID: 91, TopicID: "p2p_7_43", FromUID: 7, Content: "读取石墨表格", MsgType: "text",
	})
	if history == nil {
		t.Fatal("missing history data")
	}
	if _, exists := history.Metadata["catsco_skill_connectors"]; exists {
		t.Fatalf("history replay leaked connector grant: %#v", history.Metadata)
	}
}

func TestShimoSkillGrantFailsClosedForUnsafeConfiguration(t *testing.T) {
	t.Setenv("CATSCO_SHIMO_ACTOR_SECRET", string(shimoTestSecret))
	t.Setenv("CATSCO_SHIMO_SKILL_ID", "catsco/shimo-reader")
	t.Setenv("CATSCO_SHIMO_CONNECTOR_URL", "http://remote.example")
	data := &agentIdentityE2EStore{users: map[int64]*types.User{
		7:  {ID: 7, AccountType: types.AccountHuman},
		43: {ID: 43, AccountType: types.AccountBot},
	}}
	if got := NewHub(data, nil).buildShimoSkillConnectorMetadata(7, 43, "p2p_7_43", 91); got != nil {
		t.Fatalf("unsafe connector URL should not issue a grant: %#v", got)
	}
	payload := &normalizedMessagePayload{
		DisplayContent: "读取石墨表格",
		DisplayType:    "text",
		StoredType:     "text",
		Metadata: map[string]interface{}{
			"catsco_skill_connectors": map[string]interface{}{"spoofed": true},
		},
	}
	message := NewHub(data, nil).messageForRecipient(7, 43, "p2p_7_43", 0, payload, 91)
	if _, exists := message.Data.Metadata["catsco_skill_connectors"]; exists {
		t.Fatalf("untrusted connector metadata survived server canonicalization: %#v", message.Data.Metadata)
	}
}
