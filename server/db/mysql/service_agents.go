package mysql

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.ServiceAgentStore = (*Adapter)(nil)

const mysqlServiceRunColumns = `run_id, session_id, state, client_token, input_json, instance_id, lease_epoch, lease_expires_at, attempts, result_json, checkpoint_ref, COALESCE(error_text, '')`

type mysqlServiceRunScanner interface {
	Scan(dest ...interface{}) error
}

func scanMySQLServiceRun(scanner mysqlServiceRunScanner) (store.ServiceRun, error) {
	var run store.ServiceRun
	var input, result []byte
	var leaseExpires sql.NullTime
	var clientToken sql.NullString
	if err := scanner.Scan(
		&run.RunID, &run.SessionID, &run.State, &clientToken, &input, &run.InstanceID,
		&run.LeaseEpoch, &leaseExpires, &run.Attempts, &result, &run.CheckpointRef, &run.ErrorText,
	); err != nil {
		return store.ServiceRun{}, err
	}
	// 幂等键未提供 = NULL（区别于空串；同会话多次无 token 调用各自成 run）。
	run.ClientToken = clientToken.String
	run.Input = append(json.RawMessage(nil), input...)
	if len(result) > 0 {
		run.Result = append(json.RawMessage(nil), result...)
	}
	if leaseExpires.Valid {
		run.LeaseExpiresAt = leaseExpires.Time
	}
	return run, nil
}

func newMySQLServiceEntityID(prefix string) (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate service id: %w", err)
	}
	return prefix + hex.EncodeToString(buf), nil
}

func (a *Adapter) GetServiceAgentBySlug(ctx context.Context, slug string) (int64, string, int64, int, error) {
	var (
		id        int64
		name      string
		version   int64
		threshold int
	)
	err := a.db.QueryRowContext(ctx, `
		SELECT a.id, a.name, v.version,
		       COALESCE(CAST(v.definition_json->>'$.async_threshold_seconds' AS UNSIGNED), 60)
		FROM service_agents a
		JOIN service_agent_versions v ON v.agent_id = a.id
		WHERE a.slug = ? AND a.state = 'active'
		ORDER BY v.version DESC
		LIMIT 1`, slug).Scan(&id, &name, &version, &threshold)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", 0, 0, store.ErrServiceAgentNotFound
	}
	if err != nil {
		return 0, "", 0, 0, fmt.Errorf("get service agent by slug: %w", err)
	}
	return id, name, version, threshold, nil
}

// GetServiceAgentAccess 取当前归属、可见性与 definition（调用准入用）。
//
// 为什么要单独一条查询：工具闭包里的 descriptor 是建立 MCP 会话时的快照，
// 用快照判定可见性会让「改成自用」对已连接的调用方失效。definition 一并返回，
// 是为了让「分享给好友」按**当前**执行 bot 求值（改派之后该算新 bot 的好友）。
func (a *Adapter) GetServiceAgentAccess(ctx context.Context, slug string) (int64, string, json.RawMessage, error) {
	var (
		ownerUID   int64
		visibility string
		definition []byte
	)
	err := a.db.QueryRowContext(ctx, `
		SELECT COALESCE(a.owner_uid, 0), COALESCE(a.visibility, ''), COALESCE(CAST(v.definition_json AS CHAR), '{}')
		FROM service_agents a
		JOIN service_agent_versions v ON v.agent_id = a.id
		WHERE a.slug = ? AND a.state = 'active'
		ORDER BY v.version DESC
		LIMIT 1`, slug).Scan(&ownerUID, &visibility, &definition)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", nil, store.ErrServiceAgentNotFound
	}
	if err != nil {
		return 0, "", nil, fmt.Errorf("get service agent access: %w", err)
	}
	return ownerUID, visibility, json.RawMessage(definition), nil
}

func (a *Adapter) CreateServiceSession(ctx context.Context, agentID, definitionRev, callerUID, callerBotUID int64, assetsHash string) (string, error) {
	sessionID, err := newMySQLServiceEntityID("sess_")
	if err != nil {
		return "", err
	}
	if _, err := a.db.ExecContext(ctx, `
		INSERT INTO service_sessions (session_id, agent_id, definition_rev, assets_hash, caller_uid, caller_bot_uid)
		VALUES (?, ?, ?, ?, ?, ?)`,
		sessionID, agentID, definitionRev, assetsHash, callerUID, callerBotUID); err != nil {
		return "", fmt.Errorf("create service session: %w", err)
	}
	return sessionID, nil
}

func (a *Adapter) GetServiceSession(ctx context.Context, sessionID string) (store.ServiceSession, error) {
	var s store.ServiceSession
	err := a.db.QueryRowContext(ctx, `
		SELECT session_id, agent_id, definition_rev, assets_hash, caller_uid, caller_bot_uid, state, created_at
		FROM service_sessions WHERE session_id = ?`, sessionID).
		Scan(&s.SessionID, &s.AgentID, &s.DefinitionRev, &s.AssetsHash, &s.CallerUID, &s.CallerBotUID, &s.State, &s.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ServiceSession{}, store.ErrServiceSessionNotFound
	}
	if err != nil {
		return store.ServiceSession{}, fmt.Errorf("get service session: %w", err)
	}
	return s, nil
}

func (a *Adapter) GetServiceRun(ctx context.Context, runID string) (store.ServiceRun, error) {
	run, err := scanMySQLServiceRun(a.db.QueryRowContext(ctx,
		`SELECT `+mysqlServiceRunColumns+` FROM service_runs WHERE run_id = ?`, runID))
	if errors.Is(err, sql.ErrNoRows) {
		return store.ServiceRun{}, store.ErrServiceRunNotFound
	}
	if err != nil {
		return store.ServiceRun{}, fmt.Errorf("get service run: %w", err)
	}
	return run, nil
}

func (a *Adapter) CreateServiceRunIdempotent(ctx context.Context, sessionID, clientToken string, input json.RawMessage) (store.ServiceRun, bool, error) {
	if len(input) == 0 {
		input = json.RawMessage(`{}`)
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return store.ServiceRun{}, false, fmt.Errorf("begin create service run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var lockedSession string
	if err := tx.QueryRowContext(ctx,
		`SELECT session_id FROM service_sessions WHERE session_id = ? FOR UPDATE`, sessionID).
		Scan(&lockedSession); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.ServiceRun{}, false, store.ErrServiceSessionNotFound
		}
		return store.ServiceRun{}, false, fmt.Errorf("lock service session: %w", err)
	}

	if clientToken != "" {
		existing, err := scanMySQLServiceRun(tx.QueryRowContext(ctx,
			`SELECT `+mysqlServiceRunColumns+` FROM service_runs WHERE session_id = ? AND client_token = ?`,
			sessionID, clientToken))
		if err == nil {
			if err := tx.Commit(); err != nil {
				return store.ServiceRun{}, false, fmt.Errorf("commit idempotent service run: %w", err)
			}
			return existing, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return store.ServiceRun{}, false, fmt.Errorf("lookup service run idempotency: %w", err)
		}
	}

	var activeRunID string
	err = tx.QueryRowContext(ctx,
		`SELECT run_id FROM service_runs WHERE session_id = ? AND state IN ('submitted','running') LIMIT 1`,
		sessionID).Scan(&activeRunID)
	if err == nil {
		return store.ServiceRun{}, false, store.ErrServiceSessionBusy
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return store.ServiceRun{}, false, fmt.Errorf("check active service run: %w", err)
	}

	runID, err := newMySQLServiceEntityID("run_")
	if err != nil {
		return store.ServiceRun{}, false, err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO service_runs (run_id, session_id, state, client_token, input_json, error_text)
		VALUES (?, ?, 'submitted', NULLIF(?, ''), ?, '')`, runID, sessionID, clientToken, []byte(input)); err != nil {
		return store.ServiceRun{}, false, fmt.Errorf("create service run: %w", err)
	}
	run, err := scanMySQLServiceRun(tx.QueryRowContext(ctx,
		`SELECT `+mysqlServiceRunColumns+` FROM service_runs WHERE run_id = ?`, runID))
	if err != nil {
		return store.ServiceRun{}, false, fmt.Errorf("load created service run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return store.ServiceRun{}, false, fmt.Errorf("commit service run: %w", err)
	}
	return run, false, nil
}

func (a *Adapter) ClaimServiceRuns(ctx context.Context, instanceID string, limit int) ([]store.ServiceRun, error) {
	return a.claimServiceRuns(ctx, instanceID, limit, nil, 0)
}

// ClaimServiceRunsScoped 只认领指定 slug 的 run（worker 声明自己服务哪些应用）。
// agentSlugs 为空 = 不限（等价 ClaimServiceRuns）。
func (a *Adapter) ClaimServiceRunsScoped(ctx context.Context, instanceID string, limit int, agentSlugs []string) ([]store.ServiceRun, error) {
	return a.claimServiceRuns(ctx, instanceID, limit, agentSlugs, 0)
}

// ClaimServiceRunsOwnedBy 只认领 ownerUID 名下应用的 run（per-user worker 凭据）。
func (a *Adapter) ClaimServiceRunsOwnedBy(ctx context.Context, instanceID string, limit int, ownerUID int64) ([]store.ServiceRun, error) {
	return a.claimServiceRuns(ctx, instanceID, limit, nil, ownerUID)
}

// ListUnclaimedServiceRuns 列出「已创建但还没被任何执行方领过」的 run（重启后重新转发用）。
func (a *Adapter) ListUnclaimedServiceRuns(ctx context.Context, olderThan time.Duration, limit int) ([]store.ServiceRun, error) {
	if limit <= 0 {
		limit = 20
	}
	if olderThan <= 0 {
		olderThan = 5 * time.Second
	}
	rows, err := a.db.QueryContext(ctx, `
		SELECT run_id, session_id, state, COALESCE(client_token, ''), input_json, instance_id, lease_epoch, attempts
		FROM service_runs
		WHERE state = 'submitted' AND attempts = 0
		  AND created_at < DATE_SUB(CURRENT_TIMESTAMP(6), INTERVAL ? SECOND)
		ORDER BY created_at
		LIMIT ?`, int(olderThan.Seconds()), limit)
	if err != nil {
		return nil, fmt.Errorf("list unclaimed service runs: %w", err)
	}
	defer rows.Close()
	var out []store.ServiceRun
	for rows.Next() {
		var run store.ServiceRun
		if err := rows.Scan(&run.RunID, &run.SessionID, &run.State, &run.ClientToken,
			&run.Input, &run.InstanceID, &run.LeaseEpoch, &run.Attempts); err != nil {
			return nil, fmt.Errorf("scan unclaimed service run: %w", err)
		}
		out = append(out, run)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate unclaimed service runs: %w", err)
	}
	return out, nil
}

func (a *Adapter) claimServiceRuns(ctx context.Context, instanceID string, limit int, agentSlugs []string, ownerUID int64) ([]store.ServiceRun, error) {
	if limit <= 0 {
		limit = 10
	}
	slugs := make([]string, 0, len(agentSlugs))
	for _, slug := range agentSlugs {
		if trimmed := strings.TrimSpace(slug); trimmed != "" {
			slugs = append(slugs, trimmed)
		}
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin claim service runs: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 与 PostgreSQL 侧同一套约束：只有 codex / worker 这类「外部执行」的 run 允许被认领。
	// bot / http / mcp 由平台自己执行，被 worker 抢走只会落到 no_command 上。
	predicate := "COALESCE(JSON_UNQUOTE(JSON_EXTRACT(v.definition_json, '$.runner.type')), 'codex') IN ('codex','worker')"
	query := `
		SELECT r.run_id
		FROM service_runs r
		JOIN service_sessions s ON s.session_id = r.session_id
		JOIN service_agents a ON a.id = s.agent_id
		LEFT JOIN service_agent_versions v ON v.agent_id = s.agent_id AND v.version = s.definition_rev
		WHERE r.state IN ('submitted','orphaned') AND ` + predicate
	condArgs := []any{}
	if ownerUID > 0 {
		// per-user 凭据：只领自己名下应用的 run（平台 internal 应用不在此列）。
		query += ` AND a.owner_kind = 'user' AND a.owner_uid = ?`
		condArgs = append(condArgs, ownerUID)
	} else if len(slugs) > 0 {
		// 按 agent slug 过滤：worker 只领自己声明的应用（FIND_IN_SET 兼容 MySQL 5.7）。
		query += ` AND FIND_IN_SET(a.slug, ?) > 0`
		condArgs = append(condArgs, strings.Join(slugs, ","))
	}
	query += `
		ORDER BY r.created_at
		LIMIT ?
		FOR UPDATE SKIP LOCKED`
	queryArgs := append(condArgs, limit)
	rows, err := tx.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return nil, fmt.Errorf("select claimable service runs: %w", err)
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan claimable service run: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("iterate claimable service runs: %w", err)
	}
	rows.Close()
	if len(ids) == 0 {
		if err := tx.Commit(); err != nil {
			return nil, fmt.Errorf("commit empty claim: %w", err)
		}
		return []store.ServiceRun{}, nil
	}

	placeholders := make([]string, len(ids))
	args := make([]interface{}, 0, len(ids)+1)
	args = append(args, instanceID)
	for i, id := range ids {
		placeholders[i] = "?"
		args = append(args, id)
	}
	inClause := strings.Join(placeholders, ",")

	if _, err := tx.ExecContext(ctx, `
		UPDATE service_runs SET
			state = 'running',
			instance_id = ?,
			lease_expires_at = DATE_ADD(NOW(6), INTERVAL 30 SECOND),
			lease_epoch = lease_epoch + 1,
			attempts = attempts + 1,
			started_at = COALESCE(started_at, NOW(6))
		WHERE run_id IN (`+inClause+`)`, args...); err != nil {
		return nil, fmt.Errorf("claim service runs: %w", err)
	}

	readArgs := make([]interface{}, 0, len(ids))
	for _, id := range ids {
		readArgs = append(readArgs, id)
	}
	updated, err := tx.QueryContext(ctx, `
		SELECT `+mysqlServiceRunColumns+` FROM service_runs
		WHERE run_id IN (`+inClause+`)
		ORDER BY created_at`, readArgs...)
	if err != nil {
		return nil, fmt.Errorf("load claimed service runs: %w", err)
	}
	claimed := make([]store.ServiceRun, 0, len(ids))
	for updated.Next() {
		run, err := scanMySQLServiceRun(updated)
		if err != nil {
			updated.Close()
			return nil, fmt.Errorf("scan claimed service run: %w", err)
		}
		claimed = append(claimed, run)
	}
	if err := updated.Err(); err != nil {
		updated.Close()
		return nil, fmt.Errorf("iterate claimed service runs: %w", err)
	}
	updated.Close()
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit claim service runs: %w", err)
	}
	return claimed, nil
}

func (a *Adapter) RenewServiceLease(ctx context.Context, runID, instanceID string) (bool, error) {
	res, err := a.db.ExecContext(ctx, `
		UPDATE service_runs SET lease_expires_at = DATE_ADD(NOW(6), INTERVAL 30 SECOND)
		WHERE run_id = ? AND state = 'running' AND instance_id = ?`, runID, instanceID)
	if err != nil {
		return false, fmt.Errorf("renew service lease: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("renew service lease affected: %w", err)
	}
	return n > 0, nil
}

func (a *Adapter) AppendServiceRunEvent(ctx context.Context, runID, typ string, payload json.RawMessage) (int64, error) {
	var payloadVal interface{}
	if len(payload) > 0 {
		payloadVal = []byte(payload)
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin append service event: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var sessionID string
	err = tx.QueryRowContext(ctx, `SELECT session_id FROM service_runs WHERE run_id = ?`, runID).Scan(&sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, store.ErrServiceRunNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("load service run for event: %w", err)
	}
	var lockedSession string
	if err := tx.QueryRowContext(ctx,
		`SELECT session_id FROM service_sessions WHERE session_id = ? FOR UPDATE`, sessionID).
		Scan(&lockedSession); err != nil {
		return 0, fmt.Errorf("lock service session for event: %w", err)
	}

	var seq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM service_run_events WHERE run_id = ?`, runID).
		Scan(&seq); err != nil {
		return 0, fmt.Errorf("next service event seq: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO service_run_events (run_id, seq, type, payload_json) VALUES (?, ?, ?, ?)`,
		runID, seq, typ, payloadVal); err != nil {
		return 0, fmt.Errorf("insert service event: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE service_sessions SET last_event_seq = ?, updated_at = NOW(6) WHERE session_id = ?`,
		seq, sessionID); err != nil {
		return 0, fmt.Errorf("bump service session event seq: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit service event: %w", err)
	}
	return seq, nil
}

func (a *Adapter) ListServiceRunEvents(ctx context.Context, runID string, afterSeq int64, limit int) ([]store.ServiceRunEventRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := a.db.QueryContext(ctx, `
		SELECT run_id, seq, type, payload_json, created_at
		FROM service_run_events
		WHERE run_id = ? AND seq > ?
		ORDER BY seq
		LIMIT ?`, runID, afterSeq, limit)
	if err != nil {
		return nil, fmt.Errorf("list service events: %w", err)
	}
	defer rows.Close()
	var events []store.ServiceRunEventRecord
	for rows.Next() {
		var rec store.ServiceRunEventRecord
		var payload []byte
		if err := rows.Scan(&rec.RunID, &rec.Seq, &rec.Type, &payload, &rec.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan service event: %w", err)
		}
		rec.Payload = append(json.RawMessage(nil), payload...)
		events = append(events, rec)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate service events: %w", err)
	}
	return events, nil
}

func (a *Adapter) SaveServiceCheckpoint(ctx context.Context, sessionID string, revision int64, snapshot json.RawMessage) error {
	if _, err := a.db.ExecContext(ctx, `
		INSERT INTO service_session_checkpoints (session_id, revision, snapshot_json)
		VALUES (?, ?, ?)
		ON DUPLICATE KEY UPDATE snapshot_json = VALUES(snapshot_json)`,
		sessionID, revision, []byte(snapshot)); err != nil {
		return fmt.Errorf("save service checkpoint: %w", err)
	}
	return nil
}

func (a *Adapter) LatestServiceCheckpoint(ctx context.Context, sessionID string) (int64, json.RawMessage, error) {
	var (
		revision int64
		snapshot []byte
	)
	err := a.db.QueryRowContext(ctx, `
		SELECT revision, snapshot_json FROM service_session_checkpoints
		WHERE session_id = ?
		ORDER BY revision DESC
		LIMIT 1`, sessionID).Scan(&revision, &snapshot)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil, nil
	}
	if err != nil {
		return 0, nil, fmt.Errorf("latest service checkpoint: %w", err)
	}
	return revision, append(json.RawMessage(nil), snapshot...), nil
}

func (a *Adapter) CompleteServiceRun(ctx context.Context, runID, resultID string, result json.RawMessage, usage json.RawMessage) (bool, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin complete service run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		state     string
		existing  string
		sessionID string
	)
	err = tx.QueryRowContext(ctx,
		`SELECT state, result_id, session_id FROM service_runs WHERE run_id = ? FOR UPDATE`, runID).
		Scan(&state, &existing, &sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, store.ErrServiceRunNotFound
	}
	if err != nil {
		return false, fmt.Errorf("lock service run for complete: %w", err)
	}
	if state == "completed" && existing == resultID {
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit completed service run replay: %w", err)
		}
		return false, nil
	}
	if state != "running" {
		return false, fmt.Errorf("complete service run state=%s: %w", state, store.ErrServiceRunConflict)
	}
	var resultVal, usageVal interface{}
	if len(result) > 0 {
		resultVal = []byte(result)
	}
	if len(usage) > 0 {
		usageVal = []byte(usage)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE service_runs SET
			state = 'completed', result_id = ?, result_json = ?, usage_json = ?,
			finished_at = NOW(6), instance_id = '', lease_expires_at = NULL
		WHERE run_id = ?`, resultID, resultVal, usageVal, runID); err != nil {
		return false, fmt.Errorf("complete service run: %w", err)
	}
	// 生命周期回注：终态与回执同事务。
	if err := insertServiceRunReceiptTx(ctx, tx, runID, sessionID, "completed", "", ""); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit complete service run: %w", err)
	}
	return true, nil
}

// AwaitServiceRunInput 把 running run 收尾为 awaiting_input（PG 镜像；设计 §3.3-8）。
func (a *Adapter) AwaitServiceRunInput(ctx context.Context, runID, resultID string, result json.RawMessage) (bool, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("begin await service run input: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		state     string
		existing  string
		sessionID string
	)
	err = tx.QueryRowContext(ctx,
		`SELECT state, result_id, session_id FROM service_runs WHERE run_id = ? FOR UPDATE`, runID).
		Scan(&state, &existing, &sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return false, store.ErrServiceRunNotFound
	}
	if err != nil {
		return false, fmt.Errorf("lock service run for await input: %w", err)
	}
	if state == "awaiting_input" && existing == resultID {
		if err := tx.Commit(); err != nil {
			return false, fmt.Errorf("commit awaiting service run replay: %w", err)
		}
		return false, nil
	}
	if state != "running" {
		return false, fmt.Errorf("await service run state=%s: %w", state, store.ErrServiceRunConflict)
	}
	var resultVal interface{}
	if len(result) > 0 {
		resultVal = []byte(result)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE service_runs SET
			state = 'awaiting_input', result_id = ?, result_json = ?,
			finished_at = NOW(6), instance_id = '', lease_expires_at = NULL
		WHERE run_id = ?`, resultID, resultVal, runID); err != nil {
		return false, fmt.Errorf("await service run input: %w", err)
	}
	if err := insertServiceRunReceiptTx(ctx, tx, runID, sessionID, "awaiting_input", store.ServiceAwaitQuestionFromResult(result), ""); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit await service run input: %w", err)
	}
	return true, nil
}

func (a *Adapter) FailServiceRun(ctx context.Context, runID, errorText string, retryable bool) error {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin fail service run: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var (
		state     string
		sessionID string
	)
	err = tx.QueryRowContext(ctx,
		`SELECT state, session_id FROM service_runs WHERE run_id = ? FOR UPDATE`, runID).
		Scan(&state, &sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrServiceRunNotFound
	}
	if err != nil {
		return fmt.Errorf("lock service run for fail: %w", err)
	}
	if state != "running" {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit failed service run noop: %w", err)
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE service_runs SET
			state = CASE WHEN ? THEN 'orphaned' ELSE 'failed' END,
			error_text = ?,
			instance_id = '',
			lease_expires_at = NULL,
			finished_at = CASE WHEN ? THEN finished_at ELSE COALESCE(finished_at, NOW(6)) END
		WHERE run_id = ?`, retryable, errorText, retryable, runID); err != nil {
		return fmt.Errorf("fail service run: %w", err)
	}
	if !retryable {
		// 不可重试失败=终态 → 回执入箱；orphaned 是中间态，不产生回执。
		if err := insertServiceRunReceiptTx(ctx, tx, runID, sessionID, "failed", "", errorText); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit fail service run: %w", err)
	}
	return nil
}

func (a *Adapter) ReapExpiredServiceRuns(ctx context.Context) (int, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("begin reap service runs: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// 租约过期的 running：可被认领的执行方（codex/worker）继续「接管」，次数耗尽转终态失败。
	//
	// bot 型 run 例外：没有人能接管它（claim 只认 codex/worker），attempts 永远不会增长，
	// 留在 orphaned 就是永久悬挂——调用方轮询一辈子也等不到结论。所以直接判终态失败，
	// 并给出明确原因；重试会开一个新的 bot 会话，可以安全重来。
	rows, err := tx.QueryContext(ctx, `
		SELECT r.run_id, r.session_id, r.attempts,
		       COALESCE(JSON_UNQUOTE(JSON_EXTRACT(v.definition_json, '$.runner.type')), 'codex')
		FROM service_runs r
		JOIN service_sessions s ON s.session_id = r.session_id
		JOIN service_agents a ON a.id = s.agent_id
		LEFT JOIN service_agent_versions v ON v.agent_id = s.agent_id AND v.version = s.definition_rev
		WHERE r.state = 'running' AND r.lease_expires_at < NOW(6)
		FOR UPDATE OF r`)
	if err != nil {
		return 0, fmt.Errorf("reap service runs query: %w", err)
	}
	type reapedRun struct {
		runID     string
		sessionID string
		state     string
		errorText string
	}
	var reaped []reapedRun
	for rows.Next() {
		var (
			r          reapedRun
			attempts   int
			runnerType string
		)
		if err := rows.Scan(&r.runID, &r.sessionID, &attempts, &runnerType); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan reaped service run: %w", err)
		}
		switch {
		case runnerType == "bot":
			r.state = "failed"
			r.errorText = "bot_runner_lost"
		case attempts >= 3:
			r.state = "failed"
			r.errorText = "lease_expired_attempts"
		default:
			r.state = "orphaned"
		}
		reaped = append(reaped, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate reaped service runs: %w", err)
	}
	rows.Close()
	if len(reaped) == 0 {
		if err := tx.Commit(); err != nil {
			return 0, fmt.Errorf("commit reap service runs: %w", err)
		}
		return 0, nil
	}
	for _, r := range reaped {
		if r.errorText != "" {
			if _, err := tx.ExecContext(ctx, `
				UPDATE service_runs SET state = ?, error_text = ?, instance_id = '' WHERE run_id = ?`,
				r.state, r.errorText, r.runID); err != nil {
				return 0, fmt.Errorf("reap service runs: %w", err)
			}
		} else if _, err := tx.ExecContext(ctx, `
			UPDATE service_runs SET state = ?, instance_id = '' WHERE run_id = ?`,
			r.state, r.runID); err != nil {
			return 0, fmt.Errorf("reap service runs: %w", err)
		}
		if r.state == "failed" {
			// 终态失败：回执入箱（orphaned 中间态不产生回执）。
			if err := insertServiceRunReceiptTx(ctx, tx, r.runID, r.sessionID, "failed", "", r.errorText); err != nil {
				return 0, err
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit reap service runs: %w", err)
	}
	return len(reaped), nil
}
