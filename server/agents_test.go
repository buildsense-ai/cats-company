package server

import "testing"

func TestParseAgentSubroute(t *testing.T) {
	agentUID, parts, ok := parseAgentSubroute("/api/agents/110/access/55")
	if !ok {
		t.Fatal("expected route to parse")
	}
	if agentUID != 110 {
		t.Fatalf("agentUID = %d, want 110", agentUID)
	}
	if len(parts) != 2 || parts[0] != "access" || parts[1] != "55" {
		t.Fatalf("parts = %#v, want access/55", parts)
	}

	if _, _, ok := parseAgentSubroute("/api/agents/open"); ok {
		t.Fatal("open route should not parse as an agent access subroute")
	}
}

func TestNormalizeAgentPermission(t *testing.T) {
	for _, value := range []string{"", "view", "use", "manage"} {
		if _, ok := normalizeAgentPermission(value); !ok {
			t.Fatalf("permission %q should be accepted", value)
		}
	}
	if _, ok := normalizeAgentPermission("owner"); ok {
		t.Fatal("owner should not be accepted as an agent permission")
	}
}

func TestNormalizeAgentAccessStatus(t *testing.T) {
	cases := map[string]string{
		"":               "pending_accept",
		"pending":        "pending_accept",
		"invited":        "pending_accept",
		"pending_accept": "pending_accept",
		"active":         "active",
		"blocked":        "blocked",
		"revoked":        "revoked",
	}
	for input, want := range cases {
		got, ok := normalizeAgentAccessStatus(input)
		if !ok {
			t.Fatalf("status %q should be accepted", input)
		}
		if got != want {
			t.Fatalf("status %q normalized to %q, want %q", input, got, want)
		}
	}
	if _, ok := normalizeAgentAccessStatus("unknown"); ok {
		t.Fatal("unknown should not be accepted as an agent access status")
	}
}
