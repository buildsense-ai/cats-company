package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.SkillHubPublicationRepairModelCallStore = (*Adapter)(nil)

func sameRepairModelLease(a, b store.SkillHubPublicationRepairLease) bool {
	at, bt := a.Until, b.Until
	a.Until, b.Until = time.Time{}, time.Time{}
	return a == b && at.Equal(bt)
}

func lockRepairModelRound(ctx context.Context, tx *sql.Tx, actor int64, lease store.SkillHubPublicationRepairLease) error {
	t, err := lockPublicationDescriptionTask(ctx, tx, actor, lease.EntryID)
	if err != nil {
		return err
	}
	if t.ID != lease.TaskID || len(t.Rounds) != lease.Round {
		return store.ErrSkillHubPublicationStale
	}
	r := t.Rounds[lease.Round-1]
	if !sameRepairModelLease(r.Lease, lease) || r.Result != nil {
		return store.ErrSkillHubPublicationStale
	}
	var live bool
	if err := tx.QueryRowContext(ctx, `SELECT lease_until>clock_timestamp() FROM skillhub_publication_repair_rounds WHERE entry_id=$1 AND round_number=$2`, lease.EntryID, lease.Round).Scan(&live); err != nil {
		return err
	}
	if !live {
		return store.ErrSkillHubPublicationStale
	}
	return nil
}

func readRepairModelCall(ctx context.Context, tx *sql.Tx, id string, round int) (*store.SkillHubPublicationRepairModelCall, error) {
	c := &store.SkillHubPublicationRepairModelCall{}
	var raw []byte
	var output, hash sql.NullString
	var completed sql.NullTime
	err := tx.QueryRowContext(ctx, `SELECT call_id,dispatch_lease,request_hash,created_at,output_text,output_hash,completed_at
		FROM skillhub_publication_repair_model_calls WHERE entry_id=$1 AND round_number=$2`, id, round).
		Scan(&c.CallID, &raw, &c.Input.RequestHash, &c.CreatedAt, &output, &hash, &completed)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(raw, &c.DispatchLease) != nil || c.DispatchLease.EntryID != id || c.DispatchLease.Round != round ||
		output.Valid != hash.Valid || output.Valid != completed.Valid {
		return nil, store.ErrSkillHubPublicationConflict
	}
	if output.Valid {
		c.Output = &store.SkillHubPublicationRepairModelOutput{Text: output.String}
		c.OutputHash, c.CompletedAt = hash.String, &completed.Time
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (a *Adapter) StartSkillHubPublicationRepairModelCall(ctx context.Context, actor int64, lease store.SkillHubPublicationRepairLease, in store.SkillHubPublicationRepairModelCallInput) (*store.SkillHubPublicationRepairModelCall, bool, error) {
	if actor <= 0 || lease.Validate() != nil || in.Validate() != nil {
		return nil, false, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	if err := lockRepairModelRound(ctx, tx, actor, lease); err != nil {
		return nil, false, err
	}
	c, err := readRepairModelCall(ctx, tx, lease.EntryID, lease.Round)
	if err != nil {
		return nil, false, err
	}
	created := c == nil
	if c != nil && c.Input != in {
		return nil, false, store.ErrSkillHubPublicationConflict
	}
	if created {
		suffix, err := uploadRandomHex(16)
		if err != nil {
			return nil, false, err
		}
		raw, err := json.Marshal(lease)
		if err != nil {
			return nil, false, err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO skillhub_publication_repair_model_calls(entry_id,round_number,call_id,request_hash,dispatch_lease)
			VALUES($1,$2,$3,$4,$5)`, lease.EntryID, lease.Round, "pubmodel_"+suffix, in.RequestHash, raw)
		if err != nil {
			return nil, false, err
		}
		c, err = readRepairModelCall(ctx, tx, lease.EntryID, lease.Round)
		if err != nil {
			return nil, false, err
		}
	}
	// Never return dispatch permission until commit has succeeded. An unknown
	// commit result is deliberately conservative: recovery cannot grant it again.
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return c, created, nil
}

func (a *Adapter) CompleteSkillHubPublicationRepairModelCall(ctx context.Context, actor int64, lease store.SkillHubPublicationRepairLease, callID string, output store.SkillHubPublicationRepairModelOutput) (*store.SkillHubPublicationRepairModelCall, error) {
	if actor <= 0 || lease.Validate() != nil || output.Validate() != nil || len(callID) != 41 {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if err := lockRepairModelRound(ctx, tx, actor, lease); err != nil {
		return nil, err
	}
	c, err := readRepairModelCall(ctx, tx, lease.EntryID, lease.Round)
	if err != nil {
		return nil, err
	}
	if c == nil || c.CallID != callID || !sameRepairModelLease(c.DispatchLease, lease) {
		return nil, store.ErrSkillHubPublicationStale
	}
	if c.Output != nil {
		if *c.Output != output {
			return nil, store.ErrSkillHubPublicationConflict
		}
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE skillhub_publication_repair_model_calls SET output_text=$3,output_hash=$4,completed_at=clock_timestamp()
			WHERE entry_id=$1 AND round_number=$2`, lease.EntryID, lease.Round, output.Text, output.Hash())
		if err != nil {
			return nil, err
		}
		c, err = readRepairModelCall(ctx, tx, lease.EntryID, lease.Round)
		if err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return c, nil
}

// Only private history: no authority renewal, model replay or output execution.
func (a *Adapter) GetSkillHubPublicationRepairModelCall(ctx context.Context, actor int64, id string, round int) (*store.SkillHubPublicationRepairModelCall, error) {
	if actor <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(id) || round < 1 || round > store.SkillHubPublicationRepairMaxRounds {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var allowed bool
	if err := tx.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM skillhub_publication_entries e JOIN users u ON u.id=e.actor_uid
		WHERE e.entry_id=$1 AND e.actor_uid=$2 AND u.account_type='human' AND u.state=0)`, id, actor).Scan(&allowed); err != nil {
		return nil, err
	}
	if !allowed {
		return nil, nil
	}
	c, err := readRepairModelCall(ctx, tx, id, round)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return c, nil
}
