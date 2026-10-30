package server

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
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
		if os.Getenv("CATSCO_REQUIRE_REPAIR_MATERIAL_CONTRACT") == "1" {
			t.Fatal("real Cloud checkout and PostgreSQL are mandatory")
		}
		t.Skip("requires Cloud checkout and migrated PostgreSQL")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	cmd := exec.CommandContext(ctx, "node", filepath.Join("..", "scripts", "fixtures", "publication-repair-material-cloud.mjs"))
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
	receipt := store.SkillHubPublicationCloudReceipt{Query: store.SkillHubPublicationUploadQuery{
		EntryID: r.EntryRequestID, Issuer: setup.Issuer, Audience: setup.Audience, Action: store.SkillHubPublicationUploadAction,
		Revision: r.CandidateRevision, Epoch: r.AcceptanceEpoch, RequestHash: r.RequestHash,
		SourceSnapshotHash: r.SourceSnapshotHash, RequestBytes: r.RequestBytes},
		DelegationID: r.DelegationID, IdempotencyKey: r.IdempotencyKey, TaskID: r.TaskID, EventID: r.EventID}
	if receipt.Validate() != nil || len(setup.Raw) == 0 {
		t.Fatal("invalid fixture receipt/material")
	}
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
		material, err := projectPublicationRepairMaterial(ctx, task, got, setup.Report)
		if err != nil || material.SourceText == "" || !strings.Contains(material.SourceText, `"entryIndex":2,"path":"SKILL.md"`) {
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
	if s := command("state"); s.Requests != 3 || !s.Unchanged {
		t.Fatal("replay changed retained task", s)
	}
	command("disable")
	denied(client)
	if s := command("state"); s.Requests != 4 || !s.Unchanged {
		t.Fatal("disabled read retried or mutated task", s)
	}
	command("enable")
	exact(client)
	if s := command("state"); s.Requests != 5 || !s.Unchanged {
		t.Fatal("reenabled read changed task", s)
	}
	if s := command("cancel"); s.State != "cancelled" {
		t.Fatal("cancel did not settle", s)
	}
	command("restart")
	denied(newClient())
	if s := command("state"); s.Requests != 6 || s.State != "cancelled" || !s.CancelledUnchanged {
		t.Fatal("cancelled read retried or resurrected task", s)
	}
	t.Log("real CatsCo client -> Cloud HTTPS handler -> PostgreSQL -> strict original projection: Cloud semantic hash matches Go across UTF-8/non-BMP/U+2028/U+2029/<>& and Base64; preserved source index; lost response without auto-retry, reconstructed replay, disabled publisher/cancellation deny; zero commits/permits; local task/upstream grant synthetic, no Runtime/model")
}
