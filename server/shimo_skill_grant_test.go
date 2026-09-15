package server

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/openchat/openchat/server/store/types"
)

func TestShimoErrorCategory(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{name: "not found", err: sql.ErrNoRows, want: "not_found"},
		{name: "validation", err: func() error {
			_, err := GenerateShimoActorToken([]byte("short"), "usr43", "usr7", "catsco:p2p_7_43:91", "catsco/shimo-reader", shimoSkillGrantTTL)
			return err
		}(), want: "validation"},
		{name: "entropy", err: errors.New("random source failed"), want: "entropy"},
		{name: "signing", err: errors.New("key is invalid"), want: "signing"},
		{name: "other", err: errors.New("unexpected failure"), want: "other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shimoErrorCategory(tc.err); got != tc.want {
				t.Fatalf("shimoErrorCategory(%q) = %q, want %q", tc.err, got, tc.want)
			}
		})
	}
}

func TestShimoGrantDiagnosticsAreQuietByDefaultAndClassifyConfiguredFailures(t *testing.T) {
	previousWriter := log.Writer()
	previousFlags := log.Flags()
	defer func() {
		log.SetOutput(previousWriter)
		log.SetFlags(previousFlags)
	}()
	log.SetFlags(0)

	data := &agentIdentityE2EStore{users: map[int64]*types.User{
		7:  {ID: 7, AccountType: types.AccountHuman},
		43: {ID: 43, AccountType: types.AccountBot},
	}}
	hub := NewHub(data, nil)
	var output bytes.Buffer
	log.SetOutput(&output)

	t.Setenv("CATSCO_SHIMO_ACTOR_SECRET", "")
	t.Setenv("CATSCO_SHIMO_SKILL_ID", "")
	t.Setenv("CATSCO_SHIMO_CONNECTOR_URL", "")
	t.Setenv("CATSCO_SHIMO_GRANT_DEBUG", "")
	hub.buildShimoSkillConnectorMetadata(7, 7, "p2p_7_7", 91)
	if strings.Contains(output.String(), "[shimo_connector]") {
		t.Fatalf("unconfigured Shimo should not log, got %q", output.String())
	}
	output.Reset()
	hub.buildShimoSkillConnectorMetadata(7, 43, "p2p_7_43", 92)
	if strings.Contains(output.String(), "[shimo_connector]") {
		t.Fatalf("unconfigured Shimo should not log configured-path failures, got %q", output.String())
	}

	t.Setenv("CATSCO_SHIMO_ACTOR_SECRET", string(shimoTestSecret))
	t.Setenv("CATSCO_SHIMO_SKILL_ID", "catsco/shimo-reader")
	t.Setenv("CATSCO_SHIMO_CONNECTOR_URL", "https://app.catsco.test")
	hub.buildShimoSkillConnectorMetadata(7, 43, "p2p_7_43", 91)
	if !strings.Contains(output.String(), "grant issued") || strings.Contains(output.String(), string(shimoTestSecret)) {
		t.Fatalf("success diagnostic is missing or leaked a credential: %q", output.String())
	}

	output.Reset()
	t.Setenv("CATSCO_SHIMO_ACTOR_SECRET", "short")
	hub.buildShimoSkillConnectorMetadata(7, 43, "p2p_7_43", 92)
	if !strings.Contains(output.String(), "reason=invalid_secret") {
		t.Fatalf("invalid secret reason missing: %q", output.String())
	}

	output.Reset()
	t.Setenv("CATSCO_SHIMO_ACTOR_SECRET", string(shimoTestSecret))
	t.Setenv("CATSCO_SHIMO_SKILL_ID", "invalid")
	hub.buildShimoSkillConnectorMetadata(7, 43, "p2p_7_43", 93)
	if !strings.Contains(output.String(), "reason=invalid_skill_id") {
		t.Fatalf("invalid Skill ID reason missing: %q", output.String())
	}

	output.Reset()
	t.Setenv("CATSCO_SHIMO_SKILL_ID", "catsco/shimo-reader")
	t.Setenv("CATSCO_SHIMO_CONNECTOR_URL", "http://remote.example")
	hub.buildShimoSkillConnectorMetadata(7, 43, "p2p_7_43", 94)
	if !strings.Contains(output.String(), "reason=invalid_connector_url") {
		t.Fatalf("invalid connector URL reason missing: %q", output.String())
	}

	output.Reset()
	hub.buildShimoSkillConnectorMetadata(7, 7, "p2p_7_7", 95)
	if strings.Contains(output.String(), "[shimo_connector]") {
		t.Fatalf("non-bot delivery should stay quiet by default, got %q", output.String())
	}
	t.Setenv("CATSCO_SHIMO_GRANT_DEBUG", "1")
	hub.buildShimoSkillConnectorMetadata(7, 7, "p2p_7_7", 96)
	if !strings.Contains(output.String(), "reason=same_actor_recipient") {
		t.Fatalf("debug-only non-bot reason missing: %q", output.String())
	}
}

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

func TestGroupBotDeliveryCarriesBoundShimoSkillGrant(t *testing.T) {
	t.Setenv("CATSCO_SHIMO_ACTOR_SECRET", string(shimoTestSecret))
	t.Setenv("CATSCO_SHIMO_SKILL_ID", "catsco/shimo-reader")
	t.Setenv("CATSCO_SHIMO_CONNECTOR_URL", "https://app.catsco.test")
	db := &groupStreamCancelStore{members: []*types.GroupMember{
		{UserID: 7, IsBot: false},
		{UserID: 43, IsBot: true},
	}}
	hub := NewHub(db, nil)
	client := &Client{uid: 43, accountType: types.AccountBot, send: make(chan []byte, 1)}
	hub.clients[43] = map[*Client]struct{}{client: {}}

	message := &ServerMessage{Data: &MsgServerData{
		Topic:    "grp_80",
		From:     "usr7",
		SeqID:    91,
		Content:  "读取石墨表格",
		Type:     "text",
		Metadata: map[string]interface{}{},
	}}
	// A normal message is not a task delivery; the return value is unrelated
	// to whether the bot received the message.
	_ = hub.broadcastToGroupWithMentions(80, message, 7, nil, 7, false)
	var delivered ServerMessage
	if err := json.Unmarshal(<-client.send, &delivered); err != nil {
		t.Fatal(err)
	}
	connectors := metadataMapFromServerMessage(t, &delivered, "catsco_skill_connectors")
	if connectors["schema"] != "catsco.skill_connectors.v1" {
		t.Fatalf("group bot delivery lost connector grant: %#v", connectors)
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

// shimoGroupResumeStore adds group membership to the shared identity store so
// the resume path can be exercised without a database.
type shimoGroupResumeStore struct {
	*agentIdentityE2EStore
	members map[int64]map[int64]bool
}

func (s *shimoGroupResumeStore) IsGroupMember(groupID, userID int64) (bool, error) {
	return s.members[groupID][userID], nil
}

func (s *shimoGroupResumeStore) GetGroupMembers(groupID int64) ([]*types.GroupMember, error) {
	members := make([]*types.GroupMember, 0, len(s.members[groupID]))
	for userID, isMember := range s.members[groupID] {
		if isMember {
			members = append(members, &types.GroupMember{UserID: userID})
		}
	}
	return members, nil
}

func (s *shimoGroupResumeStore) IsChannelManagedGroup(int64) (bool, error) {
	return false, nil
}

func TestShimoLoginResumeIntoGroupRequiresBothMembers(t *testing.T) {
	t.Setenv("CATSCO_SHIMO_ACTOR_SECRET", string(shimoTestSecret))
	t.Setenv("CATSCO_SHIMO_SKILL_ID", "catsco/shimo-reader")
	t.Setenv("CATSCO_SHIMO_CONNECTOR_URL", "https://app.catsco.test")
	data := &shimoGroupResumeStore{
		agentIdentityE2EStore: &agentIdentityE2EStore{users: map[int64]*types.User{
			7:  {ID: 7, AccountType: types.AccountHuman},
			8:  {ID: 8, AccountType: types.AccountHuman},
			43: {ID: 43, AccountType: types.AccountBot},
		}},
		members: map[int64]map[int64]bool{
			80: {7: true, 43: true},
			81: {7: true},
		},
	}
	hub := NewHub(data, nil)
	client := &Client{uid: 43, accountType: types.AccountBot, send: make(chan []byte, 1)}
	hub.clients[43] = map[*Client]struct{}{client: {}}

	if !hub.DeliverShimoLoginResume(ShimoLoginResume{AgentUID: 43, ActorUID: 7, TopicID: "grp_80", MessageID: 91}) {
		t.Fatal("expected a group resume for a person who is in the group")
	}
	var message ServerMessage
	if err := json.Unmarshal(<-client.send, &message); err != nil {
		t.Fatal(err)
	}
	if message.Data == nil || message.Data.SeqID != 0 || message.Data.Topic != "grp_80" || message.Data.From != "usr7" {
		t.Fatalf("unexpected group resume envelope: %#v", message.Data)
	}
	if message.Data.Metadata["catsco_transient"] != true || message.Data.Metadata["catsco_skill_login_resume"] != true {
		t.Fatalf("missing trusted resume metadata: %#v", message.Data.Metadata)
	}
	connectors := metadataMapFromServerMessage(t, &message, "catsco_skill_connectors")
	if connectors["schema"] != "catsco.skill_connectors.v1" {
		t.Fatalf("group resume did not carry a fresh connector grant: %#v", connectors)
	}

	if hub.DeliverShimoLoginResume(ShimoLoginResume{AgentUID: 43, ActorUID: 8, TopicID: "grp_80", MessageID: 91}) {
		t.Fatal("resumed for a person who is not in the group")
	}
	if hub.DeliverShimoLoginResume(ShimoLoginResume{AgentUID: 43, ActorUID: 7, TopicID: "grp_81", MessageID: 91}) {
		t.Fatal("resumed in a group the virtual employee is not in")
	}
	if hub.DeliverShimoLoginResume(ShimoLoginResume{AgentUID: 43, ActorUID: 8, TopicID: "p2p_7_43", MessageID: 91}) {
		t.Fatal("resumed a one-to-one topic on behalf of somebody else")
	}
}
