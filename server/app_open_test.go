package server

import (
	"context"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

// appOpenTestResolver answers gateway app ownership from a static table.
type appOpenTestResolver struct {
	owners map[string]string
}

func (r *appOpenTestResolver) GatewayAppOwner(_ context.Context, appID string) (string, bool, error) {
	owner, ok := r.owners[appID]
	if !ok {
		return "", false, nil
	}
	return owner, true, nil
}

func newAppOpenHub(t *testing.T) *Hub {
	t.Helper()
	fake := &gatewayAnnotationFakeStore{
		users: map[int64]*types.User{
			7:   {ID: 7, AccountType: types.AccountHuman},
			9:   {ID: 9, AccountType: types.AccountBot},
			10:  {ID: 10, AccountType: types.AccountBot},
			440: {ID: 440, AccountType: types.AccountBot},
		},
		groupMembers: map[string]bool{
			groupMemberKeyForAnnotations(80, 7): true,
			groupMemberKeyForAnnotations(80, 9): true,
		},
		friendPairs:   map[string]bool{},
		groupMuted:    map[string]bool{},
		groups:        map[int64]*types.Group{},
		members:       map[int64][]*types.GroupMember{},
		storedHistory: map[string][]*types.Message{},
	}
	hub := &Hub{db: fake}
	hub.gatewayAnnotationAppResolver = &appOpenTestResolver{
		owners: map[string]string{
			// The gateway registry stores the owning account as bare digits.
			"research": "9",
			"other":    "10",
		},
	}
	return hub
}

func appOpenValue(appID string, agentUID interface{}) map[string]interface{} {
	return map[string]interface{}{
		"contract_version": AppOpenContractV1,
		"app_id":           appID,
		"agent_uid":        agentUID,
	}
}

func appOpenMetadata(value interface{}) map[string]interface{} {
	return map[string]interface{}{appOpenMetadataKey: value}
}

func TestValidateAppOpenRestampsAuthorizedCommand(t *testing.T) {
	hub := newAppOpenHub(t)
	metadata := appOpenMetadata(appOpenValue("research", float64(9)))
	metadata["other"] = "kept"

	got := hub.validateAppOpenMetadata(9, "p2p_7_9", metadata)

	block, ok := got[appOpenMetadataKey].(map[string]interface{})
	if !ok {
		t.Fatalf("app_open block missing: %#v", got)
	}
	if block["app_id"] != "research" || block["agent_uid"] != int64(9) {
		t.Fatalf("unexpected canonical block: %#v", block)
	}
	if block["contract_version"] != AppOpenContractV1 {
		t.Fatalf("contract_version = %#v", block["contract_version"])
	}
	if got["other"] != "kept" {
		t.Fatalf("unrelated metadata was dropped: %#v", got)
	}
}

func TestValidateAppOpenDropsCommandFromHuman(t *testing.T) {
	hub := newAppOpenHub(t)
	metadata := appOpenMetadata(appOpenValue("research", float64(9)))

	got := hub.validateAppOpenMetadata(7, "p2p_7_9", metadata)

	if _, present := got[appOpenMetadataKey]; present {
		t.Fatalf("a human account must not command the viewer: %#v", got)
	}
	if got != nil {
		t.Fatalf("metadata should collapse to nil, got %#v", got)
	}
}

func TestValidateAppOpenDropsAgentNamingAnotherAgent(t *testing.T) {
	hub := newAppOpenHub(t)
	// Bot 10 is a party to this topic, but claims bot 9's application.
	metadata := appOpenMetadata(appOpenValue("research", float64(9)))

	got := hub.validateAppOpenMetadata(10, "p2p_9_10", metadata)

	if _, present := got[appOpenMetadataKey]; present {
		t.Fatalf("an Agent must not request another Agent's application: %#v", got)
	}
}

func TestValidateAppOpenDropsAppTheAgentDoesNotOwn(t *testing.T) {
	hub := newAppOpenHub(t)
	metadata := appOpenMetadata(appOpenValue("other", float64(9)))

	got := hub.validateAppOpenMetadata(9, "p2p_7_9", metadata)

	if _, present := got[appOpenMetadataKey]; present {
		t.Fatalf("app ownership was not enforced: %#v", got)
	}
}

func TestValidateAppOpenDropsUnknownApp(t *testing.T) {
	hub := newAppOpenHub(t)
	metadata := appOpenMetadata(appOpenValue("does-not-exist", float64(9)))

	got := hub.validateAppOpenMetadata(9, "p2p_7_9", metadata)

	if _, present := got[appOpenMetadataKey]; present {
		t.Fatalf("an unregistered app must not be openable: %#v", got)
	}
}

func TestValidateAppOpenDropsSenderOutsideTopic(t *testing.T) {
	hub := newAppOpenHub(t)
	metadata := appOpenMetadata(appOpenValue("research", float64(440)))

	got := hub.validateAppOpenMetadata(440, "p2p_7_9", metadata)

	if _, present := got[appOpenMetadataKey]; present {
		t.Fatalf("a bot outside this conversation must not command it: %#v", got)
	}
}

func TestValidateAppOpenDropsMalformedCommand(t *testing.T) {
	hub := newAppOpenHub(t)
	cases := map[string]interface{}{
		"no contract": map[string]interface{}{"app_id": "research", "agent_uid": float64(9)},
		"wrong contract": map[string]interface{}{
			"contract_version": "catsco.app-open.v2", "app_id": "research", "agent_uid": float64(9),
		},
		"no app": map[string]interface{}{"contract_version": AppOpenContractV1, "agent_uid": float64(9)},
		"bad agent": map[string]interface{}{
			"contract_version": AppOpenContractV1, "app_id": "research", "agent_uid": "nine",
		},
		"string": "open research",
		"array":  []interface{}{"research"},
	}
	for name, value := range cases {
		got := hub.validateAppOpenMetadata(9, "p2p_7_9", appOpenMetadata(value))
		if _, present := got[appOpenMetadataKey]; present {
			t.Fatalf("%s: malformed command survived: %#v", name, got)
		}
	}
}

func TestValidateAppOpenAllowsGroupAgent(t *testing.T) {
	hub := newAppOpenHub(t)
	metadata := appOpenMetadata(appOpenValue("research", float64(9)))

	got := hub.validateAppOpenMetadata(9, "grp_80", metadata)

	if _, present := got[appOpenMetadataKey]; !present {
		t.Fatalf("the group's Agent should be able to open its app: %#v", got)
	}
}

func TestValidateAppOpenIgnoresMessagesWithoutTheKey(t *testing.T) {
	hub := newAppOpenHub(t)
	metadata := map[string]interface{}{"gateway_annotations": "x"}

	got := hub.validateAppOpenMetadata(9, "p2p_7_9", metadata)

	if len(got) != 1 || got["gateway_annotations"] != "x" {
		t.Fatalf("unrelated metadata changed: %#v", got)
	}
}

func TestValidateAppOpenWithoutServerStateDropsCommand(t *testing.T) {
	hub := &Hub{}
	metadata := appOpenMetadata(appOpenValue("research", float64(9)))

	got := hub.validateAppOpenMetadata(9, "p2p_7_9", metadata)

	if _, present := got[appOpenMetadataKey]; present {
		t.Fatalf("without server state nothing can be authorised: %#v", got)
	}
}

func TestMetadataWithoutAppOpen(t *testing.T) {
	if got := metadataWithoutAppOpen(map[string]interface{}{appOpenMetadataKey: "x"}); got != nil {
		t.Fatalf("emptied metadata should be nil, got %#v", got)
	}
	got := metadataWithoutAppOpen(map[string]interface{}{appOpenMetadataKey: "x", "keep": 1})
	if len(got) != 1 || got["keep"] != 1 {
		t.Fatalf("unexpected result: %#v", got)
	}
	if got := metadataWithoutAppOpen(nil); got != nil {
		t.Fatalf("nil metadata should stay nil, got %#v", got)
	}
}
