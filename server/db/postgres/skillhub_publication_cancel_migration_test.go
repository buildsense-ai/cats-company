package postgres

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func publicationCancellationMigration(t *testing.T, direction string) string {
	t.Helper()
	data, err := os.ReadFile("../migrations/postgres/000035_skillhub_publication_cancellation." + direction + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSkillHubPublicationCancellationMigrationMatchesStartup(t *testing.T) {
	if strings.Join(strings.Fields(publicationCancellationMigration(t, "up")), " ") != strings.Join(strings.Fields(migrateSkillHubPublicationCancellation), " ") {
		t.Fatal("migration 000035 must mirror startup")
	}
}

func TestPostgresSkillHubPublicationCancellationMigration(t *testing.T) {
	t.Parallel()
	for _, order := range []string{"empty_sql", "historical_sql_first", "historical_startup_first"} {
		t.Run(order, func(t *testing.T) {
			db, _ := publicationEmptyDatabase(t)
			up, down := publicationCancellationMigration(t, "up"), publicationCancellationMigration(t, "down")
			// Frozen relevant tables plus unchanged 000028/000030 DDL reproduce
			// the pre-PR cancellation schema, not a full production backup.
			baseline, err := os.ReadFile("testdata/skillhub_publication_pre620.sql")
			if err != nil {
				t.Fatal(err)
			}
			if order != "empty_sql" {
				deliveryExec(t, db, string(baseline))
			}
			deliveryExec(t, db, publicationMigrationSQL(t, "up"))
			deliveryExec(t, db, publicationDeliveryMigration(t, "up"))
			in := store.SkillHubPublicationEntryInput{ActorUID: 1001, BotUID: 1002, RequestID: "historical-cancel", Entrypoint: "workspace", Source: store.SkillHubPublicationSource{DeviceID: "device", BodyID: "legacy-body", InstallationID: "install", RouteGeneration: 1, WorkspaceGeneration: "workspace", LocalSkillID: "local"}}
			var old *store.SkillHubPublicationEntry
			if order != "empty_sql" {
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
			deliveryExec(t, db, up)
			deliveryExec(t, db, up)
			deliveryExec(t, db, down) // Safe only before any cancellation evidence.
			deliveryExec(t, db, up)
			if err := db.CreateSchema(); err != nil {
				t.Fatal(err)
			}
			if err := db.CreateSchema(); err != nil {
				t.Fatal(err)
			}
			if old == nil {
				publicationCounts(t, db, 0)
				return
			}
			got, err := db.GetSkillHubPublicationEntry(context.Background(), 1001, old.ID)
			if err != nil || got.Input != old.Input || got.IdentityHash != old.IdentityHash || got.State != "waiting_runtime" {
				t.Fatal("upgrade changed historical intent")
			}
			if _, err := db.CancelSkillHubPublicationEntry(context.Background(), 1001, old.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := db.db.Exec(down); err == nil || !strings.Contains(err.Error(), "retained cancellation evidence") {
				t.Fatalf("unsafe rollback: %v", err)
			}
			deliveryExec(t, db, up)
			if err := db.CreateSchema(); err != nil {
				t.Fatal(err)
			}
			replay, yes, err := db.CreateSkillHubPublicationEntry(context.Background(), in, store.DefaultSkillHubPublicationEntryPolicy())
			if err != nil || !yes || replay.State != "cancelled" {
				t.Fatal("upgrade/replay revived cancellation")
			}
			var model, body string
			if err := db.db.QueryRow(`SELECT model,body_id FROM bot_config WHERE user_id=1002`).Scan(&model, &body); err != nil || model != "unchanged-model" || body != "legacy-body" {
				t.Fatal("legacy Bot modified")
			}
			publicationCounts(t, db, 1)
		})
	}
}
