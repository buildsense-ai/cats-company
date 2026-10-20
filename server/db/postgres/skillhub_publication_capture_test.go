package postgres

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func captureFixture(t *testing.T) (*Adapter, string, store.SkillHubPublicationCaptureInput, int64) {
	t.Helper()
	db, dsn, id, b, other := publicationDeliveryFixture(t)
	l := deliveryClaim(t, db, id, b)
	if err := db.FinishSkillHubPublicationDelivery(context.Background(), *l, ""); err != nil {
		t.Fatal(err)
	}
	return db, dsn, store.SkillHubPublicationCaptureInput{EntryID: id, Binding: b, Revision: 1, RequestHash: strings.Repeat("a", 64), SourceSnapshotHash: strings.Repeat("b", 64), RequestBytes: 1234}, other
}

func recordCaptureForTest(t *testing.T, db *Adapter, in store.SkillHubPublicationCaptureInput) *store.SkillHubPublicationCaptureRecord {
	t.Helper()
	r, err := db.RecordSkillHubPublicationCapture(context.Background(), in)
	if err != nil || r == nil || r.CapturedAt.IsZero() {
		t.Fatalf("capture: %v %v", r, err)
	}
	return r
}

func uploadForCapture(in store.SkillHubPublicationCaptureInput) store.SkillHubPublicationUploadInput {
	return store.SkillHubPublicationUploadInput{EntryID: in.EntryID, Binding: in.Binding, Revision: in.Revision, RequestHash: in.RequestHash, SourceSnapshotHash: in.SourceSnapshotHash, RequestBytes: in.RequestBytes, Issuer: "https://cats.example", Audience: "https://hub.example", Action: store.SkillHubPublicationUploadAction}
}

func captureMigration(t *testing.T, direction string) string {
	t.Helper()
	b, e := os.ReadFile("../migrations/postgres/000043_skillhub_publication_captures." + direction + ".sql")
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}

func TestSkillHubPublicationCaptureMigrationMatchesStartup(t *testing.T) {
	if strings.Join(strings.Fields(captureMigration(t, "up")), " ") != strings.Join(strings.Fields(createSkillHubPublicationCapturesTable), " ") {
		t.Fatal("043 differs from startup")
	}
}

func TestPostgresSkillHubPublicationCaptureRenumberedMigrationPreservesEvidence(t *testing.T) {
	t.Parallel()
	db, _, in, _ := captureFixture(t)
	first := recordCaptureForTest(t, db, in)
	u := uploadForCapture(in)
	g, token := issueUpload(t, db, u)
	// Startup DDL (including the already-deployed PR674 initializer) can have
	// created and populated this table before numbered migration 043 runs.
	// Reapplying the unchanged SQL must not rewrite receipts or upload grants.
	for attempt := 0; attempt < 2; attempt++ {
		deliveryExec(t, db, captureMigration(t, "up"))
		if got := recordCaptureForTest(t, db, in); *got != *first {
			t.Fatal("renumbered migration changed retained evidence")
		}
		if _, err := db.CheckSkillHubPublicationUploadGrant(context.Background(), token, u, g.Epoch); err != nil {
			t.Fatalf("renumbered migration invalidated upload grant: %v", err)
		}
	}
	for _, statement := range []string{
		`UPDATE skillhub_publication_captures SET request_bytes=request_bytes+1`,
		`DELETE FROM skillhub_publication_captures`,
		captureMigration(t, "down"),
	} {
		if _, err := db.db.Exec(statement); err == nil {
			t.Fatal("renumbered migration lost retained-evidence protection")
		}
	}
	if got := recordCaptureForTest(t, db, in); *got != *first {
		t.Fatal("failed rollback changed retained evidence")
	}
}

func TestPostgresSkillHubPublicationCaptureRequiredAndImmutable(t *testing.T) {
	t.Parallel()
	db, dsn, in, _ := captureFixture(t)
	ctx := context.Background()
	u := uploadForCapture(in)
	if _, _, e := db.IssueSkillHubPublicationUploadGrant(ctx, u); !errors.Is(e, store.ErrSkillHubPublicationDenied) {
		t.Fatal("probe became upload authority", e)
	}
	first := recordCaptureForTest(t, db, in)
	if first.Input != in {
		t.Fatal("capture identity")
	}
	reopened := &Adapter{}
	if e := reopened.Open(dsn); e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	if e := reopened.CreateSchema(); e != nil {
		t.Fatal(e)
	}
	if again := recordCaptureForTest(t, reopened, in); *again != *first {
		t.Fatal("replay replaced evidence")
	}
	for name, change := range map[string]func(*store.SkillHubPublicationCaptureInput){
		"bytes":     func(i *store.SkillHubPublicationCaptureInput) { i.RequestBytes++ },
		"request":   func(i *store.SkillHubPublicationCaptureInput) { i.RequestHash = strings.Repeat("c", 64) },
		"candidate": func(i *store.SkillHubPublicationCaptureInput) { i.SourceSnapshotHash = strings.Repeat("d", 64) },
		"socket":    func(i *store.SkillHubPublicationCaptureInput) { i.Binding.ConnectionID = "other-socket" },
		"node":      func(i *store.SkillHubPublicationCaptureInput) { i.Binding.NodeID = "other-node" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := in
			change(&bad)
			if _, e := db.RecordSkillHubPublicationCapture(ctx, bad); !errors.Is(e, store.ErrSkillHubPublicationConflict) {
				t.Fatal("receipt replaced", e)
			}
			if _, _, e := db.IssueSkillHubPublicationUploadGrant(ctx, uploadForCapture(bad)); !errors.Is(e, store.ErrSkillHubPublicationConflict) {
				t.Fatal("unrelated bytes authorized", e)
			}
		})
	}
	for _, statement := range []string{`UPDATE skillhub_publication_captures SET request_bytes=request_bytes+1`, `DELETE FROM skillhub_publication_captures`, captureMigration(t, "down")} {
		if _, e := db.db.Exec(statement); e == nil {
			t.Fatal("retained evidence changed")
		}
	}
	g, token := issueUpload(t, db, u)
	if _, e := db.CheckSkillHubPublicationUploadGrant(ctx, token, u, g.Epoch); e != nil {
		t.Fatal(e)
	}
	if got := recordCaptureForTest(t, db, in); *got != *first {
		t.Fatal("issue changed evidence")
	}
}

func TestPostgresSkillHubPublicationCaptureReconnect(t *testing.T) {
	t.Parallel()
	db, _, in, _ := captureFixture(t)
	ctx := context.Background()
	first := recordCaptureForTest(t, db, in)
	g, _ := issueUpload(t, db, uploadForCapture(in))
	ageUpload(t, db, in.EntryID, "6 seconds")
	if _, e := db.BumpBotConnectionGeneration(in.Binding.BotUID); e != nil {
		t.Fatal(e)
	}
	if _, e := db.RecordSkillHubPublicationCapture(ctx, in); !errors.Is(e, store.ErrSkillHubPublicationStale) {
		t.Fatal("stale runtime accepted", e)
	}
	next := in
	next.Binding.Source.RouteGeneration++
	next.Binding.NodeID = "node-b"
	next.Binding.ConnectionID = "socket-b"
	bad := next
	bad.SourceSnapshotHash = strings.Repeat("c", 64)
	if _, e := db.RecordSkillHubPublicationCapture(ctx, bad); !errors.Is(e, store.ErrSkillHubPublicationConflict) {
		t.Fatal("reconnect changed candidate", e)
	}
	if got := recordCaptureForTest(t, db, next); *got != *first {
		t.Fatal("reconnect rewrote original evidence")
	}
	renewed, _ := issueUpload(t, db, uploadForCapture(next))
	if renewed.Epoch != 2 || renewed.IdempotencyKey != g.IdempotencyKey {
		t.Fatal("reconnect changed upload identity")
	}
}

func TestPostgresSkillHubPublicationCaptureGrantSizeBoundary(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"startup", "migration"} {
		t.Run(mode, func(t *testing.T) {
			for _, size := range []int64{32*1024*1024 + 1, 75 * 1024 * 1024, 75*1024*1024 + 1} {
				db, _, in, _ := captureFixture(t)
				ctx := context.Background()
				if mode == "migration" {
					deliveryExec(t, db, captureMigration(t, "down"))
					deliveryExec(t, db, captureMigration(t, "up"))
				}
				// Metadata-only authority contract: no large byte buffers are
				// fabricated and this is not a live runtime upload capability test.
				in.RequestBytes = size
				u := uploadForCapture(in)
				if size > 75*1024*1024 {
					if _, err := db.RecordSkillHubPublicationCapture(ctx, in); !errors.Is(err, store.ErrSkillHubPublicationInvalid) {
						t.Fatal("oversize capture not rejected as invalid", err)
					}
					if _, _, err := db.IssueSkillHubPublicationUploadGrant(ctx, u); !errors.Is(err, store.ErrSkillHubPublicationInvalid) {
						t.Fatal("oversize grant fell through to missing-evidence denial", err)
					}
					_, err := db.db.Exec(`INSERT INTO skillhub_publication_captures
						(entry_id,candidate_revision,request_hash,source_snapshot_hash,request_bytes,route_generation,node_id,connection_id)
						VALUES ($1,1,$2,$3,$4,$5,$6,$7)`, in.EntryID, in.RequestHash, in.SourceSnapshotHash, size,
						in.Binding.Source.RouteGeneration, in.Binding.NodeID, in.Binding.ConnectionID)
					if err == nil || !strings.Contains(err.Error(), "skillhub_publication_captures_request_bytes_check") {
						t.Fatal("DB size constraint missing", err)
					}
					continue
				}
				first := recordCaptureForTest(t, db, in)
				g, token := issueUpload(t, db, u)
				if _, err := db.CheckSkillHubPublicationUploadGrant(ctx, token, u, g.Epoch); err != nil {
					t.Fatal("valid-sized receipt stranded before check", size, err)
				}
				if _, err := db.IntrospectSkillHubPublicationUploadGrant(ctx, token, u.Query(g.Epoch)); err != nil {
					t.Fatal("valid-sized grant not introspectable", size, err)
				}
				ageUpload(t, db, in.EntryID, "6 seconds")
				renewed, _ := issueUpload(t, db, u)
				if renewed.IdempotencyKey != g.IdempotencyKey || renewed.Epoch != 2 {
					t.Fatal("size boundary broke renewal")
				}
				if replay := recordCaptureForTest(t, db, in); *replay != *first {
					t.Fatal("size boundary changed receipt")
				}
			}
		})
	}
}

func TestPostgresSkillHubPublicationCaptureAuthority(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"unverified", "owner", "human_disabled", "bot_disabled", "body", "cancel", "source", "device", "install", "context"} {
		t.Run(mode, func(t *testing.T) {
			db, _, in, other := captureFixture(t)
			ctx := context.Background()
			switch mode {
			case "unverified":
				deliveryExec(t, db, `UPDATE skillhub_publication_outbox SET delivery_state='needs_user_action',source_verified_at=NULL,last_code='publication.source_changed'`)
			case "owner":
				deliveryExec(t, db, `UPDATE bot_config SET owner_id=$1 WHERE user_id=$2`, other, in.Binding.BotUID)
			case "human_disabled":
				deliveryExec(t, db, `UPDATE users SET state=1 WHERE id=$1`, in.Binding.ActorUID)
			case "bot_disabled":
				deliveryExec(t, db, `UPDATE bot_config SET enabled=false WHERE user_id=$1`, in.Binding.BotUID)
			case "body":
				deliveryExec(t, db, `UPDATE bot_config SET body_id='changed' WHERE user_id=$1`, in.Binding.BotUID)
			case "cancel":
				if _, e := db.CancelSkillHubPublicationEntry(ctx, in.Binding.ActorUID, in.EntryID); e != nil {
					t.Fatal(e)
				}
			case "source":
				in.Binding.Source.LocalSkillID = "other"
			case "device":
				in.Binding.Source.DeviceID = "other"
			case "install":
				in.Binding.Source.InstallationID = "other"
			case "context":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if r, e := db.RecordSkillHubPublicationCapture(ctx, in); e == nil || r != nil {
				t.Fatal("lost authority recorded", r, e)
			}
			var n int
			if e := db.db.QueryRow(`SELECT count(*) FROM skillhub_publication_captures`).Scan(&n); e != nil || n != 0 {
				t.Fatal("failed recording persisted", n, e)
			}
		})
	}
}

func TestPostgresSkillHubPublicationCaptureConcurrentReplayAndCancel(t *testing.T) {
	t.Parallel()
	for _, cancelled := range []bool{false, true} {
		db, _, in, _ := captureFixture(t)
		ctx := context.Background()
		start := make(chan struct{})
		var wg sync.WaitGroup
		results := make(chan *store.SkillHubPublicationCaptureRecord, 8)
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				if cancelled && i == 0 {
					if _, e := db.CancelSkillHubPublicationEntry(ctx, in.Binding.ActorUID, in.EntryID); e != nil {
						t.Error(e)
					}
					return
				}
				r, e := db.RecordSkillHubPublicationCapture(ctx, in)
				if e != nil && !errors.Is(e, store.ErrSkillHubPublicationSettled) {
					t.Error(e)
				}
				if r != nil {
					results <- r
				}
			}(i)
		}
		close(start)
		wg.Wait()
		close(results)
		var first *store.SkillHubPublicationCaptureRecord
		for r := range results {
			if first == nil {
				first = r
			}
			if *r != *first {
				t.Fatal("torn first receipt")
			}
		}
		var n int
		if e := db.db.QueryRow(`SELECT count(*) FROM skillhub_publication_captures`).Scan(&n); e != nil || n > 1 || (!cancelled && n != 1) {
			t.Fatal("receipt cardinality", n, e)
		}
		if cancelled {
			if _, e := db.RecordSkillHubPublicationCapture(ctx, in); !errors.Is(e, store.ErrSkillHubPublicationSettled) {
				t.Fatal("cancelled replay", e)
			}
			if _, _, e := db.IssueSkillHubPublicationUploadGrant(ctx, uploadForCapture(in)); !errors.Is(e, store.ErrSkillHubPublicationSettled) {
				t.Fatal("cancel lost", e)
			}
		}
	}
}

func TestPostgresSkillHubPublicationCaptureWriteFailure(t *testing.T) {
	t.Parallel()
	db, _, in, _ := captureFixture(t)
	deliveryExec(t, db, `CREATE FUNCTION fail_capture() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'synthetic capture failure'; END $$;
		CREATE TRIGGER fail_capture BEFORE INSERT ON skillhub_publication_captures FOR EACH ROW EXECUTE FUNCTION fail_capture();`)
	if r, e := db.RecordSkillHubPublicationCapture(context.Background(), in); e == nil || r != nil {
		t.Fatal("failed write returned evidence")
	}
	if _, _, e := db.IssueSkillHubPublicationUploadGrant(context.Background(), uploadForCapture(in)); !errors.Is(e, store.ErrSkillHubPublicationDenied) {
		t.Fatal("failed write authorized", e)
	}
	deliveryExec(t, db, `DROP TRIGGER fail_capture ON skillhub_publication_captures; DROP FUNCTION fail_capture()`)
	recordCaptureForTest(t, db, in)
	issueUpload(t, db, uploadForCapture(in))
}

func TestPostgresSkillHubPublicationCaptureRevokedReplay(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"cancel", "owner", "body", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			db, _, in, other := captureFixture(t)
			ctx := context.Background()
			first := recordCaptureForTest(t, db, in)
			switch mode {
			case "cancel":
				if _, err := db.CancelSkillHubPublicationEntry(ctx, in.Binding.ActorUID, in.EntryID); err != nil {
					t.Fatal(err)
				}
			case "owner":
				deliveryExec(t, db, `UPDATE bot_config SET owner_id=$1 WHERE user_id=$2`, other, in.Binding.BotUID)
			case "body":
				deliveryExec(t, db, `UPDATE bot_config SET body_id='changed' WHERE user_id=$1`, in.Binding.BotUID)
			case "disabled":
				deliveryExec(t, db, `UPDATE users SET state=1 WHERE id=$1`, in.Binding.ActorUID)
			}
			if r, err := db.RecordSkillHubPublicationCapture(ctx, in); err == nil || r != nil {
				t.Fatal("revoked replay returned evidence")
			}
			if _, _, err := db.IssueSkillHubPublicationUploadGrant(ctx, uploadForCapture(in)); err == nil {
				t.Fatal("revoked receipt minted a grant")
			}
			tx, err := db.db.BeginTx(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback()
			retained, err := readPublicationCapture(ctx, tx, in.EntryID)
			if err != nil || retained == nil || *retained != *first {
				t.Fatal("revocation deleted/changed evidence", err)
			}
		})
	}
}

func TestPostgresSkillHubPublicationCaptureConflictingRace(t *testing.T) {
	t.Parallel()
	db, _, in, _ := captureFixture(t)
	ctx := context.Background()
	start := make(chan struct{})
	type result struct {
		record *store.SkillHubPublicationCaptureRecord
		err    error
	}
	results := make(chan result, 2)
	other := in
	other.RequestHash = strings.Repeat("c", 64)
	for _, candidate := range []store.SkillHubPublicationCaptureInput{in, other} {
		go func(candidate store.SkillHubPublicationCaptureInput) {
			<-start
			r, err := db.RecordSkillHubPublicationCapture(ctx, candidate)
			results <- result{r, err}
		}(candidate)
	}
	close(start)
	wins, conflicts := 0, 0
	for n := 0; n < 2; n++ {
		r := <-results
		if r.err == nil && r.record != nil {
			wins++
		} else if errors.Is(r.err, store.ErrSkillHubPublicationConflict) {
			conflicts++
		} else {
			t.Fatal("unexpected race result", r.err)
		}
	}
	if wins != 1 || conflicts != 1 {
		t.Fatal("conflicting candidates both accepted", wins, conflicts)
	}
}

// Frozen pre-042 fixture. Do not use the current issuer to manufacture history
// (it now correctly requires evidence). No capture is inferred from this row.
func seedLegacyUploadGrant(t *testing.T, db *Adapter, in store.SkillHubPublicationUploadInput) (*store.SkillHubPublicationUploadGrant, store.SkillHubPublicationUploadToken) {
	t.Helper()
	ctx := context.Background()
	token, e := store.ParseSkillHubPublicationUploadToken("pubupload_" + strings.Repeat("f", 64))
	if e != nil {
		t.Fatal(e)
	}
	deliveryExec(t, db, `INSERT INTO skillhub_publication_upload_grants
		(entry_id,grant_id,issuer,audience,action,candidate_revision,request_hash,source_snapshot_hash,request_bytes,idempotency_key,token_hash,authority_epoch,route_generation,node_id,connection_id,issued_at,expires_at)
		VALUES ($1,$2,$3,$4,$5,1,$6,$7,$8,$9,$10,1,$11,$12,$13,clock_timestamp(),clock_timestamp()+interval '119 seconds')`,
		in.EntryID, "pubgrant_"+strings.Repeat("d", 32), in.Issuer, in.Audience, in.Action, in.RequestHash, in.SourceSnapshotHash, in.RequestBytes, "pubcandidate_"+strings.Repeat("e", 32), uploadTokenHash(token), in.Binding.Source.RouteGeneration, in.Binding.NodeID, in.Binding.ConnectionID)
	tx, e := db.db.BeginTx(ctx, nil)
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	g, _, e := readUploadGrant(ctx, tx, in.EntryID)
	if e != nil || g == nil {
		t.Fatal(e)
	}
	return g, token
}

func TestPostgresSkillHubPublicationCaptureMigrationNoBackfill(t *testing.T) {
	t.Parallel()
	for _, order := range []string{"sql_first", "startup_first"} {
		t.Run(order, func(t *testing.T) {
			db, _, in, _ := captureFixture(t)
			ctx := context.Background()
			up, down := captureMigration(t, "up"), captureMigration(t, "down")
			deliveryExec(t, db, down)
			deliveryExec(t, db, down)
			g, token := seedLegacyUploadGrant(t, db, uploadForCapture(in))
			if order == "startup_first" {
				if e := db.CreateSchema(); e != nil {
					t.Fatal(e)
				}
			}
			deliveryExec(t, db, up)
			deliveryExec(t, db, up)
			if e := db.CreateSchema(); e != nil {
				t.Fatal(e)
			}
			var n int
			if e := db.db.QueryRow(`SELECT count(*) FROM skillhub_publication_captures`).Scan(&n); e != nil || n != 0 {
				t.Fatal("fabricated historical evidence", n, e)
			}
			if _, e := db.CheckSkillHubPublicationUploadGrant(ctx, token, g.Input, 1); e != nil {
				t.Fatal("unexpired old grant destroyed", e)
			}
			ageUpload(t, db, in.EntryID, "6 seconds")
			if _, _, e := db.IssueSkillHubPublicationUploadGrant(ctx, g.Input); !errors.Is(e, store.ErrSkillHubPublicationDenied) {
				t.Fatal("old grant renewed without evidence", e)
			}
			bad := in
			bad.RequestHash = strings.Repeat("c", 64)
			if _, e := db.RecordSkillHubPublicationCapture(ctx, bad); !errors.Is(e, store.ErrSkillHubPublicationConflict) {
				t.Fatal("receipt disagreed with old grant", e)
			}
			first := recordCaptureForTest(t, db, in)
			issueUpload(t, db, g.Input)
			if _, e := db.db.Exec(down); e == nil {
				t.Fatal("retained evidence dropped")
			}
			deliveryExec(t, db, up)
			if e := db.CreateSchema(); e != nil {
				t.Fatal(e)
			}
			if got := recordCaptureForTest(t, db, in); *got != *first {
				t.Fatal("startup changed capture")
			}
		})
	}
}
