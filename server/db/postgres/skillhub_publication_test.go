package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func publicationEmptyDatabase(t *testing.T) (*Adapter, string) {
	t.Helper()
	dsn := os.Getenv("CATS_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("set CATS_PG_TEST_DSN for publication entry integration tests")
	}
	base := &Adapter{}
	if err := base.Open(dsn); err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("cats_publication_%d", time.Now().UnixNano())
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schema)); err != nil {
		base.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := base.db.Exec(`DROP SCHEMA ` + quoteIdent(schema) + ` CASCADE`); err != nil {
			t.Error(err)
		}
		base.Close()
	})
	dsn = dsnWithSearchPath(t, dsn, schema)
	db := &Adapter{}
	if err := db.Open(dsn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db, dsn
}

func publicationDatabase(t *testing.T) (*Adapter, string, store.SkillHubPublicationEntryInput, int64) {
	t.Helper()
	db, dsn := publicationEmptyDatabase(t)
	// Use the production initializer to catch missing table registration. A
	// second initialization must preserve entries and the admission lock.
	if err := db.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	create := func(name string, accountType types.AccountType) int64 {
		uid, err := db.CreateUser(&types.User{Username: name, AccountType: accountType, PassHash: []byte("test")})
		if err != nil {
			t.Fatal(err)
		}
		return uid
	}
	owner := create("publisher", types.AccountHuman)
	other := create("other", types.AccountHuman)
	bot := create("bot", types.AccountBot)
	if err := db.SaveBotConfigWithOwner(bot, owner, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`UPDATE bot_config SET body_id='body-1' WHERE user_id=$1`, bot); err != nil {
		t.Fatal(err)
	}
	return db, dsn, store.SkillHubPublicationEntryInput{ActorUID: owner, BotUID: bot, Entrypoint: "workspace", RequestID: "request-0001",
		Source: store.SkillHubPublicationSource{DeviceID: "device:1", BodyID: "body-1", InstallationID: "installation-1",
			RouteGeneration: 1, WorkspaceGeneration: "revision-1", LocalSkillID: "local:skill-1"}}, other
}

func publicationCounts(t *testing.T, db *Adapter, expected int) {
	t.Helper()
	for _, table := range []string{"skillhub_publication_entries", "skillhub_publication_outbox"} {
		var count int
		if err := db.db.QueryRow(`SELECT COUNT(*) FROM ` + table).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != expected {
			t.Fatalf("%s count=%d want=%d", table, count, expected)
		}
	}
}

func TestPostgresSkillHubPublicationPersistence(t *testing.T) {
	t.Parallel()
	db, dsn, in, other := publicationDatabase(t)
	ctx := context.Background()
	policy := store.SkillHubPublicationEntryPolicy{MaxEntries: 1, MaxEntriesPerActor: 1, MaxCreatesPerMinute: 1}
	e, replay, err := db.CreateSkillHubPublicationEntry(ctx, in, policy)
	if err != nil || replay || e == nil {
		t.Fatalf("create: entry=%v replay=%v err=%v", e, replay, err)
	}
	if e.Input != in || e.State != "waiting_runtime" || e.CreatedAt.IsZero() {
		t.Fatalf("lost facts: %+v", e)
	}
	publicationCounts(t, db, 1)
	if err := db.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	// Independent connection/Adapter models loss of the API process and response.
	reopened := &Adapter{}
	if err := reopened.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	again, replay, err := reopened.CreateSkillHubPublicationEntry(ctx, in, policy)
	if err != nil || !replay || again.ID != e.ID {
		t.Fatalf("replay at capacity: %v %v %v", again, replay, err)
	}
	read, err := reopened.GetSkillHubPublicationEntry(ctx, in.ActorUID, e.ID)
	if err != nil || read == nil || read.Input != in {
		t.Fatalf("read: %+v %v", read, err)
	}
	if got, err := reopened.GetSkillHubPublicationEntryByRequest(ctx, in.ActorUID, in.RequestID); err != nil || got == nil || got.ID != e.ID {
		t.Fatalf("lost response recovery: %+v %v", got, err)
	}
	for _, actor := range []int64{other, in.BotUID, 987654} {
		got, err := reopened.GetSkillHubPublicationEntry(ctx, actor, e.ID)
		if err != nil || got != nil {
			t.Fatalf("cross-account read: %+v %v", got, err)
		}
		if got, err := reopened.GetSkillHubPublicationEntryByRequest(ctx, actor, in.RequestID); err != nil || got != nil {
			t.Fatalf("cross-account recovery: %+v %v", got, err)
		}
	}
	changed := in
	changed.Source.LocalSkillID = "local:other"
	if _, _, err := reopened.CreateSkillHubPublicationEntry(ctx, changed, policy); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
		t.Fatalf("conflict: %v", err)
	}
	changed = in
	changed.RequestID = "request-0002"
	if _, _, err := reopened.CreateSkillHubPublicationEntry(ctx, changed, policy); !errors.Is(err, store.ErrSkillHubPublicationCapacity) {
		t.Fatalf("capacity: %v", err)
	}
	publicationCounts(t, db, 1)
	// Current authority is checked even for exact replay. History remains private
	// to its original publisher; transfer does not transfer the failed materials.
	if _, err := db.db.Exec(`UPDATE bot_config SET owner_id=$1 WHERE user_id=$2`, other, in.BotUID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.CreateSkillHubPublicationEntry(ctx, in, policy); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
		t.Fatalf("replay after transfer: %v", err)
	}
	if got, err := db.GetSkillHubPublicationEntry(ctx, in.ActorUID, e.ID); err != nil || got == nil {
		t.Fatalf("original history unavailable: %v", err)
	}
	if got, err := db.GetSkillHubPublicationEntryByRequest(ctx, in.ActorUID, in.RequestID); err != nil || got == nil {
		t.Fatalf("history recovery after transfer: %+v %v", got, err)
	}
	if _, err := db.db.Exec(`UPDATE users SET state=1 WHERE id=$1`, in.ActorUID); err != nil {
		t.Fatal(err)
	}
	if got, err := db.GetSkillHubPublicationEntry(ctx, in.ActorUID, e.ID); err != nil || got != nil {
		t.Fatalf("disabled account read: %v %v", got, err)
	}
	if got, err := db.GetSkillHubPublicationEntryByRequest(ctx, in.ActorUID, in.RequestID); err != nil || got != nil {
		t.Fatalf("disabled recovery: %+v %v", got, err)
	}
	if _, err := db.GetSkillHubPublicationEntryByRequest(ctx, in.ActorUID, "../invalid"); !errors.Is(err, store.ErrSkillHubPublicationInvalid) {
		t.Fatalf("invalid recovery key: %v", err)
	}
}

func TestPostgresSkillHubPublicationAuthority(t *testing.T) {
	t.Parallel()
	db, _, in, other := publicationDatabase(t)
	ctx := context.Background()
	policy := store.DefaultSkillHubPublicationEntryPolicy()
	for _, change := range []func(*store.SkillHubPublicationEntryInput){
		func(v *store.SkillHubPublicationEntryInput) { v.ActorUID = other },
		func(v *store.SkillHubPublicationEntryInput) { v.BotUID = other },
		func(v *store.SkillHubPublicationEntryInput) { v.Source.BodyID = "other-body" },
	} {
		bad := in
		change(&bad)
		if _, _, err := db.CreateSkillHubPublicationEntry(ctx, bad, policy); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
			t.Fatalf("authority: %v", err)
		}
	}
	for _, kind := range []string{"bot", "service"} {
		if _, err := db.db.Exec(`UPDATE users SET account_type=$1 WHERE id=$2`, kind, in.ActorUID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := db.CreateSkillHubPublicationEntry(ctx, in, policy); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
			t.Fatalf("non-human: %v", err)
		}
	}
	if _, err := db.db.Exec(`UPDATE users SET account_type='human', state=1 WHERE id=$1`, in.ActorUID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.CreateSkillHubPublicationEntry(ctx, in, policy); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
		t.Fatalf("inactive: %v", err)
	}
	if _, err := db.db.Exec(`UPDATE users SET state=0 WHERE id=$1`, in.ActorUID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`UPDATE bot_config SET enabled=false WHERE user_id=$1`, in.BotUID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.CreateSkillHubPublicationEntry(ctx, in, policy); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
		t.Fatalf("disabled Bot: %v", err)
	}
	publicationCounts(t, db, 0)
}

func TestPostgresSkillHubPublicationAtomicOutbox(t *testing.T) {
	t.Parallel()
	db, _, in, _ := publicationDatabase(t)
	ctx := context.Background()
	_, err := db.db.Exec(`CREATE FUNCTION reject_publication_event() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'injected outbox failure'; END; $$;
		CREATE TRIGGER reject_publication_event BEFORE INSERT ON skillhub_publication_outbox
		FOR EACH ROW EXECUTE FUNCTION reject_publication_event();`)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.CreateSkillHubPublicationEntry(ctx, in, store.DefaultSkillHubPublicationEntryPolicy()); err == nil {
		t.Fatal("outbox failure accepted")
	}
	publicationCounts(t, db, 0)
	if _, err := db.db.Exec(`DROP TRIGGER reject_publication_event ON skillhub_publication_outbox`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := db.CreateSkillHubPublicationEntry(ctx, in, store.DefaultSkillHubPublicationEntryPolicy()); err != nil {
		t.Fatal(err)
	}
	publicationCounts(t, db, 1)
	// No account FK may make the new inactive feature block existing account
	// deletion. Identity history stays inert; deleted actors cannot read it.
	if _, err := db.db.Exec(`DELETE FROM users WHERE id=$1`, in.ActorUID); err != nil {
		t.Fatal(err)
	}
	publicationCounts(t, db, 1)
}

func TestPostgresSkillHubPublicationConcurrentReplay(t *testing.T) {
	t.Parallel()
	db, dsn, in, _ := publicationDatabase(t)
	second := &Adapter{}
	if err := second.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var wg sync.WaitGroup
	ids := make(chan string, 8)
	errorsSeen := make(chan error, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			target := db
			if index%2 == 1 {
				target = second
			}
			e, _, err := target.CreateSkillHubPublicationEntry(ctx, in, store.DefaultSkillHubPublicationEntryPolicy())
			if err != nil {
				errorsSeen <- err
				return
			}
			ids <- e.ID
		}(i)
	}
	wg.Wait()
	close(ids)
	close(errorsSeen)
	for err := range errorsSeen {
		t.Error(err)
	}
	id := ""
	for got := range ids {
		if id != "" && id != got {
			t.Fatal("duplicate entry")
		}
		id = got
	}
	publicationCounts(t, db, 1)
	// Distinct intents racing for the last per-actor slot cannot both be admitted.
	policy := store.SkillHubPublicationEntryPolicy{MaxEntries: 10, MaxEntriesPerActor: 2, MaxCreatesPerMinute: 10}
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(index int) {
			changed := in
			changed.RequestID = fmt.Sprintf("request-race-%d", index)
			_, _, err := db.CreateSkillHubPublicationEntry(ctx, changed, policy)
			results <- err
		}(i)
	}
	a, b := <-results, <-results
	if !((a == nil && errors.Is(b, store.ErrSkillHubPublicationCapacity)) || (b == nil && errors.Is(a, store.ErrSkillHubPublicationCapacity))) {
		t.Fatalf("quota race: %v / %v", a, b)
	}
	publicationCounts(t, db, 2)
}

func TestPostgresSkillHubPublicationOwnerRace(t *testing.T) {
	t.Parallel()
	db, _, in, other := publicationDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `UPDATE bot_config SET owner_id=$1 WHERE user_id=$2`, other, in.BotUID); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	go func() {
		_, _, err := db.CreateSkillHubPublicationEntry(ctx, in, store.DefaultSkillHubPublicationEntryPolicy())
		result <- err
	}()
	select {
	case err := <-result:
		t.Fatalf("admitted against uncommitted authority: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, store.ErrSkillHubPublicationDenied) {
		t.Fatalf("stale owner admitted: %v", err)
	}
	publicationCounts(t, db, 0)
}

func TestPostgresSkillHubPublicationRateAndCancellation(t *testing.T) {
	t.Parallel()
	db, _, in, _ := publicationDatabase(t)
	ctx := context.Background()
	policy := store.SkillHubPublicationEntryPolicy{MaxEntries: 10, MaxEntriesPerActor: 10, MaxCreatesPerMinute: 1}
	if _, _, err := db.CreateSkillHubPublicationEntry(ctx, in, policy); err != nil {
		t.Fatal(err)
	}
	if _, replay, err := db.CreateSkillHubPublicationEntry(ctx, in, policy); err != nil || !replay {
		t.Fatalf("limited replay: %v", err)
	}
	in.RequestID = "request-0002"
	if _, _, err := db.CreateSkillHubPublicationEntry(ctx, in, policy); !errors.Is(err, store.ErrSkillHubPublicationRateLimit) {
		t.Fatalf("rate: %v", err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, _, err := db.CreateSkillHubPublicationEntry(cancelled, in, policy); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled context: %v", err)
	}
	publicationCounts(t, db, 1)
}

func TestPostgresSkillHubPublicationIdentityIsolation(t *testing.T) {
	t.Parallel()
	db, _, in, other := publicationDatabase(t)
	ctx := context.Background()
	policy := store.DefaultSkillHubPublicationEntryPolicy()
	first, _, err := db.CreateSkillHubPublicationEntry(ctx, in, policy)
	if err != nil {
		t.Fatal(err)
	}
	for i, owner := range []int64{in.ActorUID, other} {
		bot, err := db.CreateUser(&types.User{Username: fmt.Sprintf("extra-bot-%d", i), DisplayName: "Same Bot Name", AccountType: types.AccountBot, PassHash: []byte("test")})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.SaveBotConfigWithOwner(bot, owner, "", ""); err != nil {
			t.Fatal(err)
		}
		if _, err := db.db.Exec(`UPDATE bot_config SET body_id='body-1' WHERE user_id=$1`, bot); err != nil {
			t.Fatal(err)
		}
		changed := in
		changed.ActorUID, changed.BotUID = owner, bot
		e, replay, err := db.CreateSkillHubPublicationEntry(ctx, changed, policy)
		if i == 0 {
			if !errors.Is(err, store.ErrSkillHubPublicationConflict) {
				t.Fatalf("same-key Bot substitution: %v", err)
			}
		} else if err != nil || replay || e.ID == first.ID {
			t.Fatalf("different publisher not isolated: %+v %v %v", e, replay, err)
		}
	}
	publicationCounts(t, db, 2)
}
