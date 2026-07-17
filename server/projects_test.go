package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type projectHandlerTestStore struct {
	store.Store
	projects       []*types.Project
	assignOwnerUID int64
	assignProject  int64
	assignTopic    string
	removedOwner   int64
	removedTopic   string
	renamedOwner   int64
	renamedProject int64
	renamedName    string
	deletedOwner   int64
	deletedProject int64
}

func (s *projectHandlerTestStore) CreateProject(ownerUID int64, name string) (*types.Project, error) {
	for _, project := range s.projects {
		if project.OwnerUID == ownerUID && project.Name == name {
			return nil, store.ErrProjectNameConflict
		}
	}
	project := &types.Project{ID: int64(len(s.projects) + 1), OwnerUID: ownerUID, Name: name, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	s.projects = append(s.projects, project)
	return project, nil
}

func (s *projectHandlerTestStore) ListProjects(ownerUID int64) ([]*types.Project, error) {
	items := make([]*types.Project, 0)
	for _, project := range s.projects {
		if project.OwnerUID == ownerUID {
			items = append(items, project)
		}
	}
	return items, nil
}

func (s *projectHandlerTestStore) RenameProject(ownerUID, projectID int64, name string) error {
	s.renamedOwner = ownerUID
	s.renamedProject = projectID
	s.renamedName = name
	return nil
}

func (s *projectHandlerTestStore) DeleteProject(ownerUID, projectID int64) error {
	s.deletedOwner = ownerUID
	s.deletedProject = projectID
	return nil
}

func (s *projectHandlerTestStore) AssignTopicToProject(ownerUID, projectID int64, topicID string) error {
	s.assignOwnerUID = ownerUID
	s.assignProject = projectID
	s.assignTopic = topicID
	return nil
}

func (s *projectHandlerTestStore) RemoveTopicFromProject(ownerUID int64, topicID string) error {
	s.removedOwner = ownerUID
	s.removedTopic = topicID
	return nil
}

func (s *projectHandlerTestStore) ListProjectTopics(ownerUID int64) ([]*types.ProjectTopic, error) {
	return nil, nil
}

func projectRequest(method, target, body string, uid int64) *http.Request {
	req := httptest.NewRequest(method, target, bytes.NewBufferString(body))
	return req.WithContext(context.WithValue(req.Context(), uidKey, uid))
}

func TestProjectHandlerCreatesAndListsOwnerProjects(t *testing.T) {
	db := &projectHandlerTestStore{}
	handler := NewProjectHandler(db)

	createRec := httptest.NewRecorder()
	handler.HandleProjects(createRec, projectRequest(http.MethodPost, "/api/projects", `{"name":"  Website Launch  "}`, 7))
	if createRec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", createRec.Code, createRec.Body.String())
	}
	if len(db.projects) != 1 || db.projects[0].OwnerUID != 7 || db.projects[0].Name != "Website Launch" {
		t.Fatalf("created project=%+v", db.projects)
	}

	listRec := httptest.NewRecorder()
	handler.HandleProjects(listRec, projectRequest(http.MethodGet, "/api/projects", "", 7))
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status=%d body=%s", listRec.Code, listRec.Body.String())
	}
	var response struct {
		Projects []*types.Project `json:"projects"`
	}
	if err := json.Unmarshal(listRec.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode projects: %v", err)
	}
	if len(response.Projects) != 1 || response.Projects[0].Name != "Website Launch" {
		t.Fatalf("listed projects=%+v", response.Projects)
	}
}

func TestProjectHandlerAssignsAndRemovesTopicForAuthenticatedOwner(t *testing.T) {
	db := &projectHandlerTestStore{}
	handler := NewProjectHandler(db)

	assignRec := httptest.NewRecorder()
	handler.HandleProjectTopic(assignRec, projectRequest(http.MethodPost, "/api/projects/topic", `{"project_id":12,"topic_id":"p2p_7_42"}`, 7))
	if assignRec.Code != http.StatusOK {
		t.Fatalf("assign status=%d body=%s", assignRec.Code, assignRec.Body.String())
	}
	if db.assignOwnerUID != 7 || db.assignProject != 12 || db.assignTopic != "p2p_7_42" {
		t.Fatalf("assignment owner=%d project=%d topic=%q", db.assignOwnerUID, db.assignProject, db.assignTopic)
	}

	removeRec := httptest.NewRecorder()
	handler.HandleProjectTopic(removeRec, projectRequest(http.MethodDelete, "/api/projects/topic?topic_id=p2p_7_42", "", 7))
	if removeRec.Code != http.StatusOK {
		t.Fatalf("remove status=%d body=%s", removeRec.Code, removeRec.Body.String())
	}
	if db.removedOwner != 7 || db.removedTopic != "p2p_7_42" {
		t.Fatalf("removed owner=%d topic=%q", db.removedOwner, db.removedTopic)
	}
}

func TestProjectHandlerRejectsDuplicateName(t *testing.T) {
	db := &projectHandlerTestStore{projects: []*types.Project{{ID: 1, OwnerUID: 7, Name: "Website Launch"}}}
	handler := NewProjectHandler(db)
	rec := httptest.NewRecorder()

	handler.HandleProjects(rec, projectRequest(http.MethodPost, "/api/projects", `{"name":"Website Launch"}`, 7))
	if rec.Code != http.StatusConflict {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestProjectHandlerRenamesAndDeletesOwnerProject(t *testing.T) {
	db := &projectHandlerTestStore{}
	handler := NewProjectHandler(db)

	renameRec := httptest.NewRecorder()
	handler.HandleProjects(renameRec, projectRequest(http.MethodPatch, "/api/projects", `{"project_id":12,"name":"  Updated Website  "}`, 7))
	if renameRec.Code != http.StatusOK {
		t.Fatalf("rename status=%d body=%s", renameRec.Code, renameRec.Body.String())
	}
	if db.renamedOwner != 7 || db.renamedProject != 12 || db.renamedName != "Updated Website" {
		t.Fatalf("rename owner=%d project=%d name=%q", db.renamedOwner, db.renamedProject, db.renamedName)
	}

	deleteRec := httptest.NewRecorder()
	handler.HandleProjects(deleteRec, projectRequest(http.MethodDelete, "/api/projects?project_id=12", "", 7))
	if deleteRec.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleteRec.Code, deleteRec.Body.String())
	}
	if db.deletedOwner != 7 || db.deletedProject != 12 {
		t.Fatalf("delete owner=%d project=%d", db.deletedOwner, db.deletedProject)
	}
}
