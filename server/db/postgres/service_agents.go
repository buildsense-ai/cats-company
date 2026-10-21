package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/openchat/openchat/server/store"
)

var _ store.ServiceAgentStore = (*Adapter)(nil)

const postgresServiceRunColumns = `run_id, session_id, state, client_token, input_json, instance_id, lease_epoch, lease_expires_at, attempts, result_json, checkpoint_ref`

type postgresServiceRunScanner interface {
	Scan(dest ...interface{}) error
}

func scanPostgresServiceRun(scanner postgresServiceRunScanner) (store.ServiceRun, error) {
	var run store.ServiceRun
	var input, result []byte
	var leaseExpires sql.NullTime
	var clientToken sql.NullString
	if err := scanner.Scan(
		&run.RunID, &run.SessionID, &run.State, &clientToken, &input, &run.InstanceID,
		&run.LeaseEpoch, &leaseExpires, &run.Attempts, &result, &run.CheckpointRef,
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

func newServiceEntityID(prefix string) (string, error) {
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
		       COALESCE((v.definition_json->>'async_threshold_seconds')::int, 60)
		FROM service_agents a
		JOIN service_agent_versions v ON v.agent_id = a.id
		WHERE a.slug = $1 AND a.state = 'active'
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

func (a *Adapter) CreateServiceSession(ctx context.Context, agentID, definitionRev, callerUID, callerBotUID int64, assetsHash string) (string, error) {
	sessionID, err := newServiceEntityID("sess_")
	if err != nil {
		return "", err
	}
	if _, err := a.db.ExecContext(ctx, `
		INSERT INTO service_sessions (session_id, agent_id, definition_rev, assets_hash, caller_uid, caller_bot_uid)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		sessionID, agentID, definitionRev, assetsHash, callerUID, callerBotUID); err != nil {
		return "", fmt.Errorf("create service session: %w", err)
	}
	return sessionID, nil
}

func (a *Adapter) GetServiceSession(ctx context.Context, sessionID string) (store.ServiceSession, error) {
	var s store.ServiceSession
	err := a.db.QueryRowContext(ctx, `
		SELECT session_id, agent_id, definition_rev, assets_hash, caller_uid, caller_bot_uid, state, created_at
		FROM service_sessions WHERE session_id = $1`, sessionID).
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
	run, err := scanPostgresServiceRun(a.db.QueryRowContext(ctx,
		`SELECT `+postgresServiceRunColumns+` FROM service_runs WHERE run_id = $1`, runID))
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

	// ① 锁 session 行（同会话保串行的序列化点）
	var lockedSession string
	if err := tx.QueryRowContext(ctx,
		`SELECT session_id FROM service_sessions WHERE session_id = $1 FOR UPDATE`, sessionID).
		Scan(&lockedSession); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.ServiceRun{}, false, store.ErrServiceSessionNotFound
		}
		return store.ServiceRun{}, false, fmt.Errorf("lock service session: %w", err)
	}

	// ② client_token 命中 → 返回既有 run（幂等）
	if clientToken != "" {
		existing, err := scanPostgresServiceRun(tx.QueryRowContext(ctx,
			`SELECT `+postgresServiceRunColumns+` FROM service_runs WHERE session_id = $1 AND client_token = $2`,
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

	// ③ 存在活跃 run → busy
	var activeRunID string
	err = tx.QueryRowContext(ctx,
		`SELECT run_id FROM service_runs WHERE session_id = $1 AND state IN ('submitted','running') LIMIT 1`,
		sessionID).Scan(&activeRunID)
	if err == nil {
		return store.ServiceRun{}, false, store.ErrServiceSessionBusy
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return store.ServiceRun{}, false, fmt.Errorf("check active service run: %w", err)
	}

	// ④ 插入新 run
	runID, err := newServiceEntityID("run_")
	if err != nil {
		return store.ServiceRun{}, false, err
	}
	run, err := scanPostgresServiceRun(tx.QueryRowContext(ctx, `
		INSERT INTO service_runs (run_id, session_id, state, client_token, input_json)
		VALUES ($1, $2, 'submitted', NULLIF($3, ''), $4)
		RETURNING `+postgresServiceRunColumns, runID, sessionID, clientToken, []byte(input)))
	if err != nil {
		return store.ServiceRun{}, false, fmt.Errorf("create service run: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return store.ServiceRun{}, false, fmt.Errorf("commit service run: %w", err)
	}
	return run, false, nil
}

func (a *Adapter) ClaimServiceRuns(ctx context.Context, instanceID string, limit int) ([]store.ServiceRun, error) {
	return a.claimServiceRuns(ctx, instanceID, limit, nil)
}

// ClaimServiceRunsScoped 只认领指定 slug 的 run（worker 声明自己服务哪些应用）。
// agentSlugs 为空 = 不限（等价 ClaimServiceRuns）。
func (a *Adapter) ClaimServiceRunsScoped(ctx context.Context, instanceID string, limit int, agentSlugs []string) ([]store.ServiceRun, error) {
	return a.claimServiceRuns(ctx, instanceID, limit, agentSlugs)
}

func (a *Adapter) claimServiceRuns(ctx context.Context, instanceID string, limit int, agentSlugs []string) ([]store.ServiceRun, error) {
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

	query := `
		SELECT r.run_id FROM service_runs r
		WHERE r.state IN ('submitted','orphaned')
		ORDER BY r.created_at
		FOR UPDATE SKIP LOCKED
		LIMIT $1`
	args := []any{limit}
	if len(slugs) > 0 {
		// 按 agent slug 过滤：join 会话 → 应用；worker 只领自己声明的应用。
		query = `
		SELECT r.run_id
		FROM service_runs r
		JOIN service_sessions s ON s.session_id = r.session_id
		JOIN service_agents a ON a.id = s.agent_id
		WHERE r.state IN ('submitted','orphaned') AND a.slug = ANY($2)
		ORDER BY r.created_at
		FOR UPDATE SKIP LOCKED
		LIMIT $1`
		args = append(args, slugs)
	}
	rows, err := tx.QueryContext(ctx, query, args...)
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
	updateArgs := make([]interface{}, 0, len(ids)+1)
	updateArgs = append(updateArgs, instanceID)
	for i, id := range ids {
		placeholders[i] = fmt.Sprintf("$%d", i+2)
		updateArgs = append(updateArgs, id)
	}
	updated, err := tx.QueryContext(ctx, `
		UPDATE service_runs SET
			state = 'running',
			instance_id = $1,
			lease_expires_at = now() + interval '30 seconds',
			lease_epoch = lease_epoch + 1,
			attempts = attempts + 1,
			started_at = COALESCE(started_at, now())
		WHERE run_id IN (`+strings.Join(placeholders, ",")+`)
		RETURNING `+postgresServiceRunColumns, updateArgs...)
	if err != nil {
		return nil, fmt.Errorf("claim service runs: %w", err)
	}
	claimed := make([]store.ServiceRun, 0, len(ids))
	for updated.Next() {
		run, err := scanPostgresServiceRun(updated)
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
		UPDATE service_runs SET lease_expires_at = now() + interval '30 seconds'
		WHERE run_id = $1 AND state = 'running' AND instance_id = $2`, runID, instanceID)
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
	err = tx.QueryRowContext(ctx, `
		SELECT s.session_id
		FROM service_sessions s
		JOIN service_runs r ON r.session_id = s.session_id
		WHERE r.run_id = $1
		FOR UPDATE OF s`, runID).Scan(&sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, store.ErrServiceRunNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("lock service session for event: %w", err)
	}

	var seq int64
	if err := tx.QueryRowContext(ctx,
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM service_run_events WHERE run_id = $1`, runID).
		Scan(&seq); err != nil {
		return 0, fmt.Errorf("next service event seq: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO service_run_events (run_id, seq, type, payload_json) VALUES ($1, $2, $3, $4)`,
		runID, seq, typ, payloadVal); err != nil {
		return 0, fmt.Errorf("insert service event: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE service_sessions SET last_event_seq = $1, updated_at = now() WHERE session_id = $2`,
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
		WHERE run_id = $1 AND seq > $2
		ORDER BY seq
		LIMIT $3`, runID, afterSeq, limit)
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
		VALUES ($1, $2, $3)
		ON CONFLICT (session_id, revision) DO UPDATE SET snapshot_json = EXCLUDED.snapshot_json`,
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
		WHERE session_id = $1
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
		`SELECT state, result_id, session_id FROM service_runs WHERE run_id = $1 FOR UPDATE`, runID).
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
		return false, nil // 幂等重放
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
			state = 'completed', result_id = $2, result_json = $3, usage_json = $4,
			finished_at = now(), instance_id = '', lease_expires_at = NULL
		WHERE run_id = $1`, runID, resultID, resultVal, usageVal); err != nil {
		return false, fmt.Errorf("complete service run: %w", err)
	}
	// 生命周期回注：终态与回执同事务（至少一次投递的持久化起点）。
	if err := insertServiceRunReceiptTx(ctx, tx, runID, sessionID, "completed", "", ""); err != nil {
		return false, err
	}
	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("commit complete service run: %w", err)
	}
	return true, nil
}

// AwaitServiceRunInput 把 running run 收尾为 awaiting_input（回合制返问；设计 §3.3-8）。
// 幂等/错态语义与 CompleteServiceRun 一致；question 从 result envelope 的 output.question 提取。
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
		`SELECT state, result_id, session_id FROM service_runs WHERE run_id = $1 FOR UPDATE`, runID).
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
		return false, nil // 幂等重放
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
			state = 'awaiting_input', result_id = $2, result_json = $3,
			finished_at = now(), instance_id = '', lease_expires_at = NULL
		WHERE run_id = $1`, runID, resultID, resultVal); err != nil {
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
		`SELECT state, session_id FROM service_runs WHERE run_id = $1 FOR UPDATE`, runID).
		Scan(&state, &sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrServiceRunNotFound
	}
	if err != nil {
		return fmt.Errorf("lock service run for fail: %w", err)
	}
	if state != "running" {
		// 宽容处理（幂等）：已终态/已接管 → 无操作。
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit failed service run noop: %w", err)
		}
		return nil
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE service_runs SET
			state = CASE WHEN $3 THEN 'orphaned' ELSE 'failed' END,
			error_text = $2,
			instance_id = '',
			lease_expires_at = NULL,
			finished_at = CASE WHEN $3 THEN finished_at ELSE COALESCE(finished_at, now()) END
		WHERE run_id = $1`, runID, errorText, retryable); err != nil {
		return fmt.Errorf("fail service run: %w", err)
	}
	if !retryable {
		// 不可重试失败=终态 → 回执入箱；orphaned 是中间态（等待接管重跑），不产生回执。
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

	rows, err := tx.QueryContext(ctx, `
		UPDATE service_runs SET
			state = CASE WHEN attempts >= 3 THEN 'failed' ELSE 'orphaned' END,
			error_text = CASE WHEN attempts >= 3 THEN 'lease_expired_attempts' ELSE error_text END,
			instance_id = ''
		WHERE state = 'running' AND lease_expires_at < now()
		RETURNING run_id, session_id, state`)
	if err != nil {
		return 0, fmt.Errorf("reap service runs: %w", err)
	}
	type reapedRun struct {
		runID     string
		sessionID string
		state     string
	}
	var reaped []reapedRun
	for rows.Next() {
		var r reapedRun
		if err := rows.Scan(&r.runID, &r.sessionID, &r.state); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan reaped service run: %w", err)
		}
		reaped = append(reaped, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("iterate reaped service runs: %w", err)
	}
	rows.Close()
	for _, r := range reaped {
		if r.state != "failed" {
			continue
		}
		// 接管次数耗尽 → 终态失败：回执入箱（与 orphaned 中间态区分）。
		if err := insertServiceRunReceiptTx(ctx, tx, r.runID, r.sessionID, "failed", "", "lease_expired_attempts"); err != nil {
			return 0, err
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("commit reap service runs: %w", err)
	}
	return len(reaped), nil
}
