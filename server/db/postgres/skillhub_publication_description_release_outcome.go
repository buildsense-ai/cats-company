package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.SkillHubPublicationDescriptionReleaseObservationReader = (*Adapter)(nil)

func (a *Adapter) GetSkillHubPublicationDescriptionReleaseObservation(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationDescriptionReleaseObservation, error) {
	if actor <= 0 || !store.ValidSkillHubPublicationDeliveryEntryID(id) {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// One coherent historical snapshot. No live source/body/expiry dependency,
	// and no current Bot-owner substitution for the original publisher.
	tx, err := a.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	out, err := readDescriptionReleaseObservation(ctx, tx, actor, id)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func readDescriptionReleaseObservation(ctx context.Context, tx *sql.Tx, actor int64, id string) (*store.SkillHubPublicationDescriptionReleaseObservation, error) {
	var found string
	err := tx.QueryRowContext(ctx, `SELECT e.entry_id FROM skillhub_publication_entries e
		JOIN users u ON u.id=e.actor_uid AND u.account_type='human' AND u.state=0
		WHERE e.entry_id=$1 AND e.actor_uid=$2`, id, actor).Scan(&found)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrSkillHubPublicationDenied
	}
	if err != nil {
		return nil, err
	}
	task, err := readPublicationRepairTask(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	draft, err := readPublicationDescription(ctx, tx, task)
	if err != nil {
		return nil, err
	}
	if draft == nil || draft.ConfirmedAt == nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	receipt, err := readPublicationCommitReceipt(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if receipt == nil || *receipt != task.Input.Receipt {
		return nil, store.ErrSkillHubPublicationConflict
	}
	app, err := readPublicationDescriptionApplication(ctx, tx, task, draft)
	if err != nil {
		return nil, err
	}
	if app == nil || app.Result == nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	accepted, err := readDescriptionAcceptance(ctx, tx, *app)
	if err != nil {
		return nil, err
	}
	if accepted == nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	continuation := store.SkillHubPublicationDescriptionContinuation{Task: *task, Draft: *draft, Application: *app, Acceptance: *accepted}
	intent, err := readSkillHubPublicationDescriptionReleaseIntent(ctx, tx, continuation)
	if err != nil {
		return nil, err
	}
	if intent == nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	out := &store.SkillHubPublicationDescriptionReleaseObservation{Continuation: continuation, Intent: *intent,
		Permit: store.SkillHubPublicationDescriptionReleasePermit{Intent: *intent}}
	var entry, hash string
	err = tx.QueryRowContext(ctx, `SELECT permit_id,entry_id,intent_identity_hash,authorized_at
		FROM skillhub_publication_description_release_permits WHERE application_id=$1`, app.ID).
		Scan(&out.Permit.ID, &entry, &hash, &out.Permit.AuthorizedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrSkillHubPublicationConflict
	}
	if err != nil {
		return nil, err
	}
	if entry != id || hash != intent.IdentityHash || out.ValidateFor(actor, id) != nil {
		return nil, store.ErrSkillHubPublicationConflict
	}
	return out, nil
}
