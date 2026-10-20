package postgres

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func publicationUploadMigration(t *testing.T, direction string) string {
	t.Helper()
	b, err := os.ReadFile("../migrations/postgres/000036_skillhub_publication_upload_grants." + direction + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSkillHubPublicationUploadMigrationMatchesStartup(t *testing.T) {
	if strings.Join(strings.Fields(publicationUploadMigration(t, "up")), " ") != strings.Join(strings.Fields(createSkillHubPublicationUploadGrantsTable), " ") {
		t.Fatal("000036 differs from startup")
	}
}

func TestPostgresSkillHubPublicationUploadMigration(t *testing.T) {
	t.Parallel()
	for _, order := range []string{"empty_sql", "historical_sql_first", "historical_startup_first"} {
		t.Run(order, func(t *testing.T) {
			db, _ := publicationEmptyDatabase(t)
			if order != "empty_sql" {
				baseline, err := os.ReadFile("testdata/skillhub_publication_pre620.sql")
				if err != nil {
					t.Fatal(err)
				}
				deliveryExec(t, db, string(baseline))
			}
			// Frozen pre-grant relevant schema; never edit applied migrations.
			deliveryExec(t, db, publicationMigrationSQL(t, "up"))
			deliveryExec(t, db, publicationDeliveryMigration(t, "up"))
			deliveryExec(t, db, publicationCancellationMigration(t, "up"))
			in := store.SkillHubPublicationEntryInput{ActorUID: 1001, BotUID: 1002, RequestID: "historical-upload", Entrypoint: "workspace",
				Source: store.SkillHubPublicationSource{DeviceID: "device", BodyID: "legacy-body", InstallationID: "install", RouteGeneration: 1, WorkspaceGeneration: "workspace", LocalSkillID: "local"}}
			var old *store.SkillHubPublicationEntry
			if order != "empty_sql" {
				var err error
				old, _, err = db.CreateSkillHubPublicationEntry(context.Background(), in, store.DefaultSkillHubPublicationEntryPolicy())
				if err != nil {
					t.Fatal(err)
				}
			}
			if order == "historical_startup_first" {
				if err := db.CreateSchema(); err != nil {
					t.Fatal(err)
				}
			}
			up, down := publicationUploadMigration(t, "up"), publicationUploadMigration(t, "down")
			deliveryExec(t, db, up)
			deliveryExec(t, db, up)
			deliveryExec(t, db, down)
			deliveryExec(t, db, down)
			deliveryExec(t, db, up)
			for i := 0; i < 2; i++ {
				if err := db.CreateSchema(); err != nil {
					t.Fatal(err)
				}
			}
			if old == nil {
				publicationCounts(t, db, 0)
				return
			}
			got, err := db.GetSkillHubPublicationEntry(context.Background(), in.ActorUID, old.ID)
			if err != nil || got == nil || got.Input != old.Input || got.IdentityHash != old.IdentityHash || got.State != old.State || !got.CreatedAt.Equal(old.CreatedAt) {
				t.Fatal("upgrade changed historical intent")
			}
			if _, err := db.BumpBotConnectionGeneration(in.BotUID); err != nil {
				t.Fatal(err)
			}
			b := store.SkillHubPublicationDeliveryBinding{ActorUID: in.ActorUID, BotUID: in.BotUID, Source: in.Source, NodeID: "node", ConnectionID: "socket"}
			lease := deliveryClaim(t, db, old.ID, b)
			if err := db.FinishSkillHubPublicationDelivery(context.Background(), *lease, ""); err != nil {
				t.Fatal(err)
			}
			upload := store.SkillHubPublicationUploadInput{EntryID: old.ID, Binding: b, Issuer: "https://cats.example", Audience: "https://hub.example",
				Action: store.SkillHubPublicationUploadAction, Revision: 1, RequestHash: strings.Repeat("a", 64), SourceSnapshotHash: strings.Repeat("b", 64), RequestBytes: 100}
			recordCaptureForTest(t, db, captureForUpload(upload))
			g, token := issueUpload(t, db, upload)
			if _, err := db.db.Exec(down); err == nil || !strings.Contains(err.Error(), "retained publication upload authority") {
				t.Fatalf("unsafe down: %v", err)
			}
			deliveryExec(t, db, up)
			if err := db.CreateSchema(); err != nil {
				t.Fatal(err)
			}
			if checked, err := db.CheckSkillHubPublicationUploadGrant(context.Background(), token, upload, g.Epoch); err != nil || checked.IdempotencyKey != g.IdempotencyKey {
				t.Fatal("startup destroyed grant")
			}
			if _, err := db.CancelSkillHubPublicationEntry(context.Background(), in.ActorUID, old.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.db.Exec(down); err == nil {
				t.Fatal("cancelled grant evidence deleted")
			}
			var model, body string
			if err := db.db.QueryRow(`SELECT model,body_id FROM bot_config WHERE user_id=1002`).Scan(&model, &body); err != nil || model != "unchanged-model" || body != "legacy-body" {
				t.Fatal("historical Bot modified")
			}
			publicationCounts(t, db, 1)
		})
	}
}
