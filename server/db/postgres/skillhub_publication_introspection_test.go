package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func TestPostgresSkillHubPublicationIntrospectionIdentityAndReadOnly(t *testing.T) {
	t.Parallel()
	db, dsn, in, _ := uploadGrantFixture(t)
	g, token := issueUpload(t, db, in)
	ctx := context.Background()
	snapshot := func() string {
		t.Helper()
		var s string
		if err := db.db.QueryRow(`SELECT json_build_array(row_to_json(e),row_to_json(g),row_to_json(o))::text
			FROM skillhub_publication_entries e JOIN skillhub_publication_upload_grants g USING(entry_id)
			JOIN skillhub_publication_outbox o USING(entry_id) WHERE e.entry_id=$1`, in.EntryID).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := snapshot()
	reopened := &Adapter{}
	if err := reopened.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	for i := 0; i < 2; i++ {
		o, err := reopened.IntrospectSkillHubPublicationUploadGrant(ctx, token, in.Query(g.Epoch))
		if err != nil || o == nil || o.Grant.Input != in || o.ObservedAt.Before(g.IssuedAt) || !o.ObservedAt.Before(g.ExpiresAt) {
			t.Fatalf("introspection: %v", err)
		}
	}
	for _, mode := range []string{"hash", "audience", "epoch", "missing", "token"} {
		q := in.Query(g.Epoch)
		bearer := token
		switch mode {
		case "hash":
			q.RequestHash = strings.Repeat("e", 64)
		case "audience":
			q.Audience = "https://other.example"
		case "epoch":
			q.Epoch++
		case "missing":
			q.EntryID = "pubentry_" + strings.Repeat("e", 32)
		case "token":
			bearer, _ = store.ParseSkillHubPublicationUploadToken("pubupload_" + strings.Repeat("e", 64))
		}
		if _, err := reopened.IntrospectSkillHubPublicationUploadGrant(ctx, bearer, q); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
			t.Fatalf("%s: %v", mode, err)
		}
	}
	if before != snapshot() {
		t.Fatal("read changed durable state")
	}
}

func TestPostgresSkillHubPublicationIntrospectionAuthority(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"cancel", "disabled", "transfer", "rebind", "reconnect", "expired", "rotate"} {
		t.Run(mode, func(t *testing.T) {
			db, _, in, other := uploadGrantFixture(t)
			g, token := issueUpload(t, db, in)
			ctx := context.Background()
			switch mode {
			case "cancel":
				if _, err := db.CancelSkillHubPublicationEntry(ctx, in.Binding.ActorUID, in.EntryID); err != nil {
					t.Fatal(err)
				}
			case "disabled":
				deliveryExec(t, db, `UPDATE users SET state=1 WHERE id=$1`, in.Binding.ActorUID)
			case "transfer":
				deliveryExec(t, db, `UPDATE bot_config SET owner_id=$1 WHERE user_id=$2`, other, in.Binding.BotUID)
			case "rebind":
				deliveryExec(t, db, `UPDATE bot_config SET body_id='other' WHERE user_id=$1`, in.Binding.BotUID)
			case "reconnect":
				if _, err := db.BumpBotConnectionGeneration(in.Binding.BotUID); err != nil {
					t.Fatal(err)
				}
			case "expired":
				ageUpload(t, db, in.EntryID, "121 seconds")
			case "rotate":
				ageUpload(t, db, in.EntryID, "6 seconds")
				issueUpload(t, db, in)
			}
			if _, err := db.IntrospectSkillHubPublicationUploadGrant(ctx, token, in.Query(g.Epoch)); err == nil {
				t.Fatal("stale authority accepted")
			}
		})
	}
}

func TestPostgresSkillHubPublicationIntrospectionConcurrentCancel(t *testing.T) {
	t.Parallel()
	db, _, in, _ := uploadGrantFixture(t)
	g, token := issueUpload(t, db, in)
	start := make(chan struct{})
	errs := make(chan error, 8)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			if i == 0 {
				_, err := db.CancelSkillHubPublicationEntry(context.Background(), in.Binding.ActorUID, in.EntryID)
				errs <- err
				return
			}
			_, err := db.IntrospectSkillHubPublicationUploadGrant(context.Background(), token, in.Query(g.Epoch))
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil && !errors.Is(err, store.ErrSkillHubPublicationSettled) {
			t.Fatal(err)
		}
	}
	if _, err := db.IntrospectSkillHubPublicationUploadGrant(context.Background(), token, in.Query(g.Epoch)); !errors.Is(err, store.ErrSkillHubPublicationSettled) {
		t.Fatalf("post-cancel=%v", err)
	}
}
