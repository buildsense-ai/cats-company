package server

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

const testDirectArtifactTemplate = "https://agent-{uid}.artifacts.catsco.fun:19991/artifacts"

type artifactRoundTripFunc func(*http.Request) (*http.Response, error)

func (f artifactRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func TestParseArtifactDirectURLTemplate(t *testing.T) {
	template, err := parseArtifactDirectURLTemplate(
		testDirectArtifactTemplate,
		testArtifactApplicationBaseURL,
	)
	if err != nil {
		t.Fatal(err)
	}
	node, err := template.resolve(535)
	if err != nil {
		t.Fatal(err)
	}
	if node.publicBaseURL !=
		"https://agent-535.artifacts.catsco.fun:19991/artifacts" {
		t.Fatalf("public base URL = %q", node.publicBaseURL)
	}
	if !node.rootPublicIndex || node.managementURL != "" {
		t.Fatalf("direct node = %+v", node)
	}
}

func TestParseArtifactDirectURLTemplateRejectsInvalidShapes(t *testing.T) {
	for name, value := range map[string]string{
		"missing UID":        "https://artifacts.catsco.fun:19991/artifacts",
		"duplicate UID":      "https://agent-{uid}.artifacts.catsco.fun/{uid}/artifacts",
		"HTTP":               "http://agent-{uid}.artifacts.catsco.fun:19991/artifacts",
		"UID in path":        "https://artifacts.catsco.fun:19991/{uid}/artifacts",
		"wrong path":         "https://agent-{uid}.artifacts.catsco.fun:19991/static",
		"query":              "https://agent-{uid}.artifacts.catsco.fun:19991/artifacts?q=1",
		"leading whitespace": " " + testDirectArtifactTemplate,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseArtifactDirectURLTemplate(
				value,
				testArtifactApplicationBaseURL,
			); err == nil {
				t.Fatalf("template %q was accepted", value)
			}
		})
	}
}

func TestLoadArtifactDirectURLTemplateFromEnv(t *testing.T) {
	t.Setenv("CATSCO_DIRECT_ARTIFACT_URL_TEMPLATE", testDirectArtifactTemplate)
	t.Setenv("CATSCO_PUBLIC_BASE_URL", testArtifactApplicationBaseURL)
	template, err := loadArtifactDirectURLTemplateFromEnv()
	if err != nil {
		t.Fatal(err)
	}
	if template == nil || template.value != testDirectArtifactTemplate {
		t.Fatalf("template = %+v", template)
	}
}

func TestCloudArtifactHandlerListsDirectRootIndex(t *testing.T) {
	var calls int
	handler := directTemplateHandler(t, 440, artifactRoundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			calls++
			want := "https://agent-440.artifacts.catsco.fun:19991/" +
				"artifacts/artifacts-index.json"
			if request.URL.String() != want {
				t.Fatalf("index request = %q, want %q", request.URL, want)
			}
			if request.Header.Get("Authorization") != "" {
				t.Fatalf(
					"index Authorization = %q",
					request.Header.Get("Authorization"),
				)
			}
			return artifactHTTPResponse(
				http.StatusOK,
				directArtifactIndexJSON(
					440,
					"lesson-game",
					"Lesson game",
				),
			), nil
		},
	))

	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(
		rec,
		authenticatedArtifactRequestPath(
			http.MethodGet,
			"/api/agents/440/artifacts?status=active",
		),
	)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	var list cloudArtifactManagementList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Count != 1 || len(list.Artifacts) != 1 {
		t.Fatalf("list = %+v", list)
	}
	artifact := list.Artifacts[0]
	if artifact.AgentUID != "440" || artifact.CanDelete || artifact.CanRestore {
		t.Fatalf("artifact = %+v", artifact)
	}
	if calls != 1 {
		t.Fatalf("index calls = %d", calls)
	}

	deleteRec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(
		deleteRec,
		authenticatedArtifactRequestPath(
			http.MethodDelete,
			"/api/agents/440/artifacts/lesson-game",
		),
	)
	if deleteRec.Code != http.StatusServiceUnavailable {
		t.Fatalf(
			"delete status = %d, body = %s",
			deleteRec.Code,
			deleteRec.Body.String(),
		)
	}
}

func TestCloudArtifactHandlerTreatsMissingDirectIndexAsEmpty(t *testing.T) {
	for name, roundTrip := range map[string]artifactRoundTripFunc{
		"HTTP 404": func(request *http.Request) (*http.Response, error) {
			return artifactHTTPResponse(http.StatusNotFound, "not found"), nil
		},
		"DNS NXDOMAIN": func(request *http.Request) (*http.Response, error) {
			return nil, &net.DNSError{
				Err:        "no such host",
				Name:       request.URL.Hostname(),
				IsNotFound: true,
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			handler := directTemplateHandler(t, 440, roundTrip)
			rec := httptest.NewRecorder()
			handler.HandleAgentArtifacts(
				rec,
				authenticatedArtifactRequestPath(
					http.MethodGet,
					"/api/agents/440/artifacts?status=active",
				),
			)
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), `"count":0`) ||
				!strings.Contains(rec.Body.String(), `"artifacts":[]`) {
				t.Fatalf("body = %s", rec.Body.String())
			}
		})
	}
}

func TestCloudArtifactHandlerReportsBrokenDirectIndex(t *testing.T) {
	for name, roundTrip := range map[string]artifactRoundTripFunc{
		"connection failure": func(request *http.Request) (*http.Response, error) {
			return nil, errors.New("connection refused")
		},
		"HTTP 500": func(request *http.Request) (*http.Response, error) {
			return artifactHTTPResponse(http.StatusInternalServerError, "failed"), nil
		},
		"invalid JSON": func(request *http.Request) (*http.Response, error) {
			return artifactHTTPResponse(http.StatusOK, "{broken"), nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			handler := directTemplateHandler(t, 440, roundTrip)
			rec := httptest.NewRecorder()
			handler.HandleAgentArtifacts(
				rec,
				authenticatedArtifactRequestPath(
					http.MethodGet,
					"/api/agents/440/artifacts?status=active",
				),
			)
			if rec.Code != http.StatusBadGateway {
				t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
			}
		})
	}
}

func TestCloudArtifactHandlerRejectsWrongDirectHostname(t *testing.T) {
	handler := directTemplateHandler(t, 440, artifactRoundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			return artifactHTTPResponse(
				http.StatusOK,
				directArtifactIndexJSON(
					310,
					"wrong-agent-game",
					"Wrong agent game",
				),
			), nil
		},
	))
	rec := httptest.NewRecorder()
	handler.HandleAgentArtifacts(
		rec,
		authenticatedArtifactRequestPath(
			http.MethodGet,
			"/api/agents/440/artifacts?status=active",
		),
	)
	if rec.Code != http.StatusBadGateway ||
		!strings.Contains(rec.Body.String(), `"code":"artifact_response_invalid"`) {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func TestCloudArtifactHandlerPrefersExplicitNodeThenDirectTemplate(t *testing.T) {
	requests := []string{}
	client := &http.Client{Transport: artifactRoundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			requests = append(requests, request.URL.String())
			switch request.URL.Hostname() {
			case "mapped.example.test":
				return artifactHTTPResponse(
					http.StatusOK,
					mappedArtifactIndexJSON(440),
				), nil
			case "agent-310.artifacts.catsco.fun":
				return artifactHTTPResponse(
					http.StatusOK,
					directArtifactIndexJSON(
						310,
						"direct-game",
						"Direct game",
					),
				), nil
			default:
				t.Fatalf("unexpected request = %s", request.URL)
				return nil, errors.New("unexpected request")
			}
		},
	)}
	handler := NewCloudArtifactManagementHandler(
		"https://legacy.example.test/artifacts-index.json",
		"https://legacy.example.test/internal/artifacts",
		"legacy-management-token-abcdefghijklmnopqrstuvwxyz",
		client,
	)
	handler.nodeRegistry = mustArtifactNodeRegistry(t, nil, map[string]any{
		"nodes": map[string]any{
			"mapped": map[string]string{
				"public_base_url": "https://mapped.example.test/artifacts",
			},
		},
		"agents": map[string]string{"440": "mapped"},
	})
	handler.directTemplate = mustDirectArtifactTemplate(t)
	handler.SetStore(twoManagedArtifactAgentsStore())

	for _, agentUID := range []string{"440", "310"} {
		rec := httptest.NewRecorder()
		handler.HandleAgentArtifacts(
			rec,
			authenticatedArtifactRequestPath(
				http.MethodGet,
				"/api/agents/"+agentUID+"/artifacts?status=active",
			),
		)
		if rec.Code != http.StatusOK {
			t.Fatalf(
				"agent %s status = %d, body = %s",
				agentUID,
				rec.Code,
				rec.Body.String(),
			)
		}
	}
	if len(requests) != 2 ||
		!strings.Contains(
			requests[0],
			"mapped.example.test/artifacts/by-agent/440/artifacts-index.json",
		) ||
		!strings.Contains(
			requests[1],
			"agent-310.artifacts.catsco.fun:19991/artifacts/artifacts-index.json",
		) {
		t.Fatalf("requests = %#v", requests)
	}
}

func TestCloudArtifactHandlerDisablesUnscopedListForDirectTemplate(t *testing.T) {
	handler := directTemplateHandler(t, 440, artifactRoundTripFunc(
		func(request *http.Request) (*http.Response, error) {
			t.Fatal("unscoped list must not contact an artifact host")
			return nil, errors.New("unexpected request")
		},
	))
	rec := httptest.NewRecorder()
	handler.HandleList(
		rec,
		authenticatedArtifactRequestPath(http.MethodGet, "/api/artifacts"),
	)
	if rec.Code != http.StatusGone ||
		!strings.Contains(rec.Body.String(), `"code":"artifact_agent_required"`) {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
}

func directTemplateHandler(
	t *testing.T,
	agentUID int64,
	roundTrip artifactRoundTripFunc,
) *CloudArtifactHandler {
	t.Helper()
	handler := NewCloudArtifactManagementHandler(
		"https://legacy.example.test/artifacts-index.json",
		"https://legacy.example.test/internal/artifacts",
		"legacy-management-token-abcdefghijklmnopqrstuvwxyz",
		&http.Client{Transport: roundTrip},
	)
	handler.directTemplate = mustDirectArtifactTemplate(t)
	handler.SetStore(managedArtifactAgentStore(7, agentUID, true))
	return handler
}

func mustDirectArtifactTemplate(t *testing.T) *artifactDirectURLTemplate {
	t.Helper()
	template, err := parseArtifactDirectURLTemplate(
		testDirectArtifactTemplate,
		testArtifactApplicationBaseURL,
	)
	if err != nil {
		t.Fatal(err)
	}
	return template
}

func artifactHTTPResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func directArtifactIndexJSON(
	agentUID int64,
	artifactID, title string,
) string {
	data, _ := json.Marshal(cloudArtifactIndex{
		ContractVersion: artifactIndexContract,
		UpdatedAt:       "2026-07-30T00:00:00Z",
		Artifacts: []cloudArtifact{{
			ID:    artifactID,
			Title: title,
			Kind:  "html",
			URL: "https://agent-" + formatAgentUID(agentUID) +
				".artifacts.catsco.fun:19991/artifacts/" +
				artifactID + "/latest/",
			UpdatedAt: "2026-07-30T00:00:00Z",
		}},
	})
	return string(data)
}

func mappedArtifactIndexJSON(agentUID int64) string {
	agentID := formatAgentUID(agentUID)
	data, _ := json.Marshal(cloudArtifactIndex{
		ContractVersion: artifactIndexContract,
		UpdatedAt:       "2026-07-30T00:00:00Z",
		Artifacts: []cloudArtifact{{
			ID:    "mapped-game",
			Title: "Mapped game",
			Kind:  "html",
			URL: "https://mapped.example.test/artifacts/by-agent/" +
				agentID + "/mapped-game/latest/",
			UpdatedAt: "2026-07-30T00:00:00Z",
		}},
	})
	return string(data)
}

func formatAgentUID(agentUID int64) string {
	return strconv.FormatInt(agentUID, 10)
}
