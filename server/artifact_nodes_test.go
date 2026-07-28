package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

const testArtifactApplicationBaseURL = "https://app.catsco.cc"

func TestCloudArtifactHandlerRoutesAgentsToConfiguredNodes(t *testing.T) {
	const tokenA = "node-a-management-token-abcdefghijklmnopqrstuvwxyz"
	const tokenB = "node-b-management-token-abcdefghijklmnopqrstuvwxyz"
	var callsA, callsB int
	var upstreamA, upstreamB *httptest.Server
	upstreamA = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callsA++
		if r.Method != http.MethodGet || r.URL.Path != "/internal/agents/440/artifacts" {
			t.Fatalf("node A request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+tokenA {
			t.Fatalf("node A Authorization = %q", got)
		}
		_, _ = w.Write(managedNodeListJSON(upstreamA.URL+"/artifacts", "440", "active"))
	}))
	defer upstreamA.Close()
	upstreamB = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callsB++
		if r.Method != http.MethodDelete || r.URL.Path != "/internal/agents/310/artifacts/shared-game" {
			t.Fatalf("node B request = %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+tokenB {
			t.Fatalf("node B Authorization = %q", got)
		}
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"actor_uid":"7"`) {
			t.Fatalf("node B actor body = %s", body)
		}
		_, _ = w.Write(managedNodeOperationJSON(upstreamB.URL+"/artifacts", "310", "shared-game", "deleted"))
	}))
	defer upstreamB.Close()

	registry := mustArtifactNodeRegistry(t, map[string]string{
		"NODE_A_TOKEN": tokenA,
		"NODE_B_TOKEN": tokenB,
	}, map[string]any{
		"nodes": map[string]any{
			"node-a": map[string]string{
				"public_base_url":      upstreamA.URL + "/artifacts",
				"management_url":       upstreamA.URL + "/internal/artifacts",
				"management_token_env": "NODE_A_TOKEN",
			},
			"node-b": map[string]string{
				"public_base_url":      upstreamB.URL + "/artifacts",
				"management_url":       upstreamB.URL + "/internal/artifacts",
				"management_token_env": "NODE_B_TOKEN",
			},
		},
		"agents": map[string]string{"440": "node-a", "310": "node-b"},
	})

	handler := NewCloudArtifactManagementHandler(
		"https://legacy.example.test/artifacts-index.json",
		"https://legacy.example.test/internal/artifacts",
		"legacy-management-token-abcdefghijklmnopqrstuvwxyz",
		upstreamA.Client(),
	)
	handler.nodeRegistry = registry
	handler.SetStore(twoManagedArtifactAgentsStore())

	list := httptest.NewRecorder()
	handler.HandleAgentArtifacts(
		list,
		authenticatedArtifactRequestPath(http.MethodGet, "/api/agents/440/artifacts?status=active"),
	)
	if list.Code != http.StatusOK {
		t.Fatalf("node A list status = %d, body = %s", list.Code, list.Body.String())
	}

	deleteResult := httptest.NewRecorder()
	handler.HandleAgentArtifacts(
		deleteResult,
		authenticatedArtifactRequestPath(http.MethodDelete, "/api/agents/310/artifacts/shared-game"),
	)
	if deleteResult.Code != http.StatusOK {
		t.Fatalf("node B delete status = %d, body = %s", deleteResult.Code, deleteResult.Body.String())
	}
	if callsA != 1 || callsB != 1 {
		t.Fatalf("node calls = A:%d B:%d, want 1 each", callsA, callsB)
	}
}

func TestCloudArtifactHandlerDoesNotFallbackForUnmappedAgent(t *testing.T) {
	const token = "node-a-management-token-abcdefghijklmnopqrstuvwxyz"
	var legacyCalls int
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legacyCalls++
	}))
	defer legacy.Close()

	registry := mustArtifactNodeRegistry(t, map[string]string{"NODE_A_TOKEN": token}, map[string]any{
		"nodes": map[string]any{
			"node-a": map[string]string{
				"public_base_url":      legacy.URL + "/artifacts",
				"management_url":       legacy.URL + "/internal/artifacts",
				"management_token_env": "NODE_A_TOKEN",
			},
		},
		"agents": map[string]string{"440": "node-a"},
	})
	handler := NewCloudArtifactManagementHandler(
		"https://legacy.example.test/artifacts-index.json",
		legacy.URL+"/internal/artifacts",
		"legacy-management-token-abcdefghijklmnopqrstuvwxyz",
		legacy.Client(),
	)
	handler.nodeRegistry = registry
	handler.SetStore(twoManagedArtifactAgentsStore())

	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(
		rec,
		authenticatedArtifactRequestPath(http.MethodGet, "/api/agents/310/artifacts"),
	)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if legacyCalls != 0 {
		t.Fatalf("legacy calls = %d, want 0", legacyCalls)
	}
}

func TestCloudArtifactHandlerDisablesLegacyRoutesWhenNodeRegistryIsEnabled(t *testing.T) {
	const token = "node-a-management-token-abcdefghijklmnopqrstuvwxyz"
	var legacyCalls int
	legacy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		legacyCalls++
	}))
	defer legacy.Close()

	handler := NewCloudArtifactManagementHandler(
		legacy.URL+"/artifacts-index.json",
		legacy.URL+"/internal/artifacts",
		"legacy-management-token-abcdefghijklmnopqrstuvwxyz",
		legacy.Client(),
	)
	handler.nodeRegistry = mustArtifactNodeRegistry(t, map[string]string{"NODE_A_TOKEN": token}, map[string]any{
		"nodes":  artifactNodeTestNodes(),
		"agents": map[string]string{"440": "node-a"},
	})

	requests := []*http.Request{
		authenticatedArtifactRequestPath(http.MethodGet, "/api/artifacts"),
		authenticatedArtifactRequestPath(http.MethodDelete, "/api/artifacts/shared-game"),
		authenticatedArtifactRequestPath(http.MethodPost, "/api/artifacts/shared-game/restore"),
	}
	for _, request := range requests {
		rec := httptest.NewRecorder()
		handler.Handle(rec, request)
		if rec.Code != http.StatusGone || !strings.Contains(rec.Body.String(), `"code":"artifact_agent_required"`) {
			t.Fatalf("%s %s status = %d, body = %s", request.Method, request.URL.Path, rec.Code, rec.Body.String())
		}
	}
	if legacyCalls != 0 {
		t.Fatalf("legacy calls = %d, want 0", legacyCalls)
	}
}

func TestCloudArtifactHandlerRejectsURLFromWrongNode(t *testing.T) {
	const token = "node-a-management-token-abcdefghijklmnopqrstuvwxyz"
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(managedNodeListJSON("https://wrong-node.example/artifacts", "440", "active"))
	}))
	defer upstream.Close()
	registry := mustArtifactNodeRegistry(t, map[string]string{"NODE_A_TOKEN": token}, map[string]any{
		"nodes": map[string]any{
			"node-a": map[string]string{
				"public_base_url":      upstream.URL + "/artifacts",
				"management_url":       upstream.URL + "/internal/artifacts",
				"management_token_env": "NODE_A_TOKEN",
			},
		},
		"agents": map[string]string{"440": "node-a"},
	})
	handler := NewCloudArtifactManagementHandler(
		"https://legacy.example.test/artifacts-index.json",
		upstream.URL+"/internal/artifacts",
		token,
		upstream.Client(),
	)
	handler.nodeRegistry = registry
	handler.SetStore(managedArtifactAgentStore(7, 440, true))

	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(
		rec,
		authenticatedArtifactRequestPath(http.MethodGet, "/api/agents/440/artifacts"),
	)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestCloudArtifactHandlerRejectsURLFromWrongAgentNamespace(t *testing.T) {
	const token = "node-a-management-token-abcdefghijklmnopqrstuvwxyz"
	var upstream *httptest.Server
	upstream = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		artifact := managedNodeArtifact(upstream.URL+"/artifacts", "335", "shared-game", "active")
		artifact.AgentUID = "440"
		data, _ := json.Marshal(cloudArtifactManagementList{
			ContractVersion: artifactManagementContract,
			Status:          "active",
			Count:           1,
			Artifacts:       []cloudArtifact{artifact},
		})
		_, _ = w.Write(data)
	}))
	defer upstream.Close()
	registry := mustArtifactNodeRegistry(t, map[string]string{"NODE_A_TOKEN": token}, map[string]any{
		"nodes": map[string]any{
			"node-a": map[string]string{
				"public_base_url":      upstream.URL + "/artifacts",
				"management_url":       upstream.URL + "/internal/artifacts",
				"management_token_env": "NODE_A_TOKEN",
			},
		},
		"agents": map[string]string{"440": "node-a"},
	})
	handler := NewCloudArtifactManagementHandler(
		"https://legacy.example.test/artifacts-index.json",
		upstream.URL+"/internal/artifacts",
		token,
		upstream.Client(),
	)
	handler.nodeRegistry = registry
	handler.SetStore(managedArtifactAgentStore(7, 440, true))

	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(
		rec,
		authenticatedArtifactRequestPath(http.MethodGet, "/api/agents/440/artifacts"),
	)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestParseArtifactNodeRegistryRejectsIncompleteMappings(t *testing.T) {
	const token = "node-a-management-token-abcdefghijklmnopqrstuvwxyz"
	tests := []struct {
		name     string
		document map[string]any
		contains string
	}{
		{
			name: "unknown node",
			document: map[string]any{
				"nodes":  artifactNodeTestNodes(),
				"agents": map[string]string{"440": "missing"},
			},
			contains: "unknown node",
		},
		{
			name: "unknown field",
			document: map[string]any{
				"nodes":         artifactNodeTestNodes(),
				"agents":        map[string]string{"440": "node-a"},
				"fallback_node": "node-a",
			},
			contains: "unknown field",
		},
		{
			name: "non-canonical UID",
			document: map[string]any{
				"nodes":  artifactNodeTestNodes(),
				"agents": map[string]string{"0440": "node-a"},
			},
			contains: "invalid artifact agent UID",
		},
		{
			name: "whitespace node ID",
			document: map[string]any{
				"nodes": map[string]any{
					" node-a": map[string]string{
						"public_base_url":      "https://node-a.example/artifacts",
						"management_url":       "https://node-a.example/internal/artifacts",
						"management_token_env": "NODE_A_TOKEN",
					},
				},
				"agents": map[string]string{"440": " node-a"},
			},
			contains: "invalid artifact node ID",
		},
		{
			name: "non-canonical management path",
			document: map[string]any{
				"nodes": map[string]any{
					"node-a": map[string]string{
						"public_base_url":      "https://node-a.example/artifacts",
						"management_url":       "https://node-a.example/internal/a/../artifacts",
						"management_token_env": "NODE_A_TOKEN",
					},
				},
				"agents": map[string]string{"440": "node-a"},
			},
			contains: "canonical",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, _ := json.Marshal(tc.document)
			_, err := parseArtifactNodeRegistry(data, func(key string) (string, bool) {
				return token, key == "NODE_A_TOKEN"
			}, testArtifactApplicationBaseURL)
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("error = %v, want %q", err, tc.contains)
			}
		})
	}
}

func TestParseArtifactNodeRegistryRequiresArtifactNodesOnADifferentOrigin(t *testing.T) {
	const token = "node-a-management-token-abcdefghijklmnopqrstuvwxyz"
	tests := []struct {
		name               string
		applicationBaseURL string
		publicBaseURL      string
		wantError          bool
	}{
		{
			name:               "same hostname and default HTTPS port",
			applicationBaseURL: "https://app.catsco.cc/console",
			publicBaseURL:      "https://APP.catsco.cc:443/artifacts",
			wantError:          true,
		},
		{
			name:               "different non-default port",
			applicationBaseURL: "https://app.catsco.cc",
			publicBaseURL:      "https://app.catsco.cc:9000/artifacts",
		},
		{
			name:               "different hostname",
			applicationBaseURL: "https://app.catsco.cc",
			publicBaseURL:      "https://artifacts.catsco.cc/artifacts",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			document := map[string]any{
				"nodes": map[string]any{
					"node-a": map[string]string{
						"public_base_url":      tc.publicBaseURL,
						"management_url":       "https://management.example/internal/artifacts",
						"management_token_env": "NODE_A_TOKEN",
					},
				},
				"agents": map[string]string{"440": "node-a"},
			}
			data, _ := json.Marshal(document)
			_, err := parseArtifactNodeRegistry(data, func(key string) (string, bool) {
				return token, key == "NODE_A_TOKEN"
			}, tc.applicationBaseURL)
			if tc.wantError {
				if err == nil || !strings.Contains(err.Error(), "different origin") {
					t.Fatalf("error = %v, want different origin", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestParseArtifactNodeRegistryRejectsDuplicateJSONKeys(t *testing.T) {
	const document = `{
		"nodes": {
			"node-a": {
				"public_base_url": "https://node-a.example/artifacts",
				"management_url": "https://node-a.example/internal/artifacts",
				"management_token_env": "NODE_A_TOKEN"
			}
		},
		"agents": {"440": "node-a", "440": "node-a"}
	}`
	_, err := parseArtifactNodeRegistry([]byte(document), func(key string) (string, bool) {
		return "node-a-management-token-abcdefghijklmnopqrstuvwxyz", key == "NODE_A_TOKEN"
	}, testArtifactApplicationBaseURL)
	if err == nil || !strings.Contains(err.Error(), "duplicate key") {
		t.Fatalf("error = %v, want duplicate key", err)
	}
}

func TestParseArtifactNodeRegistryReadsIndependentTokenFile(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "node-a.token")
	const token = "node-a-independent-token-abcdefghijklmnopqrstuvwxyz"
	if err := os.WriteFile(tokenFile, []byte(token+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	document := map[string]any{
		"nodes": map[string]any{
			"node-a": map[string]string{
				"public_base_url":       "https://node-a.example/artifacts",
				"management_url":        "https://node-a.example/internal/artifacts",
				"management_token_file": tokenFile,
			},
		},
		"agents": map[string]string{"440": "node-a"},
	}
	data, _ := json.Marshal(document)
	registry, err := parseArtifactNodeRegistry(data, nil, testArtifactApplicationBaseURL)
	if err != nil {
		t.Fatal(err)
	}
	node, err := registry.resolve(440)
	if err != nil {
		t.Fatal(err)
	}
	if node.managementToken != token {
		t.Fatalf("management token = %q", node.managementToken)
	}
}

func mustArtifactNodeRegistry(t *testing.T, environment map[string]string, document map[string]any) *artifactNodeRegistry {
	t.Helper()
	data, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	registry, err := parseArtifactNodeRegistry(data, func(key string) (string, bool) {
		value, ok := environment[key]
		return value, ok
	}, testArtifactApplicationBaseURL)
	if err != nil {
		t.Fatal(err)
	}
	return registry
}

func twoManagedArtifactAgentsStore() *agentTestStore {
	return &agentTestStore{
		users: map[int64]*types.User{
			440: {ID: 440, Username: "agent-440", AccountType: types.AccountBot},
			310: {ID: 310, Username: "agent-310", AccountType: types.AccountBot},
		},
		owners:      map[int64]int64{440: 7, 310: 7},
		friendPairs: map[string]bool{},
		tenantNames: map[int64]string{440: "tenant-440", 310: "tenant-310"},
	}
}

func managedNodeListJSON(publicBaseURL, agentUID, status string) []byte {
	artifact := managedNodeArtifact(publicBaseURL, agentUID, "shared-game", status)
	data, _ := json.Marshal(cloudArtifactManagementList{
		ContractVersion: artifactManagementContract,
		Status:          status,
		Count:           1,
		Artifacts:       []cloudArtifact{artifact},
	})
	return data
}

func managedNodeOperationJSON(publicBaseURL, agentUID, artifactID, status string) []byte {
	data, _ := json.Marshal(cloudArtifactOperation{
		OK:       true,
		Artifact: managedNodeArtifact(publicBaseURL, agentUID, artifactID, status),
	})
	return data
}

func managedNodeArtifact(publicBaseURL, agentUID, artifactID, status string) cloudArtifact {
	artifact := cloudArtifact{
		ID:         artifactID,
		Title:      "Shared game",
		Kind:       "html",
		URL:        strings.TrimRight(publicBaseURL, "/") + "/by-agent/" + agentUID + "/" + artifactID + "/latest/",
		Status:     status,
		CreatedAt:  "2026-07-22T05:00:00.000Z",
		UpdatedAt:  "2026-07-22T07:00:00.000Z",
		AgentUID:   agentUID,
		CanDelete:  status == "active",
		CanRestore: status == "deleted",
	}
	if status == "deleted" {
		artifact.DeletedAt = "2026-07-22T07:00:00.000Z"
	}
	return artifact
}

func artifactNodeTestNodes() map[string]any {
	return map[string]any{
		"node-a": map[string]string{
			"public_base_url":      "https://node-a.example/artifacts",
			"management_url":       "https://node-a.example/internal/artifacts",
			"management_token_env": "NODE_A_TOKEN",
		},
	}
}
