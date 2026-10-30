package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openchat/openchat/server/db/postgres"
	"github.com/openchat/openchat/server/store"
)

// Required by the existing real JWT/HTTPS/PostgreSQL publication contract. The
// model text and Cloud report are synthetic; no candidate or live Skill is edited.
func verifyHumanDescriptionConfirmationPostgres(t *testing.T, db *postgres.Adapter, in store.SkillHubPublicationCommitInput, actor int64) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	id := in.Receipt.Query.EntryID
	task, err := db.GetSkillHubPublicationRepairTask(ctx, actor, id)
	if err != nil || task == nil {
		t.Fatal("description task", err)
	}
	round, err := db.ClaimSkillHubPublicationRepairRound(ctx, actor, id, store.SkillHubPublicationRepairRoundInput{RequestID: "description-contract-round-1", Evidence: task.Input.Evidence}, "description-contract-worker")
	if err != nil {
		t.Fatal(err)
	}
	index := int64(0)
	modelDraft := verifyRepairModelExecutionPostgres(t, ctx, db, actor, round.Lease, in.Receipt)
	draft, err := proposePublicationDescription(ctx, db, actor, round.Lease, publicationReportFixture(in.Receipt).Validation,
		store.SkillHubPublicationDescriptionProposal{RelativePath: "SKILL.md", Field: "description", EntryIndex: &index, Text: modelDraft})
	if err != nil {
		t.Fatal("retaining verified draft", err)
	}
	cloud := &humanReportCloud{&humanCommitCloud{in: in}, func(context.Context, store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
		return nil, fmt.Errorf("draft confirmation must not call Cloud")
	}}
	open := func() *SkillHubPublicationHumanCommitHandler {
		h := newPublicationHumanCommitHandler(db, cloud, true)
		h.repairEnabled = true
		return h
	}
	var current atomic.Pointer[SkillHubPublicationHumanCommitHandler]
	current.Store(open())
	var lose atomic.Bool
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		record := httptest.NewRecorder()
		current.Load().Handle(&permitContractWriter{ResponseWriter: w, record: record}, r)
		if r.Method == "POST" && record.Code == 200 && lose.Swap(false) {
			conn, _, err := http.NewResponseController(w).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		for k, v := range record.Header() {
			w.Header()[k] = v
		}
		w.WriteHeader(record.Code)
		w.Write(record.Body.Bytes())
	}))
	defer ts.Close()
	call := func(method, body string, want int) []byte {
		t.Helper()
		r := humanCommitRequest(t, "repair-description", body, actor).WithContext(ctx)
		r.Method = method
		r.URL.Path = strings.Replace(r.URL.Path, publicationReadTestID, id, 1)
		r.URL.Scheme, r.URL.Host, r.RequestURI = "https", strings.TrimPrefix(ts.URL, "https://"), ""
		res, err := ts.Client().Do(r)
		if want == 0 {
			if err == nil {
				res.Body.Close()
				t.Fatal("did not lose committed confirmation response")
			}
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, err := io.ReadAll(res.Body)
		if err != nil || res.StatusCode != want {
			t.Fatalf("description %s %d %s %v", method, res.StatusCode, b, err)
		}
		return b
	}
	before := call("GET", "", 200)
	if !strings.Contains(string(before), `"state":"awaiting_confirmation"`) {
		t.Fatal("GET confirmed a draft")
	}
	call("POST", `{"schemaVersion":1,"confirmationKey":"`+strings.Repeat("f", 64)+`"}`, 409)
	body := `{"schemaVersion":1,"confirmationKey":"` + draft.ConfirmationKey + `"}`
	lose.Store(true)
	call("POST", body, 0)
	retained, err := db.GetSkillHubPublicationDescription(ctx, actor, id)
	if err != nil || retained == nil || retained.ConfirmedAt == nil {
		t.Fatal("confirmation not retained before lost reply", err)
	}
	current.Store(open())
	get := call("GET", "", 200)
	replay := call("POST", body, 200)
	if string(get) != string(replay) {
		t.Fatal("restarted handler changed confirmation")
	}
	var out struct {
		Automatic bool `json:"automaticRepairEnabled"`
		Draft     struct {
			Key   string `json:"confirmationKey"`
			Text  string `json:"text"`
			State string `json:"state"`
		} `json:"draft"`
	}
	if json.Unmarshal(replay, &out) != nil || out.Automatic || out.Draft.Key != draft.ConfirmationKey || out.Draft.Text != draft.Input.Proposal.Text || out.Draft.State != "confirmed" {
		t.Fatal("wrong confirmed draft projection")
	}
	after, err := db.GetSkillHubPublicationRepairTask(ctx, actor, id)
	if err != nil || after == nil || len(after.Rounds) != 1 || after.State() != "waiting_confirmation" {
		t.Fatal("confirmation reset budget or ran work", err)
	}
	local, err := db.GetSkillHubPublicationCommitContext(ctx, actor, id, true)
	if err != nil || local == nil || local.Intent != nil || local.Receipt == nil || *local.Receipt != in.Receipt {
		t.Fatal("confirmation changed receipt or published", err)
	}
	t.Log("real JWT/HTTPS/PostgreSQL description confirmation: exact text, wrong key rejected, committed response loss, reconstructed-handler GET/POST recovery, same one-round waiting checkpoint; synthetic model draft/report, no model/file mutation or publication")
	verifyDescriptionContinuationPostgres(t, db, in, actor)
}
