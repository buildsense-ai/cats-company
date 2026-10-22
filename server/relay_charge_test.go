package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// ChargeUser 走 relay 内部代扣接口：路径、鉴权、幂等字段与 duplicate 解析。
func TestRelayAdminClientChargeUserPostsToRelay(t *testing.T) {
	var gotPath, gotAuth string
	var gotBody map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		if err := json.NewDecoder(r.Body).Decode(&gotBody); err != nil {
			t.Errorf("decode charge body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"uid":442,"request_id":"run_x","amount_cny":0.02,"duplicate":true,"manual_charge_total_cny":0.02}`))
	}))
	defer srv.Close()

	client := &RelayAdminClient{baseURL: srv.URL, token: "secret", client: srv.Client()}
	duplicate, err := client.ChargeUser(context.Background(), 442, "run_x", 0.02, "service run run_x")
	if err != nil {
		t.Fatalf("charge: %v", err)
	}
	if !duplicate {
		t.Fatal("duplicate = false, want true from relay response")
	}
	if gotPath != "/internal/users/442/charge" {
		t.Fatalf("path = %q, want /internal/users/442/charge", gotPath)
	}
	if gotAuth != "Bearer secret" {
		t.Fatalf("authorization = %q, want Bearer secret", gotAuth)
	}
	if gotBody["request_id"] != "run_x" {
		t.Fatalf("request_id = %v, want run_x", gotBody["request_id"])
	}
	if amount, _ := gotBody["amount_cny"].(float64); amount != 0.02 {
		t.Fatalf("amount_cny = %v, want 0.02", gotBody["amount_cny"])
	}
	if gotBody["note"] != "service run run_x" {
		t.Fatalf("note = %v", gotBody["note"])
	}
}

// relay 4xx 要保留状态码（补扣扫描据此判定「明确拒绝」不重试）。
func TestRelayAdminClientChargeUserKeepsStatusOnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"relay key not found"}`))
	}))
	defer srv.Close()

	client := &RelayAdminClient{baseURL: srv.URL, token: "secret", client: srv.Client()}
	_, err := client.ChargeUser(context.Background(), 442, "run_x", 0.02, "")
	if err == nil {
		t.Fatal("expected error")
	}
	if !isPermanentChargeError(err) {
		t.Fatalf("404 should be permanent, got %v", err)
	}
}
