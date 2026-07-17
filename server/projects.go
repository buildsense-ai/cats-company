package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store"
)

const maxProjectNameLength = 128

// ProjectHandler manages projects and their existing conversation topics.
type ProjectHandler struct {
	db store.Store
}

// NewProjectHandler creates a project handler over an optional project store.
func NewProjectHandler(db store.Store) *ProjectHandler {
	return &ProjectHandler{db: db}
}

func (h *ProjectHandler) projectStore(w http.ResponseWriter) (store.ProjectStore, bool) {
	projects, ok := h.db.(store.ProjectStore)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "project storage unavailable"})
	}
	return projects, ok
}

// HandleProjects handles GET/POST/PATCH/DELETE /api/projects.
func (h *ProjectHandler) HandleProjects(w http.ResponseWriter, r *http.Request) {
	projects, ok := h.projectStore(w)
	if !ok {
		return
	}

	switch r.Method {
	case http.MethodGet:
		h.handleList(w, r, projects)
	case http.MethodPost:
		h.handleCreate(w, r, projects)
	case http.MethodPatch:
		h.handleRename(w, r, projects)
	case http.MethodDelete:
		h.handleDelete(w, r, projects)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (h *ProjectHandler) handleRename(w http.ResponseWriter, r *http.Request, projects store.ProjectStore) {
	var req struct {
		ProjectID int64  `json:"project_id"`
		Name      string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	name := strings.TrimSpace(req.Name)
	if req.ProjectID <= 0 || name == "" || utf8.RuneCountInString(name) > maxProjectNameLength {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project_id and a 1-128 character name are required"})
		return
	}
	if err := projects.RenameProject(UIDFromContext(r.Context()), req.ProjectID, name); err != nil {
		switch {
		case errors.Is(err, store.ErrProjectNameConflict):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "project name already exists"})
		case errors.Is(err, store.ErrProjectNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to rename project"})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *ProjectHandler) handleDelete(w http.ResponseWriter, r *http.Request, projects store.ProjectStore) {
	projectID, err := strconv.ParseInt(strings.TrimSpace(r.URL.Query().Get("project_id")), 10, 64)
	if err != nil || projectID <= 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project_id is required"})
		return
	}
	if err := projects.DeleteProject(UIDFromContext(r.Context()), projectID); err != nil {
		if errors.Is(err, store.ErrProjectNotFound) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "project not found"})
		} else {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to delete project"})
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// HandleProjectTopic handles POST/DELETE /api/projects/topic.
func (h *ProjectHandler) HandleProjectTopic(w http.ResponseWriter, r *http.Request) {
	projects, ok := h.projectStore(w)
	if !ok {
		return
	}

	switch r.Method {
	case http.MethodPost:
		h.handleAssignTopic(w, r, projects)
	case http.MethodDelete:
		h.handleRemoveTopic(w, r, projects)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (h *ProjectHandler) handleList(w http.ResponseWriter, r *http.Request, projects store.ProjectStore) {
	items, err := projects.ListProjects(UIDFromContext(r.Context()))
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to list projects"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"projects": items})
}

func (h *ProjectHandler) handleCreate(w http.ResponseWriter, r *http.Request, projects store.ProjectStore) {
	var req struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" || utf8.RuneCountInString(name) > maxProjectNameLength {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project name must be 1-128 characters"})
		return
	}

	project, err := projects.CreateProject(UIDFromContext(r.Context()), name)
	if errors.Is(err, store.ErrProjectNameConflict) {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "project name already exists"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to create project"})
		return
	}
	writeJSON(w, http.StatusCreated, map[string]interface{}{"project": project})
}

func (h *ProjectHandler) handleAssignTopic(w http.ResponseWriter, r *http.Request, projects store.ProjectStore) {
	var req struct {
		ProjectID int64  `json:"project_id"`
		TopicID   string `json:"topic_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	req.TopicID = strings.TrimSpace(req.TopicID)
	if req.ProjectID <= 0 || req.TopicID == "" || len(req.TopicID) > 64 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "project_id and topic_id are required"})
		return
	}

	err := projects.AssignTopicToProject(UIDFromContext(r.Context()), req.ProjectID, req.TopicID)
	if errors.Is(err, store.ErrProjectTopicNotFound) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "project or conversation not found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to assign conversation"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *ProjectHandler) handleRemoveTopic(w http.ResponseWriter, r *http.Request, projects store.ProjectStore) {
	topicID := strings.TrimSpace(r.URL.Query().Get("topic_id"))
	if topicID == "" || len(topicID) > 64 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "topic_id is required"})
		return
	}
	if err := projects.RemoveTopicFromProject(UIDFromContext(r.Context()), topicID); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to remove conversation"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
