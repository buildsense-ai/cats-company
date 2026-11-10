package server

import (
	"testing"
	"time"
)

func handoffTestValue() map[string]interface{} {
	return map[string]interface{}{"request_id": "request-123", "topic_id": "p2p_7_9", "action": "initialize", "expires_at": float64(time.Now().Add(time.Minute).UnixMilli()), "parameters": map[string]interface{}{"server": "compute-1", "data_path": "/data/sample"}}
}
func TestAppHandoffSafety(t *testing.T) {
	if _, err := normalizeAppHandoff(handoffTestValue()); err != nil {
		t.Fatal(err)
	}
	for _, parameters := range []map[string]interface{}{{"token": "secret"}, {"password": "x"}, {"server": "ssh://user:password@host"}, {"data_path": "-----BEGIN PRIVATE KEY-----"}, {"nested": map[string]interface{}{}}, {"command": "touch /tmp/x"}} {
		value := handoffTestValue()
		value["parameters"] = parameters
		if _, err := normalizeAppHandoff(value); err == nil {
			t.Fatalf("accepted unsafe parameters %#v", parameters)
		}
	}
	for _, change := range []map[string]interface{}{{"action": "run"}, {"expires_at": float64(1)}, {"expires_at": float64(time.Now().Add(time.Hour).UnixMilli())}, {"request_id": ""}} {
		value := handoffTestValue()
		for k, v := range change {
			value[k] = v
		}
		if _, err := normalizeAppHandoff(value); err == nil {
			t.Fatalf("accepted invalid request %#v", change)
		}
	}
}
func TestAppHandoffTopicOwnershipAndCanonicalShape(t *testing.T) {
	hub := newAppOpenHub(t)
	value := appOpenValue("research", float64(9))
	value["contract_version"] = AppOpenContractV2
	value["handoff"] = handoffTestValue()
	value["url"] = "https://evil.example"
	got := hub.validateAppOpenMetadata(9, "p2p_7_9", appOpenMetadata(value))
	block, ok := got[appOpenMetadataKey].(map[string]interface{})
	if !ok {
		t.Fatalf("missing v2: %#v", got)
	}
	if block["contract_version"] != AppOpenContractV2 || block["handoff"] == nil || block["url"] != nil {
		t.Fatalf("bad canonical v2 %#v", block)
	}
	value["handoff"].(map[string]interface{})["topic_id"] = "p2p_7_10"
	if got := hub.validateAppOpenMetadata(9, "p2p_7_9", appOpenMetadata(value)); got != nil {
		t.Fatalf("cross-topic survived: %#v", got)
	}
	value["handoff"] = handoffTestValue()
	if got := hub.validateAppOpenMetadata(7, "p2p_7_9", appOpenMetadata(value)); got != nil {
		t.Fatalf("human survived: %#v", got)
	}
	value["app_id"] = "other"
	if got := hub.validateAppOpenMetadata(9, "p2p_7_9", appOpenMetadata(value)); got != nil {
		t.Fatalf("foreign app survived: %#v", got)
	}
}
