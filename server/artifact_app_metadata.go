package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func appMetadataFailure() *artifactAppsFailure {
	return &artifactAppsFailure{status: http.StatusServiceUnavailable, value: "artifact_management_unavailable"}
}

func (h *ArtifactAppsHandler) findApp(ctx context.Context, id string) (artifactApp, bool, *artifactAppsFailure) {
	_, body, failure := h.call(ctx, http.MethodGet, artifactAppsGatewayPath, nil)
	if failure != nil {
		return artifactApp{}, false, failure
	}
	var result struct {
		Apps []artifactApp `json:"apps"`
	}
	if err := json.Unmarshal(body, &result); err != nil {
		return artifactApp{}, false, &artifactAppsFailure{status: http.StatusBadGateway, value: "artifact_gateway_unavailable"}
	}
	for _, app := range result.Apps {
		if app.ID == id {
			return app, true, nil
		}
	}
	return artifactApp{}, false, nil
}

// A bot supplies the original request message, never an arbitrary creator UID.
// Legacy applications keep unknown provenance rather than being claimed on edit.
func (h *ArtifactAppsHandler) prepareMetadata(ctx context.Context, uid int64, request artifactAppRequest, existing artifactApp, found bool) *artifactAppsFailure {
	metadata, ok := h.db.(store.ArtifactAppMetadataStore)
	if !ok {
		return nil
	}
	items, err := metadata.ListArtifactAppMetadata(ctx, uid)
	if err != nil {
		return appMetadataFailure()
	}
	id := strings.TrimSpace(request.ID)
	if _, exists := items[id]; exists {
		return nil
	}
	creator := int64(0)
	title := strings.TrimSpace(request.Title)
	if found {
		title = existing.Title
	} else {
		user, err := h.db.GetUser(uid)
		if err != nil || user == nil {
			return appMetadataFailure()
		}
		creator = uid
		if user.AccountType == types.AccountBot {
			invalid := &artifactAppsFailure{status: http.StatusBadRequest, value: "artifact_source_message_required"}
			if request.SourceMessageID <= 0 || request.SourceTopicID == "" {
				return invalid
			}
			hub := &Hub{db: h.db}
			if code, _ := hub.validateTopicReadAccess(uid, user.AccountType, request.SourceTopicID); code != 0 {
				return invalid
			}
			messages, err := h.db.GetMessagesSince(request.SourceTopicID, request.SourceMessageID-1, 1)
			if err != nil {
				return appMetadataFailure()
			}
			if len(messages) != 1 || messages[0].ID != request.SourceMessageID || messages[0].TopicID != request.SourceTopicID {
				return invalid
			}
			creator = messages[0].FromUID
			human, err := h.db.GetUser(creator)
			if err != nil {
				return appMetadataFailure()
			}
			if human == nil || (human.AccountType != types.AccountHuman && human.AccountType != "") {
				return invalid
			}
		}
	}
	// Reserve once before publishing so retries cannot change the initiator, even
	// if publishing succeeds but its response is lost. Content retries preserve it.
	if err := metadata.EnsureArtifactAppMetadata(ctx, store.ArtifactAppMetadata{AppID: id, AgentUID: uid, CreatorUID: creator, Title: title, Description: existing.Description, IconURL: existing.IconURL}); err != nil {
		return appMetadataFailure()
	}
	return nil
}

func (h *ArtifactAppsHandler) decorateApps(ctx context.Context, caller, agentUID int64, apps []artifactApp) *artifactAppsFailure {
	// Permission and provenance are platform facts, never trusted gateway fields.
	for i := range apps {
		apps[i].CanManage = false
		apps[i].CreatorUID = 0
	}
	metadata, ok := h.db.(store.ArtifactAppMetadataStore)
	if !ok {
		return nil
	}
	items, err := metadata.ListArtifactAppMetadata(ctx, agentUID)
	if err != nil {
		return appMetadataFailure()
	}
	agent, err := h.db.GetUser(agentUID)
	if err != nil || agent == nil {
		return appMetadataFailure()
	}
	owner := int64(0)
	if agent.AccountType == types.AccountBot {
		owner, err = h.db.GetBotOwner(agentUID)
		if err != nil {
			return appMetadataFailure()
		}
	}
	for i := range apps {
		if item, exists := items[apps[i].ID]; exists {
			apps[i].Title, apps[i].Description, apps[i].IconURL = item.Title, item.Description, item.IconURL
			apps[i].CreatorUID = item.CreatorUID
		}
		apps[i].CanManage = caller != agentUID && (owner == caller || apps[i].CreatorUID == caller)
		// Direct human publications are owned by the authenticated human.
		if caller == agentUID && apps[i].CreatorUID == caller {
			apps[i].CanManage = true
		}
	}
	return nil
}

func (h *ArtifactAppsHandler) handleMetadataUpdate(w http.ResponseWriter, r *http.Request) {
	uid, ok := h.caller(w, r)
	if !ok {
		return
	}
	metadata, available := h.db.(store.ArtifactAppMetadataStore)
	if !available {
		writeArtifactAppsFailure(w, appMetadataFailure())
		return
	}
	id, valid := artifactAppsItemID(r.URL.Path)
	if !valid {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "artifact_app_not_found"})
		return
	}
	app, found, failure := h.findApp(r.Context(), id)
	if failure != nil {
		writeArtifactAppsFailure(w, failure)
		return
	}
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "artifact_app_not_found"})
		return
	}
	agentUID, err := strconv.ParseInt(app.Agent, 10, 64)
	if err != nil || agentUID <= 0 {
		writeArtifactAppsFailure(w, appMetadataFailure())
		return
	}
	apps := []artifactApp{app}
	if failure := h.decorateApps(r.Context(), uid, agentUID, apps); failure != nil {
		writeArtifactAppsFailure(w, failure)
		return
	}
	if !apps[0].CanManage {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "artifact_app_forbidden"})
		return
	}
	body, ok := readArtifactAppsBody(w, r)
	if !ok {
		return
	}
	// Full presentation replacement, with no tunnel or ownership fields accepted.
	var input struct {
		Title       string `json:"title"`
		Description string `json:"description"`
		IconURL     string `json:"icon_url"`
	}
	if json.Unmarshal(body, &input) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "artifact_request_invalid"})
		return
	}
	input.Title, input.Description, input.IconURL = strings.TrimSpace(input.Title), strings.TrimSpace(input.Description), strings.TrimSpace(input.IconURL)
	if input.Title == "" || utf8.RuneCountInString(input.Title) > 128 || strings.ContainsAny(input.Title, "\r\n\x00") || utf8.RuneCountInString(input.Description) > 1000 || strings.ContainsRune(input.Description, '\x00') || !validAppIconURL(input.IconURL) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "artifact_app_metadata_invalid"})
		return
	}
	item := store.ArtifactAppMetadata{AppID: id, AgentUID: agentUID, CreatorUID: apps[0].CreatorUID, Title: input.Title, Description: input.Description, IconURL: input.IconURL}
	if err := metadata.EnsureArtifactAppMetadata(r.Context(), item); err != nil {
		writeArtifactAppsFailure(w, appMetadataFailure())
		return
	}
	if err := metadata.UpdateArtifactAppMetadata(r.Context(), item); err != nil {
		writeArtifactAppsFailure(w, appMetadataFailure())
		return
	}
	app = apps[0]
	app.Title, app.Description, app.IconURL = input.Title, input.Description, input.IconURL
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, app)
}

func validAppIconURL(value string) bool {
	if value == "" {
		return true
	}
	if len(value) > 2048 || strings.ContainsAny(value, "\r\n\x00\\") {
		return false
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.User != nil {
		return false
	}
	return (parsed.Scheme == "https" && parsed.Host != "") || (parsed.Scheme == "" && parsed.Host == "" && strings.HasPrefix(path.Clean(parsed.Path), "/uploads/"))
}
