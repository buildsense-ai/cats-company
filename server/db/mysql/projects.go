package mysql

import (
	"database/sql"
	"errors"
	"fmt"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// CreateProject creates one owner-scoped project.
func (a *Adapter) CreateProject(ownerUID int64, name string) (*types.Project, error) {
	result, err := a.db.Exec(`INSERT INTO projects (owner_uid, name) VALUES (?, ?)`, ownerUID, name)
	if err != nil {
		var mysqlErr *mysqldriver.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1062 {
			return nil, store.ErrProjectNameConflict
		}
		return nil, fmt.Errorf("create project: %w", err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		return nil, fmt.Errorf("read project id: %w", err)
	}

	project := &types.Project{}
	err = a.db.QueryRow(
		`SELECT id, owner_uid, name, created_at, updated_at FROM projects WHERE id = ? AND owner_uid = ?`,
		id, ownerUID,
	).Scan(&project.ID, &project.OwnerUID, &project.Name, &project.CreatedAt, &project.UpdatedAt)
	if err != nil {
		return nil, fmt.Errorf("load created project: %w", err)
	}
	return project, nil
}

// ListProjects lists projects with their current task counts.
func (a *Adapter) ListProjects(ownerUID int64) ([]*types.Project, error) {
	rows, err := a.db.Query(
		`SELECT p.id, p.owner_uid, p.name, COUNT(pt.topic_id), p.created_at, p.updated_at
		 FROM projects p
		 LEFT JOIN project_topics pt ON pt.project_id = p.id AND pt.owner_uid = p.owner_uid
		 WHERE p.owner_uid = ?
		 GROUP BY p.id, p.owner_uid, p.name, p.created_at, p.updated_at
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
		 SELECT ?, t.id, p.id
		 FROM projects p
		 JOIN topics t ON t.id = ? AND t.owner_id = ?
		 WHERE p.id = ? AND p.owner_uid = ?
		 ON DUPLICATE KEY UPDATE project_id = VALUES(project_id), created_at = CURRENT_TIMESTAMP`,
		ownerUID, topicID, ownerUID, projectID, ownerUID,
	)
	if err != nil {
		return fmt.Errorf("assign topic to project: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read project assignment result: %w", err)
	}
	if affected == 0 {
		var exists int
		err := a.db.QueryRow(
			`SELECT 1 FROM projects p JOIN topics t ON t.id = ? AND t.owner_id = ? WHERE p.id = ? AND p.owner_uid = ?`,
			topicID, ownerUID, projectID, ownerUID,
		).Scan(&exists)
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrProjectTopicNotFound
		}
		if err != nil {
			return fmt.Errorf("verify project assignment: %w", err)
		}
	}
	if _, err := a.db.Exec(`UPDATE projects SET updated_at = CURRENT_TIMESTAMP WHERE id = ? AND owner_uid = ?`, projectID, ownerUID); err != nil {
		return fmt.Errorf("touch assigned project: %w", err)
	}
	return nil
}

// RemoveTopicFromProject removes an assignment without deleting the topic.
func (a *Adapter) RemoveTopicFromProject(ownerUID int64, topicID string) error {
	_, err := a.db.Exec(`DELETE FROM project_topics WHERE owner_uid = ? AND topic_id = ?`, ownerUID, topicID)
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
		 WHERE pt.owner_uid = ?
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
