package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store"
)

func TestPublicationRepairMaterialCloudPostgres(t *testing.T) {
	if os.Getenv("SKILLHUB_REPAIR_MATERIAL_ROOT") == "" || os.Getenv("SKILLHUB_TEST_DATABASE_URL") == "" {
		if os.Getenv("CATSCO_REQUIRE_REPAIR_MATERIAL_CONTRACT") == "1" || os.Getenv("CATSCO_REQUIRE_REPAIR_RUNTIME_CLOUD_CONTRACT") == "1" {
			t.Fatal("real Cloud checkout and PostgreSQL are mandatory")
		}
		t.Skip("requires Cloud checkout and migrated PostgreSQL")
	}
	var runtime *repairSourcePairedRuntime
	var runtimeFixture *publicationWorkspaceFixture
	var runtimeStore *repairSourceTestStore
	entryID := publicationReadTestID
	candidatePath := ""
	if xiaoba := os.Getenv("XIAOBA_REPAIR_PAIRED_ROOT"); xiaoba != "" {
		idBytes := make([]byte, 16)
		if _, err := rand.Read(idBytes); err != nil {
			t.Fatal("generate paired entry ID", err)
		}
		entryID = "pubentry_" + hex.EncodeToString(idBytes)
		runtime = startRepairSourcePairedRuntime(t, "missing-description")
		runtimeFixture, runtimeStore = repairSourcePairedFixtureEntryID(t, runtime, true, entryID)
		captured := runtime.candidate(t, entryID)
		candidatePath = filepath.Join(t.TempDir(), "runtime-candidate.json")
		if err := os.WriteFile(candidatePath, captured.Body, 0o600); err != nil {
			t.Fatal("write isolated candidate fixture", err)
		}
		capture := runtimeStore.capture.Input
		if capture.EntryID != captured.EntryID || capture.RequestHash != captured.RequestHash || capture.SourceSnapshotHash != captured.SourceSnapshotHash || capture.RequestBytes != captured.RequestBytes ||
			runtimeStore.app.Input.Receipt.Query.RequestHash != captured.RequestHash || runtimeStore.app.Input.Receipt.Query.SourceSnapshotHash != captured.SourceSnapshotHash || runtimeStore.app.Input.Receipt.Query.RequestBytes != captured.RequestBytes {
			t.Fatal("local source authority fixture lost the Runtime capture binding")
		}
	} else if os.Getenv("CATSCO_REQUIRE_REPAIR_RUNTIME_CLOUD_CONTRACT") == "1" {
		t.Fatal("pinned XiaoBa Runtime is mandatory for the combined contract")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "scripts", "fixtures", "publication-repair-material-cloud.mjs"))
	cmd.Env = os.Environ()
	if candidatePath != "" {
		cmd.Env = append(cmd.Env, "SKILLHUB_REPAIR_PAIRED_CANDIDATE_PATH="+candidatePath, "SKILLHUB_REPAIR_PAIRED_ENTRY_ID="+entryID)
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	// Diagnostics stay in the test process; no real credentials or candidates.
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		stdin.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Cloud fixture failed: %v: %s", err, stderr.String())
			}
		case <-time.After(10 * time.Second):
			cancel()
			<-done
			t.Error("Cloud fixture cleanup timed out")
		}
	})
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 4096), 16384)
	read := func(out any) {
		t.Helper()
		if !scanner.Scan() {
			t.Fatalf("Cloud fixture EOF: %v", scanner.Err())
		}
		if err := json.Unmarshal(scanner.Bytes(), out); err != nil {
			t.Fatal(err)
		}
	}
	var setup struct {
		Issuer, Audience, CA string
		Receipt              publicationCloudReceiptRequest
		Raw                  []byte
		Report               PublicationValidationReport
	}
	read(&setup)
	r := setup.Receipt
	if runtime != nil && (r.EntryRequestID != entryID || r.RequestHash != runtimeStore.capture.Input.RequestHash || r.SourceSnapshotHash != runtimeStore.capture.Input.SourceSnapshotHash || r.RequestBytes != runtimeStore.capture.Input.RequestBytes) {
		t.Fatal("Runtime capture and Cloud rejection do not share the exact entry/hash/bytes")
	}
	receipt := store.SkillHubPublicationCloudReceipt{Query: store.SkillHubPublicationUploadQuery{
		EntryID: r.EntryRequestID, Issuer: setup.Issuer, Audience: setup.Audience, Action: store.SkillHubPublicationUploadAction,
		Revision: r.CandidateRevision, Epoch: r.AcceptanceEpoch, RequestHash: r.RequestHash,
		SourceSnapshotHash: r.SourceSnapshotHash, RequestBytes: r.RequestBytes},
		DelegationID: r.DelegationID, IdempotencyKey: r.IdempotencyKey, TaskID: r.TaskID, EventID: r.EventID}
	if receipt.Validate() != nil || len(setup.Raw) == 0 {
		t.Fatal("invalid fixture receipt/material")
	}
	projectionReport := setup.Report
	// Synthetic local task only; Cloud source/hash/report and HTTPS/store are
	// real. Current CatsCo task and live Runtime authority remain future work.
	local, _, _, _, _ := repairModelTestFixture(t)
	task := local.task
	task.Input.Receipt = receipt
	task.Rounds[0].Lease.EntryID = receipt.Query.EntryID
	bindProjectionTestTask(t, &task, setup.Report)
	ca := x509.NewCertPool()
	if !ca.AppendCertsFromPEM([]byte(setup.CA)) {
		t.Fatal("invalid fixture CA")
	}
	newClient := func() *publicationRepairMaterialClient {
		t.Helper()
		c, err := newPublicationRepairMaterialClient(publicationRepairMaterialConfig{
			Enabled: true, Issuer: setup.Issuer, Audience: setup.Audience, PeerSecret: "pubmaterial_" + strings.Repeat("a", 64)})
		if err != nil {
			t.Fatal(err)
		}
		tr := c.client.Transport.(*http.Transport)
		tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: ca}
		t.Cleanup(tr.CloseIdleConnections)
		return c
	}
	type state struct {
		Command, State             string
		Requests, Commits, Permits int
		Unchanged                  bool
		CancelledUnchanged         bool
	}
	command := func(name string) state {
		t.Helper()
		if _, err := io.WriteString(stdin, `{"command":"`+name+`"}`+"\n"); err != nil {
			t.Fatal(err)
		}
		var s state
		read(&s)
		if s.Command != name || s.Commits != 0 || s.Permits != 0 {
			t.Fatal("unexpected fixture state", s)
		}
		return s
	}
	exact := func(c *publicationRepairMaterialClient) {
		t.Helper()
		got, err := c.readOriginal(ctx, receipt)
		if err != nil || !bytes.Equal(got, setup.Raw) {
			t.Fatal("real material read failed", err)
		}
		material, err := projectPublicationRepairMaterial(ctx, task, got, projectionReport)
		skillIndex := `"entryIndex":2,"path":"SKILL.md"`
		if runtime != nil {
			skillIndex = `"entryIndex":0,"path":"SKILL.md"`
		}
		if err != nil || material.SourceText == "" || !strings.Contains(material.SourceText, skillIndex) {
			t.Fatal("Cloud candidate/report did not project with exact source hash/order", err)
		}
	}
	denied := func(c *publicationRepairMaterialClient) {
		t.Helper()
		got, err := c.readOriginal(ctx, receipt)
		if got != nil || !errors.Is(err, errPublicationRepairMaterialPeer) {
			t.Fatal("denied read leaked material", err)
		}
	}
	client := newClient()
	command("lose")
	denied(client)
	if s := command("state"); s.Requests != 1 || !s.Unchanged {
		t.Fatal("lost reply retried or mutated task", s)
	}
	command("restart")
	client = newClient()
	exact(client)
	exact(client)
	// Compose both real Cloud/PostgreSQL reads with the constrained coordinator.
	// Runtime authority, provider and dispatch journal remain explicit test doubles.
	reportClient, err := NewPublicationCommitCloudClient(PublicationCommitCloudConfig{
		Enabled: true, Issuer: setup.Issuer, Audience: setup.Audience, PeerSecret: strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal("construct Cloud report client", err)
	}
	reportTransport := reportClient.client.Transport.(*http.Transport)
	reportTransport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: ca}
	t.Cleanup(reportTransport.CloseIdleConnections)
	// Intake validates independently and persists its own immutable report ID.
	// Seed the synthetic task from that exact retained report, as real admission does.
	admissionReport, err := reportClient.Report(ctx, receipt)
	if err != nil || admissionReport == nil || admissionReport.Receipt != publicationControlReceipt(receipt) {
		t.Fatal("read retained Cloud report for synthetic admission", err)
	}
	projectionReport = admissionReport.Validation
	modelStore, lease, modelConfig, _, _ := repairModelTestFixture(t)
	task = modelStore.task
	task.Input.Receipt = receipt
	lease.EntryID = receipt.Query.EntryID
	task.Rounds[0].Lease = lease
	bindProjectionTestTask(t, &task, admissionReport.Validation)
	lease = task.Rounds[0].Lease
	modelStore.task = snapshotPublicationRepairTask(task)
	if runtimeStore != nil {
		runtimeStore.task = snapshotPublicationRepairTask(task)
	}
	var sourceChecks, invocations int
	var reportReadErr, materialReadErr error
	var cloudReport *PublicationCloudValidationReport
	reports := repairMaterialReportReaderFunc(func(readCtx context.Context, trusted store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
		out, readErr := reportClient.Report(readCtx, trusted)
		reportReadErr = readErr
		cloudReport = out
		return out, readErr
	})
	materials := repairOriginalMaterialReaderFunc(func(readCtx context.Context, trusted store.SkillHubPublicationCloudReceipt) ([]byte, error) {
		out, readErr := client.readOriginal(readCtx, trusted)
		materialReadErr = readErr
		return out, readErr
	})
	ports := newPublicationRepairModelPorts(
		func(_ context.Context, got store.SkillHubPublicationRepairTask, gotLease store.SkillHubPublicationRepairLease) error {
			sourceChecks++
			if got.ID != task.ID || got.IdentityHash != task.IdentityHash || !samePublicationRepairModelLease(gotLease, lease) {
				t.Fatal("Runtime verifier lost the synthetic task/lease binding")
			}
			if runtime != nil {
				return repairTaskSourcePairedCall(t, runtimeFixture, got, runtime)
			}
			return nil
		}, reports, materials,
		func(_ context.Context, request publicationRepairModelRequest) (store.SkillHubPublicationRepairModelOutput, error) {
			invocations++
			if request.TaskID != task.ID || request.SourceText == "" || request.Report.CandidateHash != receipt.Query.SourceSnapshotHash {
				t.Fatal("coordinator did not pass verified Cloud report and projected private material")
			}
			return store.SkillHubPublicationRepairModelOutput{Text: "synthetic draft only"}, nil
		},
	)
	result, err := runPublicationRepairModel(ctx, modelStore, 1, lease, modelConfig, ports)
	if err != nil || result == nil || result.call.Output == nil || result.call.Output.Text != "synthetic draft only" || invocations != 1 || sourceChecks < 5 {
		if cloudReport == nil {
			t.Fatal("real Cloud reads did not compose through the constrained coordinator", err, "report read:", reportReadErr, "material read:", materialReadErr, "report: nil", "invocations:", invocations, "source checks:", sourceChecks)
		}
		reportEvidence, evidenceErr := publicationRepairEvidence(cloudReport.Validation, receipt.Query.SourceSnapshotHash)
		t.Fatal("real Cloud reads did not compose through the constrained coordinator", err,
			"report read:", reportReadErr, "material read:", materialReadErr,
			"report schema:", cloudReport.SchemaVersion, "receipt matches:", cloudReport.Receipt == publicationControlReceipt(receipt),
			"report valid:", validPublicationValidationReport(cloudReport.Validation, receipt.Query.SourceSnapshotHash),
			"outcome:", cloudReport.Validation.Outcome, "truncated:", cloudReport.Validation.Truncated,
			"candidate matches:", cloudReport.Validation.CandidateHash == receipt.Query.SourceSnapshotHash,
			"evidence matches:", evidenceErr == nil && reportEvidence == task.Input.Evidence, "evidence error:", evidenceErr,
			"invocations:", invocations, "source checks:", sourceChecks)
	}
	if s := command("state"); s.Requests != 6 || !s.Unchanged || s.Commits != 0 || s.Permits != 0 {
		t.Fatal("replay changed retained task", s)
	}
	command("disable")
	denied(client)
	if s := command("state"); s.Requests != 7 || !s.Unchanged {
		t.Fatal("disabled read retried or mutated task", s)
	}
	command("enable")
	exact(client)
	if s := command("state"); s.Requests != 8 || !s.Unchanged {
		t.Fatal("reenabled read changed task", s)
	}
	if s := command("cancel"); s.State != "cancelled" {
		t.Fatal("cancel did not settle", s)
	}
	command("restart")
	denied(newClient())
	if s := command("state"); s.Requests != 9 || s.State != "cancelled" || !s.CancelledUnchanged {
		t.Fatal("cancelled read retried or resurrected task", s)
	}
	if runtime != nil {
		t.Log("same captured Runtime candidate -> real Cloud HTTPS/PostgreSQL rejection, report and original-material reads -> constrained coordinator with real Runtime source checks; synthetic provider/dispatch only, no publication commits/permits")
	} else {
		// The mandatory runner separately exercises the Cloud-owned Unicode
		// vectors in this mode, with only Runtime authority synthetic.
		t.Log("Cloud-owned exotic encoding vectors exercised")
		t.Log("real CatsCo Cloud report/material clients -> Cloud HTTPS handlers -> PostgreSQL -> constrained coordinator and strict projection; Runtime verifier, provider and dispatch store synthetic; exactly one synthetic invocation; lost response without retry, reconstructed replay, disabled publisher/cancellation deny; zero Cloud commits/permits")
	}
}
