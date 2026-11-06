package server

import (
	"bufio"
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/db/postgres"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

// Capture only the HTTP reply, not the transaction. Unwrap keeps real transport
// read deadlines active. The loss happens AFTER the real handler commits.
type permitContractWriter struct {
	http.ResponseWriter
	record *httptest.ResponseRecorder
}

func (w *permitContractWriter) Header() http.Header         { return w.record.Header() }
func (w *permitContractWriter) WriteHeader(s int)           { w.record.WriteHeader(s) }
func (w *permitContractWriter) Write(b []byte) (int, error) { return w.record.Write(b) }
func (w *permitContractWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

type permitContractReply struct {
	Error  string         `json:"error"`
	Permit map[string]any `json:"permit"`
}

func TestSkillHubPublicationPeerCommitPostgres(t *testing.T) {
	runPublicationCommitContract(t, false)
}

func TestSkillHubPublicationCommitCloudContract(t *testing.T) {
	runPublicationCommitContract(t, true)
}

func runPublicationCommitContract(t *testing.T, cloud bool) {
	runPublicationCommitContractScenario(t, cloud, false)
}

func runPublicationCommitContractScenario(t *testing.T, cloud, releaseHistory bool) {
	t.Helper()
	dsn, root := os.Getenv("CATS_PG_TEST_DSN"), os.Getenv("SKILLHUB_COMMIT_CONTRACT_ROOT")
	if dsn == "" || (cloud && root == "") {
		if os.Getenv("CATSCO_REQUIRE_COMMIT_CONTRACT") == "1" {
			t.Fatal("commit contract dependencies missing")
		}
		t.Skip("requires PostgreSQL; cross-repo variant also requires Cloud checkout")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" {
		t.Fatal("PostgreSQL URL required")
	}
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	schema := fmt.Sprintf("cats_permit_contract_%d", time.Now().UnixNano())
	if _, err := admin.Exec(`CREATE SCHEMA "` + schema + `"`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(`DROP SCHEMA "` + schema + `" CASCADE`); err != nil {
			t.Error(err)
		}
	})
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	scopedDSN := u.String()
	scoped, err := sql.Open("pgx", scopedDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { scoped.Close() })
	open := func() *postgres.Adapter {
		t.Helper()
		a := &postgres.Adapter{}
		if err := a.Open(scopedDSN); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { a.Close() })
		return a
	}
	a := open()
	if err := a.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	var current atomic.Pointer[SkillHubPublicationPeerHandler]
	var lose atomic.Bool
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record := httptest.NewRecorder()
		current.Load().Handle(&permitContractWriter{ResponseWriter: w, record: record}, r)
		if record.Code == 200 && lose.Swap(false) {
			c, _, err := http.NewResponseController(w).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			c.Close()
			return
		}
		for k, v := range record.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(record.Code)
		w.Write(record.Body.Bytes())
	}))
	issuer := "https://" + ts.Listener.Addr().String()
	const audience = "https://hub.example"
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_INTROSPECTION_ENABLED", "")
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_CLOUD_RECEIPTS_ENABLED", "")
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_COMMIT_PERMITS_ENABLED", "true")
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_ISSUER", issuer)
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_CLOUD_AUDIENCE", audience)
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_CLOUD_PEER_SECRET", peerSecret)
	t.Setenv("CATSCO_SKILLHUB_PUBLICATION_TRUSTED_PROXY_CIDRS", "")
	current.Store(NewSkillHubPublicationPeerHandlerFromEnv(a))
	ts.StartTLS()
	t.Cleanup(ts.Close)

	// Go variant is run in CatsCo's required PG CI. Cloud variant uses the real
	// Node HTTPS client; no fake successful responses or copied client validator.
	call := func(b publicationCommitPermitRequest, wrongSecret bool) permitContractReply {
		t.Helper()
		data, _ := json.Marshal(b)
		r, _ := http.NewRequestWithContext(ctx, "POST", issuer+SkillHubPublicationCommitPermitPath, bytes.NewReader(data))
		secret := peerSecret
		if wrongSecret {
			secret = strings.Repeat("f", 64)
		}
		r.Header.Set("Authorization", "Bearer "+secret)
		r.Header.Set("Content-Type", "application/json")
		res, err := ts.Client().Do(r)
		if err != nil {
			return permitContractReply{Error: "unavailable"}
		}
		defer res.Body.Close()
		if res.StatusCode == 200 {
			var p map[string]any
			if err := json.NewDecoder(res.Body).Decode(&p); err != nil {
				t.Fatal(err)
			}
			return permitContractReply{Permit: p}
		}
		var envelope struct{ Error struct{ Code string } }
		if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil {
			t.Fatal(err)
		}
		kinds := map[string]string{"publication.commit_conflict": "conflict", "publication.commit_cancelled": "cancelled", "publication.commit_denied": "denied"}
		kind := kinds[envelope.Error.Code]
		if kind == "" {
			kind = "unavailable"
		}
		return permitContractReply{Error: kind}
	}
	if cloud {
		cmd := exec.CommandContext(ctx, "node", filepath.Join("testdata", "publication-commit-cloud.mjs"))
		cmd.Env = append(os.Environ(), "SKILLHUB_COMMIT_CONTRACT_ROOT="+root)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdin, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			stdin.Close()
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case err := <-done:
				if err != nil {
					t.Error("Cloud fixture", err)
				}
			case <-time.After(5 * time.Second):
				cmd.Process.Kill()
				<-done
				t.Error("Cloud fixture did not stop")
			}
		})
		cert := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ts.Certificate().Raw}))
		scanner := bufio.NewScanner(stdout)
		call = func(b publicationCommitPermitRequest, wrongSecret bool) permitContractReply {
			t.Helper()
			config := map[string]any{"enabled": true, "issuer": issuer, "audience": audience, "peerSecret": peerSecret}
			if wrongSecret {
				config["peerSecret"] = strings.Repeat("f", 64)
			}
			if err := json.NewEncoder(stdin).Encode(map[string]any{"config": config, "ca": cert, "input": b}); err != nil {
				t.Fatal(err)
			}
			if !scanner.Scan() {
				t.Fatal("Cloud reply missing", scanner.Err())
			}
			var out permitContractReply
			if err := json.Unmarshal(scanner.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			return out
		}
	}
	actor, err := a.CreateUser(&types.User{Username: "permit-owner", AccountType: types.AccountHuman, PassHash: []byte("test")})
	if err != nil {
		t.Fatal(err)
	}
	bot, err := a.CreateUser(&types.User{Username: "permit-bot", AccountType: types.AccountBot, PassHash: []byte("test")})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.SaveBotConfigWithOwner(bot, actor, "", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := scoped.Exec("UPDATE bot_config SET body_id='permit-body' WHERE user_id=$1", bot); err != nil {
		t.Fatal(err)
	}
	generation, err := a.BumpBotConnectionGeneration(bot)
	if err != nil {
		t.Fatal(err)
	}
	source := store.SkillHubPublicationSource{DeviceID: "permit-device", BodyID: "permit-body", InstallationID: "permit-install",
		RouteGeneration: int64(generation), WorkspaceGeneration: "rev-1", LocalSkillID: "contract"}
	seed := func(requestID string) publicationCommitPermitRequest {
		t.Helper()
		entry, _, err := a.CreateSkillHubPublicationEntry(ctx, store.SkillHubPublicationEntryInput{ActorUID: actor, BotUID: bot, Entrypoint: "workspace", RequestID: requestID, Source: source}, store.DefaultSkillHubPublicationEntryPolicy())
		if err != nil {
			t.Fatal(err)
		}
		binding := store.SkillHubPublicationDeliveryBinding{ActorUID: actor, BotUID: bot, Source: source, NodeID: "permit-node", ConnectionID: "permit-connection"}
		probe, err := a.ClaimSkillHubPublicationDelivery(ctx, entry.ID, binding)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.FinishSkillHubPublicationDelivery(ctx, *probe, ""); err != nil {
			t.Fatal(err)
		}
		in := store.SkillHubPublicationUploadInput{EntryID: entry.ID, Binding: binding, Issuer: issuer, Audience: audience, Action: store.SkillHubPublicationUploadAction, Revision: 1, RequestHash: strings.Repeat("a", 64), SourceSnapshotHash: strings.Repeat("b", 64), RequestBytes: 123}
		// Explicit trusted-runtime test adapter, not a live capture dispatch.
		if _, err := a.RecordSkillHubPublicationCapture(ctx, store.SkillHubPublicationCaptureInput{EntryID: in.EntryID, Binding: binding, Revision: 1, RequestHash: in.RequestHash, SourceSnapshotHash: in.SourceSnapshotHash, RequestBytes: in.RequestBytes}); err != nil {
			t.Fatal(err)
		}
		g, _, err := a.IssueSkillHubPublicationUploadGrant(ctx, in)
		if err != nil {
			t.Fatal(err)
		}
		wire := publicationCommitPermitRequest{SchemaVersion: 1, Receipt: publicationCloudReceiptRequest{
			SchemaVersion: 1, EntryRequestID: entry.ID, CandidateRevision: 1, AcceptanceEpoch: g.Epoch, RequestHash: in.RequestHash, SourceSnapshotHash: in.SourceSnapshotHash, RequestBytes: in.RequestBytes, DelegationID: g.ID, IdempotencyKey: g.IdempotencyKey,
			TaskID: "pubtask_11111111-1111-4111-8111-111111111111", EventID: "pubevent_22222222-2222-4222-8222-222222222222"},
			Target: store.SkillHubPublicationCommitTarget{CandidateHash: in.SourceSnapshotHash, ManifestHash: strings.Repeat("d", 64), PublisherUserID: "usr_test", PublisherProfileID: "pub_test", Namespace: "publisher", SkillID: "publisher/test", Version: "1.0.1", UploadID: "upl_test", ReleaseAt: "2026-10-19T00:00:00.000Z", ConfirmVersionPublish: true}}
		// Each entry has its own immutable Cloud task/event. Synthetic acceptance
		// and trusted human intent seeding are explicit: not a full publishing E2E.
		if requestID == "cancel-first" {
			wire.Receipt.TaskID = "pubtask_33333333-3333-4333-8333-333333333333"
			wire.Receipt.EventID = "pubevent_44444444-4444-4444-8444-444444444444"
		}
		if _, err := a.RecordSkillHubPublicationCloudReceipt(ctx, wire.input(issuer, audience).Receipt); err != nil {
			t.Fatal(err)
		}
		return wire
	}
	expectError := func(wire publicationCommitPermitRequest, wrong bool, want string) {
		t.Helper()
		got := call(wire, wrong)
		if got.Error != want || got.Permit != nil {
			t.Fatalf("want %s, got %+v", want, got)
		}
	}
	first, stopped := seed("permit-first"), seed("cancel-first")
	if releaseHistory {
		in := first.input(issuer, audience)
		verifyHumanRepairContextPostgres(t, a, in, actor)
		verifyHumanRepairAdmissionPostgres(t, a, in, actor)
		verifyDescriptionReleaseHistoryPostgres(t, a, scoped, in, actor, bot, open)
		return
	}
	expectError(first, false, "conflict")
	var count int
	if err := scoped.QueryRow("SELECT count(*) FROM skillhub_publication_commit_intents").Scan(&count); err != nil || count != 0 {
		t.Fatal("service created intent", err)
	}
	for _, wire := range []publicationCommitPermitRequest{first, stopped} {
		if !cloud {
			// Real active-human JWT + HTTPS handler + PostgreSQL admission. The
			// Cloud observation is synthetic here; real peers are covered separately.
			admitHumanCommitPostgres(t, a, wire.input(issuer, audience), actor)
		} else if _, err := a.RecordSkillHubPublicationCommitIntent(ctx, actor, wire.input(issuer, audience)); err != nil {
			t.Fatal(err)
		}
	}
	expectError(first, true, "unavailable")
	changed := first
	changed.Target.Version = "1.0.2"
	expectError(changed, false, "conflict")
	lose.Store(true)
	if cloud {
		expectError(first, false, "unavailable")
	} else {
		// The real human route saved intent before its failed Cloud call. A new
		// dispatcher must recover that exact intent. This Cloud observation is a
		// test adapter; its authorization traverses the real TLS permit handler.
		observation := &humanCommitCloud{in: first.input(issuer, audience), state: "delegated_pending"}
		dispatchCloud := &commitDispatchCloud{read: observation.Read, authorize: func(_ context.Context, r store.SkillHubPublicationCloudReceipt) (*PublicationCommitCloudStatus, error) {
			if r != observation.in.Receipt {
				t.Fatal("dispatcher changed receipt")
			}
			expectError(first, false, "unavailable")
			return nil, errPublicationCommitCloud
		}}
		worker := &PublicationCommitDispatcher{enabled: true, scope: store.SkillHubPublicationCancellationScope{Issuer: issuer, Audience: audience}, ledger: a, cloud: dispatchCloud}
		if err := worker.RunOnce(ctx); err != nil {
			t.Fatal(err)
		}
		s, err := a.GetSkillHubPublicationCommitDelivery(ctx, actor, first.Receipt.EntryRequestID)
		if err != nil || s == nil || s.State != "pending" || s.Attempts != 1 {
			t.Fatal("lost reply was not retained", s, err)
		}
	}
	durable, err := a.GetSkillHubPublicationCommitPermit(ctx, actor, first.Receipt.EntryRequestID)
	if err != nil || durable == nil {
		t.Fatal("permit did not commit before loss", err)
	}
	// Reopen the adapter and handler; Node constructs a new client per request.
	reopened := open()
	current.Store(NewSkillHubPublicationPeerHandlerFromEnv(reopened))
	replay := call(first, false)
	if replay.Error != "" || replay.Permit["permitId"] != durable.ID || replay.Permit["authorizedAt"] != durable.AuthorizedAt.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("lost-response replay %+v", replay)
	}
	if _, err := reopened.RequestSkillHubPublicationCancellation(ctx, actor, first.Receipt.EntryRequestID); err != nil {
		t.Fatal("request Cloud-linked cancellation after permit", err)
	}
	if _, err := reopened.RequestSkillHubPublicationCancellation(ctx, actor, stopped.Receipt.EntryRequestID); err != nil {
		t.Fatal("request Cloud-linked cancellation before permit", err)
	}
	expectError(stopped, false, "cancelled")
	if _, err := scoped.Exec("UPDATE bot_config SET enabled=false WHERE user_id=$1", bot); err != nil {
		t.Fatal(err)
	}
	after := call(first, false)
	if !reflect.DeepEqual(after, replay) {
		t.Fatal("historical permit changed after cancel/disable")
	}
	expectError(changed, false, "conflict")
	if err := scoped.QueryRow("SELECT count(*) FROM skillhub_publication_commit_permits").Scan(&count); err != nil || count != 1 {
		t.Fatal("duplicate or cancelled permit", err)
	}
	if !cloud {
		if _, err := scoped.Exec(`UPDATE skillhub_publication_commit_deliveries SET next_attempt_at='1970-01-01' WHERE entry_id=$1`, first.Receipt.EntryRequestID); err != nil {
			t.Fatal(err)
		}
		dispatchCloud := &commitDispatchCloud{
			read: func(_ context.Context, r store.SkillHubPublicationCloudReceipt) (*PublicationCommitCloudStatus, error) {
				wire := stopped
				if r == first.input(issuer, audience).Receipt {
					wire = first
				}
				out := &PublicationCommitCloudStatus{SchemaVersion: 1, Receipt: publicationControlReceipt(r), State: "delegated_pending", Target: &wire.Target}
				if wire.Receipt == first.Receipt {
					out.State = "committing"
					out.PermitID = &durable.ID
				}
				return out, nil
			},
			authorize: func(_ context.Context, r store.SkillHubPublicationCloudReceipt) (*PublicationCommitCloudStatus, error) {
				if r != stopped.input(issuer, audience).Receipt {
					t.Fatal("reauthorized accepted intent")
				}
				expectError(stopped, false, "cancelled")
				return nil, store.ErrSkillHubPublicationSettled
			},
		}
		worker := &PublicationCommitDispatcher{enabled: true, scope: store.SkillHubPublicationCancellationScope{Issuer: issuer, Audience: audience}, ledger: reopened, cloud: dispatchCloud}
		for n := 0; n < 2; n++ {
			if err := worker.RunOnce(ctx); err != nil {
				t.Fatal(err)
			}
		}
		for _, tc := range []struct {
			id, state string
			attempts  int
		}{{first.Receipt.EntryRequestID, "accepted", 2}, {stopped.Receipt.EntryRequestID, "needs_attention", 1}} {
			s, err := reopened.GetSkillHubPublicationCommitDelivery(ctx, actor, tc.id)
			if err != nil || s == nil || s.State != tc.state || s.Attempts != tc.attempts {
				t.Fatal("dispatcher restart/cancellation", s, err)
			}
		}
		if err := scoped.QueryRow("SELECT count(*) FROM skillhub_publication_commit_permits").Scan(&count); err != nil || count != 1 {
			t.Fatal("dispatcher duplicated permit", err)
		}
	}
	t.Log("actual PG + HTTPS: no service-side intent admission, exact scope, lost reply, reopened replay with identical timestamp, cancel-first denied, historical replay after cancel/disable; no Cloud package writes")
}

func admitHumanCommitPostgres(t *testing.T, db *postgres.Adapter, in store.SkillHubPublicationCommitInput, actor int64) {
	t.Helper()
	verifyHumanRepairContextPostgres(t, db, in, actor)
	verifyHumanRepairAdmissionPostgres(t, db, in, actor)
	c := &humanCommitCloud{in: in, state: "delegated_pending", authorize: func(context.Context) error { return errPublicationCommitCloud }}
	h := newPublicationHumanCommitHandler(db, c, true)
	ts := httptest.NewTLSServer(http.HandlerFunc(h.Handle))
	defer ts.Close()
	call := func(action, body string, want int) []byte {
		t.Helper()
		r := humanCommitRequest(t, action, body, actor)
		r.URL.Path = strings.Replace(r.URL.Path, publicationReadTestID, in.Receipt.Query.EntryID, 1)
		r.URL.Scheme = "https"
		r.URL.Host = strings.TrimPrefix(ts.URL, "https://")
		r.RequestURI = ""
		res, err := ts.Client().Do(r)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != want {
			t.Fatalf("human PG %s: %d %s %v", action, res.StatusCode, b, err)
		}
		return b
	}
	data := call("plan", `{"schemaVersion":1,"confirmVersionPublish":true}`, 200)
	var planned struct {
		Plan struct {
			Key string `json:"confirmationKey"`
		} `json:"plan"`
	}
	if json.Unmarshal(data, &planned) != nil {
		t.Fatal("plan decode")
	}
	hash, _ := in.IdentityHash()
	if planned.Plan.Key != hash {
		t.Fatal("different plan")
	}
	before, err := db.GetSkillHubPublicationCommitContext(context.Background(), actor, in.Receipt.Query.EntryID, true)
	if err != nil || before == nil || before.Intent != nil {
		t.Fatal("plan granted intent", err)
	}
	body := `{"schemaVersion":1,"confirmationKey":"` + hash + `"}`
	call("confirm", body, 503)
	call("confirm", body, 503)
	after, err := db.GetSkillHubPublicationCommitContext(context.Background(), actor, in.Receipt.Query.EntryID, true)
	if err != nil || after == nil || after.Intent == nil || after.Intent.Input != in {
		t.Fatal("human intent not durable", err)
	}
	t.Log("real JWT/HTTPS/PostgreSQL human plan and confirmation recorded one exact intent; Cloud execution failure remains uncertain")
}

func verifyHumanRepairContextPostgres(t *testing.T, db *postgres.Adapter, in store.SkillHubPublicationCommitInput, actor int64) {
	t.Helper()
	c := &humanCommitCloud{in: in}
	c.plan = func(context.Context) error { t.Fatal("context read planned publication"); return nil }
	c.authorize = func(context.Context) error { t.Fatal("context read authorized publication"); return nil }
	peer := &humanReportCloud{c, func(_ context.Context, r store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
		if r != in.Receipt {
			t.Fatal("context read changed persisted receipt")
		}
		return publicationReportFixture(r), nil
	}}
	h := newPublicationHumanCommitHandler(db, peer, true)
	ts := httptest.NewTLSServer(http.HandlerFunc(h.Handle))
	defer ts.Close()
	r := repairContextRequest(t, actor)
	r.URL.Path = strings.Replace(r.URL.Path, publicationReadTestID, in.Receipt.Query.EntryID, 1)
	r.URL.Scheme, r.URL.Host, r.RequestURI = "https", strings.TrimPrefix(ts.URL, "https://"), ""
	res, err := ts.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(res.Body)
	if err != nil || res.StatusCode != 200 || !strings.Contains(string(data), `"maxFormatRepairRounds":3`) ||
		!strings.Contains(string(data), `"entryRequestId":"`+in.Receipt.Query.EntryID+`"`) {
		t.Fatalf("repair context PG status=%d data=%s err=%v", res.StatusCode, data, err)
	}
	state, err := db.GetSkillHubPublicationCommitContext(context.Background(), actor, in.Receipt.Query.EntryID, true)
	if err != nil || state == nil || state.Intent != nil || state.Receipt == nil || *state.Receipt != in.Receipt {
		t.Fatal("context read mutated publication evidence", err)
	}
	t.Log("real JWT/HTTPS/PostgreSQL repair context read retained original receipt and no commit intent; Cloud rejection is synthetic")
}
