package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// Deliberately implements only the read interface, not the terminal writer.
type publicationHistoryTestStore struct {
	*publicationReadTestStore
	observation   store.SkillHubPublicationDescriptionReleaseObservation
	receipt       *store.SkillHubPublicationDescriptionReleaseTerminalReceipt
	reads         atomic.Int32
	terminalReads atomic.Int32
	onObservation func(int32) error
	onReceipt     func(context.Context) error
}

func (db *publicationHistoryTestStore) GetSkillHubPublicationDescriptionReleaseObservation(_ context.Context, actor int64, id string) (*store.SkillHubPublicationDescriptionReleaseObservation, error) {
	n := db.reads.Add(1)
	if db.onObservation != nil {
		if err := db.onObservation(n); err != nil {
			return nil, err
		}
	}
	if db.observation.ValidateFor(actor, id) != nil {
		return nil, store.ErrSkillHubPublicationDenied
	}
	o := snapshotDescriptionReleaseObservation(db.observation)
	return &o, nil
}

func (db *publicationHistoryTestStore) GetSkillHubPublicationDescriptionReleaseTerminal(ctx context.Context, actor int64, id string) (*store.SkillHubPublicationDescriptionReleaseTerminalReceipt, error) {
	db.terminalReads.Add(1)
	if db.onReceipt != nil {
		if err := db.onReceipt(ctx); err != nil {
			return nil, err
		}
	}
	return db.receipt, nil
}

func publicationHistoryFixture(t *testing.T, state string) (*publicationHistoryTestStore, int64, string) {
	t.Helper()
	r, _ := descriptionOutcomeFixture(t)
	o := r.observation
	actor, id := o.Intent.ActorUID, o.Intent.EntryID
	base := newPublicationReadTestStore()
	base.users[actor] = &types.User{ID: actor, AccountType: types.AccountHuman}
	base.entry.ID, base.entry.Input.ActorUID = id, actor
	db := &publicationHistoryTestStore{publicationReadTestStore: base, observation: o}
	if state != "not_recorded" {
		terminal := store.SkillHubPublicationDescriptionReleaseTerminal{ApplicationID: o.Intent.ApplicationID, EntryID: id,
			PermitID: o.Permit.ID, IntentHash: o.Intent.IdentityHash, Target: o.Intent.Target, State: state}
		if state == "published" {
			terminal.Result = &store.SkillHubPublicationDescriptionReleaseResult{SkillID: o.Intent.Target.SkillID, Version: o.Intent.Target.Version,
				PublishedAt: o.Intent.Target.ReleaseAt, PackageID: "pkg_12345678-1234-4123-8123-123456789abc", Checksum: strings.Repeat("a", 64)}
		} else {
			terminal.ErrorCode = "skillhub.publication_description_release_commit_rejected"
		}
		hash, err := terminal.Digest()
		if err != nil {
			t.Fatal(err)
		}
		db.receipt = &store.SkillHubPublicationDescriptionReleaseTerminalReceipt{Terminal: terminal, IdentityHash: hash, RecordedAt: o.Permit.AuthorizedAt.Add(time.Second)}
	}
	return db, actor, skillHubPublicationPrefix + "/entries/" + id + "/description-release-history"
}

func historyTestHandler(db store.Store) *SkillHubPublicationReadHandler {
	h := NewSkillHubPublicationReadHandler(db, true)
	h.descriptionReleaseHistoryEnabled = true
	return h
}

func TestSkillHubPublicationReadDescriptionReleaseHistoryProjection(t *testing.T) {
	for _, state := range []string{"not_recorded", "published", "publish_failed"} {
		t.Run(state, func(t *testing.T) {
			db, actor, path := publicationHistoryFixture(t, state)
			before := snapshotDescriptionReleaseObservation(db.observation)
			rec := runPublicationRead(t, historyTestHandler(db), publicationReadRequest(t, "GET", path, actor), 200)
			var got map[string]json.RawMessage
			if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			var view map[string]json.RawMessage
			if err := json.Unmarshal(got["descriptionReleaseHistory"], &view); err != nil {
				t.Fatal(err)
			}
			want := 3
			if state == "not_recorded" {
				want = 1
			}
			if len(got) != 3 || len(view) != want || string(view["stage"]) != `"`+state+`"` {
				t.Fatal("unexpected fields", rec.Body.String())
			}
			if state == "published" {
				var result map[string]string
				_ = json.Unmarshal(view["result"], &result)
				if len(result) != 3 || result["skillId"] != db.observation.Intent.Target.SkillID || result["version"] != db.observation.Intent.Target.Version || result["publishedAt"] != db.observation.Intent.Target.ReleaseAt {
					t.Fatal("invalid result", result)
				}
			}
			for _, secret := range []string{"private-", "pkg_", "pubdesc_", "pubdescpermit_", "intentHash", "candidateHash", "uploadId", "checksumSha256", "target", "ActorUID", "bodyId", "applicationId"} {
				if strings.Contains(rec.Body.String(), secret) {
					t.Fatal("private field leak", secret)
				}
			}
			if db.reads.Load() != 2 || db.terminalReads.Load() != 1 || !reflect.DeepEqual(before, db.observation) || db.entry.State != "waiting_runtime" {
				t.Fatal("not a pure historical read")
			}
		})
	}
}

func TestSkillHubPublicationReadDescriptionReleaseHistoryFailsClosed(t *testing.T) {
	for _, mode := range []string{"denied-before", "denied-after", "changed-after", "bad-hash", "wrong-target", "wrong-entry", "future-state", "private-code", "read-error", "cancelled", "cancelled-after"} {
		t.Run(mode, func(t *testing.T) {
			db, actor, path := publicationHistoryFixture(t, "published")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := 503
			db.onObservation = func(n int32) error {
				if mode == "denied-before" || (mode == "denied-after" && n == 2) {
					return store.ErrSkillHubPublicationDenied
				}
				if mode == "changed-after" && n == 2 {
					db.observation.Permit.AuthorizedAt = db.observation.Permit.AuthorizedAt.Add(1)
				}
				return nil
			}
			switch mode {
			case "denied-before", "denied-after":
				want = 404
			case "bad-hash":
				db.receipt.IdentityHash = strings.Repeat("b", 64)
			case "wrong-target":
				db.receipt.Terminal.Target.Version = "2.0.0"
			case "wrong-entry":
				db.receipt.Terminal.EntryID = publicationReadTestID
			case "future-state":
				db.receipt.Terminal.State = "committing"
			case "private-code":
				db.receipt.Terminal.ErrorCode = "/private/token"
			case "read-error":
				db.onReceipt = func(context.Context) error { return errors.New("/private/token") }
			case "cancelled":
				cancel()
			case "cancelled-after":
				db.onReceipt = func(context.Context) error { cancel(); return nil }
			}
			rec := runPublicationRead(t, historyTestHandler(db), publicationReadRequest(t, "GET", path, actor).WithContext(ctx), want)
			if strings.Contains(rec.Body.String(), "private") || strings.Contains(rec.Body.String(), "published") {
				t.Fatal("unverified disclosure", rec.Body.String())
			}
		})
	}
}

func TestSkillHubPublicationReadDescriptionReleaseHistoryFreezesReceipt(t *testing.T) {
	db, actor, _ := publicationHistoryFixture(t, "published")
	original := *db.receipt.Terminal.Result
	db.onObservation = func(n int32) error {
		if n == 2 {
			db.receipt.Terminal.Result.Checksum = "adapter mutation"
		}
		return nil
	}
	r, err := readConfirmedPublicationDescriptionReleaseTerminal(context.Background(), db, actor, db.observation.Intent.EntryID)
	if err != nil || r == nil || *r.Terminal.Result != original {
		t.Fatal("receipt alias", err)
	}
}

func TestSkillHubPublicationReadDescriptionReleaseHistoryGatesAndTransport(t *testing.T) {
	for _, global := range []bool{false, true} {
		for _, flag := range []string{"", "false", "yes", "1", "TRUE"} {
			t.Setenv("CATSCO_SKILLHUB_PUBLICATION_READS_ENABLED", "false")
			if global {
				t.Setenv("CATSCO_SKILLHUB_PUBLICATION_READS_ENABLED", "1")
			}
			t.Setenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_RELEASE_HISTORY_ENABLED", flag)
			db, actor, path := publicationHistoryFixture(t, "not_recorded")
			h := NewSkillHubPublicationReadHandlerFromEnv(db)
			enabled := global && (flag == "1" || flag == "TRUE")
			want := 404
			if enabled {
				want = 200
			}
			runPublicationRead(t, h, publicationReadRequest(t, "GET", path, actor), want)
			caps := runPublicationRead(t, h, publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/capabilities", actor), 200)
			var c map[string]any
			_ = json.Unmarshal(caps.Body.Bytes(), &c)
			if c["descriptionReleaseHistoryReadsEnabled"] != enabled {
				t.Fatal("wrong capability", c)
			}
			if !enabled && (db.calls.Load() != 0 || db.reads.Load() != 0) {
				t.Fatal("disabled path read storage")
			}
		}
	}
	for _, mode := range []string{"anonymous", "foreign", "bot", "disabled", "post", "query", "body", "encoded", "duplicate-auth", "cookie"} {
		db, actor, path := publicationHistoryFixture(t, "published")
		uid, method, want := actor, "GET", 401
		switch mode {
		case "anonymous":
			uid = 0
		case "foreign":
			uid, want = actor+1000, 404
			db.users[uid] = &types.User{ID: uid, AccountType: types.AccountHuman}
		case "bot":
			db.users[actor].AccountType = types.AccountBot
			want = 403
		case "disabled":
			db.users[actor].State = 1
			want = 403
		case "post":
			method, want = "POST", 405
		case "query":
			path += "?token=private"
			want = 400
		case "body":
			want = 400
		case "encoded":
			path = strings.Replace(path, "pubentry_", "%70ubentry_", 1)
			want = 404
		}
		r := publicationReadRequest(t, method, path, uid)
		switch mode {
		case "body":
			r.ContentLength = 1
		case "duplicate-auth":
			r.Header.Add("Authorization", r.Header.Get("Authorization"))
		case "cookie":
			r.Header.Set("Cookie", "token="+r.Header.Get("Authorization"))
			r.Header.Del("Authorization")
		}
		runPublicationRead(t, historyTestHandler(db), r, want)
		if db.reads.Load() != 0 || db.terminalReads.Load() != 0 {
			t.Fatal("invalid/unauthorized request read history", mode)
		}
	}
	db, actor, path := publicationHistoryFixture(t, "published")
	runPublicationRead(t, historyTestHandler(db.publicationReadTestStore), publicationReadRequest(t, "GET", path, actor), 503)
	h := historyTestHandler(db)
	for i := 0; i < 3; i++ {
		runPublicationRead(t, h, publicationReadRequest(t, "GET", strings.TrimSuffix(path, "/description-release-history"), actor), 200)
	}
	runPublicationRead(t, h, publicationReadRequest(t, "GET", path, actor), 429)
}

func TestSkillHubPublicationReadDescriptionReleaseHistoryConcurrency(t *testing.T) {
	db, actor, path := publicationHistoryFixture(t, "published")
	started := make(chan struct{}, 16)
	db.onReceipt = func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			return errors.New("missing timeout")
		}
		started <- struct{}{}
		<-ctx.Done()
		return ctx.Err()
	}
	h := historyTestHandler(db)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan int, 16)
	for i := 0; i < 16; i++ {
		// Independent actor limiters isolate the semaphore test from per-actor
		// polling limits while exercising the actual handler and timeout setup.
		peer := historyTestHandler(db)
		peer.reads = h.reads
		r := publicationReadRequest(t, "GET", path, actor).WithContext(ctx)
		go func() {
			w := httptest.NewRecorder()
			peer.Handle(w, r)
			done <- w.Code
		}()
	}
	for i := 0; i < 16; i++ {
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			t.Fatal("read did not start")
		}
	}
	runPublicationRead(t, h, publicationReadRequest(t, "GET", path, actor), 503)
	cancel()
	for i := 0; i < 16; i++ {
		select {
		case code := <-done:
			if code != 503 {
				t.Fatal("cancelled read succeeded")
			}
		case <-time.After(3 * time.Second):
			t.Fatal("cancelled read did not exit")
		}
	}
	if len(h.reads) != 0 {
		t.Fatal("read slot leak")
	}
}
