package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/db/postgres"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func TestSkillHubPublicationDescriptionReleaseHistoryPostgres(t *testing.T) {
	runPublicationCommitContractScenario(t, false, true)
}

// Real JWT/HTTPS and PostgreSQL from admission through durable terminal reads.
// Cloud report/derivation/result and Runtime source remain synthetic ports.
func verifyDescriptionReleaseHistoryPostgres(t *testing.T, db *postgres.Adapter, sqlDB *sql.DB, in store.SkillHubPublicationCommitInput,
	actor, bot int64, reopen func() *postgres.Adapter,
) {
	t.Helper()
	ctx, id := context.Background(), in.Receipt.Query.EntryID
	c, err := db.GetSkillHubPublicationDescriptionContinuation(ctx, actor, id)
	if err != nil || c == nil {
		t.Fatal("continuation", err)
	}
	target := in.Target
	target.CandidateHash = c.Application.Result.Derivation.DerivedCandidateHash
	if _, err := db.RecordSkillHubPublicationDescriptionReleaseIntent(ctx, actor, *c, target); err != nil {
		t.Fatal(err)
	}
	if _, err := db.AuthorizeSkillHubPublicationDescriptionRelease(ctx, id, c.Application.ID, target); err != nil {
		t.Fatal(err)
	}
	o, err := db.GetSkillHubPublicationDescriptionReleaseObservation(ctx, actor, id)
	if err != nil {
		t.Fatal(err)
	}
	other, err := db.CreateUser(&types.User{Username: "history-other", AccountType: types.AccountHuman, PassHash: []byte("test")})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_READS_ENABLED", "1")
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_RELEASE_HISTORY_ENABLED", "1")
	var current atomic.Pointer[SkillHubPublicationReadHandler]
	current.Store(NewSkillHubPublicationReadHandlerFromEnv(db))
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { current.Load().Handle(w, r) }))
	defer ts.Close()
	call := func(uid int64, want int) []byte {
		t.Helper()
		r := publicationReadRequest(t, "GET", skillHubPublicationPrefix+"/entries/"+id+"/description-release-history", uid)
		r.URL.Scheme, r.URL.Host, r.RequestURI = "https", strings.TrimPrefix(ts.URL, "https://"), ""
		res, err := ts.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != want || res.Header.Get("Cache-Control") != "private, no-store" {
			t.Fatalf("history status %d want %d: %s (%v)", res.StatusCode, want, b, err)
		}
		for _, secret := range []string{"pkg_", "candidateHash", "checksumSha256", "permitId", "uploadId", "pubdesc_", "bodyId"} {
			if strings.Contains(string(b), secret) {
				t.Fatal("private history field", secret)
			}
		}
		return b
	}
	counts := func() string {
		t.Helper()
		var out string
		err := sqlDB.QueryRow(`SELECT json_build_array(
			(SELECT count(*) FROM skillhub_publication_entries),
			(SELECT count(*) FROM skillhub_publication_repair_rounds),
			(SELECT count(*) FROM skillhub_publication_description_release_intents),
			(SELECT count(*) FROM skillhub_publication_description_release_permits),
			(SELECT count(*) FROM skillhub_publication_description_release_terminals),
			(SELECT count(*) FROM skillhub_publication_commit_intents))::text`).Scan(&out)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	before := counts()
	if !strings.Contains(string(call(actor, 200)), `"stage":"not_recorded"`) {
		t.Fatal("missing receipt became execution status")
	}
	call(0, 401)
	call(other, 404)
	if counts() != before {
		t.Fatal("GET created release evidence")
	}
	status := releaseStatusFixture(c.Application, publicationDescriptionCloudReleasePermit{SchemaVersion: 1, ApplicationID: c.Application.ID,
		EntryID: id, Target: target, IntentHash: o.Intent.IdentityHash, PermitID: o.Permit.ID, AuthorizedAt: o.Permit.AuthorizedAt.UTC().Format(time.RFC3339Nano)})
	status.State = "published"
	status.Result = &PublicationCommitCloudResult{SkillID: target.SkillID, Version: target.Version, PublishedAt: target.ReleaseAt,
		PackageID: "pkg_12345678-1234-4123-8123-123456789abc", Checksum: strings.Repeat("a", 64)}
	ports := publicationDescriptionReleaseOutcomePorts{ReadCloud: func(context.Context, store.SkillHubPublicationDescriptionApplication,
		store.SkillHubPublicationDescriptionReleaseIntent, publicationDescriptionCloudReleasePermit) (*publicationDescriptionCloudReleaseStatus, error) {
		return &status, nil
	}}
	w := &PublicationDescriptionReconciler{enabled: true, scope: store.SkillHubPublicationCancellationScope{Issuer: in.Receipt.Query.Issuer, Audience: in.Receipt.Query.Audience}, ledger: db, ports: ports}
	// The real persistent scheduler observes progress without producing a
	// terminal, then a reconstructed worker records only the peer terminal.
	status.State, status.Result = "committing", nil
	if err := w.RunOnce(ctx); err != nil {
		t.Fatal("progress reconciliation", err)
	}
	if !strings.Contains(string(call(actor, 200)), `"stage":"not_recorded"`) {
		t.Fatal("progress fabricated terminal")
	}
	if _, err := sqlDB.Exec(`UPDATE skillhub_publication_description_reconciles SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE application_id=$1`, c.Application.ID); err != nil {
		t.Fatal(err)
	}
	status.State = "published"
	status.Result = &PublicationCommitCloudResult{SkillID: target.SkillID, Version: target.Version, PublishedAt: target.ReleaseAt,
		PackageID: "pkg_12345678-1234-4123-8123-123456789abc", Checksum: strings.Repeat("a", 64)}
	w = &PublicationDescriptionReconciler{enabled: true, scope: w.scope, ledger: reopen(), ports: ports}
	if err := w.RunOnce(ctx); err != nil {
		t.Fatal("terminal reconciliation", err)
	}
	var reconcileState string
	if err := sqlDB.QueryRow(`SELECT state FROM skillhub_publication_description_reconciles WHERE application_id=$1`, c.Application.ID).Scan(&reconcileState); err != nil || reconcileState != "recorded" {
		t.Fatal("terminal scheduler settlement", reconcileState, err)
	}
	// Reconstruct both adapter and handler, not an in-memory copy of the result.
	current.Store(NewSkillHubPublicationReadHandlerFromEnv(reopen()))
	before = counts()
	published := call(actor, 200)
	var projected struct {
		History publicationDescriptionReleaseHistoryView `json:"descriptionReleaseHistory"`
	}
	if json.Unmarshal(published, &projected) != nil || projected.History.Stage != "published" || projected.History.Result == nil || projected.History.Result.SkillID != target.SkillID {
		t.Fatal("terminal projection", string(published))
	}
	// Check the actual permit fence while actor is still the live Bot Owner;
	// after transfer a generic denied result would not prove this invariant.
	if _, err := db.CancelSkillHubPublicationEntry(ctx, actor, id); !errors.Is(err, store.ErrSkillHubPublicationConflict) {
		t.Fatal("history read cleared cancellation fence", err)
	}
	if _, err := sqlDB.Exec(`UPDATE bot_config SET owner_id=$1,enabled=false,body_id='offline' WHERE user_id=$2`, other, bot); err != nil {
		t.Fatal(err)
	}
	current.Store(NewSkillHubPublicationReadHandlerFromEnv(reopen()))
	if string(call(actor, 200)) != string(published) {
		t.Fatal("Bot transfer changed historical result")
	}
	call(other, 404)
	if counts() != before {
		t.Fatal("history reads mutated publication state")
	}
	if err := db.UpdateUserState(actor, 1); err != nil {
		t.Fatal(err)
	}
	current.Store(NewSkillHubPublicationReadHandlerFromEnv(reopen()))
	call(actor, 403)
	if counts() != before {
		t.Fatal("denied history read mutated publication state")
	}
	t.Log("real JWT/HTTPS/PostgreSQL derived history and reconciliation: committing remains not_recorded, reconstructed worker records durable published result, reconstructed handler recovers history, Bot transfer/offline keeps original publisher history, new Owner/disabled publisher denied, read-only row counts and cancellation fence preserved; synthetic Cloud/Runtime, no paid model or real publication")
}
