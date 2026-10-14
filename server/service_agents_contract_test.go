package server

import (
	"encoding/json"
	"testing"
)

func TestServiceInvokeRequestJSONShape(t *testing.T) {
	raw := `{"session_id":"sess_1","input":{"topic":"x"},"client_token":"ct_1"}`
	var req ServiceInvokeRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatal(err)
	}
	if req.SessionID != "sess_1" || req.ClientToken != "ct_1" || string(req.Input) != `{"topic":"x"}` {
		t.Fatalf("unexpected: %+v", req)
	}
}

func TestServiceRunEnvelopeRoundTrip(t *testing.T) {
	env := ServiceRunEnvelope{
		ContractVersion: "catsco.service-run-result.v1",
		Status:          ServiceRunCompleted,
		Output:          json.RawMessage(`{"answer":"ok"}`),
		Usage:           &ServiceRunUsage{PromptTokens: 10, CompletionTokens: 5, CostCNY: 0.01},
	}
	b, _ := json.Marshal(env)
	var back ServiceRunEnvelope
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatal(err)
	}
	if back.Status != ServiceRunCompleted || back.Usage.CostCNY != 0.01 {
		t.Fatalf("roundtrip mismatch: %s", b)
	}
}

func TestServiceAsyncNoticeShape(t *testing.T) {
	n := ServiceAsyncNotice{ContractVersion: "catsco.service-run-async.v1", RunID: "run_abc", SessionID: "sess_1", Cause: "threshold"}
	b, _ := json.Marshal(n)
	want := `{"contract_version":"catsco.service-run-async.v1","run_id":"run_abc","session_id":"sess_1","cause":"threshold"}`
	if string(b) != want {
		t.Fatalf("got %s want %s", b, want)
	}
}
