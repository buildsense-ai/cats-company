package postgres

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func publicationDeliveryMigration(t *testing.T, direction string) string {
	t.Helper()
	data, err := os.ReadFile("../migrations/postgres/000030_skillhub_publication_delivery." + direction + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSkillHubPublicationDeliveryMigrationMatchesStartup(t *testing.T) {
	if strings.Join(strings.Fields(publicationDeliveryMigration(t, "up")), " ") != strings.Join(strings.Fields(migrateSkillHubPublicationDelivery), " ") {
		t.Fatal("migration 000030 must mirror startup delivery DDL")
	}
}

func TestPostgresSkillHubPublicationDeliveryMigration(t *testing.T) {
	t.Parallel()
	for _, order := range []string{"empty_sql", "historical_sql_first", "historical_startup_first"} {
		t.Run(order, func(t *testing.T) {
			db, _ := publicationEmptyDatabase(t)
			up, down := publicationDeliveryMigration(t, "up"), publicationDeliveryMigration(t, "down")
			startup := func() {
				t.Helper()
				if err := db.CreateSchema(); err != nil {
					t.Fatal(err)
				}
			}
			var entryID string
			in := store.SkillHubPublicationEntryInput{ActorUID: 1001, BotUID: 1002, RequestID: "historical-delivery", Entrypoint: "workspace",
				Source: store.SkillHubPublicationSource{DeviceID: "old-device", BodyID: "legacy-body", InstallationID: "old-install", RouteGeneration: 1, WorkspaceGeneration: "old-workspace", LocalSkillID: "old-skill"}}
			// Freeze pre-PR627 publication DDL with the existing relevant user/Bot
			// fixture. Seed retained intent BEFORE any new column is installed.
			if order != "empty_sql" {
				baseline, err := os.ReadFile("testdata/skillhub_publication_pre620.sql")
				if err != nil {
					t.Fatal(err)
				}
				deliveryExec(t, db, string(baseline))
			}
			deliveryExec(t, db, publicationMigrationSQL(t, "up"))
			if order != "empty_sql" {
				e, _, err := db.CreateSkillHubPublicationEntry(context.Background(), in, store.DefaultSkillHubPublicationEntryPolicy())
				if err != nil {
					t.Fatal(err)
				}
				entryID = e.ID
			}
			if order == "historical_startup_first" {
				startup()
				startup()
			}
			deliveryExec(t, db, up)
			deliveryExec(t, db, up)
			// With no delivery attempt, rolling back only new columns preserves
			// old entries/outbox, even when the historical database is populated.
			deliveryExec(t, db, down)
			if order == "empty_sql" {
				publicationCounts(t, db, 0)
			} else {
				publicationCounts(t, db, 1)
			}
			deliveryExec(t, db, up)
			startup()
			startup()
			deliveryExec(t, db, up)
			if order == "empty_sql" {
				return
			}
			old, err := db.GetSkillHubPublicationEntry(context.Background(), 1001, entryID)
			if err != nil || old == nil || old.Input != in {
				t.Fatalf("historical facts: %+v %v", old, err)
			}
			var state string
			var attempts int
			if err := db.db.QueryRow(`SELECT delivery_state,delivery_attempts FROM skillhub_publication_outbox WHERE entry_id=$1`, entryID).Scan(&state, &attempts); err != nil {
				t.Fatal(err)
			}
			if state != "pending" || attempts != 0 {
				t.Fatalf("defaults: %s %d", state, attempts)
			}
			var owner int64
			var model, body string
			if err := db.db.QueryRow(`SELECT owner_id,model,body_id FROM bot_config WHERE user_id=1002`).Scan(&owner, &model, &body); err != nil {
				t.Fatal(err)
			}
			if owner != 1001 || model != "unchanged-model" || body != "legacy-body" {
				t.Fatal("historical bot modified")
			}
			if _, err := db.BumpBotConnectionGeneration(1002); err != nil {
				t.Fatal(err)
			}
			b := store.SkillHubPublicationDeliveryBinding{ActorUID: 1001, BotUID: 1002, Source: in.Source, NodeID: "node", ConnectionID: "socket"}
			lease := deliveryClaim(t, db, entryID, b)
			if _, err := db.db.Exec(down); err == nil || !strings.Contains(err.Error(), "retained delivery evidence") {
				t.Fatalf("unsafe down: %v", err)
			}
			deliveryExec(t, db, up)
			startup()
			if err := db.FinishSkillHubPublicationDelivery(context.Background(), *lease, ""); err != nil {
				t.Fatalf("lease lost across startup: %v", err)
			}
			if _, err := db.db.Exec(down); err == nil {
				t.Fatal("completed evidence erased")
			}
			publicationCounts(t, db, 1)
		})
	}
}
