package postgres

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func publicationReceiptMigration(t *testing.T, direction string) string {
	t.Helper()
	b, err := os.ReadFile("../migrations/postgres/000037_skillhub_publication_cloud_receipts." + direction + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSkillHubPublicationReceiptMigrationMatchesStartup(t *testing.T) {
	if strings.Join(strings.Fields(publicationReceiptMigration(t, "up")), " ") != strings.Join(strings.Fields(createSkillHubPublicationCloudReceiptsTable), " ") {
		t.Fatal("000037 differs from startup")
	}
}

func TestPostgresSkillHubPublicationReceiptMigration(t *testing.T) {
	t.Parallel()
	for _, order := range []string{"empty_sql", "historical_sql_first", "historical_startup_first"} {
		t.Run(order, func(t *testing.T) {
			db, _ := publicationEmptyDatabase(t)
			if order != "empty_sql" {
				b, err := os.ReadFile("testdata/skillhub_publication_pre620.sql")
				if err != nil {
					t.Fatal(err)
				}
				deliveryExec(t, db, string(b))
				b, err = os.ReadFile("testdata/skillhub_publication_pre_receipt_generations.sql")
				if err != nil {
					t.Fatal(err)
				}
				deliveryExec(t, db, string(b))
			}
			for _, sql := range []string{publicationMigrationSQL(t, "up"), publicationDeliveryMigration(t, "up"), publicationCancellationMigration(t, "up"), publicationUploadMigration(t, "up")} {
				deliveryExec(t, db, sql)
			}
			var g *store.SkillHubPublicationUploadGrant
			var token store.SkillHubPublicationUploadToken
			if order != "empty_sql" {
				in := store.SkillHubPublicationEntryInput{ActorUID: 1001, BotUID: 1002, RequestID: "historical-cloud-receipt", Entrypoint: "workspace",
					Source: store.SkillHubPublicationSource{DeviceID: "device", BodyID: "legacy-body", InstallationID: "install", RouteGeneration: 1, WorkspaceGeneration: "workspace", LocalSkillID: "local"}}
				e, _, err := db.CreateSkillHubPublicationEntry(context.Background(), in, store.DefaultSkillHubPublicationEntryPolicy())
				if err != nil {
					t.Fatal(err)
				}
				if _, err := db.BumpBotConnectionGeneration(in.BotUID); err != nil {
					t.Fatal(err)
				}
				b := store.SkillHubPublicationDeliveryBinding{ActorUID: in.ActorUID, BotUID: in.BotUID, Source: in.Source, NodeID: "node", ConnectionID: "socket"}
				lease := deliveryClaim(t, db, e.ID, b)
				if err := db.FinishSkillHubPublicationDelivery(context.Background(), *lease, ""); err != nil {
					t.Fatal(err)
				}
				upload := store.SkillHubPublicationUploadInput{EntryID: e.ID, Binding: b, Issuer: "https://cats.example", Audience: "https://hub.example", Action: store.SkillHubPublicationUploadAction, Revision: 1, RequestHash: strings.Repeat("a", 64), SourceSnapshotHash: strings.Repeat("b", 64), RequestBytes: 100}
				g, token = seedLegacyUploadGrant(t, db, upload) // Frozen pre-capture/pre-receipt historical authority.
			}
			if order == "historical_startup_first" {
				if err := db.CreateSchema(); err != nil {
					t.Fatal(err)
				}
			}
			up, down := publicationReceiptMigration(t, "up"), publicationReceiptMigration(t, "down")
			deliveryExec(t, db, commitDeliveryMigration(t, "down"))
			deliveryExec(t, db, publicationCommitMigration(t, "down"))
			for _, sql := range []string{up, up, down, down, up} {
				deliveryExec(t, db, sql)
			}
			if err := db.CreateSchema(); err != nil {
				t.Fatal(err)
			}
			if g == nil {
				publicationCounts(t, db, 0)
				return
			}
			if _, err := db.CheckSkillHubPublicationUploadGrant(context.Background(), token, g.Input, g.Epoch); err != nil {
				t.Fatal("upgrade damaged historical grant", err)
			}
			r := receiptForGrant(g)
			recordReceipt(t, db, r, false)
			before := receiptSnapshot(t, db, g.Input.EntryID)
			if _, err := db.db.Exec(down); err == nil || !strings.Contains(err.Error(), "retained publication Cloud receipts") {
				t.Fatalf("unsafe down: %v", err)
			}
			deliveryExec(t, db, up)
			if err := db.CreateSchema(); err != nil {
				t.Fatal(err)
			}
			recordReceipt(t, db, r, false)
			if receiptSnapshot(t, db, g.Input.EntryID) != before {
				t.Fatal("upgrade changed receipt")
			}
			var model, body string
			if err := db.db.QueryRow(`SELECT model,body_id FROM bot_config WHERE user_id=1002`).Scan(&model, &body); err != nil || model != "unchanged-model" || body != "legacy-body" {
				t.Fatal("historical Bot changed")
			}
			publicationCounts(t, db, 1)
		})
	}
}
