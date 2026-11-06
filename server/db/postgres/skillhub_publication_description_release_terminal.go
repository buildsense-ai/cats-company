package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.SkillHubPublicationDescriptionReleaseTerminalStore = (*Adapter)(nil)

func readDescriptionReleaseTerminal(ctx context.Context, tx *sql.Tx, o store.SkillHubPublicationDescriptionReleaseObservation) (*store.SkillHubPublicationDescriptionReleaseTerminalReceipt, error) {
	var raw []byte
	r := &store.SkillHubPublicationDescriptionReleaseTerminalReceipt{}
	var entry, permit, intent string
	err := tx.QueryRowContext(ctx, `SELECT entry_id,permit_id,intent_identity_hash,payload,identity_hash,recorded_at
		FROM skillhub_publication_description_release_terminals WHERE application_id=$1`, o.Intent.ApplicationID).
		Scan(&entry, &permit, &intent, &raw, &r.IdentityHash, &r.RecordedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if json.Unmarshal(raw, &r.Terminal) != nil || r.ValidateFor(o) != nil || entry != o.Intent.EntryID || permit != o.Permit.ID || intent != o.Intent.IdentityHash {
		return nil, store.ErrSkillHubPublicationConflict
	}
	return r, nil
}

func (a *Adapter) GetSkillHubPublicationDescriptionReleaseTerminal(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationDescriptionReleaseTerminalReceipt, error) {
	if actor <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(id) {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	o, err := readDescriptionReleaseObservation(ctx, tx, actor, id)
	if err != nil {
		return nil, err
	}
	r, err := readDescriptionReleaseTerminal(ctx, tx, *o)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r, nil
}

func (a *Adapter) RecordSkillHubPublicationDescriptionReleaseTerminal(ctx context.Context, actor int64, expected store.SkillHubPublicationDescriptionReleaseObservation,
	terminal store.SkillHubPublicationDescriptionReleaseTerminal) (*store.SkillHubPublicationDescriptionReleaseTerminalReceipt, error) {
	if expected.ValidateFor(actor, terminal.EntryID) != nil || terminal.ValidateFor(expected) != nil {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	// Freeze optional result before hashing/encoding for immutable idempotency.
	if terminal.Result != nil {
		copy := *terminal.Result
		terminal.Result = &copy
	}
	hash, err := terminal.Digest()
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(terminal)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	// Hold active-account authority through commit. This is historical viewing,
	// not the live Bot/Runtime/source authority used for execution.
	var uid int64
	err = tx.QueryRowContext(ctx, `SELECT u.id FROM users u JOIN skillhub_publication_entries e ON e.actor_uid=u.id
		WHERE e.entry_id=$1 AND u.id=$2 AND u.account_type='human' AND u.state=0 FOR SHARE OF u`, terminal.EntryID, actor).Scan(&uid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrSkillHubPublicationDenied
	}
	if err != nil {
		return nil, err
	}
	o, err := readDescriptionReleaseObservation(ctx, tx, actor, terminal.EntryID)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(*o, expected) {
		return nil, store.ErrSkillHubPublicationConflict
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO skillhub_publication_description_release_terminals
		(application_id,entry_id,permit_id,intent_identity_hash,payload,identity_hash) VALUES($1,$2,$3,$4,$5,$6)
		ON CONFLICT(application_id) DO NOTHING`, terminal.ApplicationID, terminal.EntryID, terminal.PermitID, terminal.IntentHash, raw, hash)
	if err != nil {
		return nil, err
	}
	r, err := readDescriptionReleaseTerminal(ctx, tx, *o)
	if err != nil {
		return nil, err
	}
	if r == nil || r.IdentityHash != hash || !reflect.DeepEqual(r.Terminal, terminal) {
		return nil, store.ErrSkillHubPublicationConflict
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return r, nil
}
