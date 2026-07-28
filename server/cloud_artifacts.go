package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
)

const (
	artifactIndexContract        = "cloud-artifacts.index.v1"
	artifactManagementContract   = "cloud-artifacts.management-list.v1"
	defaultArtifactIndexURL      = "https://logs.catsco.fun:9000/artifacts/artifacts-index.json"
	defaultArtifactManagementURL = "https://logs.catsco.fun:9000/internal/artifacts"
	artifactResponseMaxBytes     = 1 << 20
	artifactUpstreamTimeout      = 10 * time.Second
)

var artifactIDPattern = regexp.MustCompile(`^[a-z0-9]+(?:[a-z0-9._-]*[a-z0-9])?$`)

// CloudArtifactHandler proxies the public index and the protected artifact-management service.
type CloudArtifactHandler struct {
	indexURL        string
	managementURL   string
	managementToken string
	httpClient      *http.Client
	db              store.Store
	configErr       error
	managementErr   error
	nodeRegistry    *artifactNodeRegistry
	nodeRegistryErr error
}

type cloudArtifactIndex struct {
	ContractVersion string          `json:"contract_version"`
	UpdatedAt       string          `json:"updated_at,omitempty"`
	Artifacts       []cloudArtifact `json:"artifacts"`
}

type cloudArtifactManagementList struct {
	ContractVersion string          `json:"contract_version"`
	Status          string          `json:"status"`
	Count           int             `json:"count"`
	Artifacts       []cloudArtifact `json:"artifacts"`
}

type cloudArtifactOperation struct {
	OK       bool          `json:"ok"`
	Artifact cloudArtifact `json:"artifact"`
}

type cloudArtifact struct {
	ID             string `json:"id"`
	Title          string `json:"title"`
	Kind           string `json:"kind"`
	URL            string `json:"url"`
	Status         string `json:"status,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	UpdatedAt      string `json:"updated_at"`
	PublishVersion *int   `json:"publish_version,omitempty"`
	AgentUID       string `json:"agent_uid,omitempty"`
	AgentName      string `json:"agent_name,omitempty"`
	SourceTitle    string `json:"source_title,omitempty"`
	DeletedAt      string `json:"deleted_at,omitempty"`
	CanDelete      bool   `json:"can_delete,omitempty"`
	CanRestore     bool   `json:"can_restore,omitempty"`
}

type artifactUpstreamError struct {
	status int
	code   string
}

func (e *artifactUpstreamError) Error() string {
	return e.code
}

// NewCloudArtifactHandler builds the legacy read-only proxy.
func NewCloudArtifactHandler(indexURL string, client *http.Client) *CloudArtifactHandler {
	return newCloudArtifactHandler(indexURL, "", "", client)
}

// NewCloudArtifactManagementHandler enables list, delete, and restore through the protected host API.
func NewCloudArtifactManagementHandler(indexURL, managementURL, managementToken string, client *http.Client) *CloudArtifactHandler {
	return newCloudArtifactHandler(indexURL, managementURL, managementToken, client)
}

// SetStore enables access checks for agent-scoped artifact routes.
func (h *CloudArtifactHandler) SetStore(db store.Store) {
	if h != nil {
		h.db = db
	}
}

func newCloudArtifactHandler(indexURL, managementURL, managementToken string, client *http.Client) *CloudArtifactHandler {
	h := &CloudArtifactHandler{httpClient: client}
	if h.httpClient == nil {
		h.httpClient = &http.Client{Timeout: artifactUpstreamTimeout}
	}

	parsedIndex, err := parseArtifactURL(indexURL)
	if err != nil {
		h.configErr = fmt.Errorf("invalid CATSCO_ARTIFACT_INDEX_URL")
	} else {
		h.indexURL = parsedIndex
	}

	managementURL = strings.TrimSpace(managementURL)
	managementToken = strings.TrimSpace(managementToken)
	if managementURL == "" && managementToken == "" {
		return h
	}
	if managementURL == "" || managementToken == "" {
		h.managementErr = fmt.Errorf("incomplete artifact management configuration")
		return h
	}
	parsedManagement, err := parseArtifactURL(managementURL)
	if err != nil || len(managementToken) < 32 {
		h.managementErr = fmt.Errorf("invalid artifact management configuration")
		return h
	}
	h.managementURL = strings.TrimRight(parsedManagement, "/")
	h.managementToken = managementToken
	return h
}

// NewCloudArtifactHandlerFromEnv uses the current CatsCo artifact host.
func NewCloudArtifactHandlerFromEnv() *CloudArtifactHandler {
	indexURL := strings.TrimSpace(os.Getenv("CATSCO_ARTIFACT_INDEX_URL"))
	if indexURL == "" {
		indexURL = defaultArtifactIndexURL
	}
	managementURL := strings.TrimSpace(os.Getenv("CATSCO_ARTIFACT_MANAGEMENT_URL"))
	managementToken := strings.TrimSpace(os.Getenv("CATSCO_ARTIFACT_MANAGEMENT_TOKEN"))
	if managementURL == "" && managementToken != "" {
		managementURL = defaultArtifactManagementURL
	}
	handler := newCloudArtifactHandler(indexURL, managementURL, managementToken, nil)
	handler.nodeRegistry, handler.nodeRegistryErr = loadArtifactNodeRegistryFromEnv()
	return handler
}

// Handle routes the artifact collection and exact-ID mutation endpoints.
func (h *CloudArtifactHandler) Handle(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/api/artifacts" || r.URL.Path == "/api/artifacts/" {
		h.HandleList(w, r)
		return
	}
	uid := UIDFromContext(r.Context())
	if uid <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if h != nil && h.nodeRegistryErr != nil {
		writeArtifactError(w, http.StatusServiceUnavailable, "artifact_management_unavailable")
		return
	}
	if h != nil && h.nodeRegistry != nil {
		writeArtifactError(w, http.StatusGone, "artifact_agent_required")
		return
	}
	artifactID, action, ok := parseArtifactAPIPath(r.URL.Path)
	if !ok {
		writeArtifactError(w, http.StatusNotFound, "artifact_not_found")
		return
	}
	if h == nil || h.managementErr != nil || h.managementURL == "" {
		writeArtifactError(w, http.StatusServiceUnavailable, "artifact_management_unavailable")
		return
	}

	switch {
	case action == "delete" && r.Method == http.MethodDelete:
		h.handleMutation(w, r, artifactID, "", uid, h.managementURL, h.managementToken, "", 0)
	case action == "restore" && r.Method == http.MethodPost:
		h.handleMutation(w, r, artifactID, "/restore", uid, h.managementURL, h.managementToken, "", 0)
	default:
		w.Header().Set("Allow", allowedArtifactMethod(action))
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

// HandleAgentArtifacts serves artifact operations scoped to one managed virtual employee.
func (h *CloudArtifactHandler) HandleAgentArtifacts(w http.ResponseWriter, r *http.Request) {
	viewerUID := UIDFromContext(r.Context())
	if viewerUID <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	route, ok := parseAgentArtifactAPIPath(r.URL.Path)
	if !ok {
		writeArtifactError(w, http.StatusNotFound, "artifact_not_found")
		return
	}
	if h == nil || h.db == nil {
		writeArtifactError(w, http.StatusServiceUnavailable, "artifact_management_unavailable")
		return
	}
	if _, _, status, err := accessibleAgentUser(h.db, viewerUID, route.agentUID); err != nil {
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	node, err := h.resolveArtifactNode(route.agentUID)
	if err != nil {
		writeArtifactError(w, http.StatusServiceUnavailable, "artifact_management_unavailable")
		return
	}
	collectionURL, err := agentManagementCollectionURL(node.managementURL, route.agentUID)
	if err != nil {
		writeArtifactError(w, http.StatusServiceUnavailable, "artifact_management_unavailable")
		return
	}

	switch route.action {
	case "list":
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		status := strings.TrimSpace(r.URL.Query().Get("status"))
		if status == "" {
			status = "active"
		}
		if status != "active" && status != "deleted" {
			writeArtifactError(w, http.StatusBadRequest, "artifact_status_invalid")
			return
		}
		h.handleManagedList(w, r, status, collectionURL, node.managementToken, node.publicBaseURL, route.agentUID)
	case "delete":
		if r.Method != http.MethodDelete {
			w.Header().Set("Allow", http.MethodDelete)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		h.handleMutation(w, r, route.artifactID, "", viewerUID, collectionURL, node.managementToken, node.publicBaseURL, route.agentUID)
	case "restore":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		h.handleMutation(w, r, route.artifactID, "/restore", viewerUID, collectionURL, node.managementToken, node.publicBaseURL, route.agentUID)
	default:
		writeArtifactError(w, http.StatusNotFound, "artifact_not_found")
	}
}

// HandleList serves GET /api/artifacts for authenticated CatsCo users.
func (h *CloudArtifactHandler) HandleList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if UIDFromContext(r.Context()) <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if h != nil && h.nodeRegistryErr != nil {
		writeArtifactError(w, http.StatusServiceUnavailable, "artifact_management_unavailable")
		return
	}
	if h != nil && h.nodeRegistry != nil {
		writeArtifactError(w, http.StatusGone, "artifact_agent_required")
		return
	}
	if h == nil || h.configErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "artifact index is not configured"})
		return
	}
	status := strings.TrimSpace(r.URL.Query().Get("status"))
	if status == "" {
		status = "active"
	}
	if status != "active" && status != "deleted" {
		writeArtifactError(w, http.StatusBadRequest, "artifact_status_invalid")
		return
	}
	if h.managementErr != nil {
		writeArtifactError(w, http.StatusServiceUnavailable, "artifact_management_unavailable")
		return
	}
	if h.managementURL != "" {
		h.handleManagedList(w, r, status, h.managementURL, h.managementToken, "", 0)
		return
	}
	if status == "deleted" {
		writeArtifactError(w, http.StatusServiceUnavailable, "artifact_management_unavailable")
		return
	}
	h.handlePublicIndexList(w, r)
}

func (h *CloudArtifactHandler) handleManagedList(
	w http.ResponseWriter,
	r *http.Request,
	status, collectionURL string,
	managementToken, publicBaseURL string,
	agentUID int64,
) {
	target := collectionURL + "?status=" + url.QueryEscape(status)
	body, err := h.requestManagement(r, http.MethodGet, target, nil, managementToken)
	if err != nil {
		writeArtifactUpstreamError(w, err)
		return
	}
	var list cloudArtifactManagementList
	if err := json.Unmarshal(body, &list); err != nil || validateManagedArtifactList(list, status) != nil {
		writeArtifactError(w, http.StatusBadGateway, "artifact_response_invalid")
		return
	}
	if agentUID > 0 && validateManagedArtifactAgentUIDs(list.Artifacts, agentUID) != nil {
		writeArtifactError(w, http.StatusBadGateway, "artifact_response_invalid")
		return
	}
	if validateManagedArtifactNodeURLs(list.Artifacts, publicBaseURL, agentUID) != nil {
		writeArtifactError(w, http.StatusBadGateway, "artifact_response_invalid")
		return
	}
	if list.Artifacts == nil {
		list.Artifacts = []cloudArtifact{}
	}
	list.Count = len(list.Artifacts)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, list)
}

func (h *CloudArtifactHandler) handlePublicIndexList(w http.ResponseWriter, r *http.Request) {
	upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, h.indexURL, nil)
	if err != nil {
		writeArtifactError(w, http.StatusInternalServerError, "artifact_request_failed")
		return
	}
	upstreamReq.Header.Set("Accept", "application/json")
	upstreamReq.Header.Set("User-Agent", "catsco-cloud-artifacts/1.0")

	resp, err := h.httpClient.Do(upstreamReq)
	if err != nil {
		writeArtifactError(w, http.StatusBadGateway, "artifact_index_unavailable")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		writeArtifactError(w, http.StatusBadGateway, "artifact_index_unavailable")
		return
	}
	body, err := readArtifactResponse(resp.Body)
	if err != nil {
		writeArtifactError(w, http.StatusBadGateway, "artifact_response_invalid")
		return
	}

	var index cloudArtifactIndex
	if err := json.Unmarshal(body, &index); err != nil || validateCloudArtifactIndex(index) != nil {
		writeArtifactError(w, http.StatusBadGateway, "artifact_response_invalid")
		return
	}
	if index.Artifacts == nil {
		index.Artifacts = []cloudArtifact{}
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, index)
}

func (h *CloudArtifactHandler) handleMutation(
	w http.ResponseWriter,
	r *http.Request,
	artifactID, suffix string,
	uid int64,
	collectionURL string,
	managementToken, publicBaseURL string,
	agentUID int64,
) {
	payload, _ := json.Marshal(map[string]string{"actor_uid": strconv.FormatInt(uid, 10)})
	target := collectionURL + "/" + url.PathEscape(artifactID) + suffix
	body, err := h.requestManagement(r, r.Method, target, payload, managementToken)
	if err != nil {
		writeArtifactUpstreamError(w, err)
		return
	}
	var operation cloudArtifactOperation
	if err := json.Unmarshal(body, &operation); err != nil || !operation.OK || validateManagedArtifact(operation.Artifact) != nil {
		writeArtifactError(w, http.StatusBadGateway, "artifact_response_invalid")
		return
	}
	if operation.Artifact.ID != artifactID {
		writeArtifactError(w, http.StatusBadGateway, "artifact_response_invalid")
		return
	}
	if agentUID > 0 && operation.Artifact.AgentUID != strconv.FormatInt(agentUID, 10) {
		writeArtifactError(w, http.StatusBadGateway, "artifact_response_invalid")
		return
	}
	if validateArtifactNodeURL(operation.Artifact.URL, publicBaseURL, agentUID) != nil {
		writeArtifactError(w, http.StatusBadGateway, "artifact_response_invalid")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, operation)
}

func agentManagementCollectionURL(managementURL string, agentUID int64) (string, error) {
	if agentUID <= 0 {
		return "", errors.New("invalid artifact agent")
	}
	parsed, err := url.Parse(managementURL)
	if err != nil {
		return "", err
	}
	basePath := strings.TrimRight(parsed.Path, "/")
	if !strings.HasSuffix(basePath, "/artifacts") {
		return "", errors.New("artifact management URL must end with /artifacts")
	}
	basePath = strings.TrimSuffix(basePath, "/artifacts")
	parsed.Path = basePath + "/agents/" + strconv.FormatInt(agentUID, 10) + "/artifacts"
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return strings.TrimRight(parsed.String(), "/"), nil
}

func (h *CloudArtifactHandler) resolveArtifactNode(agentUID int64) (artifactNode, error) {
	if h == nil || agentUID <= 0 || h.nodeRegistryErr != nil {
		return artifactNode{}, errors.New("artifact node is unavailable")
	}
	if h.nodeRegistry != nil {
		return h.nodeRegistry.resolve(agentUID)
	}
	if h.managementErr != nil || h.managementURL == "" || h.managementToken == "" {
		return artifactNode{}, errors.New("artifact management is unavailable")
	}
	return artifactNode{
		id:              "legacy",
		managementURL:   h.managementURL,
		managementToken: h.managementToken,
	}, nil
}

func (h *CloudArtifactHandler) requestManagement(r *http.Request, method, target string, payload []byte, managementToken string) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	request, err := http.NewRequestWithContext(r.Context(), method, target, body)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Authorization", "Bearer "+managementToken)
	request.Header.Set("User-Agent", "catsco-cloud-artifacts/1.0")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := h.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	responseBody, readErr := readArtifactResponse(response.Body)
	if readErr != nil {
		return nil, readErr
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, parseArtifactUpstreamError(response.StatusCode, responseBody)
	}
	return responseBody, nil
}

func validateCloudArtifactIndex(index cloudArtifactIndex) error {
	if index.ContractVersion != artifactIndexContract {
		return errors.New("unsupported artifact index contract")
	}
	seen := make(map[string]struct{}, len(index.Artifacts))
	for _, artifact := range index.Artifacts {
		if err := validateArtifactIdentity(artifact); err != nil {
			return err
		}
		if _, err := time.Parse(time.RFC3339, artifact.UpdatedAt); err != nil {
			return errors.New("invalid artifact timestamp")
		}
		if _, exists := seen[artifact.ID]; exists {
			return errors.New("duplicate artifact ID")
		}
		seen[artifact.ID] = struct{}{}
	}
	return nil
}

func validateManagedArtifactList(list cloudArtifactManagementList, expectedStatus string) error {
	if list.ContractVersion != artifactManagementContract || list.Status != expectedStatus {
		return errors.New("unsupported artifact management contract")
	}
	seen := make(map[string]struct{}, len(list.Artifacts))
	for _, artifact := range list.Artifacts {
		if err := validateManagedArtifact(artifact); err != nil {
			return err
		}
		if artifact.Status != expectedStatus {
			return errors.New("artifact status mismatch")
		}
		if _, exists := seen[artifact.ID]; exists {
			return errors.New("duplicate artifact ID")
		}
		seen[artifact.ID] = struct{}{}
	}
	return nil
}

func validateManagedArtifactAgentUIDs(artifacts []cloudArtifact, expectedAgentUID int64) error {
	expected := strconv.FormatInt(expectedAgentUID, 10)
	for _, artifact := range artifacts {
		if artifact.AgentUID != expected {
			return errors.New("artifact agent UID mismatch")
		}
	}
	return nil
}

func validateManagedArtifactNodeURLs(artifacts []cloudArtifact, publicBaseURL string, expectedAgentUID int64) error {
	for _, artifact := range artifacts {
		if err := validateArtifactNodeURL(artifact.URL, publicBaseURL, expectedAgentUID); err != nil {
			return err
		}
	}
	return nil
}

func validateManagedArtifact(artifact cloudArtifact) error {
	if err := validateArtifactIdentity(artifact); err != nil {
		return err
	}
	if artifact.Status != "active" && artifact.Status != "deleted" {
		return errors.New("invalid artifact status")
	}
	if _, err := time.Parse(time.RFC3339, artifact.CreatedAt); err != nil {
		return errors.New("invalid artifact created timestamp")
	}
	if _, err := time.Parse(time.RFC3339, artifact.UpdatedAt); err != nil {
		return errors.New("invalid artifact updated timestamp")
	}
	if artifact.Status == "active" && !artifact.CanDelete {
		return errors.New("active artifact must be deletable")
	}
	if artifact.Status == "deleted" {
		if !artifact.CanRestore {
			return errors.New("deleted artifact must be restorable")
		}
		if _, err := time.Parse(time.RFC3339, artifact.DeletedAt); err != nil {
			return errors.New("invalid artifact deleted timestamp")
		}
	}
	return nil
}

func validateArtifactIdentity(artifact cloudArtifact) error {
	artifactURL, err := url.Parse(strings.TrimSpace(artifact.URL))
	if err != nil || artifactURL.Host == "" || (artifactURL.Scheme != "http" && artifactURL.Scheme != "https") {
		return errors.New("invalid artifact URL")
	}
	if !artifactIDPattern.MatchString(artifact.ID) || strings.TrimSpace(artifact.Title) == "" {
		return errors.New("invalid artifact identity")
	}
	if artifact.Kind != "html" && artifact.Kind != "mini_app" {
		return errors.New("invalid artifact kind")
	}
	return nil
}

func parseArtifactAPIPath(value string) (string, string, bool) {
	relative := strings.TrimPrefix(value, "/api/artifacts/")
	parts := strings.Split(relative, "/")
	if len(parts) < 1 || len(parts) > 2 {
		return "", "", false
	}
	artifactID, err := url.PathUnescape(parts[0])
	if err != nil || !artifactIDPattern.MatchString(artifactID) {
		return "", "", false
	}
	if len(parts) == 1 {
		return artifactID, "delete", true
	}
	if parts[1] == "restore" {
		return artifactID, "restore", true
	}
	return "", "", false
}

type agentArtifactAPIRoute struct {
	agentUID   int64
	artifactID string
	action     string
}

func parseAgentArtifactAPIPath(value string) (agentArtifactAPIRoute, bool) {
	relative := strings.TrimPrefix(value, "/api/agents/")
	if relative == value {
		return agentArtifactAPIRoute{}, false
	}
	parts := strings.Split(strings.Trim(relative, "/"), "/")
	if len(parts) < 2 || len(parts) > 4 || parts[1] != "artifacts" {
		return agentArtifactAPIRoute{}, false
	}
	agentUID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || agentUID <= 0 {
		return agentArtifactAPIRoute{}, false
	}
	if len(parts) == 2 {
		return agentArtifactAPIRoute{agentUID: agentUID, action: "list"}, true
	}
	artifactID, err := url.PathUnescape(parts[2])
	if err != nil || !artifactIDPattern.MatchString(artifactID) {
		return agentArtifactAPIRoute{}, false
	}
	if len(parts) == 3 {
		return agentArtifactAPIRoute{
			agentUID: agentUID, artifactID: artifactID, action: "delete",
		}, true
	}
	if parts[3] == "restore" {
		return agentArtifactAPIRoute{
			agentUID: agentUID, artifactID: artifactID, action: "restore",
		}, true
	}
	return agentArtifactAPIRoute{}, false
}

func allowedArtifactMethod(action string) string {
	if action == "restore" {
		return http.MethodPost
	}
	return http.MethodDelete
}

func parseArtifactURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil {
		return "", errors.New("invalid artifact URL")
	}
	parsed.Fragment = ""
	return parsed.String(), nil
}

func readArtifactResponse(reader io.Reader) ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(reader, artifactResponseMaxBytes+1))
	if err != nil || len(body) > artifactResponseMaxBytes {
		return nil, errors.New("artifact response is invalid")
	}
	return body, nil
}

func parseArtifactUpstreamError(status int, body []byte) error {
	var payload struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &payload)
	code := payload.Error.Code
	if !allowedArtifactErrorCode(code) {
		code = "artifact_service_unavailable"
	}
	return &artifactUpstreamError{status: status, code: code}
}

func allowedArtifactErrorCode(code string) bool {
	switch code {
	case "artifact_not_found", "artifact_already_deleted", "artifact_not_deleted",
		"artifact_path_invalid", "artifact_operation_conflict":
		return true
	default:
		return false
	}
}

func writeArtifactUpstreamError(w http.ResponseWriter, err error) {
	var upstream *artifactUpstreamError
	if !errors.As(err, &upstream) {
		writeArtifactError(w, http.StatusBadGateway, "artifact_service_unavailable")
		return
	}
	status := http.StatusBadGateway
	if upstream.status == http.StatusBadRequest || upstream.status == http.StatusNotFound || upstream.status == http.StatusConflict {
		status = upstream.status
	}
	writeArtifactError(w, status, upstream.code)
}

func writeArtifactError(w http.ResponseWriter, status int, code string) {
	messages := map[string]string{
		"artifact_not_found":              "产物不存在",
		"artifact_already_deleted":        "产物已在回收站中",
		"artifact_not_deleted":            "产物不在回收站中",
		"artifact_path_invalid":           "产物标识无效",
		"artifact_operation_conflict":     "产物状态已变化，请刷新后重试",
		"artifact_status_invalid":         "产物列表状态无效",
		"artifact_agent_required":         "请从具体虚拟员工的生成物入口访问",
		"artifact_management_unavailable": "产物管理服务暂不可用",
		"artifact_index_unavailable":      "产物列表暂不可用",
		"artifact_response_invalid":       "产物服务返回了无效数据",
		"artifact_request_failed":         "产物请求创建失败",
		"artifact_service_unavailable":    "产物服务暂不可用",
	}
	message := messages[code]
	if message == "" {
		message = "产物操作失败"
	}
	writeJSON(w, status, map[string]string{"error": message, "code": code})
}
