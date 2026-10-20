package postgres

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"time"

	"github.com/openchat/openchat/server/store"
)

var _ store.SkillHubPublicationCaptureDeliveryStore = (*Adapter)(nil)

// Runtime filtering precedes LIMIT: another installation must not starve this
// socket's queue. Only verified sources without immutable evidence are due.
func (a *Adapter) ListSkillHubPublicationCaptureDeliveries(ctx context.Context, s store.SkillHubPublicationRuntimeSelector, limit int) ([]string, error) {
	if s.Validate() != nil || limit < 1 || limit > 32 {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := a.db.QueryContext(ctx, `SELECT e.entry_id FROM skillhub_publication_entries e
		JOIN skillhub_publication_outbox o ON o.entry_id=e.entry_id AND o.kind='capture_candidate'
		JOIN users actor ON actor.id=e.actor_uid AND actor.account_type='human' AND actor.state=0
		JOIN users bot ON bot.id=e.bot_uid AND bot.account_type='bot' AND bot.state=0
		JOIN bot_config b ON b.user_id=e.bot_uid AND b.owner_id=e.actor_uid AND b.body_id=e.body_id AND b.enabled=true
		LEFT JOIN skillhub_publication_capture_deliveries d ON d.entry_id=e.entry_id
		WHERE e.actor_uid=$1 AND e.bot_uid=$2 AND e.device_id=$3 AND e.body_id=$4 AND e.installation_id=$5
		AND e.state='waiting_runtime' AND o.delivery_state='source_verified' AND o.source_verified_at IS NOT NULL
		AND NOT EXISTS (SELECT 1 FROM skillhub_publication_captures c WHERE c.entry_id=e.entry_id)
		AND (d.entry_id IS NULL OR (d.state='pending' AND d.attempts<8 AND d.next_attempt_at<=clock_timestamp())
			OR (d.state='leased' AND d.lease_until<=clock_timestamp()))
		ORDER BY e.created_at,e.entry_id LIMIT $6`, s.ActorUID, s.BotUID, s.DeviceID, s.BodyID, s.InstallationID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func (a *Adapter) ClaimSkillHubPublicationCaptureDelivery(ctx context.Context, id string, b store.SkillHubPublicationDeliveryBinding) (*store.SkillHubPublicationCaptureLease, error) {
	if !store.ValidSkillHubPublicationDeliveryEntryID(id) || b.Validate() != nil {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var locked string
	err = tx.QueryRowContext(ctx, `SELECT entry_id FROM skillhub_publication_entries WHERE entry_id=$1 FOR UPDATE`, id).Scan(&locked)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, store.ErrSkillHubPublicationDenied
	}
	if err != nil {
		return nil, err
	}
	if err = lockPublicationDeliveryAuthority(ctx, tx, id, b); err != nil {
		return nil, err
	}
	if err = lockPublicationVerifiedSource(ctx, tx, id); err != nil {
		return nil, err
	}
	capture, err := readPublicationCapture(ctx, tx, id)
	if err != nil {
		return nil, err
	}
	if capture != nil {
		return nil, store.ErrSkillHubPublicationSettled
	}
	var state string
	var attempts int
	var due bool
	err = tx.QueryRowContext(ctx, `SELECT state,attempts,CASE WHEN state='leased' THEN lease_until<=clock_timestamp()
		ELSE next_attempt_at<=clock_timestamp() END FROM skillhub_publication_capture_deliveries WHERE entry_id=$1 FOR UPDATE`, id).Scan(&state, &attempts, &due)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		if state != "pending" && state != "leased" {
			return nil, store.ErrSkillHubPublicationSettled
		}
		if !due {
			return nil, store.ErrSkillHubPublicationBusy
		}
		if attempts >= store.SkillHubPublicationDeliveryAttempts {
			if _, err = tx.ExecContext(ctx, `UPDATE skillhub_publication_capture_deliveries SET state='needs_attention',last_code='exhausted' WHERE entry_id=$1`, id); err != nil {
				return nil, err
			}
			if err = tx.Commit(); err != nil {
				return nil, err
			}
			return nil, store.ErrSkillHubPublicationExhausted
		}
	}
	var random [16]byte
	if _, err = rand.Read(random[:]); err != nil {
		return nil, err
	}
	l := &store.SkillHubPublicationCaptureLease{EntryID: id, Binding: b, Token: hex.EncodeToString(random[:]), Attempt: attempts + 1}
	err = tx.QueryRowContext(ctx, `INSERT INTO skillhub_publication_capture_deliveries
		(entry_id,state,attempts,lease_token,lease_node,lease_connection,lease_generation,lease_until)
		VALUES ($1,'leased',$2,$3,$4,$5,$6,clock_timestamp()+($7 * interval '1 second'))
		ON CONFLICT (entry_id) DO UPDATE SET state='leased',attempts=EXCLUDED.attempts,lease_token=EXCLUDED.lease_token,
		lease_node=EXCLUDED.lease_node,lease_connection=EXCLUDED.lease_connection,lease_generation=EXCLUDED.lease_generation,
		lease_until=EXCLUDED.lease_until,last_code='' RETURNING lease_until`,
		id, l.Attempt, l.Token, b.NodeID, b.ConnectionID, b.Source.RouteGeneration, int(store.SkillHubPublicationCaptureLeaseDuration/time.Second)).Scan(&l.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return l, nil
}

func (a *Adapter) FinishSkillHubPublicationCaptureDelivery(ctx context.Context, l store.SkillHubPublicationCaptureLease, outcome string) error {
	if l.Validate() != nil || (outcome != "captured" && outcome != "retry" && outcome != "rejected") {
		return store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := beginPublicationDelivery(ctx, a.db)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var id string
	if err = tx.QueryRowContext(ctx, `SELECT entry_id FROM skillhub_publication_entries WHERE entry_id=$1 FOR UPDATE`, l.EntryID).Scan(&id); err != nil {
		return err
	}
	if err = lockPublicationDeliveryAuthority(ctx, tx, l.EntryID, l.Binding); err != nil {
		return err
	}
	if err = lockPublicationVerifiedSource(ctx, tx, l.EntryID); err != nil {
		return err
	}
	evidence, err := readPublicationCapture(ctx, tx, l.EntryID)
	if err != nil {
		return err
	}
	state, code := "pending", "retry"
	if evidence != nil {
		state, code = "captured", ""
	} else if outcome == "captured" {
		return store.ErrSkillHubPublicationDenied
	} else if outcome == "rejected" {
		state, code = "needs_attention", "rejected"
	} else if l.Attempt >= store.SkillHubPublicationDeliveryAttempts {
		state, code = "needs_attention", "exhausted"
	}
	res, err := tx.ExecContext(ctx, `UPDATE skillhub_publication_capture_deliveries SET state=$2,last_code=$3,
		next_attempt_at=clock_timestamp()+($4 * interval '1 second')
		WHERE entry_id=$1 AND state='leased' AND lease_token=$5 AND attempts=$6 AND lease_until=$7
		AND lease_node=$8 AND lease_connection=$9 AND lease_generation=$10 AND lease_until>clock_timestamp()`,
		l.EntryID, state, code, int(store.SkillHubPublicationDeliveryBackoff(l.Attempt)/time.Second),
		l.Token, l.Attempt, l.ExpiresAt, l.Binding.NodeID, l.Binding.ConnectionID, l.Binding.Source.RouteGeneration)
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
	return tx.Commit()
}
