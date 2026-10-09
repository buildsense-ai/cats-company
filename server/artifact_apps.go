package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store"
)

// Artifact apps are the lightweight applications a bot serves from its own
// machine. Their public address and tunnel belong to the Artifact gateway, but
// the gateway's control token lets whoever holds it publish for any account, so
// a bot never gets one. A bot therefore asks this endpoint to publish on its
// behalf: the request carries the bot's own platform credential — its API key,
// or a person's login when a person is publishing — and only after the platform
// knows who is calling does it use the shared gateway token, the same credential
// artifact_launch.go uses for a launch handoff, to write the gateway
// configuration.
//
// The platform checks catalog access and mutation permissions because the
// gateway trusts its control token. The publishing Agent comes from the session;
// human attribution and editable presentation live separately in the platform.
const (
	artifactAppsGatewayPath = "/_gateway/apps"
	artifactAppsAPIPath     = "/api/artifacts/apps"
	artifactAppsUserAgent   = "catsco-artifact-apps/1.0"
	artifactAppsMaxBody     = 16 << 10
	artifactAppsMaxTitleLen = 128
	artifactAppsMaxKeyLen   = 4096
	// Keep the platform's public contract to a small set of predictable upload
	// presets. The gateway itself accepts a bounded nginx-size string (`maxBody`)
	// rather than this integer; the registration adapter below performs that
	// conversion. The field is omitted when absent, so the gateway keeps an
	// existing app's maxBody on updates; a new app then gets the gateway's 1m
	// default. One MiB remains an explicit preset for callers that want it.
	artifactAppsDefaultUploadLimitMB = 1
	// Bound on the gateway's own error text before it is relayed to a caller.
	artifactAppsMaxErrorTextLen = 200
)

// ArtifactAppsHandler publishes, lists and removes the caller's own Artifact
// applications. It mirrors ArtifactLaunchHandler: environment configuration, a
// bounded timeout, a bounded response, and an error mapping that keeps our own
// credential problems out of the caller's view.
type ArtifactAppsHandler struct {
	gatewayURL string
	token      string
	httpClient *http.Client
	configErr  error
	db         store.Store
}

// SetStore enables the authenticated list endpoint to verify that a requested
// Agent belongs to the caller or is an accepted friend.
func (h *ArtifactAppsHandler) SetStore(db store.Store) { h.db = db }

// artifactAppRequest is what a publishing bot sends. There is no owner field:
// the owner is the authenticated caller, and accepting one from the body would
// let a bot publish an application under another account's identity, because the
// gateway has no way to tell the difference.
type artifactAppRequest struct {
	SourceTopicID   string `json:"source_topic_id,omitempty"`
	SourceMessageID int64  `json:"source_message_id,omitempty"`
	ID              string `json:"id"`
	Title           string `json:"title"`
	PublicKey       string `json:"publicKey"`
	LocalPort       *int   `json:"localPort,omitempty"`
	UploadLimitMB   *int   `json:"uploadLimitMb,omitempty"`
}

// artifactApp is the gateway's view of a registered application. remote_port and
// the tunnel transport are assigned by the gateway, so nothing here is echoed
// back from a request.
type artifactApp struct {
	CreatorUID  int64    `json:"creator_uid,omitempty"`
	Description string   `json:"description"`
	IconURL     string   `json:"icon_url"`
	CanManage   bool     `json:"can_manage"`
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Agent       string   `json:"agent"`
	RemotePort  int      `json:"remote_port"`
	URL         string   `json:"url"`
	URLs        []string `json:"urls,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
}

// artifactAppRegistration is the gateway's answer to a publish. `status` is
// "registered" for a new id and "updated" for one the gateway already stores.
type artifactAppRegistration struct {
	artifactApp
	Status       string `json:"status"`
	TransportURL string `json:"transport_url,omitempty"`
}

func NewArtifactAppsHandlerFromEnv() *ArtifactAppsHandler {
	h := &ArtifactAppsHandler{
		gatewayURL: strings.TrimRight(strings.TrimSpace(os.Getenv("CATSCO_ARTIFACT_GATEWAY_URL")), "/"),
		token:      strings.TrimSpace(os.Getenv("CATSCO_ARTIFACT_GATEWAY_TOKEN")),
		httpClient: &http.Client{Timeout: artifactUpstreamTimeout},
	}
	if h.gatewayURL == "" || h.token == "" {
		h.configErr = errors.New("artifact gateway is not configured")
		return h
	}
	if !validGatewayOrigin(h.gatewayURL) {
		h.configErr = errors.New("invalid CATSCO_ARTIFACT_GATEWAY_URL")
		return h
	}
	return h
}

// Enabled reports whether the handler can serve requests, so callers can decide
// between returning 503 and registering the route at all.
func (h *ArtifactAppsHandler) Enabled() bool {
	return h != nil && h.configErr == nil
}

// HandleApps serves both route shapes under /api/artifacts/apps, so every method
// on the collection and on one application is answered here instead of falling
// through to the artifact-ID subtree, which would read "apps" as an artifact id.
func (h *ArtifactAppsHandler) HandleApps(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == artifactAppsAPIPath || r.URL.Path == artifactAppsAPIPath+"/" {
		h.HandleCollection(w, r)
		return
	}
	h.HandleItem(w, r)
}

func (h *ArtifactAppsHandler) HandleCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		h.handleRegister(w, r)
	case http.MethodGet:
		h.handleList(w, r)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodPost)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
	}
}

func (h *ArtifactAppsHandler) HandleItem(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.handleGet(w, r)
	case http.MethodDelete:
		h.handleDelete(w, r)
	case http.MethodPatch:
		h.handleMetadataUpdate(w, r)
	default:
		w.Header().Set("Allow", http.MethodGet+", "+http.MethodDelete+", "+http.MethodPatch)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
	}
}

// handleRegister publishes a new application, or updates the caller's own one if
// the id is already known.
func (h *ArtifactAppsHandler) handleRegister(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.caller(w, r)
	if !ok {
		return
	}
	body, ok := readArtifactAppsBody(w, r)
	if !ok {
		return
	}

	var request artifactAppRequest
	if err := json.Unmarshal(body, &request); err != nil {
		writeArtifactAppsFailure(w, &artifactAppsFailure{status: http.StatusBadRequest, value: "artifact_request_invalid"})
		return
	}
	app := strings.TrimSpace(request.ID)
	// The same rule the launch endpoint applies, so an id accepted here is one
	// the gateway can route on.
	if !artifactLaunchAppPattern.MatchString(app) {
		writeArtifactAppsFailure(w, &artifactAppsFailure{status: http.StatusBadRequest, value: "artifact_app_invalid"})
		return
	}
	title := strings.TrimSpace(request.Title)
	if title == "" || utf8.RuneCountInString(title) > artifactAppsMaxTitleLen || strings.ContainsAny(title, "\r\n\x00") {
		writeArtifactAppsFailure(w, &artifactAppsFailure{status: http.StatusBadRequest, value: "artifact_app_title_invalid"})
		return
	}
	publicKey := strings.TrimSpace(request.PublicKey)
	// A key is optional: an update that does not rotate it has to be able to omit
	// it, because the stored key cannot be read back. Whether one is required is
	// decided where the stored value lives — the gateway refuses a first
	// registration without one, and keeps the existing key when none is sent.
	if len(publicKey) > artifactAppsMaxKeyLen || strings.ContainsAny(publicKey, "\r\n\x00") {
		writeArtifactAppsFailure(w, &artifactAppsFailure{status: http.StatusBadRequest, value: "artifact_app_key_invalid"})
		return
	}
	if request.LocalPort != nil && (*request.LocalPort < 1 || *request.LocalPort > 65535) {
		writeArtifactAppsFailure(w, &artifactAppsFailure{status: http.StatusBadRequest, value: "artifact_app_port_invalid"})
		return
	}
	if request.UploadLimitMB != nil && !validArtifactAppsUploadLimitMB(*request.UploadLimitMB) {
		writeArtifactAppsFailure(w, &artifactAppsFailure{status: http.StatusBadRequest, value: "artifact_app_upload_limit_invalid"})
		return
	}

	// A registration can also be an update, and the gateway replaces an entry by
	// id alone — it has no account of its own to check against. Without this
	// step any signed-in caller could take over somebody else's application by
	// naming its id: the owner, the public key and therefore the tunnel would all
	// move to the caller. The read paths filter by owner; registration cannot,
	// because "not mine" and "does not exist" mean different things here.
	existing, found, failure := h.findApp(r.Context(), app)
	if failure != nil {
		writeArtifactAppsFailure(w, failure)
		return
	} else if found && existing.Agent != fmt.Sprint(uid) {
		// The same answer GET and DELETE give, so a caller cannot use this route
		// to tell "somebody else has it" from "nothing has it".
		writeArtifactAppsFailure(w, &artifactAppsFailure{status: http.StatusNotFound, value: "artifact_app_not_found"})
		return
	}
	if failure := h.prepareMetadata(r.Context(), uid, request, existing, found); failure != nil {
		writeArtifactAppsFailure(w, failure)
		return
	}

	payload := map[string]any{
		"id":    app,
		"title": title,
		// The gateway routes the tunnel by this field and cannot verify it, so it
		// is taken from the session only. A body that carries an "agent" of its
		// own is simply never read.
		"agent": fmt.Sprint(uid),
	}
	if publicKey != "" {
		// Omitted rather than sent empty, so the gateway can tell "rotate to this
		// key" from "leave the key alone".
		payload["publicKey"] = publicKey
	}
	if request.LocalPort != nil {
		// Omitted rather than sent as zero: the gateway assigns a port when it is
		// not asked for one, and 0 is not a port it could honour.
		payload["localPort"] = *request.LocalPort
	}
	if request.UploadLimitMB != nil {
		// The platform-facing Skill contract uses an integer MiB value. The
		// gateway-facing contract deliberately uses nginx syntax so it can render
		// the value verbatim without a unit-conversion ambiguity.
		payload["maxBody"] = fmt.Sprintf("%dm", *request.UploadLimitMB)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		writeArtifactAppsFailure(w, &artifactAppsFailure{status: http.StatusInternalServerError, value: "artifact_request_invalid"})
		return
	}

	status, responseBody, failure := h.call(r.Context(), http.MethodPost, artifactAppsGatewayPath, encoded)
	if failure != nil {
		writeArtifactAppsFailure(w, failure)
		return
	}
	var registration artifactAppRegistration
	if err := json.Unmarshal(responseBody, &registration); err != nil || registration.ID == "" {
		writeArtifactAppsFailure(w, &artifactAppsFailure{status: http.StatusBadGateway, value: "artifact_gateway_unavailable"})
		return
	}
	decorated := []artifactApp{registration.artifactApp}
	if failure := h.decorateApps(r.Context(), uid, uid, decorated); failure != nil {
		writeArtifactAppsFailure(w, failure)
		return
	}
	registration.artifactApp = decorated[0]
	writeJSON(w, status, registration)
}

func validArtifactAppsUploadLimitMB(value int) bool {
	switch value {
	case artifactAppsDefaultUploadLimitMB, 16, 64, 128, 256:
		return true
	default:
		return false
	}
}

// handleList defaults to the caller's own applications. An explicit Agent may
// be an owned bot or accepted friend; tunnel mutations stay publisher-only.
// Filter here because the authenticated gateway catalog covers every account.
func (h *ArtifactAppsHandler) handleList(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.caller(w, r)
	if !ok {
		return
	}
	targetUID := uid
	if rawAgent := strings.TrimSpace(r.URL.Query().Get("agent")); rawAgent != "" {
		parsed, err := strconv.ParseInt(rawAgent, 10, 64)
		if err != nil || parsed <= 0 {
			writeArtifactAppsFailure(w, &artifactAppsFailure{status: http.StatusBadRequest, value: "agent_invalid"})
			return
		}
		targetUID = parsed
		if targetUID != uid {
			if h.db == nil {
				writeArtifactAppsFailure(w, &artifactAppsFailure{status: http.StatusServiceUnavailable, value: "artifact_gateway_unavailable"})
				return
			}
			_, _, status, accessErr := accessibleAgentUser(h.db, uid, targetUID)
			if accessErr != nil || status != 0 {
				if status == 0 {
					status = http.StatusForbidden
				}
				writeArtifactAppsFailure(w, &artifactAppsFailure{status: status, value: "agent_forbidden"})
				return
			}
		}
	}
	apps, failure := h.listApps(r.Context(), targetUID)
	if failure != nil {
		writeArtifactAppsFailure(w, failure)
		return
	}
	if failure := h.decorateApps(r.Context(), uid, targetUID, apps); failure != nil {
		writeArtifactAppsFailure(w, failure)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, map[string]any{"apps": apps})
}

func (h *ArtifactAppsHandler) handleGet(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.caller(w, r)
	if !ok {
		return
	}
	id, ok := artifactAppsItemID(r.URL.Path)
	if !ok {
		writeArtifactAppsFailure(w, &artifactAppsFailure{status: http.StatusNotFound, value: "artifact_app_not_found"})
		return
	}
	app, failure := h.ownedApp(r.Context(), uid, id)
	if failure != nil {
		writeArtifactAppsFailure(w, failure)
		return
	}
	apps := []artifactApp{app}
	if failure := h.decorateApps(r.Context(), uid, uid, apps); failure != nil {
		writeArtifactAppsFailure(w, failure)
		return
	}
	writeJSON(w, http.StatusOK, apps[0])
}

// handleDelete removes the caller's own application. Ownership is proven against
// the gateway list first, because the gateway's delete takes an id and has no
// account of its own to check it against: without this step any caller could
// remove somebody else's public address by guessing its id.
func (h *ArtifactAppsHandler) handleDelete(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.caller(w, r)
	if !ok {
		return
	}
	id, ok := artifactAppsItemID(r.URL.Path)
	if !ok {
		writeArtifactAppsFailure(w, &artifactAppsFailure{status: http.StatusNotFound, value: "artifact_app_not_found"})
		return
	}
	if _, failure := h.ownedApp(r.Context(), uid, id); failure != nil {
		writeArtifactAppsFailure(w, failure)
		return
	}

	_, responseBody, failure := h.call(r.Context(), http.MethodDelete, artifactAppsGatewayPath+"/"+id, nil)
	if failure != nil {
		writeArtifactAppsFailure(w, failure)
		return
	}
	// The answer is parsed rather than relayed, so a proxy or an empty 200 cannot
	// be turned into a removal the gateway never confirmed. The id reported back
	// is the one that was asked for, not whatever the body happened to contain.
	var removed struct {
		Status string `json:"status"`
		ID     string `json:"id"`
	}
	if err := json.Unmarshal(responseBody, &removed); err != nil {
		writeArtifactAppsFailure(w, &artifactAppsFailure{status: http.StatusBadGateway, value: "artifact_gateway_unavailable"})
		return
	}
	if metadata, ok := h.db.(store.ArtifactAppMetadataStore); ok {
		if err := metadata.DeleteArtifactAppMetadata(r.Context(), uid, id); err != nil {
			writeArtifactAppsFailure(w, appMetadataFailure())
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "removed", "id": id})
}

// GatewayAppOwner resolves the owning account of one registered application
// from the gateway's unfiltered list. It backs gateway annotation
// authorization: annotations may only reference an app the conversation's
// Agent owns. Errors mean the answer is unknown, so callers fail closed.
func (h *ArtifactAppsHandler) GatewayAppOwner(ctx context.Context, appID string) (string, bool, error) {
	app, found, err := h.gatewayApp(ctx, appID)
	return app.Agent, found && app.Agent != "", err
}

// gatewayApp returns one exact registry record, rejecting ambiguous duplicate
// IDs. Binding authorization also needs its canonical public URLs.
func (h *ArtifactAppsHandler) gatewayApp(ctx context.Context, appID string) (artifactApp, bool, error) {
	if !h.Enabled() {
		return artifactApp{}, false, errors.New("artifact gateway is not configured")
	}
	_, body, failure := h.call(ctx, http.MethodGet, artifactAppsGatewayPath, nil)
	if failure != nil {
		return artifactApp{}, false, errors.New("artifact gateway unavailable")
	}
	var payload struct {
		Apps []artifactApp `json:"apps"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return artifactApp{}, false, errors.New("artifact gateway unavailable")
	}
	var result artifactApp
	found := false
	for _, app := range payload.Apps {
		if app.ID == appID {
			if found {
				return artifactApp{}, false, errors.New("ambiguous gateway app")
			}
			result, found = app, true
		}
	}
	return result, found, nil
}

// caller resolves the authenticated owner, or writes the response that refuses
// the request. Every route starts here, so no route can reach the gateway
// without an owner to scope it to.
func (h *ArtifactAppsHandler) caller(w http.ResponseWriter, r *http.Request) (int64, bool) {
	if !h.Enabled() {
		writeArtifactAppsFailure(w, &artifactAppsFailure{status: http.StatusServiceUnavailable, value: "artifact_gateway_unavailable"})
		return 0, false
	}
	uid := UIDFromContext(r.Context())
	if uid <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return 0, false
	}
	return uid, true
}

// listApps reads every registered application and keeps the caller's. A
// malformed or oversized answer is treated as a gateway failure: an empty list
// here would otherwise read as "you have no applications".
func (h *ArtifactAppsHandler) listApps(ctx context.Context, uid int64) ([]artifactApp, *artifactAppsFailure) {
	_, body, failure := h.call(ctx, http.MethodGet, artifactAppsGatewayPath, nil)
	if failure != nil {
		return nil, failure
	}
	var payload struct {
		Apps []artifactApp `json:"apps"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, &artifactAppsFailure{status: http.StatusBadGateway, value: "artifact_gateway_unavailable"}
	}
	owner := fmt.Sprint(uid)
	apps := make([]artifactApp, 0, len(payload.Apps))
	for _, app := range payload.Apps {
		if app.Agent == owner {
			apps = append(apps, app)
		}
	}
	return apps, nil
}

// ownedApp resolves one of the caller's applications. An id that belongs to
// another account and an id that does not exist are answered the same way, so
// the endpoint cannot be used to probe for ids in use.
func (h *ArtifactAppsHandler) ownedApp(ctx context.Context, uid int64, id string) (artifactApp, *artifactAppsFailure) {
	apps, failure := h.listApps(ctx, uid)
	if failure != nil {
		return artifactApp{}, failure
	}
	for _, app := range apps {
		if app.ID == id {
			return app, nil
		}
	}
	return artifactApp{}, &artifactAppsFailure{status: http.StatusNotFound, value: "artifact_app_not_found"}
}

// call performs one gateway request and translates a non-2xx answer. The 2xx
// status is handed back so a publish can keep the gateway's own distinction
// between a created and an updated application.
func (h *ArtifactAppsHandler) call(ctx context.Context, method, path string, payload []byte) (int, []byte, *artifactAppsFailure) {
	var body io.Reader
	if payload != nil {
		body = strings.NewReader(string(payload))
	}
	request, err := http.NewRequestWithContext(ctx, method, h.gatewayURL+path, body)
	if err != nil {
		return 0, nil, &artifactAppsFailure{status: http.StatusBadGateway, value: "artifact_gateway_unavailable"}
	}
	request.Header.Set("Accept", "application/json")
	if payload != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	request.Header.Set("Authorization", "Bearer "+h.token)
	request.Header.Set("User-Agent", artifactAppsUserAgent)

	response, err := h.httpClient.Do(request)
	if err != nil {
		return 0, nil, &artifactAppsFailure{status: http.StatusBadGateway, value: "artifact_gateway_unavailable"}
	}
	defer response.Body.Close()
	responseBody, readErr := readArtifactResponse(response.Body)
	if readErr != nil {
		return 0, nil, &artifactAppsFailure{status: http.StatusBadGateway, value: "artifact_gateway_unavailable"}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return response.StatusCode, responseBody, artifactAppsGatewayFailure(response.StatusCode, responseBody)
	}
	return response.StatusCode, responseBody, nil
}

// artifactAppsFailure is a refusal already translated for the caller. value is
// what goes out in the response's "error" field: one of our codes, except for a
// gateway validation error, which is relayed as the gateway worded it because
// only the caller can fix it.
type artifactAppsFailure struct {
	status int
	value  string
}

func writeArtifactAppsFailure(w http.ResponseWriter, failure *artifactAppsFailure) {
	writeJSON(w, failure.status, map[string]string{"error": failure.value})
}

// artifactAppsGatewayFailure maps a non-2xx gateway answer. A 400 is the
// caller's to fix, so its message is passed on; 401/403 means our own shared
// token is wrong, which stays a platform problem and never reads as the caller
// failing to authenticate. Everything else is the gateway being unavailable.
func artifactAppsGatewayFailure(status int, body []byte) *artifactAppsFailure {
	switch status {
	case http.StatusBadRequest:
		value := artifactAppsUpstreamErrorText(body)
		if value == "" {
			value = "artifact_request_invalid"
		}
		return &artifactAppsFailure{status: http.StatusBadRequest, value: value}
	case http.StatusNotFound:
		return &artifactAppsFailure{status: http.StatusNotFound, value: "artifact_app_not_found"}
	case http.StatusConflict:
		// The gateway refused a cross-account update. That only happens if the
		// ownership pre-check raced with another registration, and it must read
		// the same as "not yours" rather than as a gateway fault.
		return &artifactAppsFailure{status: http.StatusNotFound, value: "artifact_app_not_found"}
	case http.StatusUnauthorized, http.StatusForbidden:
		return &artifactAppsFailure{status: http.StatusBadGateway, value: "artifact_gateway_unauthorized"}
	default:
		return &artifactAppsFailure{status: http.StatusBadGateway, value: "artifact_gateway_unavailable"}
	}
}

// artifactAppsUpstreamErrorText reads the gateway's own error text without
// echoing its body: the single documented "error" field is relayed, and anything
// that is not a plain string is dropped in favour of our own code.
func artifactAppsUpstreamErrorText(body []byte) string {
	var payload struct {
		Error string `json:"error"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	text := strings.TrimSpace(payload.Error)
	if len(text) > artifactAppsMaxErrorTextLen {
		text = text[:artifactAppsMaxErrorTextLen]
	}
	return strings.NewReplacer("\r", " ", "\n", " ").Replace(text)
}

// readArtifactAppsBody applies the same bounded read as the launch endpoint, so
// an unreadable or oversized body is rejected before it reaches the gateway.
func readArtifactAppsBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(io.LimitReader(r.Body, artifactAppsMaxBody+1))
	if err != nil {
		writeArtifactAppsFailure(w, &artifactAppsFailure{status: http.StatusBadRequest, value: "artifact_request_invalid"})
		return nil, false
	}
	if len(body) > artifactAppsMaxBody {
		writeArtifactAppsFailure(w, &artifactAppsFailure{status: http.StatusRequestEntityTooLarge, value: "artifact_request_too_large"})
		return nil, false
	}
	return body, true
}

// artifactAppsItemID reads the application id out of the item route. A malformed
// id is answered as "not found" rather than "invalid", matching the existing
// artifact-ID routes: an id that cannot exist is indistinguishable from one that
// does not.
func artifactAppsItemID(path string) (string, bool) {
	rest := strings.TrimPrefix(path, artifactAppsAPIPath+"/")
	if rest == "" || strings.Contains(rest, "/") {
		return "", false
	}
	id, err := url.PathUnescape(rest)
	if err != nil || !artifactLaunchAppPattern.MatchString(id) {
		return "", false
	}
	return id, true
}
