package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
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

const artifactIDMaxLength = 64

func validArtifactID(value string) bool {
	return len(value) > 0 && len(value) <= artifactIDMaxLength && artifactIDPattern.MatchString(value)
}

// CloudArtifactHandler proxies the public index and the protected artifact-management service.
type CloudArtifactHandler struct {
	indexURL          string
	managementURL     string
	managementToken   string
	httpClient        *http.Client
	db                store.Store
	configErr         error
	managementErr     error
	nodeRegistry      *artifactNodeRegistry
	nodeRegistryErr   error
	directTemplate    *artifactDirectURLTemplate
	directTemplateErr error

	artifactContextCacheMu                 sync.Mutex
	artifactContextCache                   map[artifactContextCacheKey]artifactContextCacheEntry
	artifactContextCacheTTL                time.Duration
	artifactContextExactMutationGeneration map[artifactContextCacheKey]uint64
	artifactContextIDMutationGeneration    map[string]uint64
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
	ViewerRelation  string          `json:"viewer_relation,omitempty"`
	Visibility      string          `json:"visibility,omitempty"`
	CanPublish      bool            `json:"can_publish,omitempty"`
	PublishMode     string          `json:"publish_mode,omitempty"`
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
	SourceTopicID  string `json:"source_topic_id,omitempty"`
	CreatorType    string `json:"creator_type,omitempty"`
	CreatorUID     string `json:"creator_uid,omitempty"`
	CreatorName    string `json:"creator_name,omitempty"`
	UploaderUID    string `json:"uploader_uid,omitempty"`
	UploaderName   string `json:"uploader_name,omitempty"`
	UploadedByMe   bool   `json:"uploaded_by_me,omitempty"`
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
	handler.directTemplate, handler.directTemplateErr =
		loadArtifactDirectURLTemplateFromEnv()
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
	if h != nil && (h.nodeRegistryErr != nil || h.directTemplateErr != nil) {
		writeArtifactError(w, http.StatusServiceUnavailable, "artifact_management_unavailable")
		return
	}
	if h != nil && (h.nodeRegistry != nil || h.directTemplate != nil) {
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
		h.handleMutation(w, r, artifactID, "", uid, "owner", h.managementURL, h.managementToken, "", 0)
	case action == "restore" && r.Method == http.MethodPost:
		h.handleMutation(w, r, artifactID, "/restore", uid, "owner", h.managementURL, h.managementToken, "", 0)
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
	if agentUID, ok := parseAgentFilesAPIPath(r.URL.Path); ok {
		h.handleAgentFiles(w, r, agentUID)
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
	if _, relation, status, err := accessibleAgentUser(h.db, viewerUID, route.agentUID); err != nil {
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	} else {
		route.viewerRelation = relation
	}
	memberUploadsEnabled := memberArtifactUploadsEnabled(h.db, route.agentUID)
	node, err := h.resolveArtifactNode(route.agentUID)
	if err != nil {
		writeArtifactError(w, http.StatusServiceUnavailable, "artifact_management_unavailable")
		return
	}
	collectionURL := ""
	if node.managementURL != "" {
		collectionURL, err = agentManagementCollectionURL(node.managementURL, route.agentUID)
		if err != nil {
			writeArtifactError(w, http.StatusServiceUnavailable, "artifact_management_unavailable")
			return
		}
	}

	switch route.action {
	case "list":
		if r.Method == http.MethodPost {
			if route.viewerRelation != "owner" && !memberUploadsEnabled {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "member artifact uploads are disabled"})
				return
			}
			if collectionURL == "" {
				writeArtifactError(w, http.StatusServiceUnavailable, "artifact_management_unavailable")
				return
			}
			h.handlePublish(w, r, viewerUID, route.viewerRelation, collectionURL, node.managementToken, node.publicBaseURL, route.agentUID)
			return
		}
		if r.Method != http.MethodGet {
			w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
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
		if status == "deleted" && route.viewerRelation != "owner" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "artifact management requires agent owner"})
			return
		}
		if collectionURL == "" {
			h.handleNodePublicIndexList(w, r, status, node, route.agentUID, route.viewerRelation)
			return
		}
		h.handleManagedList(w, r, status, collectionURL, node.managementToken, node.publicBaseURL, route.agentUID, viewerUID, route.viewerRelation, memberUploadsEnabled)
	case "delete":
		if r.Method != http.MethodDelete {
			w.Header().Set("Allow", http.MethodDelete)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if collectionURL == "" {
			writeArtifactError(w, http.StatusServiceUnavailable, "artifact_management_unavailable")
			return
		}
		if route.viewerRelation != "owner" {
			allowed, err := h.memberCanDeleteArtifact(r, collectionURL, node.managementToken, node.publicBaseURL, route.agentUID, viewerUID, route.artifactID)
			if err != nil {
				writeArtifactUpstreamError(w, err)
				return
			}
			if !allowed {
				writeJSON(w, http.StatusForbidden, map[string]string{"error": "members can only remove their own artifacts"})
				return
			}
		}
		h.handleMutation(w, r, route.artifactID, "", viewerUID, route.viewerRelation, collectionURL, node.managementToken, node.publicBaseURL, route.agentUID)
	case "restore":
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if collectionURL == "" {
			writeArtifactError(w, http.StatusServiceUnavailable, "artifact_management_unavailable")
			return
		}
		if route.viewerRelation != "owner" {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "artifact management requires agent owner"})
			return
		}
		h.handleMutation(w, r, route.artifactID, "/restore", viewerUID, route.viewerRelation, collectionURL, node.managementToken, node.publicBaseURL, route.agentUID)
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
	if h != nil && (h.nodeRegistryErr != nil || h.directTemplateErr != nil) {
		writeArtifactError(w, http.StatusServiceUnavailable, "artifact_management_unavailable")
		return
	}
	if h != nil && (h.nodeRegistry != nil || h.directTemplate != nil) {
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
		h.handleManagedList(w, r, status, h.managementURL, h.managementToken, "", 0, UIDFromContext(r.Context()), "owner", true)
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
	viewerUID int64,
	viewerRelation string,
	memberUploadsEnabled bool,
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
	if agentUID > 0 {
		list.ViewerRelation = viewerRelation
		list.Visibility = "agent_users"
		list.CanPublish = list.CanPublish && list.PublishMode == "immediate"
		if viewerRelation != "owner" && !memberUploadsEnabled {
			list.CanPublish = false
		}
		if !list.CanPublish {
			list.PublishMode = ""
		}
		h.enrichArtifactCreators(list.Artifacts, agentUID, false)
		for i := range list.Artifacts {
			list.Artifacts[i].UploadedByMe = list.Artifacts[i].UploaderUID != "" &&
				list.Artifacts[i].UploaderUID == strconv.FormatInt(viewerUID, 10)
			if viewerRelation != "owner" {
				list.Artifacts[i].CanDelete = list.Artifacts[i].Status == "active" && list.Artifacts[i].UploadedByMe
				list.Artifacts[i].CanRestore = false
			}
		}
	}
	list.Count = len(list.Artifacts)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, list)
}

func memberArtifactUploadsEnabled(db store.Store, agentUID int64) bool {
	if db == nil || agentUID <= 0 {
		return true
	}
	policies, ok := db.(store.BotArtifactPolicyStore)
	if !ok {
		return true
	}
	enabled, err := policies.GetBotArtifactUploadPolicy(agentUID)
	return err != nil || enabled
}

func (h *CloudArtifactHandler) enrichArtifactCreators(artifacts []cloudArtifact, fallbackAgentUID int64, assumeAgent bool) {
	if h == nil {
		return
	}

	names := make(map[int64]string)
	resolved := make(map[int64]bool)
	resolveName := func(uidValue string) string {
		if h.db == nil {
			return ""
		}
		uid, err := strconv.ParseInt(strings.TrimSpace(uidValue), 10, 64)
		if err != nil || uid <= 0 {
			return ""
		}
		if resolved[uid] {
			return names[uid]
		}
		resolved[uid] = true
		user, err := h.db.GetUser(uid)
		if err != nil || user == nil {
			return ""
		}
		name := strings.TrimSpace(user.DisplayName)
		if name == "" {
			name = strings.TrimSpace(user.Username)
		}
		names[uid] = name
		return name
	}

	for i := range artifacts {
		artifact := &artifacts[i]
		if artifact.UploaderUID != "" {
			if strings.TrimSpace(artifact.UploaderName) == "" {
				artifact.UploaderName = resolveName(artifact.UploaderUID)
			}
			artifact.CreatorType = "user"
			artifact.CreatorUID = artifact.UploaderUID
			artifact.CreatorName = artifact.UploaderName
			continue
		}

		if artifact.CreatorType == "" {
			if assumeAgent {
				artifact.CreatorType = "agent"
			} else {
				artifact.CreatorType = "unknown"
			}
		}
		if artifact.CreatorType != "agent" && artifact.CreatorType != "user" {
			artifact.CreatorUID = ""
			artifact.CreatorName = ""
			continue
		}

		if artifact.CreatorType == "agent" && artifact.CreatorUID == "" && fallbackAgentUID > 0 {
			artifact.CreatorUID = strconv.FormatInt(fallbackAgentUID, 10)
		}
		if strings.TrimSpace(artifact.CreatorName) == "" {
			artifact.CreatorName = resolveName(artifact.CreatorUID)
		}
		if artifact.CreatorType == "agent" {
			if artifact.AgentName == "" {
				artifact.AgentName = artifact.CreatorName
			}
		} else if artifact.CreatorType == "user" {
			artifact.UploaderUID = artifact.CreatorUID
			artifact.UploaderName = artifact.CreatorName
		}
	}
}

func (h *CloudArtifactHandler) handlePublicIndexList(w http.ResponseWriter, r *http.Request) {
	index, ok := h.readPublicArtifactIndex(w, r, h.indexURL)
	if !ok {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, index)
}

func (h *CloudArtifactHandler) handleNodePublicIndexList(
	w http.ResponseWriter,
	r *http.Request,
	status string,
	node artifactNode,
	agentUID int64,
	viewerRelation string,
) {
	list := cloudArtifactManagementList{
		ContractVersion: artifactManagementContract,
		Status:          status,
		Artifacts:       []cloudArtifact{},
		ViewerRelation:  viewerRelation,
		Visibility:      "agent_users",
		CanPublish:      false,
	}
	if status == "deleted" {
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, http.StatusOK, list)
		return
	}

	agentID := strconv.FormatInt(agentUID, 10)
	indexURL := strings.TrimRight(node.publicBaseURL, "/")
	urlValidationAgentUID := agentUID
	allowMissing := false
	if node.rootPublicIndex {
		indexURL += "/artifacts-index.json"
		urlValidationAgentUID = 0
		allowMissing = true
	} else {
		indexURL += "/by-agent/" + agentID + "/artifacts-index.json"
	}
	index, ok := h.readPublicArtifactIndexWithOptions(
		w,
		r,
		indexURL,
		allowMissing,
	)
	if !ok {
		return
	}
	if validateManagedArtifactNodeURLs(
		index.Artifacts,
		node.publicBaseURL,
		urlValidationAgentUID,
	) != nil {
		writeArtifactError(w, http.StatusBadGateway, "artifact_response_invalid")
		return
	}

	list.Artifacts = append(list.Artifacts, index.Artifacts...)
	h.enrichArtifactCreators(list.Artifacts, agentUID, true)
	for i := range list.Artifacts {
		list.Artifacts[i].Status = "active"
		list.Artifacts[i].AgentUID = agentID
		list.Artifacts[i].CanDelete = false
		list.Artifacts[i].CanRestore = false
	}
	list.Count = len(list.Artifacts)
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, list)
}

func (h *CloudArtifactHandler) readPublicArtifactIndex(
	w http.ResponseWriter,
	r *http.Request,
	indexURL string,
) (cloudArtifactIndex, bool) {
	return h.readPublicArtifactIndexWithOptions(w, r, indexURL, false)
}

func (h *CloudArtifactHandler) readPublicArtifactIndexWithOptions(
	w http.ResponseWriter,
	r *http.Request,
	indexURL string,
	allowMissing bool,
) (cloudArtifactIndex, bool) {
	upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodGet, indexURL, nil)
	if err != nil {
		writeArtifactError(w, http.StatusInternalServerError, "artifact_request_failed")
		return cloudArtifactIndex{}, false
	}
	upstreamReq.Header.Set("Accept", "application/json")
	upstreamReq.Header.Set("User-Agent", "catsco-cloud-artifacts/1.0")

	resp, err := h.httpClient.Do(upstreamReq)
	if err != nil {
		if allowMissing && artifactIndexDNSNotFound(err) {
			return emptyCloudArtifactIndex(), true
		}
		writeArtifactError(w, http.StatusBadGateway, "artifact_index_unavailable")
		return cloudArtifactIndex{}, false
	}
	defer resp.Body.Close()
	if allowMissing && resp.StatusCode == http.StatusNotFound {
		return emptyCloudArtifactIndex(), true
	}
	if resp.StatusCode != http.StatusOK {
		writeArtifactError(w, http.StatusBadGateway, "artifact_index_unavailable")
		return cloudArtifactIndex{}, false
	}
	body, err := readArtifactResponse(resp.Body)
	if err != nil {
		writeArtifactError(w, http.StatusBadGateway, "artifact_response_invalid")
		return cloudArtifactIndex{}, false
	}

	var index cloudArtifactIndex
	if err := json.Unmarshal(body, &index); err != nil || validateCloudArtifactIndex(index) != nil {
		writeArtifactError(w, http.StatusBadGateway, "artifact_response_invalid")
		return cloudArtifactIndex{}, false
	}
	if index.Artifacts == nil {
		index.Artifacts = []cloudArtifact{}
	}
	return index, true
}

func emptyCloudArtifactIndex() cloudArtifactIndex {
	return cloudArtifactIndex{
		ContractVersion: artifactIndexContract,
		Artifacts:       []cloudArtifact{},
	}
}

func artifactIndexDNSNotFound(err error) bool {
	var dnsErr *net.DNSError
	return errors.As(err, &dnsErr) &&
		(dnsErr.IsNotFound || strings.EqualFold(dnsErr.Err, "no such host"))
}

func (h *CloudArtifactHandler) handleMutation(
	w http.ResponseWriter,
	r *http.Request,
	artifactID, suffix string,
	uid int64,
	viewerRelation string,
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
	h.invalidateArtifactContextCache(agentUID, artifactID)
	artifacts := []cloudArtifact{operation.Artifact}
	h.enrichArtifactCreators(artifacts, agentUID, false)
	operation.Artifact = artifacts[0]
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, operation)
}

func (h *CloudArtifactHandler) handlePublish(
	w http.ResponseWriter,
	r *http.Request,
	viewerUID int64,
	viewerRelation string,
	collectionURL string,
	managementToken, publicBaseURL string,
	agentUID int64,
) {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16*1024))
	decoder.DisallowUnknownFields()
	var request cloudArtifactPublishRequest
	if err := decoder.Decode(&request); err != nil || decoder.Decode(&struct{}{}) != io.EOF {
		writeArtifactError(w, http.StatusBadRequest, "artifact_publish_request_invalid")
		return
	}
	request.Title = strings.TrimSpace(request.Title)
	request.Kind = strings.TrimSpace(request.Kind)
	request.URL = strings.TrimSpace(request.URL)
	request.SourceTitle = strings.TrimSpace(request.SourceTitle)
	request.SourceTopicID = strings.TrimSpace(request.SourceTopicID)
	if validateCloudArtifactPublishRequest(request, r.Host) != nil {
		writeArtifactError(w, http.StatusBadRequest, "artifact_publish_request_invalid")
		return
	}
	payload, _ := json.Marshal(map[string]string{
		"title":           request.Title,
		"kind":            request.Kind,
		"url":             request.URL,
		"source_title":    request.SourceTitle,
		"source_topic_id": request.SourceTopicID,
		"actor_uid":       strconv.FormatInt(viewerUID, 10),
		"actor_relation":  viewerRelation,
		"creator_type":    "user",
		"creator_uid":     strconv.FormatInt(viewerUID, 10),
		"uploader_uid":    strconv.FormatInt(viewerUID, 10),
		"publish_mode":    "immediate",
	})
	body, err := h.requestManagement(r, http.MethodPost, collectionURL, payload, managementToken)
	if err != nil {
		writeArtifactUpstreamError(w, err)
		return
	}
	var operation cloudArtifactOperation
	if err := json.Unmarshal(body, &operation); err != nil || !operation.OK || validateManagedArtifact(operation.Artifact) != nil {
		writeArtifactError(w, http.StatusBadGateway, "artifact_response_invalid")
		return
	}
	if operation.Artifact.Status != "active" ||
		operation.Artifact.AgentUID != strconv.FormatInt(agentUID, 10) ||
		operation.Artifact.UploaderUID != strconv.FormatInt(viewerUID, 10) ||
		validateArtifactNodeURL(operation.Artifact.URL, publicBaseURL, agentUID) != nil {
		writeArtifactError(w, http.StatusBadGateway, "artifact_response_invalid")
		return
	}
	operation.Artifact.UploadedByMe = true
	operation.Artifact.CanDelete = true
	operation.Artifact.CanRestore = false
	artifacts := []cloudArtifact{operation.Artifact}
	h.enrichArtifactCreators(artifacts, agentUID, false)
	operation.Artifact = artifacts[0]
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, operation)
}

func (h *CloudArtifactHandler) memberCanDeleteArtifact(
	r *http.Request,
	collectionURL, managementToken, publicBaseURL string,
	agentUID, viewerUID int64,
	artifactID string,
) (bool, error) {
	body, err := h.requestManagement(r, http.MethodGet, collectionURL+"?status=active", nil, managementToken)
	if err != nil {
		return false, err
	}
	var list cloudArtifactManagementList
	if err := json.Unmarshal(body, &list); err != nil ||
		validateManagedArtifactList(list, "active") != nil ||
		validateManagedArtifactAgentUIDs(list.Artifacts, agentUID) != nil ||
		validateManagedArtifactNodeURLs(list.Artifacts, publicBaseURL, agentUID) != nil {
		return false, &artifactUpstreamError{status: http.StatusBadGateway, code: "artifact_response_invalid"}
	}
	viewer := strconv.FormatInt(viewerUID, 10)
	for _, artifact := range list.Artifacts {
		if artifact.ID == artifactID {
			return artifact.UploaderUID != "" && artifact.UploaderUID == viewer, nil
		}
	}
	return false, &artifactUpstreamError{status: http.StatusNotFound, code: "artifact_not_found"}
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
	if h == nil || agentUID <= 0 ||
		h.nodeRegistryErr != nil || h.directTemplateErr != nil {
		return artifactNode{}, errors.New("artifact node is unavailable")
	}
	if h.nodeRegistry != nil {
		if _, mapped := h.nodeRegistry.agents[agentUID]; mapped {
			return h.nodeRegistry.resolve(agentUID)
		}
	}
	if h.directTemplate != nil {
		return h.directTemplate.resolve(agentUID)
	}
	if h.nodeRegistry != nil {
		if !h.nodeRegistry.fallbackToLegacy {
			return artifactNode{}, fmt.Errorf("artifact agent %d has no configured node", agentUID)
		}
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
	if !validArtifactID(artifact.ID) || strings.TrimSpace(artifact.Title) == "" {
		return errors.New("invalid artifact identity")
	}
	if artifact.Kind != "html" && artifact.Kind != "mini_app" {
		return errors.New("invalid artifact kind")
	}
	if len(artifact.CreatorName) > 240 {
		return errors.New("invalid artifact creator name")
	}
	switch artifact.CreatorType {
	case "", "user", "agent":
		if artifact.CreatorUID != "" {
			uid, err := strconv.ParseInt(artifact.CreatorUID, 10, 64)
			if err != nil || uid <= 0 {
				return errors.New("invalid artifact creator UID")
			}
		}
	case "unknown":
		if artifact.CreatorUID != "" || artifact.CreatorName != "" {
			return errors.New("unknown artifact creator must not contain identity")
		}
	default:
		return errors.New("invalid artifact creator type")
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
	if err != nil || !validArtifactID(artifactID) {
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
	agentUID       int64
	artifactID     string
	action         string
	viewerRelation string
}

func validateCloudArtifactPublishRequest(request cloudArtifactPublishRequest, requestHost string) error {
	if request.Title == "" || len(request.Title) > 160 ||
		(request.Kind != "html" && request.Kind != "mini_app") ||
		len(request.SourceTitle) > 240 || len(request.SourceTopicID) > 160 {
		return errors.New("invalid artifact publish request")
	}
	artifactURL, err := url.Parse(request.URL)
	if err != nil || artifactURL.Host == "" || artifactURL.User != nil ||
		(artifactURL.Scheme != "http" && artifactURL.Scheme != "https") ||
		!strings.EqualFold(artifactURL.Host, strings.TrimSpace(requestHost)) ||
		!strings.HasPrefix(artifactURL.Path, "/uploads/files/") {
		return errors.New("invalid artifact publish URL")
	}
	return nil
}

type cloudArtifactPublishRequest struct {
	Title         string `json:"title"`
	Kind          string `json:"kind"`
	URL           string `json:"url"`
	SourceTitle   string `json:"source_title,omitempty"`
	SourceTopicID string `json:"source_topic_id,omitempty"`
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
	if err != nil || !validArtifactID(artifactID) {
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
		"artifact_path_invalid", "artifact_operation_conflict",
		"artifact_publish_unsupported", "artifact_publish_invalid":
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
		"artifact_not_found":               "产物不存在",
		"artifact_already_deleted":         "产物已在回收站中",
		"artifact_not_deleted":             "产物不在回收站中",
		"artifact_path_invalid":            "产物标识无效",
		"artifact_operation_conflict":      "产物状态已变化，请刷新后重试",
		"artifact_status_invalid":          "产物列表状态无效",
		"artifact_agent_required":          "请从具体虚拟员工的生成物入口访问",
		"artifact_management_unavailable":  "产物管理服务暂不可用",
		"artifact_index_unavailable":       "产物列表暂不可用",
		"artifact_response_invalid":        "产物服务返回了无效数据",
		"artifact_request_failed":          "产物请求创建失败",
		"artifact_service_unavailable":     "产物服务暂不可用",
		"artifact_publish_request_invalid": "成果发布信息无效",
		"artifact_publish_unsupported":     "当前成果服务还不支持成员发布",
		"artifact_publish_invalid":         "成果服务无法导入这个文件",
	}
	message := messages[code]
	if message == "" {
		message = "产物操作失败"
	}
	writeJSON(w, status, map[string]string{"error": message, "code": code})
}
