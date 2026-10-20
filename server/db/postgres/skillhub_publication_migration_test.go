package postgres

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func publicationMigrationSQL(t *testing.T, direction string) string {
	t.Helper()
	data, err := os.ReadFile("../migrations/postgres/000028_skillhub_publication_entries." + direction + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestSkillHubPublicationMigrationMatchesStartup(t *testing.T) {
	want := createSkillHubPublicationEntriesTable + createSkillHubPublicationOutboxTable + createSkillHubPublicationAdmissionTable
	// Ignore formatting only, retaining all identifiers, constraints and seeds.
	if strings.Join(strings.Fields(publicationMigrationSQL(t, "up")), " ") != strings.Join(strings.Fields(want), " ") {
		t.Fatal("migration 000028 must mirror the three startup DDL definitions")
	}
}

func TestPostgresSkillHubPublicationMigration(t *testing.T) {
	t.Parallel()
	for _, order := range []string{"empty_sql", "historical_sql_first", "historical_startup_first"} {
		t.Run(order, func(t *testing.T) {
			db, _ := publicationEmptyDatabase(t)
			up, down := publicationMigrationSQL(t, "up"), publicationMigrationSQL(t, "down")
			exec := func(statement string) {
				t.Helper()
				if _, err := db.db.Exec(statement); err != nil {
					t.Fatal(err)
				}
			}
			startup := func() {
				t.Helper()
				if err := db.CreateSchema(); err != nil {
					t.Fatal(err)
				}
			}
			if order != "empty_sql" {
				baseline, err := os.ReadFile("testdata/skillhub_publication_pre620.sql")
				if err != nil {
					t.Fatal(err)
				}
				exec(string(baseline))
			}
			if order == "historical_startup_first" {
				startup()
				startup()
			}
			exec(up)
			exec(up)
			publicationCounts(t, db, 0)
			var locks int
			if err := db.db.QueryRow(`SELECT COUNT(*) FROM skillhub_publication_admission WHERE lock_id=1`).Scan(&locks); err != nil || locks != 1 {
				t.Fatalf("admission seed: %d %v", locks, err)
			}
			// Empty rollback is atomic and does not touch historical user/Bot data.
			// Later FK-dependent tables must roll back first, just as migrate down
			// would do. The applied 000028 migration remains unchanged.
			exec(captureMigration(t, "down"))
			exec(commitDeliveryMigration(t, "down"))
			exec(publicationCommitMigration(t, "down"))
			exec(publicationCancelDeliveryMigration(t, "down"))
			exec(publicationCancelIntentMigration(t, "down"))
			exec(publicationReceiptMigration(t, "down"))
			exec(publicationUploadMigration(t, "down"))
			exec(down)
			var tables int
			if err := db.db.QueryRow(`SELECT COUNT(*) FROM information_schema.tables WHERE table_schema=current_schema()
				AND table_name IN ('skillhub_publication_entries','skillhub_publication_outbox','skillhub_publication_admission')`).Scan(&tables); err != nil || tables != 0 {
				t.Fatalf("rollback tables: %d %v", tables, err)
			}
			exec(up)
			exec(up)
			startup()
			startup()
			if order == "empty_sql" {
				return
			}
			var owner int64
			var model, body, name string
			var legacy bool
			if err := db.db.QueryRow(`SELECT b.owner_id, b.model, b.body_id, (b.config->>'legacy')::boolean, u.display_name
				FROM bot_config b JOIN users u ON u.id=b.owner_id WHERE b.user_id=1002`).Scan(&owner, &model, &body, &legacy, &name); err != nil {
				t.Fatal(err)
			}
			if owner != 1001 || model != "unchanged-model" || body != "legacy-body" || !legacy || name != "Legacy Publisher" {
				t.Fatal("historical binding/config changed")
			}
			in := store.SkillHubPublicationEntryInput{ActorUID: 1001, BotUID: 1002, Entrypoint: "workspace", RequestID: "legacy-request",
				Source: store.SkillHubPublicationSource{DeviceID: "legacy-device", BodyID: body, InstallationID: "legacy-install",
					RouteGeneration: 1, WorkspaceGeneration: "legacy-rev", LocalSkillID: "legacy-skill"}}
			entry, _, err := db.CreateSkillHubPublicationEntry(context.Background(), in, store.DefaultSkillHubPublicationEntryPolicy())
			if err != nil {
				t.Fatal(err)
			}
			exec(up)
			startup()
			if _, err := db.db.Exec(down); err == nil || !strings.Contains(err.Error(), "retained publication evidence") {
				t.Fatalf("populated rollback should reject: %v", err)
			}
			publicationCounts(t, db, 1)
			again, replay, err := db.CreateSkillHubPublicationEntry(context.Background(), in, store.DefaultSkillHubPublicationEntryPolicy())
			if err != nil || !replay || again.ID != entry.ID {
				t.Fatalf("replay after migration: %v %v %v", again, replay, err)
			}
		})
	}
}
