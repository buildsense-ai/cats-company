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

func commitDeliveryMigration(t *testing.T, direction string) string {
	t.Helper()
	b, e := os.ReadFile("../migrations/postgres/000041_skillhub_publication_commit_deliveries." + direction + ".sql")
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func TestSkillHubPublicationCommitDeliveryMigrationMatchesStartup(t *testing.T) {
	if strings.Join(strings.Fields(commitDeliveryMigration(t, "up")), " ") != strings.Join(strings.Fields(createSkillHubPublicationCommitDeliveriesTable), " ") {
		t.Fatal("041 differs from startup")
	}
}
func commitDeliveryScope(c store.SkillHubPublicationCommitInput) store.SkillHubPublicationCancellationScope {
	return store.SkillHubPublicationCancellationScope{Issuer: c.Receipt.Query.Issuer, Audience: c.Receipt.Query.Audience}
}
func claimCommitDelivery(t *testing.T, db *Adapter, c store.SkillHubPublicationCommitInput) *store.SkillHubPublicationCommitLease {
	t.Helper()
	l, e := db.ClaimSkillHubPublicationCommitDelivery(context.Background(), commitDeliveryScope(c))
	if e != nil || l == nil || l.Validate() != nil || l.Intent.Input != c {
		t.Fatalf("claim %+v %v", l, e)
	}
	return l
}
func TestPostgresSkillHubPublicationCommitDeliveryLifecycleAndRecovery(t *testing.T) {
	t.Parallel()
	db, dsn, in, c, other := commitFixture(t)
	ctx := context.Background()
	if l, e := db.ClaimSkillHubPublicationCommitDelivery(ctx, commitDeliveryScope(c)); e != nil || l != nil {
		t.Fatal("receipt is not intent", e)
	}
	i := recordCommit(t, db, in.Binding.ActorUID, c)
	wrong := commitDeliveryScope(c)
	wrong.Audience = "https://unrelated.example"
	if l, e := db.ClaimSkillHubPublicationCommitDelivery(ctx, wrong); e != nil || l != nil {
		t.Fatal("wrong peer selected", e)
	}
	l := claimCommitDelivery(t, db, c)
	if l.Attempt != 1 || l.Intent != *i {
		t.Fatal("identity")
	}
	if next, e := db.ClaimSkillHubPublicationCommitDelivery(ctx, commitDeliveryScope(c)); e != nil || next != nil {
		t.Fatal("stole live lease", e)
	}
	if e := db.FinishSkillHubPublicationCommitDelivery(ctx, *l, store.SkillHubPublicationCommitOutcome{Kind: "accepted", PermitID: "pubpermit_" + strings.Repeat("a", 32)}); e == nil {
		t.Fatal("fake ACK accepted without local permit")
	}
	p := authorizeCommit(t, db, c)
	deliveryExec(t, db, `UPDATE skillhub_publication_commit_deliveries SET lease_until=clock_timestamp()-interval '1 second' WHERE entry_id=$1`, in.EntryID)
	reopened := &Adapter{}
	if e := reopened.Open(dsn); e != nil {
		t.Fatal(e)
	}
	defer reopened.Close()
	second := claimCommitDelivery(t, reopened, c)
	if second.Attempt != 2 || second.Token == l.Token || second.Intent != l.Intent {
		t.Fatal("restart identity/budget")
	}
	if e := db.FinishSkillHubPublicationCommitDelivery(ctx, *l, store.SkillHubPublicationCommitOutcome{Kind: "retry"}); !errors.Is(e, store.ErrSkillHubPublicationStale) {
		t.Fatal("stale completion", e)
	}
	// Historical permit delivery survives ownership/runtime changes; this does
	// not bypass the permit service's checks for first issuance.
	deliveryExec(t, db, `UPDATE bot_config SET owner_id=$1,enabled=false WHERE user_id=$2`, other, in.Binding.BotUID)
	if e := reopened.FinishSkillHubPublicationCommitDelivery(ctx, *second, store.SkillHubPublicationCommitOutcome{Kind: "accepted", PermitID: p.ID}); e != nil {
		t.Fatal(e)
	}
	s, e := db.GetSkillHubPublicationCommitDelivery(ctx, in.Binding.ActorUID, in.EntryID)
	if e != nil || s == nil || s.State != "accepted" || s.Attempts != 2 {
		t.Fatal("state", s, e)
	}
	if s, e := db.GetSkillHubPublicationCommitDelivery(ctx, other, in.EntryID); e != nil || s != nil {
		t.Fatal("new owner disclosure", e)
	}
	if l, e := db.ClaimSkillHubPublicationCommitDelivery(ctx, commitDeliveryScope(c)); e != nil || l != nil {
		t.Fatal("terminal replay", e)
	}
}

func TestPostgresSkillHubPublicationCommitDeliveryConcurrencyAndBudget(t *testing.T) {
	t.Parallel()
	db, _, in, c, _ := commitFixture(t)
	ctx := context.Background()
	recordCommit(t, db, in.Binding.ActorUID, c)
	var wg sync.WaitGroup
	leases := make(chan *store.SkillHubPublicationCommitLease, 8)
	for n := 0; n < 8; n++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l, e := db.ClaimSkillHubPublicationCommitDelivery(ctx, commitDeliveryScope(c))
			if e != nil {
				t.Error(e)
			}
			if l != nil {
				leases <- l
			}
		}()
	}
	wg.Wait()
	close(leases)
	var first *store.SkillHubPublicationCommitLease
	count := 0
	for l := range leases {
		count++
		first = l
	}
	if count != 1 {
		t.Fatal("duplicate claim", count)
	}
	for n := 1; n <= 8; n++ {
		l := first
		if n > 1 {
			l = claimCommitDelivery(t, db, c)
		}
		if l.Attempt != n {
			t.Fatal("attempt reset")
		}
		if e := db.FinishSkillHubPublicationCommitDelivery(ctx, *l, store.SkillHubPublicationCommitOutcome{Kind: "retry"}); e != nil {
			t.Fatal(e)
		}
		if next, e := db.ClaimSkillHubPublicationCommitDelivery(ctx, commitDeliveryScope(c)); e != nil || next != nil {
			t.Fatal("backoff ignored", e)
		}
		if n < 8 {
			deliveryExec(t, db, `UPDATE skillhub_publication_commit_deliveries SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE entry_id=$1`, in.EntryID)
		}
	}
	recordCommit(t, db, in.Binding.ActorUID, c)
	s, e := db.GetSkillHubPublicationCommitDelivery(ctx, in.Binding.ActorUID, in.EntryID)
	if e != nil || s.State != "exhausted" || s.Attempts != 8 {
		t.Fatal("not exhausted", s, e)
	}
	if _, e := db.db.Exec(`UPDATE skillhub_publication_commit_deliveries SET attempts=1,state='pending' WHERE entry_id=$1`, in.EntryID); e == nil {
		t.Fatal("budget reset")
	}
}

func TestPostgresSkillHubPublicationCommitDeliveryMigrationAndTerminalEvidence(t *testing.T) {
	t.Parallel()
	db, _, in, c, _ := commitFixture(t)
	ctx := context.Background()
	down, up := commitDeliveryMigration(t, "down"), commitDeliveryMigration(t, "up")
	deliveryExec(t, db, down)
	deliveryExec(t, db, down)
	recordCommit(t, db, in.Binding.ActorUID, c)
	deliveryExec(t, db, up)
	deliveryExec(t, db, up)
	if e := db.CreateSchema(); e != nil {
		t.Fatal(e)
	}
	var count int
	if e := db.db.QueryRow(`SELECT count(*) FROM skillhub_publication_commit_deliveries`).Scan(&count); e != nil || count != 0 {
		t.Fatal("migration manufactured scheduling evidence", e)
	}
	l := claimCommitDelivery(t, db, c)
	if e := db.FinishSkillHubPublicationCommitDelivery(ctx, *l, store.SkillHubPublicationCommitOutcome{Kind: "denied"}); e != nil {
		t.Fatal(e)
	}
	for _, sql := range []string{down, `DELETE FROM skillhub_publication_commit_deliveries`, `UPDATE skillhub_publication_commit_deliveries SET last_code='retry'`} {
		if _, e := db.db.Exec(sql); e == nil {
			t.Fatal("retained evidence removed/reset")
		}
	}
	if e := db.CreateSchema(); e != nil {
		t.Fatal(e)
	}
	s, e := db.GetSkillHubPublicationCommitDelivery(ctx, in.Binding.ActorUID, in.EntryID)
	if e != nil || s.State != "needs_attention" || s.LastCode != "denied" {
		t.Fatal(s, e)
	}
	for _, actor := range []int64{in.Binding.BotUID, in.Binding.ActorUID} {
		if actor == in.Binding.ActorUID {
			deliveryExec(t, db, `UPDATE users SET state=1 WHERE id=$1`, actor)
		}
		if s, e := db.GetSkillHubPublicationCommitDelivery(ctx, actor, in.EntryID); e != nil || s != nil {
			t.Fatal("nonactive-human disclosure", e)
		}
	}
}

func TestPostgresSkillHubPublicationCommitDeliveryExpiryAndWriteFailure(t *testing.T) {
	t.Parallel()
	db, _, in, c, _ := commitFixture(t)
	ctx := context.Background()
	recordCommit(t, db, in.Binding.ActorUID, c)
	// Claim failure must neither spend an attempt nor lose the immutable intent.
	deliveryExec(t, db, `CREATE FUNCTION fail_commit_delivery() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN RAISE EXCEPTION 'injected'; END $$;
CREATE TRIGGER fail_commit_delivery AFTER INSERT ON skillhub_publication_commit_deliveries FOR EACH ROW EXECUTE FUNCTION fail_commit_delivery()`)
	if _, e := db.ClaimSkillHubPublicationCommitDelivery(ctx, commitDeliveryScope(c)); e == nil {
		t.Fatal("injected claim succeeded")
	}
	deliveryExec(t, db, `DROP TRIGGER fail_commit_delivery ON skillhub_publication_commit_deliveries; DROP FUNCTION fail_commit_delivery()`)
	l := claimCommitDelivery(t, db, c)
	if l.Attempt != 1 {
		t.Fatal("rollback consumed budget")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if e := db.FinishSkillHubPublicationCommitDelivery(cancelled, *l, store.SkillHubPublicationCommitOutcome{Kind: "retry"}); e == nil {
		t.Fatal("cancelled write succeeded")
	}
	for n := 1; n <= 8; n++ {
		if n > 1 {
			l = claimCommitDelivery(t, db, c)
		}
		if l.Attempt != n {
			t.Fatal("lease loss reset budget")
		}
		deliveryExec(t, db, `UPDATE skillhub_publication_commit_deliveries SET lease_until=clock_timestamp()-interval '1 second' WHERE entry_id=$1`, in.EntryID)
	}
	if l, e := db.ClaimSkillHubPublicationCommitDelivery(ctx, commitDeliveryScope(c)); e != nil || l != nil {
		t.Fatal("ninth attempt", e)
	}
	s, e := db.GetSkillHubPublicationCommitDelivery(ctx, in.Binding.ActorUID, in.EntryID)
	if e != nil || s == nil || s.State != "exhausted" || s.Attempts != 8 {
		t.Fatal("expired final lease", s, e)
	}
}
