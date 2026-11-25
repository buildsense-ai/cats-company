package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.ComputeTaskStore = (*Adapter)(nil)

// Cats 算力任务的持久层。三件事必须在同一个事务里完成，顺序不能颠倒：
//
//  1. 报价是写一次就不能改的快照（compute_task_quotes.spec 是 JSONB，触发器禁止
//     UPDATE）。确认时把用户回显的额度上限和报价逐字比对，不同就是另一笔交易。
//  2. 确认才预占：把报价上限（不是实际用量）整笔从共享点数账户里扣住，monthly →
//     paid，不碰 gift、不许透支。只有确认成功才会出现 queued 行，所以崩溃只可能
//     留下「没有预占的报价」，绝不可能留下「没有预占的任务」。
//  3. 结束才结算：按实际用量结算，没用掉的部分按预占时的批次原路退回；终态行
//     上的 charged_units + progress 是用来做幂等比对的，不是新的扣费依据。
//
// 租约是平台 worker 的排他凭证：领任务时写入、保存进度和结束时校验，并额外校验
// 租约没有过期（`lease_expires_at > now`）。换了 worker 就换了租约，旧 worker 的
// 写入一律被 fence 掉，永远不会把别人的任务推到终态。
//
// 价格的单位是共享的 model credit micro-unit，账本幂等键是 quote id，所以重放
// 确认/结束都不会二次扣费。

// computeTaskToken 和两张表的 CHECK 一致：id 与租约都是 URL 安全的小字母表。
var computeTaskToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{7,63}$`)

func validComputeTaskToken(value string) bool {
	return computeTaskToken.MatchString(value)
}

// computeTaskRunQuery reads one task as its owner sees it: the immutable quote
// is joined to the lifecycle row, and a quote that was never confirmed still
// answers in state "quoted" (there is no task row yet).
const computeTaskRunQuery = `
SELECT q.quote_id, q.spec, q.sku, q.sku_logical_cpus, q.sku_memory_mib,
       q.units_per_node_period, q.period_seconds, q.increment_seconds,
       q.max_units, q.max_node_seconds, q.expires_at, q.uid,
       COALESCE(t.state, 'quoted'), COALESCE(t.progress, '{}'::jsonb),
       t.confirmed_at, t.charged_units, COALESCE(t.lease, '')
FROM compute_task_quotes q
LEFT JOIN compute_tasks t ON t.quote_id = q.quote_id AND t.uid = q.uid`

type computeTaskQuerier interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func scanComputeTaskRun(scanner interface {
	Scan(dest ...any) error
}) (*store.ComputeTaskRun, error) {
	var (
		run         store.ComputeTaskRun
		specRaw     []byte
		progressRaw []byte
		confirmedAt sql.NullTime
		charged     sql.NullInt64
	)
	if err := scanner.Scan(
		&run.ID, &specRaw, &run.SKU, &run.SKUCPUs, &run.SKUMemoryMiB,
		&run.Rate.UnitsPerNodePeriod, &run.Rate.PeriodSeconds, &run.Rate.IncrementSeconds,
		&run.MaxUnits, &run.MaxNodeSeconds, &run.ExpiresAt, &run.UID,
		&run.State, &progressRaw, &confirmedAt, &charged, &run.Lease,
	); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(specRaw, &run.Spec); err != nil {
		return nil, fmt.Errorf("decode compute task spec: %w", err)
	}
	if len(progressRaw) > 0 {
		if err := json.Unmarshal(progressRaw, &run.Progress); err != nil {
			return nil, fmt.Errorf("decode compute task progress: %w", err)
		}
	}
	if confirmedAt.Valid {
		value := confirmedAt.Time
		run.ConfirmedAt = &value
	}
	if charged.Valid {
		value := charged.Int64
		run.ChargedUnits = &value
	}
	return &run, nil
}

// readComputeTaskRun reads one run through q (a pool or a transaction).
func readComputeTaskRun(ctx context.Context, q computeTaskQuerier, id string) (*store.ComputeTaskRun, error) {
	run, err := scanComputeTaskRun(q.QueryRowContext(ctx, computeTaskRunQuery+` WHERE q.quote_id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrComputeTaskNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("read compute task: %w", err)
	}
	return run, nil
}

// commitComputeTaskRun reads the row this transaction just wrote and commits it.
func commitComputeTaskRun(ctx context.Context, tx *sql.Tx, id string) (*store.ComputeTaskRun, error) {
	run, err := readComputeTaskRun(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit compute task: %w", err)
	}
	return run, nil
}

// encodeComputeTaskQuote validates the offer and returns the immutable JSONB
// snapshot that goes into the row.
func encodeComputeTaskQuote(uid int64, quote store.ComputeTaskQuote) ([]byte, error) {
	if uid <= 0 || !validComputeTaskToken(strings.TrimSpace(quote.ID)) {
		return nil, fmt.Errorf("invalid compute task quote id %q: %w", quote.ID, store.ErrComputeTaskConflict)
	}
	if quote.Spec.Nodes <= 0 || quote.Spec.LogicalCPUs <= 0 || quote.Spec.MemoryMiB <= 0 || quote.Spec.WallSeconds <= 0 {
		return nil, fmt.Errorf("compute task quote %s has an invalid allocation: %w", quote.ID, store.ErrComputeTaskConflict)
	}
	if strings.TrimSpace(quote.SKU) == "" || len(quote.SKU) > 96 || quote.SKUCPUs <= 0 || quote.SKUMemoryMiB <= 0 {
		return nil, fmt.Errorf("compute task quote %s has an invalid SKU: %w", quote.ID, store.ErrComputeTaskConflict)
	}
	if quote.Rate.UnitsPerNodePeriod <= 0 || quote.Rate.PeriodSeconds <= 0 || quote.Rate.IncrementSeconds <= 0 ||
		quote.MaxUnits <= 0 || quote.MaxNodeSeconds <= 0 || quote.ExpiresAt.IsZero() {
		return nil, fmt.Errorf("compute task quote %s has an invalid tariff: %w", quote.ID, store.ErrComputeTaskConflict)
	}
	spec, err := json.Marshal(quote.Spec)
	if err != nil {
		return nil, fmt.Errorf("encode compute task spec: %w", err)
	}
	return spec, nil
}

// CreateComputeTaskQuote stores one immutable offer. Replaying the same quote id
// with the same terms is a no-op; reusing it with different terms is a conflict,
// never a second, silently repriced offer.
func (a *Adapter) CreateComputeTaskQuote(ctx context.Context, uid int64, quote store.ComputeTaskQuote) error {
	spec, err := encodeComputeTaskQuote(uid, quote)
	if err != nil {
		return err
	}
	id := strings.TrimSpace(quote.ID)
	expiresAt := quote.ExpiresAt.UTC().Truncate(time.Microsecond)
	result, err := a.db.ExecContext(ctx, `
		INSERT INTO compute_task_quotes(
			quote_id, uid, spec, sku, sku_logical_cpus, sku_memory_mib,
			units_per_node_period, period_seconds, increment_seconds,
			max_units, max_node_seconds, expires_at)
		VALUES ($1, $2, $3::jsonb, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		ON CONFLICT (quote_id) DO NOTHING`,
		id, uid, string(spec), strings.TrimSpace(quote.SKU), quote.SKUCPUs, quote.SKUMemoryMiB,
		quote.Rate.UnitsPerNodePeriod, quote.Rate.PeriodSeconds, quote.Rate.IncrementSeconds,
		quote.MaxUnits, quote.MaxNodeSeconds, expiresAt)
	if err != nil {
		return fmt.Errorf("create compute task quote: %w", err)
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("create compute task quote: %w", err)
	}
	if inserted == 1 {
		return nil
	}
	var (
		storedUID  int64
		sameSpec   bool
		stored     store.ComputeTaskQuote
		storedExp  time.Time
		storedRate store.ComputeTaskRate
	)
	stored.ID = id
	if err := a.db.QueryRowContext(ctx, `
		SELECT uid, spec = $2::jsonb, sku, sku_logical_cpus, sku_memory_mib,
		       units_per_node_period, period_seconds, increment_seconds,
		       max_units, max_node_seconds, expires_at
		FROM compute_task_quotes WHERE quote_id = $1`, id, string(spec)).Scan(
		&storedUID, &sameSpec, &stored.SKU, &stored.SKUCPUs, &stored.SKUMemoryMiB,
		&storedRate.UnitsPerNodePeriod, &storedRate.PeriodSeconds, &storedRate.IncrementSeconds,
		&stored.MaxUnits, &stored.MaxNodeSeconds, &storedExp); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("compute task quote %s vanished after insert: %w", id, store.ErrComputeTaskConflict)
		}
		return fmt.Errorf("read compute task quote: %w", err)
	}
	if storedUID != uid || !sameSpec || stored.SKU != strings.TrimSpace(quote.SKU) ||
		stored.SKUCPUs != quote.SKUCPUs || stored.SKUMemoryMiB != quote.SKUMemoryMiB ||
		storedRate != quote.Rate || stored.MaxUnits != quote.MaxUnits ||
		stored.MaxNodeSeconds != quote.MaxNodeSeconds || !storedExp.Equal(expiresAt) {
		return fmt.Errorf("compute task quote %s already exists with different terms: %w", id, store.ErrComputeTaskConflict)
	}
	return nil
}

// GetComputeTaskForWorker reads one task without an owner filter. Only the
// platform worker endpoint uses it: the worker already holds the quote id it
// leased and has no uid to present, and the worker token (not a user session)
// is what authorizes the call. Account-facing routes must use GetComputeTask, so
// a worker read can never be reached with a user's credentials.
//
// 任务不存在时返回 ErrComputeTaskNotFound：worker 手里的 id 可能是刚被删号级联
// 带走的，调用方按「任务不在了」处理即可。
func (a *Adapter) GetComputeTaskForWorker(ctx context.Context, id string) (*store.ComputeTaskRun, error) {
	id = strings.TrimSpace(id)
	if !validComputeTaskToken(id) {
		return nil, store.ErrComputeTaskNotFound
	}
	return readComputeTaskRun(ctx, a.db, id)
}

// GetComputeTask reads one task for its owner only. A quote that was never
// confirmed answers in state "quoted"; another user's quote id is not found.
func (a *Adapter) GetComputeTask(ctx context.Context, uid int64, id string) (*store.ComputeTaskRun, error) {
	id = strings.TrimSpace(id)
	if uid <= 0 || !validComputeTaskToken(id) {
		return nil, nil
	}
	run, err := scanComputeTaskRun(a.db.QueryRowContext(ctx, computeTaskRunQuery+
		` WHERE q.quote_id = $1 AND q.uid = $2`, id, uid))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read compute task: %w", err)
	}
	return run, nil
}

// ConfirmComputeTask turns one quote into a queued task and holds the whole
// quoted ceiling in the same transaction.
//
// 规则：
//   - 报价必须属于这个用户，额度上限必须和报价逐字相等（用户确认的是他看到的
//     那个上限，不是客户端可以重写的数字）；
//   - 过期只在「第一次确认」时拦：已经确认过的任务重放确认不再看报价有效期，
//     否则用户会在任务跑到一半时收到一个假的过期错误；
//   - 重放确认返回同一行，不再预占第二笔钱；额度上限对不上就是冲突。
func (a *Adapter) ConfirmComputeTask(ctx context.Context, uid int64, id string, maxUnits int64, now time.Time) (*store.ComputeTaskRun, error) {
	id = strings.TrimSpace(id)
	if uid <= 0 || !validComputeTaskToken(id) {
		return nil, store.ErrComputeTaskNotFound
	}
	if maxUnits <= 0 {
		return nil, fmt.Errorf("compute task %s needs a positive confirmed ceiling: %w", id, store.ErrComputeTaskConflict)
	}
	now = now.UTC()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin compute task confirmation: %w", err)
	}
	defer tx.Rollback()
	var (
		quoteMaxUnits int64
		expiresAt     time.Time
	)
	if err := tx.QueryRowContext(ctx,
		`SELECT max_units, expires_at FROM compute_task_quotes WHERE quote_id = $1 AND uid = $2`,
		id, uid).Scan(&quoteMaxUnits, &expiresAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, store.ErrComputeTaskNotFound
		}
		return nil, fmt.Errorf("read compute task quote: %w", err)
	}
	// Insert-then-lock: the loser of ON CONFLICT DO NOTHING waits for the winner
	// to commit and then locks the same row, so two confirmations of one quote
	// serialize instead of both reserving.
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO compute_tasks (quote_id, uid) VALUES ($1, $2) ON CONFLICT (quote_id) DO NOTHING`,
		id, uid); err != nil {
		return nil, fmt.Errorf("open compute task: %w", err)
	}
	var (
		state       string
		confirmedAt int64
	)
	if err := tx.QueryRowContext(ctx,
		`SELECT state, confirmed_max_units FROM compute_tasks WHERE quote_id = $1 FOR UPDATE`,
		id).Scan(&state, &confirmedAt); err != nil {
		return nil, fmt.Errorf("lock compute task: %w", err)
	}
	if state != store.ComputeTaskQuoted {
		// 重放确认：额度上限必须是同一笔，否则就是另一笔交易，不能悄悄接受。
		if confirmedAt != maxUnits {
			return nil, fmt.Errorf("compute task %s is already confirmed for %d units, not %d: %w",
				id, confirmedAt, maxUnits, store.ErrComputeTaskConflict)
		}
		return commitComputeTaskRun(ctx, tx, id)
	}
	if maxUnits != quoteMaxUnits {
		return nil, fmt.Errorf("compute task %s quote is capped at %d units, not the echoed %d: %w",
			id, quoteMaxUnits, maxUnits, store.ErrComputeTaskConflict)
	}
	if !now.Before(expiresAt.UTC()) {
		return nil, fmt.Errorf("compute task quote %s expired at %s: %w",
			id, expiresAt.UTC().Format(time.RFC3339), store.ErrComputeQuoteExpired)
	}
	if err := reservePrepaidResourceCredits(ctx, tx, prepaidResourceHold{
		Label:     "compute task",
		UID:       uid,
		RequestID: id,
		Units:     quoteMaxUnits,
	}, now); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE compute_tasks
		SET state = 'queued', confirmed_max_units = $2, confirmed_at = $3, updated_at = $3
		WHERE quote_id = $1`, id, maxUnits, now); err != nil {
		return nil, fmt.Errorf("queue compute task: %w", err)
	}
	return commitComputeTaskRun(ctx, tx, id)
}

// ClaimComputeTask leases one runnable task to the supplied worker lease.
//
// queued 的任务先走；active 但租约已过期的任务可以重新领走（旧 worker 的保存和
// 结束会被租约 + 期限双重 fence 掉，不可能再改这一行）。SELECT ... FOR UPDATE
// SKIP LOCKED 让并发的 worker 各自领到不同的行，而不是排队等同一行。
//
// 没有可领的任务时返回 (nil, nil)：这是正常空闲，不是错误。
func (a *Adapter) ClaimComputeTask(ctx context.Context, lease string, now, deadline time.Time) (*store.ComputeTaskRun, error) {
	lease = strings.TrimSpace(lease)
	if !validComputeTaskToken(lease) {
		return nil, fmt.Errorf("invalid compute task lease %q: %w", lease, store.ErrComputeTaskConflict)
	}
	now = now.UTC()
	deadline = deadline.UTC()
	if !deadline.After(now) {
		return nil, fmt.Errorf("compute task lease %s must expire after it is taken: %w", lease, store.ErrComputeTaskConflict)
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin compute task claim: %w", err)
	}
	defer tx.Rollback()
	var id string
	err = tx.QueryRowContext(ctx, `
		WITH candidate AS (
			SELECT quote_id FROM compute_tasks
			WHERE state = 'queued'
			   OR (state = 'active' AND lease_expires_at <= $2)
			ORDER BY CASE state WHEN 'queued' THEN 0 ELSE 1 END, updated_at, quote_id
			LIMIT 1
			FOR UPDATE SKIP LOCKED
		)
		UPDATE compute_tasks t
		SET state = 'active', lease = $1, lease_expires_at = $3, updated_at = $2
		FROM candidate c
		WHERE t.quote_id = c.quote_id
		RETURNING t.quote_id`, lease, now, deadline).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		if strings.Contains(err.Error(), "uk_compute_tasks_lease") {
			return nil, fmt.Errorf("compute task lease %s is already in use: %w", lease, store.ErrComputeTaskConflict)
		}
		return nil, fmt.Errorf("claim compute task: %w", err)
	}
	return commitComputeTaskRun(ctx, tx, id)
}

// SaveComputeTaskProgress records progress and renews a live lease. An expired
// or replaced lease must be claimed afresh before it can write again.
func (a *Adapter) SaveComputeTaskProgress(ctx context.Context, id, lease string, progress store.ComputeTaskProgress) error {
	id = strings.TrimSpace(id)
	lease = strings.TrimSpace(lease)
	if !validComputeTaskToken(id) || !validComputeTaskToken(lease) {
		return store.ErrComputeTaskNotFound
	}
	raw, err := json.Marshal(progress)
	if err != nil {
		return fmt.Errorf("encode compute task progress: %w", err)
	}
	result, err := a.db.ExecContext(ctx, `
		UPDATE compute_tasks
		SET progress = $3::jsonb, updated_at = CURRENT_TIMESTAMP,
		    lease_expires_at = GREATEST(lease_expires_at, CURRENT_TIMESTAMP + $4 * INTERVAL '1 second')
		WHERE quote_id = $1 AND lease = $2 AND state = 'active'
		  AND lease_expires_at > CURRENT_TIMESTAMP`, id, lease, string(raw), int64(store.ComputeTaskLeaseTTL/time.Second))
	if err != nil {
		return fmt.Errorf("save compute task progress: %w", err)
	}
	updated, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("save compute task progress: %w", err)
	}
	if updated > 0 {
		return nil
	}
	return computeTaskFenceError(ctx, a.db, id, lease)
}

// computeTaskFenceError explains why a fenced write matched no row: a missing
// task is not found, everything else is a lost lease.
func computeTaskFenceError(ctx context.Context, q computeTaskQuerier, id, lease string) error {
	var (
		state       string
		storedLease string
	)
	if err := q.QueryRowContext(ctx,
		`SELECT state, lease FROM compute_tasks WHERE quote_id = $1`, id).Scan(&state, &storedLease); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrComputeTaskNotFound
		}
		return fmt.Errorf("read compute task fence: %w", err)
	}
	if storedLease != lease {
		return fmt.Errorf("compute task %s is leased to another worker: %w", id, store.ErrComputeTaskConflict)
	}
	return fmt.Errorf("compute task %s cannot be updated in state %q: %w", id, state, store.ErrComputeTaskConflict)
}

// FinishComputeTask settles the run: it charges the measured units, returns the
// unused part of the hold to the exact lots it came from and stamps the terminal
// state. succeeded only picks completed vs failed — the money is the same either
// way, so a misinterpreted flag can never move a different amount.
//
// 只接受「state=active + 租约匹配 + 租约未过期」的结束：queued 从来没被 worker
// 认领过，旧 worker 的租约已经作废，过期租约必须重新领取。终态的重复提交如果
// 逐字相同（终态一致、progress 一致、charged_units 一致）就当成成功回执，不再
// 动一分钱；任何一处不同都是冲突，绝不会「既结算又再扣一次」。
func (a *Adapter) FinishComputeTask(ctx context.Context, id, lease string, progress store.ComputeTaskProgress, actualUnits int64, succeeded bool, now time.Time) error {
	id = strings.TrimSpace(id)
	lease = strings.TrimSpace(lease)
	if !validComputeTaskToken(id) || !validComputeTaskToken(lease) {
		return store.ErrComputeTaskNotFound
	}
	if actualUnits < 0 {
		return fmt.Errorf("compute task %s cannot charge %d units: %w", id, actualUnits, store.ErrComputeTaskConflict)
	}
	now = now.UTC()
	raw, err := json.Marshal(progress)
	if err != nil {
		return fmt.Errorf("encode compute task progress: %w", err)
	}
	terminal := store.ComputeTaskCompleted
	if !succeeded {
		terminal = store.ComputeTaskFailed
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin compute task finish: %w", err)
	}
	defer tx.Rollback()
	var (
		state          string
		storedLease    string
		leaseExpiresAt sql.NullTime
		uid            int64
		charged        sql.NullInt64
		sameProgress   bool
		maxUnits       int64
	)
	if err := tx.QueryRowContext(ctx, `
		SELECT t.state, t.lease, t.lease_expires_at, t.uid, t.charged_units,
		       t.progress = $2::jsonb, q.max_units
		FROM compute_tasks t
		JOIN compute_task_quotes q ON q.quote_id = t.quote_id AND q.uid = t.uid
		WHERE t.quote_id = $1
		FOR UPDATE OF t`, id, string(raw)).Scan(
		&state, &storedLease, &leaseExpiresAt, &uid, &charged, &sameProgress, &maxUnits); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return store.ErrComputeTaskNotFound
		}
		return fmt.Errorf("lock compute task: %w", err)
	}
	if state == store.ComputeTaskCompleted || state == store.ComputeTaskFailed {
		if state == terminal && sameProgress && charged.Valid && charged.Int64 == actualUnits {
			// 逐字相同的终态重放：任务已经是这个结果、这笔钱已经结算过。
			return nil
		}
		return fmt.Errorf("compute task %s already finished as %q for %d units: %w",
			id, state, chargedOrNone(charged), store.ErrComputeTaskConflict)
	}
	if state != store.ComputeTaskActive {
		return fmt.Errorf("compute task %s cannot finish in state %q: %w", id, state, store.ErrComputeTaskConflict)
	}
	if storedLease != lease {
		return fmt.Errorf("compute task %s is leased to another worker: %w", id, store.ErrComputeTaskConflict)
	}
	if !leaseExpiresAt.Valid || !now.Before(leaseExpiresAt.Time.UTC()) {
		return fmt.Errorf("compute task %s lease expired at %s: %w",
			id, formatComputeTaskTime(leaseExpiresAt), store.ErrComputeTaskConflict)
	}
	if actualUnits > maxUnits {
		return fmt.Errorf("compute task %s cannot charge %d of %d units: %w",
			id, actualUnits, maxUnits, store.ErrComputeTaskConflict)
	}
	if err := settlePrepaidResourceCredits(ctx, tx, prepaidResourceHold{
		Label:     "compute task",
		UID:       uid,
		RequestID: id,
		Units:     maxUnits,
	}, actualUnits, true, now); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE compute_tasks
		SET state = $2, charged_units = $3, progress = $4::jsonb,
		    lease = '', lease_expires_at = NULL, updated_at = $5
		WHERE quote_id = $1`, id, terminal, actualUnits, string(raw), now); err != nil {
		return fmt.Errorf("finish compute task: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit compute task finish: %w", err)
	}
	return nil
}

func chargedOrNone(charged sql.NullInt64) int64 {
	if charged.Valid {
		return charged.Int64
	}
	return 0
}

func formatComputeTaskTime(value sql.NullTime) string {
	if !value.Valid {
		return "never"
	}
	return value.Time.UTC().Format(time.RFC3339)
}
