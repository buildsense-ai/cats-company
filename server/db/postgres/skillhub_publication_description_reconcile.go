package postgres

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.SkillHubPublicationDescriptionReconcileStore = (*Adapter)(nil)

func (a *Adapter) ClaimSkillHubPublicationDescriptionReconcile(ctx context.Context, scope store.SkillHubPublicationCancellationScope) (*store.SkillHubPublicationDescriptionReconcileLease, error) {
	if err := scope.Validate(); err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	l := &store.SkillHubPublicationDescriptionReconcileLease{}
	// Scope and original active human are filtered before LIMIT. Bot/source
	// liveness is irrelevant to historical result recovery. Existing permits
	// alone enter the queue; no receipt/intent-only publication is admitted.
	err = tx.QueryRowContext(ctx, `SELECT p.application_id,p.entry_id,e.actor_uid
		FROM skillhub_publication_description_release_permits p
		JOIN skillhub_publication_entries e ON e.entry_id=p.entry_id
		JOIN users u ON u.id=e.actor_uid AND u.account_type='human' AND u.state=0
		JOIN skillhub_publication_cloud_receipts r ON r.entry_id=p.entry_id
		LEFT JOIN skillhub_publication_description_reconciles d ON d.application_id=p.application_id
		WHERE r.issuer=$1 AND r.audience=$2
		AND NOT EXISTS(SELECT 1 FROM skillhub_publication_description_release_terminals t WHERE t.application_id=p.application_id)
		AND (d.application_id IS NULL OR (d.state='pending' AND d.next_attempt_at<=clock_timestamp())
		OR (d.state='leased' AND d.lease_until<=clock_timestamp()))
		ORDER BY COALESCE(d.next_attempt_at,p.authorized_at),p.application_id FOR UPDATE OF p SKIP LOCKED LIMIT 1`, scope.Issuer, scope.Audience).
		Scan(&l.ApplicationID, &l.EntryID, &l.ActorUID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	l.Token, err = uploadRandomHex(16)
	if err != nil {
		return nil, err
	}
	err = tx.QueryRowContext(ctx, `INSERT INTO skillhub_publication_description_reconciles
		(application_id,state,generation,lease_token,lease_until,next_attempt_at) VALUES($1,'leased',1,$2,clock_timestamp()+interval '90 seconds',clock_timestamp())
		ON CONFLICT(application_id) DO UPDATE SET state='leased',generation=skillhub_publication_description_reconciles.generation+1,
		lease_token=EXCLUDED.lease_token,lease_until=EXCLUDED.lease_until,next_attempt_at=EXCLUDED.next_attempt_at
		WHERE (skillhub_publication_description_reconciles.state='pending' AND skillhub_publication_description_reconciles.next_attempt_at<=clock_timestamp())
		OR (skillhub_publication_description_reconciles.state='leased' AND skillhub_publication_description_reconciles.lease_until<=clock_timestamp())
		RETURNING generation,lease_until`, l.ApplicationID, l.Token).Scan(&l.Generation, &l.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, tx.Commit()
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return l, nil
}

func (a *Adapter) FinishSkillHubPublicationDescriptionReconcile(ctx context.Context, l store.SkillHubPublicationDescriptionReconcileLease, outcome string) error {
	if l.Validate() != nil || (outcome != "progress" && outcome != "retry" && outcome != "recorded") {
		return store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// CAS the entire scheduling identity and generation, not merely a token.
	// A lost settlement/lease expiry remains recoverable. Retry never becomes
	// publish_failed; failures affect only bounded poll backoff (15..300s).
	res, err := a.db.ExecContext(ctx, `UPDATE skillhub_publication_description_reconciles d SET
		state=CASE WHEN $1='recorded' THEN 'recorded' ELSE 'pending' END,
		failures=CASE WHEN $1='retry' THEN LEAST(failures+1,6) ELSE 0 END,
		lease_token=NULL,lease_until=NULL,
		next_attempt_at=clock_timestamp()+(CASE WHEN $1='retry' THEN LEAST(15*power(2,failures),300) ELSE 30 END)*interval '1 second'
		WHERE d.application_id=$2 AND d.state='leased' AND d.lease_token=$3 AND d.generation=$4
		AND d.lease_until=$5 AND d.lease_until>clock_timestamp()
		AND EXISTS(SELECT 1 FROM skillhub_publication_description_release_permits p
		JOIN skillhub_publication_entries e ON e.entry_id=p.entry_id
		WHERE p.application_id=d.application_id AND p.entry_id=$6 AND e.actor_uid=$7)
		AND ($1<>'recorded' OR EXISTS(SELECT 1 FROM skillhub_publication_description_release_terminals t WHERE t.application_id=d.application_id))`,
		outcome, l.ApplicationID, l.Token, l.Generation, l.ExpiresAt, l.EntryID, l.ActorUID)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return store.ErrSkillHubPublicationStale
	}
	return nil
}
