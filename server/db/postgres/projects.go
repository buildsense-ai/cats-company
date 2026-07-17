package postgres

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// CreateProject creates one owner-scoped project.
func (a *Adapter) CreateProject(ownerUID int64, name string) (*types.Project, error) {
	project := &types.Project{}
	err := a.db.QueryRow(
		`INSERT INTO projects (owner_uid, name)
		 VALUES ($1, $2)
		 ON CONFLICT (owner_uid, name) DO NOTHING
		 RETURNING id, owner_uid, name, created_at, updated_at`,
		ownerUID, name,
	).Scan(&project.ID, &project.OwnerUID, &project.Name, &project.CreatedAt, &project.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrProjectNameConflict
	}
	if err != nil {
		return nil, fmt.Errorf("create project: %w", err)
	}
	return project, nil
}

// ListProjects lists projects with their current task counts.
func (a *Adapter) ListProjects(ownerUID int64) ([]*types.Project, error) {
	rows, err := a.db.Query(
		`SELECT p.id, p.owner_uid, p.name, COUNT(pt.topic_id), p.created_at, p.updated_at
		 FROM projects p
		 LEFT JOIN project_topics pt ON pt.project_id = p.id AND pt.owner_uid = p.owner_uid
		 WHERE p.owner_uid = $1
		 GROUP BY p.id
		 ORDER BY p.updated_at DESC, p.id DESC`,
		ownerUID,
	)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	projects := make([]*types.Project, 0)
	for rows.Next() {
		project := &types.Project{}
		if err := rows.Scan(&project.ID, &project.OwnerUID, &project.Name, &project.TaskCount, &project.CreatedAt, &project.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan project: %w", err)
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate projects: %w", err)
	}
	return projects, nil
}

// AssignTopicToProject moves an owned topic into the selected project.
func (a *Adapter) AssignTopicToProject(ownerUID, projectID int64, topicID string) error {
	result, err := a.db.Exec(
		`INSERT INTO project_topics (owner_uid, topic_id, project_id)
		 SELECT $1, t.id, p.id
		 FROM projects p
		 JOIN topics t ON t.id = $3 AND t.owner_id = $1
		 WHERE p.id = $2 AND p.owner_uid = $1
		 ON CONFLICT (owner_uid, topic_id)
		 DO UPDATE SET project_id = EXCLUDED.project_id, created_at = CURRENT_TIMESTAMP`,
		ownerUID, projectID, topicID,
	)
	if err != nil {
		return fmt.Errorf("assign topic to project: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read project assignment result: %w", err)
	}
	if affected == 0 {
		return store.ErrProjectTopicNotFound
	}
	if _, err := a.db.Exec(`UPDATE projects SET updated_at = CURRENT_TIMESTAMP WHERE id = $1 AND owner_uid = $2`, projectID, ownerUID); err != nil {
		return fmt.Errorf("touch assigned project: %w", err)
	}
	return nil
}

// RemoveTopicFromProject removes an assignment without deleting the topic.
func (a *Adapter) RemoveTopicFromProject(ownerUID int64, topicID string) error {
	_, err := a.db.Exec(`DELETE FROM project_topics WHERE owner_uid = $1 AND topic_id = $2`, ownerUID, topicID)
	if err != nil {
		return fmt.Errorf("remove topic from project: %w", err)
	}
	return nil
}

// ListProjectTopics lists all owner-scoped topic assignments.
func (a *Adapter) ListProjectTopics(ownerUID int64) ([]*types.ProjectTopic, error) {
	rows, err := a.db.Query(
		`SELECT pt.project_id, p.name, pt.topic_id, pt.created_at
		 FROM project_topics pt
		 JOIN projects p ON p.id = pt.project_id AND p.owner_uid = pt.owner_uid
		 WHERE pt.owner_uid = $1
		 ORDER BY pt.created_at DESC`,
		ownerUID,
	)
	if err != nil {
		return nil, fmt.Errorf("list project topics: %w", err)
	}
	defer rows.Close()

	assignments := make([]*types.ProjectTopic, 0)
	for rows.Next() {
		assignment := &types.ProjectTopic{}
		if err := rows.Scan(&assignment.ProjectID, &assignment.ProjectName, &assignment.TopicID, &assignment.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan project topic: %w", err)
		}
		assignments = append(assignments, assignment)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project topics: %w", err)
	}
	return assignments, nil
}
