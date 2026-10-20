package postgres

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

func uploadGrantFixture(t *testing.T) (*Adapter, string, store.SkillHubPublicationUploadInput, int64) {
	t.Helper()
	db, dsn, id, b, other := publicationDeliveryFixture(t)
	lease := deliveryClaim(t, db, id, b)
	if err := db.FinishSkillHubPublicationDelivery(context.Background(), *lease, ""); err != nil {
		t.Fatal(err)
	}
	in := store.SkillHubPublicationUploadInput{EntryID: id, Binding: b, Issuer: "https://cats.example", Audience: "https://hub.example",
		Action: store.SkillHubPublicationUploadAction, Revision: 1, RequestHash: strings.Repeat("a", 64),
		SourceSnapshotHash: strings.Repeat("b", 64), RequestBytes: 1234}
	recordCaptureForTest(t, db, captureForUpload(in))
	return db, dsn, in, other
}

func issueUpload(t *testing.T, db *Adapter, in store.SkillHubPublicationUploadInput) (*store.SkillHubPublicationUploadGrant, store.SkillHubPublicationUploadToken) {
	t.Helper()
	g, token, err := db.IssueSkillHubPublicationUploadGrant(context.Background(), in)
	if err != nil || g == nil || token.Reveal() == "" {
		t.Fatalf("issue failed: %v", err)
	}
	return g, token
}

func ageUpload(t *testing.T, db *Adapter, id string, age string) {
	t.Helper()
	deliveryExec(t, db, `UPDATE skillhub_publication_upload_grants SET issued_at=issued_at-$2::interval, expires_at=expires_at-$2::interval WHERE entry_id=$1`, id, age)
}

func TestPostgresSkillHubPublicationUploadLifecycle(t *testing.T) {
	t.Parallel()
	db, dsn, in, _ := uploadGrantFixture(t)
	ctx := context.Background()
	g, token := issueUpload(t, db, in)
	if g.Epoch != 1 || g.Input != in || !store.ValidSkillHubPublicationRequestID(g.IdempotencyKey) ||
		g.ExpiresAt.Sub(g.IssuedAt) != store.SkillHubPublicationUploadTTL {
		t.Fatal("incorrect scope/lifetime")
	}
	var stored string
	if err := db.db.QueryRow(`SELECT row_to_json(g)::text FROM skillhub_publication_upload_grants g WHERE entry_id=$1`, in.EntryID).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, token.Reveal()) || strings.Contains(stored, strings.TrimPrefix(token.Reveal(), "pubupload_")) {
		t.Fatal("stored raw credential")
	}
	reopened := &Adapter{}
	if err := reopened.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := reopened.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	if got, err := reopened.CheckSkillHubPublicationUploadGrant(ctx, token, in, 1); err != nil || got == nil || got.ID != g.ID {
		t.Fatalf("reopen: %v", err)
	}
	// A lost issue response never authorizes a new identity. Retry is bounded,
	// rotates the unavailable secret and retains the original upload key/hash.
	if _, _, err := reopened.IssueSkillHubPublicationUploadGrant(ctx, in); !errors.Is(err, store.ErrSkillHubPublicationRateLimit) {
		t.Fatalf("unbounded issue: %v", err)
	}
	ageUpload(t, db, in.EntryID, "6 seconds")
	second, next := issueUpload(t, reopened, in)
	if second.ID != g.ID || second.IdempotencyKey != g.IdempotencyKey || second.Epoch != 2 || !second.CreatedAt.Equal(g.CreatedAt) || next.Reveal() == token.Reveal() {
		t.Fatal("rotation changed immutable identity")
	}
	if _, err := db.CheckSkillHubPublicationUploadGrant(ctx, token, in, 1); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
		t.Fatal("old token survived rotation")
	}
	if _, err := db.CheckSkillHubPublicationUploadGrant(ctx, next, in, 2); err != nil {
		t.Fatal(err)
	}
	ageUpload(t, db, in.EntryID, "121 seconds")
	if _, err := db.CheckSkillHubPublicationUploadGrant(ctx, next, in, 2); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
		t.Fatal("expired token accepted")
	}
	third, _ := issueUpload(t, reopened, in)
	if third.IdempotencyKey != g.IdempotencyKey || third.Epoch != 3 {
		t.Fatal("expiry reset identity")
	}
	for i := 4; i <= store.SkillHubPublicationUploadMaxIssues; i++ {
		ageUpload(t, db, in.EntryID, "6 seconds")
		got, _ := issueUpload(t, db, in)
		if got.Epoch != i {
			t.Fatal("budget reset")
		}
	}
	ageUpload(t, db, in.EntryID, "6 seconds")
	if _, _, err := reopened.IssueSkillHubPublicationUploadGrant(ctx, in); !errors.Is(err, store.ErrSkillHubPublicationExhausted) {
		t.Fatalf("issue budget bypassed: %v", err)
	}
	publicationCounts(t, db, 1)
}

func TestPostgresSkillHubPublicationUploadScope(t *testing.T) {
	t.Parallel()
	db, _, in, _ := uploadGrantFixture(t)
	g, token := issueUpload(t, db, in)
	ageUpload(t, db, in.EntryID, "6 seconds")
	for name, change := range map[string]func(*store.SkillHubPublicationUploadInput){
		"request bytes":          func(i *store.SkillHubPublicationUploadInput) { i.RequestBytes++ },
		"request digest":         func(i *store.SkillHubPublicationUploadInput) { i.RequestHash = strings.Repeat("c", 64) },
		"snapshot":               func(i *store.SkillHubPublicationUploadInput) { i.SourceSnapshotHash = strings.Repeat("d", 64) },
		"issuer":                 func(i *store.SkillHubPublicationUploadInput) { i.Issuer = "https://other.example" },
		"audience":               func(i *store.SkillHubPublicationUploadInput) { i.Audience = "https://other.example" },
		"action":                 func(i *store.SkillHubPublicationUploadInput) { i.Action = "publication.commit" },
		"revision":               func(i *store.SkillHubPublicationUploadInput) { i.Revision = 2 },
		"node same generation":   func(i *store.SkillHubPublicationUploadInput) { i.Binding.NodeID = "other-node" },
		"socket same generation": func(i *store.SkillHubPublicationUploadInput) { i.Binding.ConnectionID = "other-socket" },
		"device":                 func(i *store.SkillHubPublicationUploadInput) { i.Binding.Source.DeviceID = "other-device" },
		"installation":           func(i *store.SkillHubPublicationUploadInput) { i.Binding.Source.InstallationID = "other-install" },
		"workspace": func(i *store.SkillHubPublicationUploadInput) {
			i.Binding.Source.WorkspaceGeneration = "other-workspace"
		},
		"source": func(i *store.SkillHubPublicationUploadInput) { i.Binding.Source.LocalSkillID = "other-skill" },
		"entry":  func(i *store.SkillHubPublicationUploadInput) { i.EntryID = "pubentry_" + strings.Repeat("f", 32) },
	} {
		t.Run(name, func(t *testing.T) {
			bad := in
			change(&bad)
			if _, err := db.CheckSkillHubPublicationUploadGrant(context.Background(), token, bad, g.Epoch); err == nil {
				t.Fatal("wrong scope accepted")
			}
			if _, _, err := db.IssueSkillHubPublicationUploadGrant(context.Background(), bad); err == nil {
				t.Fatal("scope replaced on refresh")
			}
		})
	}
	if _, err := db.CheckSkillHubPublicationUploadGrant(context.Background(), token, in, 2); err == nil {
		t.Fatal("wrong epoch accepted")
	}
	wrong, _ := store.ParseSkillHubPublicationUploadToken("pubupload_" + strings.Repeat("f", 64))
	if _, err := db.CheckSkillHubPublicationUploadGrant(context.Background(), wrong, in, 1); err == nil {
		t.Fatal("wrong token accepted")
	}
	if _, err := db.CheckSkillHubPublicationUploadGrant(context.Background(), token, in, 1); err != nil {
		t.Fatal("failed checks mutated grant")
	}
}

func TestPostgresSkillHubPublicationUploadAuthority(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"cancelled", "human disabled", "bot disabled", "owner transfer", "body changed", "reconnect"} {
		t.Run(mode, func(t *testing.T) {
			db, _, in, other := uploadGrantFixture(t)
			g, token := issueUpload(t, db, in)
			switch mode {
			case "cancelled":
				if _, err := db.CancelSkillHubPublicationEntry(context.Background(), in.Binding.ActorUID, in.EntryID); err != nil {
					t.Fatal(err)
				}
			case "human disabled":
				deliveryExec(t, db, `UPDATE users SET state=1 WHERE id=$1`, in.Binding.ActorUID)
			case "bot disabled":
				deliveryExec(t, db, `UPDATE bot_config SET enabled=false WHERE user_id=$1`, in.Binding.BotUID)
			case "owner transfer":
				deliveryExec(t, db, `UPDATE bot_config SET owner_id=$1 WHERE user_id=$2`, other, in.Binding.BotUID)
			case "body changed":
				deliveryExec(t, db, `UPDATE bot_config SET body_id='changed' WHERE user_id=$1`, in.Binding.BotUID)
			case "reconnect":
				if _, err := db.BumpBotConnectionGeneration(in.Binding.BotUID); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := db.CheckSkillHubPublicationUploadGrant(context.Background(), token, in, g.Epoch); err == nil {
				t.Fatal("lost authority accepted")
			}
			ageUpload(t, db, in.EntryID, "6 seconds")
			if _, _, err := db.IssueSkillHubPublicationUploadGrant(context.Background(), in); err == nil {
				t.Fatal("lost authority refreshed")
			}
			if mode == "reconnect" {
				in.Binding.Source.RouteGeneration++
				in.Binding.NodeID = "node-b"
				in.Binding.ConnectionID = "socket-b"
				next, nextToken := issueUpload(t, db, in)
				if next.IdempotencyKey != g.IdempotencyKey || next.Epoch != 2 {
					t.Fatal("reconnect lost immutable identity")
				}
				if _, err := db.CheckSkillHubPublicationUploadGrant(context.Background(), nextToken, in, 2); err != nil {
					t.Fatal(err)
				}
				if _, err := db.CheckSkillHubPublicationUploadGrant(context.Background(), token, in, 2); err == nil {
					t.Fatal("old secret survived reconnect")
				}
			}
		})
	}
}

func TestPostgresSkillHubPublicationUploadRequiresProbe(t *testing.T) {
	t.Parallel()
	db, _, in, _ := uploadGrantFixture(t)
	deliveryExec(t, db, `UPDATE skillhub_publication_outbox SET delivery_state='needs_user_action',last_code='publication.source_changed',source_verified_at=NULL WHERE entry_id=$1`, in.EntryID)
	if _, _, err := db.IssueSkillHubPublicationUploadGrant(context.Background(), in); !errors.Is(err, store.ErrSkillHubPublicationDenied) {
		t.Fatalf("unchecked source accepted: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := db.IssueSkillHubPublicationUploadGrant(ctx, in); err == nil {
		t.Fatal("cancelled context issued")
	}
	var count int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM skillhub_publication_upload_grants`).Scan(&count); err != nil || count != 0 {
		t.Fatal("failed issue persisted")
	}
}

func TestPostgresSkillHubPublicationUploadConcurrentIssueAndCancel(t *testing.T) {
	t.Parallel()
	for _, cancelRequest := range []bool{false, true} {
		db, _, in, _ := uploadGrantFixture(t)
		start := make(chan struct{})
		type result struct {
			g     *store.SkillHubPublicationUploadGrant
			token store.SkillHubPublicationUploadToken
			err   error
		}
		results := make(chan result, 8)
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start
				if cancelRequest && i == 0 {
					_, err := db.CancelSkillHubPublicationEntry(context.Background(), in.Binding.ActorUID, in.EntryID)
					results <- result{err: err}
					return
				}
				g, token, err := db.IssueSkillHubPublicationUploadGrant(context.Background(), in)
				results <- result{g, token, err}
			}(i)
		}
		close(start)
		wg.Wait()
		close(results)
		issued := 0
		for r := range results {
			if r.err != nil && !errors.Is(r.err, store.ErrSkillHubPublicationSettled) && !errors.Is(r.err, store.ErrSkillHubPublicationRateLimit) {
				t.Fatal(r.err)
			}
			if r.g != nil {
				issued++
				if cancelRequest {
					if _, err := db.CheckSkillHubPublicationUploadGrant(context.Background(), r.token, in, r.g.Epoch); err == nil {
						t.Fatal("concurrent cancel lost")
					}
				}
			}
		}
		if issued > 1 || (!cancelRequest && issued != 1) {
			t.Fatalf("issues=%d", issued)
		}
		if cancelRequest {
			e, err := db.GetSkillHubPublicationEntry(context.Background(), in.Binding.ActorUID, in.EntryID)
			if err != nil || e.State != "cancelled" {
				t.Fatal("issue revived entry")
			}
		}
	}
}

func TestPostgresSkillHubPublicationUploadExpiryUsesDatabaseTime(t *testing.T) {
	t.Parallel()
	db, _, in, _ := uploadGrantFixture(t)
	g, token := issueUpload(t, db, in)
	// A future issue time is invalid too; no caller supplied clock is accepted.
	deliveryExec(t, db, `UPDATE skillhub_publication_upload_grants SET issued_at=issued_at+INTERVAL '1 hour',expires_at=expires_at+INTERVAL '1 hour' WHERE entry_id=$1`, in.EntryID)
	if _, err := db.CheckSkillHubPublicationUploadGrant(context.Background(), token, in, g.Epoch); err == nil {
		t.Fatal("not-yet-valid accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	if _, err := db.CheckSkillHubPublicationUploadGrant(ctx, token, in, g.Epoch); err == nil {
		t.Fatal("deadline ignored")
	}
}

func TestPostgresSkillHubPublicationUploadWriteFailureAtomicity(t *testing.T) {
	t.Parallel()
	db, _, in, _ := uploadGrantFixture(t)
	deliveryExec(t, db, `CREATE FUNCTION reject_upload_grant() RETURNS trigger LANGUAGE plpgsql AS $$
		BEGIN RAISE EXCEPTION 'synthetic grant failure'; END $$;
		CREATE TRIGGER reject_upload_grant BEFORE INSERT OR UPDATE ON skillhub_publication_upload_grants
		FOR EACH ROW EXECUTE FUNCTION reject_upload_grant();`)
	if g, token, err := db.IssueSkillHubPublicationUploadGrant(context.Background(), in); err == nil || g != nil || token.Reveal() != "" {
		t.Fatal("failed insert returned authority")
	}
	var count int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM skillhub_publication_upload_grants`).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial grant persisted")
	}
	deliveryExec(t, db, `ALTER TABLE skillhub_publication_upload_grants DISABLE TRIGGER reject_upload_grant`)
	g, token := issueUpload(t, db, in)
	ageUpload(t, db, in.EntryID, "6 seconds")
	deliveryExec(t, db, `ALTER TABLE skillhub_publication_upload_grants ENABLE TRIGGER reject_upload_grant`)
	if next, secret, err := db.IssueSkillHubPublicationUploadGrant(context.Background(), in); err == nil || next != nil || secret.Reveal() != "" {
		t.Fatal("failed rotation returned authority")
	}
	if checked, err := db.CheckSkillHubPublicationUploadGrant(context.Background(), token, in, g.Epoch); err != nil || checked.IdempotencyKey != g.IdempotencyKey {
		t.Fatal("failed rotation damaged previous grant")
	}
}
