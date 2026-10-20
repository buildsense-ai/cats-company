package postgres

import (
	"context"
	"os"
	"strings"
	"testing"
)

func publicationCancelIntentMigration(t *testing.T, direction string) string {
	t.Helper()
	b, err := os.ReadFile("../migrations/postgres/000038_skillhub_publication_cancel_intents." + direction + ".sql")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestSkillHubPublicationCancelIntentMigrationMatchesStartup(t *testing.T) {
	if strings.Join(strings.Fields(publicationCancelIntentMigration(t, "up")), " ") != strings.Join(strings.Fields(createSkillHubPublicationCancellationIntentsTable), " ") {
		t.Fatal("000038 differs from startup")
	}
}

func TestPostgresSkillHubPublicationCancellationIntentMigration(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"new", "grant", "receipt", "legacy_cancelled"} {
		t.Run(phase, func(t *testing.T) {
			db, _, in, _ := uploadGrantFixture(t)
			up, down := publicationCancelIntentMigration(t, "up"), publicationCancelIntentMigration(t, "down")
			deliveryExec(t, db, publicationCancelDeliveryMigration(t, "down"))
			deliveryExec(t, db, down)
			deliveryExec(t, db, down)
			ctx := context.Background()
			want := "local_only"
			if phase == "grant" || phase == "receipt" {
				g, _ := issueUpload(t, db, in)
				want = "waiting_receipt"
				if phase == "receipt" {
					recordReceipt(t, db, receiptForGrant(g), false)
					want = "ready"
				}
			}
			if phase == "legacy_cancelled" {
				if _, err := db.CancelSkillHubPublicationEntry(ctx, in.Binding.ActorUID, in.EntryID); err != nil {
					t.Fatal(err)
				}
			}
			before, err := db.GetSkillHubPublicationEntry(ctx, in.Binding.ActorUID, in.EntryID)
			if err != nil {
				t.Fatal(err)
			}
			for _, statement := range []string{up, up, down, up} {
				deliveryExec(t, db, statement)
			}
			if err := db.CreateSchema(); err != nil {
				t.Fatal(err)
			}
			after, err := db.GetSkillHubPublicationEntry(ctx, in.Binding.ActorUID, in.EntryID)
			if err != nil || *before != *after {
				t.Fatal("migration changed original intent")
			}
			// Up/startup never fabricate a new authenticated cancellation action.
			if r, err := db.GetSkillHubPublicationCancellationIntent(ctx, in.Binding.ActorUID, in.EntryID); err != nil || r != nil {
				t.Fatal("migration auto-enrolled history")
			}
			first := requestCancelIntent(t, db, in.Binding.ActorUID, in.EntryID, want)
			if _, err := db.db.Exec(down); err == nil || !strings.Contains(err.Error(), "retained publication cancellation intents") {
				t.Fatalf("unsafe rollback: %v", err)
			}
			deliveryExec(t, db, up)
			if err := db.CreateSchema(); err != nil {
				t.Fatal(err)
			}
			second := requestCancelIntent(t, db, in.Binding.ActorUID, in.EntryID, want)
			if *first != *second {
				t.Fatal("startup replaced cancellation")
			}
			publicationCounts(t, db, 1)
		})
	}
}

func TestPostgresSkillHubPublicationCancellationIntentMissingSchema(t *testing.T) {
	t.Parallel()
	db, _, in, _ := uploadGrantFixture(t)
	deliveryExec(t, db, publicationCancelDeliveryMigration(t, "down"))
	deliveryExec(t, db, publicationCancelIntentMigration(t, "down"))
	ctx := context.Background()
	if _, err := db.RequestSkillHubPublicationCancellation(ctx, in.Binding.ActorUID, in.EntryID); err == nil {
		t.Fatal("missing schema accepted")
	}
	e, err := db.GetSkillHubPublicationEntry(ctx, in.Binding.ActorUID, in.EntryID)
	if err != nil || e.State != "waiting_runtime" {
		t.Fatal("partial cancellation after missing schema")
	}
	// Old pre-Cloud cancellation remains usable without the new table.
	if _, err := db.CancelSkillHubPublicationEntry(ctx, in.Binding.ActorUID, in.EntryID); err != nil {
		t.Fatal(err)
	}
	if err := db.CreateSchema(); err != nil {
		t.Fatal(err)
	}
	requestCancelIntent(t, db, in.Binding.ActorUID, in.EntryID, "local_only")
}
