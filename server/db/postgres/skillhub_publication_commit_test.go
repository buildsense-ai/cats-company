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

func publicationCommitMigration(t *testing.T, direction string) string {
	t.Helper()
	b, err := os.ReadFile("../migrations/postgres/000040_skillhub_publication_commit_permits." + direction + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func TestSkillHubPublicationCommitMigrationMatchesStartup(t *testing.T) {
	if strings.Join(strings.Fields(publicationCommitMigration(t, "up")), " ") != strings.Join(strings.Fields(createSkillHubPublicationCommitTables), " ") {
		t.Fatal("000040 differs from startup")
	}
}
func commitFixture(t *testing.T) (*Adapter, string, store.SkillHubPublicationUploadInput, store.SkillHubPublicationCommitInput, int64) {
	t.Helper()
	db, dsn, in, other := uploadGrantFixture(t)
	g, _ := issueUpload(t, db, in)
	r := receiptForGrant(g)
	recordReceipt(t, db, r, false)
	c := store.SkillHubPublicationCommitInput{Receipt: r, Target: store.SkillHubPublicationCommitTarget{
		CandidateHash: strings.Repeat("c", 64), ManifestHash: strings.Repeat("d", 64), PublisherUserID: "usr_example", PublisherProfileID: "pub_example",
		Namespace: "publisher", SkillID: "publisher/example", Version: "1.0.1", UploadID: "upl_example", ReleaseAt: "2026-10-19T00:00:00.000Z", ConfirmVersionPublish: true}}
	return db, dsn, in, c, other
}
func recordCommit(t *testing.T, db *Adapter, actor int64, c store.SkillHubPublicationCommitInput) *store.SkillHubPublicationCommitIntent {
	t.Helper()
	i, err := db.RecordSkillHubPublicationCommitIntent(context.Background(), actor, c)
	if err != nil || i == nil || i.Input != c {
		t.Fatalf("record: %+v %v", i, err)
	}
	return i
}
func authorizeCommit(t *testing.T, db *Adapter, c store.SkillHubPublicationCommitInput) *store.SkillHubPublicationCommitPermit {
	t.Helper()
	p, err := db.AuthorizeSkillHubPublicationCommit(context.Background(), c)
	if err != nil || p == nil || p.Intent.Input != c || p.ID == "" || p.AuthorizedAt.IsZero() {
		t.Fatalf("authorize: %+v %v", p, err)
	}
	return p
}
func TestPostgresSkillHubPublicationCommitLifecycle(t *testing.T) {
	t.Parallel()
	db, dsn, in, c, _ := commitFixture(t)
	ctx := context.Background()
	if _, err := db.AuthorizeSkillHubPublicationCommit(ctx, c); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
		t.Fatal("permit without intent", err)
	}
	first := recordCommit(t, db, in.Binding.ActorUID, c)
	second := recordCommit(t, db, in.Binding.ActorUID, c)
	if *first != *second {
		t.Fatal("intent changed on replay")
	}
	// Ordinary upload expiry/offline transport is not post-upload revocation.
	ageUpload(t, db, in.EntryID, "121 seconds")
	deliveryExec(t, db, `UPDATE bot_connection_generations SET generation=generation+1 WHERE bot_uid=$1`, in.Binding.BotUID)
	p := authorizeCommit(t, db, c)
	if p.AuthorityEpoch != 1 {
		t.Fatal("epoch")
	}
	reopened := &Adapter{}
	if err := reopened.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	if got := authorizeCommit(t, reopened, c); *got != *p {
		t.Fatal("lost reply/restart changed permit")
	}
	got, err := reopened.GetSkillHubPublicationCommitPermit(ctx, in.Binding.ActorUID, in.EntryID)
	if err != nil || got == nil || *got != *p {
		t.Fatal("private history", err)
	}
	// This ledger is authorization only: no local entry state/publication write.
	e, err := db.GetSkillHubPublicationEntry(ctx, in.Binding.ActorUID, in.EntryID)
	if err != nil || e.State != "waiting_runtime" {
		t.Fatal("permit changed entry", err)
	}
}
func TestPostgresSkillHubPublicationCommitExactScope(t *testing.T) {
	t.Parallel()
	db, _, in, c, other := commitFixture(t)
	ctx := context.Background()
	if _, err := db.RecordSkillHubPublicationCommitIntent(ctx, other, c); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
		t.Fatal("other actor", err)
	}
	recordCommit(t, db, in.Binding.ActorUID, c)
	for name, change := range map[string]func(*store.SkillHubPublicationCommitInput){
		"task": func(x *store.SkillHubPublicationCommitInput) {
			x.Receipt.TaskID = "pubtask_99999999-1234-4234-8234-123456789abc"
		},
		"event": func(x *store.SkillHubPublicationCommitInput) {
			x.Receipt.EventID = "pubevent_99999999-1234-4234-8234-123456789abc"
		},
		"issuer":    func(x *store.SkillHubPublicationCommitInput) { x.Receipt.Query.Issuer = "https://other.example" },
		"audience":  func(x *store.SkillHubPublicationCommitInput) { x.Receipt.Query.Audience = "https://other.example" },
		"epoch":     func(x *store.SkillHubPublicationCommitInput) { x.Receipt.Query.Epoch++ },
		"candidate": func(x *store.SkillHubPublicationCommitInput) { x.Target.CandidateHash = strings.Repeat("f", 64) },
		"manifest":  func(x *store.SkillHubPublicationCommitInput) { x.Target.ManifestHash = strings.Repeat("f", 64) },
		"publisher": func(x *store.SkillHubPublicationCommitInput) { x.Target.PublisherUserID = "usr_other" },
		"profile":   func(x *store.SkillHubPublicationCommitInput) { x.Target.PublisherProfileID = "pub_other" },
		"namespace": func(x *store.SkillHubPublicationCommitInput) {
			x.Target.Namespace = "other"
			x.Target.SkillID = "other/example"
		},
		"skill":        func(x *store.SkillHubPublicationCommitInput) { x.Target.SkillID = "publisher/other" },
		"version":      func(x *store.SkillHubPublicationCommitInput) { x.Target.Version = "1.0.2" },
		"upload":       func(x *store.SkillHubPublicationCommitInput) { x.Target.UploadID = "upl_other" },
		"release":      func(x *store.SkillHubPublicationCommitInput) { x.Target.ReleaseAt = "2026-10-20T00:00:00.000Z" },
		"confirmation": func(x *store.SkillHubPublicationCommitInput) { x.Target.ConfirmVersionPublish = false },
	} {
		t.Run(name, func(t *testing.T) {
			x := c
			change(&x)
			if _, err := db.RecordSkillHubPublicationCommitIntent(ctx, in.Binding.ActorUID, x); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
				t.Fatal("changed intent accepted", err)
			}
			if _, err := db.AuthorizeSkillHubPublicationCommit(ctx, x); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
				t.Fatal("changed permit accepted", err)
			}
		})
	}
	authorizeCommit(t, db, c)
	changed := c
	changed.Target.Version = "2.0.0"
	if _, err := db.AuthorizeSkillHubPublicationCommit(ctx, changed); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
		t.Fatal("permit repurposed", err)
	}
}
func TestPostgresSkillHubPublicationCommitAuthorityAndHistory(t *testing.T) {
	t.Parallel()
	for _, change := range []string{"cancel", "human_disabled", "bot_disabled", "bot_account_disabled", "transfer", "body"} {
		t.Run(change, func(t *testing.T) {
			for _, after := range []bool{false, true} {
				db, _, in, c, other := commitFixture(t)
				ctx := context.Background()
				recordCommit(t, db, in.Binding.ActorUID, c)
				var p *store.SkillHubPublicationCommitPermit
				if after {
					p = authorizeCommit(t, db, c)
				}
				switch change {
				case "cancel":
					requestCancelIntent(t, db, in.Binding.ActorUID, in.EntryID, "ready")
				case "human_disabled":
					deliveryExec(t, db, `UPDATE users SET state=1 WHERE id=$1`, in.Binding.ActorUID)
				case "bot_disabled":
					deliveryExec(t, db, `UPDATE bot_config SET enabled=false WHERE user_id=$1`, in.Binding.BotUID)
				case "bot_account_disabled":
					deliveryExec(t, db, `UPDATE users SET state=1 WHERE id=$1`, in.Binding.BotUID)
				case "transfer":
					deliveryExec(t, db, `UPDATE bot_config SET owner_id=$1 WHERE user_id=$2`, other, in.Binding.BotUID)
				case "body":
					deliveryExec(t, db, `UPDATE bot_config SET body_id='changed' WHERE user_id=$1`, in.Binding.BotUID)
				}
				got, err := db.AuthorizeSkillHubPublicationCommit(ctx, c)
				if after {
					if err != nil || got == nil || *got != *p {
						t.Fatal("historical replay changed", err)
					}
				} else if err == nil {
					t.Fatal("new authority granted after revocation")
				}
				got, err = db.GetSkillHubPublicationCommitPermit(ctx, in.Binding.ActorUID, in.EntryID)
				if err != nil {
					t.Fatal(err)
				}
				if change == "human_disabled" || !after {
					if got != nil {
						t.Fatal("private history leaked")
					}
				} else if got == nil || *got != *p {
					t.Fatal("history lost")
				}
				for _, actor := range []int64{other, in.Binding.BotUID} {
					if r, err := db.GetSkillHubPublicationCommitPermit(ctx, actor, in.EntryID); err != nil || r != nil {
						t.Fatal("new owner/friend/Bot inherited permit", err)
					}
				}
			}
		})
	}
}
func TestPostgresSkillHubPublicationCommitConcurrency(t *testing.T) {
	t.Parallel()
	db, _, in, c, _ := commitFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	permits := make(chan *store.SkillHubPublicationCommitPermit, 8)
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := db.RecordSkillHubPublicationCommitIntent(ctx, in.Binding.ActorUID, c); err != nil {
				t.Error(err)
				return
			}
			p, err := db.AuthorizeSkillHubPublicationCommit(ctx, c)
			if err != nil {
				t.Error(err)
				return
			}
			permits <- p
		}()
	}
	wg.Wait()
	close(permits)
	var first *store.SkillHubPublicationCommitPermit
	count := 0
	for p := range permits {
		count++
		if first == nil {
			first = p
		} else if *first != *p {
			t.Error("parallel grant created another permit")
		}
	}
	if count != 8 {
		t.Fatal("incomplete parallel results")
	}
	// Exercise both serial orders and truly racing calls. Cancellation always
	// remains a local intent; an issued permit is not undone or reissued.
	for _, order := range []string{"cancel_first", "permit_first", "concurrent"} {
		t.Run(order, func(t *testing.T) {
			db, _, in, c, _ := commitFixture(t)
			recordCommit(t, db, in.Binding.ActorUID, c)
			var p *store.SkillHubPublicationCommitPermit
			var permitErr, cancelErr error
			grant := func() { p, permitErr = db.AuthorizeSkillHubPublicationCommit(ctx, c) }
			cancel := func() { _, cancelErr = db.RequestSkillHubPublicationCancellation(ctx, in.Binding.ActorUID, in.EntryID) }
			switch order {
			case "cancel_first":
				cancel()
				grant()
			case "permit_first":
				grant()
				cancel()
			default:
				wg.Add(2)
				go func() { defer wg.Done(); grant() }()
				go func() { defer wg.Done(); cancel() }()
				wg.Wait()
			}
			if cancelErr != nil {
				t.Fatal(cancelErr)
			}
			if order == "cancel_first" && permitErr == nil {
				t.Fatal("cancel did not win")
			}
			if order == "permit_first" && permitErr != nil {
				t.Fatal(permitErr)
			}
			if permitErr != nil && !errors.Is(permitErr, store.ErrSkillHubPublicationSettled) {
				t.Fatal(permitErr)
			}
			got, err := db.GetSkillHubPublicationCommitPermit(ctx, in.Binding.ActorUID, in.EntryID)
			if err != nil {
				t.Fatal(err)
			}
			if p == nil {
				if got != nil {
					t.Fatal("cancelled entry has new permit")
				}
			} else if got == nil || *got != *p {
				t.Fatal("permit lost")
			}
			l := claimCancellation(t, db, in)
			if l.Receipt != c.Receipt {
				t.Fatal("cancel delivery changed task")
			}
		})
	}
}
func TestPostgresSkillHubPublicationCommitMigrationAndGuards(t *testing.T) {
	t.Parallel()
	db, _, in, c, _ := commitFixture(t)
	up, down := publicationCommitMigration(t, "up"), publicationCommitMigration(t, "down")
	deliveryExec(t, db, commitDeliveryMigration(t, "down")) // reverse migration dependency order
	for _, statement := range []string{down, down, up, up, down, up} {
		deliveryExec(t, db, statement)
	}
	if err := db.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := db.db.QueryRow(`SELECT count(*) FROM skillhub_publication_commit_intents`).Scan(&count); err != nil || count != 0 {
		t.Fatal("history auto-authorized", err)
	}
	recordCommit(t, db, in.Binding.ActorUID, c)
	deliveryExec(t, db, commitDeliveryMigration(t, "down")) // test 040 retention, not a dependent FK failure
	if _, err := db.db.Exec(down); err == nil {
		t.Fatal("removed retained intent")
	}
	p := authorizeCommit(t, db, c)
	if _, err := db.db.Exec(down); err == nil {
		t.Fatal("removed retained permit")
	}
	for _, statement := range []string{
		`UPDATE skillhub_publication_commit_intents SET identity_hash=repeat('a',64) WHERE entry_id=$1`,
		`UPDATE skillhub_publication_commit_intents SET target='{}' WHERE entry_id=$1`,
		`DELETE FROM skillhub_publication_commit_intents WHERE entry_id=$1`,
		`UPDATE skillhub_publication_commit_permits SET authority_epoch=2 WHERE entry_id=$1`,
		`UPDATE skillhub_publication_commit_permits SET authorized_at=clock_timestamp() WHERE entry_id=$1`,
		`DELETE FROM skillhub_publication_commit_permits WHERE entry_id=$1`,
	} {
		if _, err := db.db.Exec(statement, in.EntryID); err == nil {
			t.Fatal("immutable evidence modified")
		}
	}
	deliveryExec(t, db, up)
	if err := db.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	if got := authorizeCommit(t, db, c); *got != *p {
		t.Fatal("startup rewrote permit")
	}
}
func TestPostgresSkillHubPublicationCommitFailureAtomicity(t *testing.T) {
	t.Parallel()
	db, _, in, c, _ := commitFixture(t)
	ctx := context.Background()
	deliveryExec(t, db, commitDeliveryMigration(t, "down"))
	deliveryExec(t, db, publicationCommitMigration(t, "down"))
	if _, err := db.RecordSkillHubPublicationCommitIntent(ctx, in.Binding.ActorUID, c); err == nil {
		t.Fatal("missing schema succeeded")
	}
	if _, err := db.AuthorizeSkillHubPublicationCommit(ctx, c); err == nil {
		t.Fatal("missing schema authorized")
	}
	if err := db.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	recordCommit(t, db, in.Binding.ActorUID, c)
	deliveryExec(t, db, `CREATE FUNCTION fail_commit_permit() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected failure'; END $$;
		CREATE TRIGGER fail_commit_permit AFTER INSERT ON skillhub_publication_commit_permits FOR EACH ROW EXECUTE FUNCTION fail_commit_permit()`)
	if _, err := db.AuthorizeSkillHubPublicationCommit(ctx, c); err == nil {
		t.Fatal("write failure authorized")
	}
	var count int
	if err := db.db.QueryRow(`SELECT count(*) FROM skillhub_publication_commit_permits`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial permit", err)
	}
	deliveryExec(t, db, `DROP TRIGGER fail_commit_permit ON skillhub_publication_commit_permits; DROP FUNCTION fail_commit_permit()`)
	authorizeCommit(t, db, c)
}

func TestPostgresSkillHubPublicationCommitRequiresReceiptAndRetainsEpoch(t *testing.T) {
	t.Parallel()
	db, _, in, other := uploadGrantFixture(t)
	ctx := context.Background()
	g, _ := issueUpload(t, db, in)
	c := store.SkillHubPublicationCommitInput{Receipt: receiptForGrant(g), Target: store.SkillHubPublicationCommitTarget{
		CandidateHash: strings.Repeat("c", 64), ManifestHash: strings.Repeat("d", 64), PublisherUserID: "usr_example", PublisherProfileID: "pub_example",
		Namespace: "publisher", SkillID: "publisher/example", Version: "1.0.1", UploadID: "upl_example", ReleaseAt: "2026-10-19T00:00:00.000Z"}}
	if _, err := db.RecordSkillHubPublicationCommitIntent(ctx, in.Binding.ActorUID, c); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
		t.Fatal("fabricated receipt accepted", err)
	}
	if _, err := db.AuthorizeSkillHubPublicationCommit(ctx, c); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
		t.Fatal("unrecorded receipt authorized", err)
	}
	recordReceipt(t, db, c.Receipt, false)
	ageUpload(t, db, in.EntryID, "6 seconds")
	issueUpload(t, db, in)
	recordCommit(t, db, in.Binding.ActorUID, c)
	p := authorizeCommit(t, db, c)
	if p.AuthorityEpoch != 2 || p.Intent.Input.Receipt.Query.Epoch != 1 {
		t.Fatal("live epoch confused with acceptance epoch")
	}
	ageUpload(t, db, in.EntryID, "6 seconds")
	issueUpload(t, db, in)
	if got := authorizeCommit(t, db, c); *got != *p {
		t.Fatal("rotation changed old permit")
	}
	deliveryExec(t, db, `UPDATE bot_config SET owner_id=$1 WHERE user_id=$2`, other, in.Binding.BotUID)
	if _, err := db.RecordSkillHubPublicationCommitIntent(ctx, in.Binding.ActorUID, c); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
		t.Fatal("record replay obtained new action authority", err)
	}
}
