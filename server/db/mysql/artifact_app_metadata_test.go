package mysql

import (
	"context"
	"fmt"
	"github.com/openchat/openchat/server/store"
	"os"
	"testing"
	"time"
)

func TestMySQLArtifactAppMetadataPersistence(t *testing.T) {
	dsn := os.Getenv("CATS_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("set CATS_MYSQL_TEST_DSN for database integration")
	}
	db := &Adapter{}
	if err := db.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.db.Exec(createArtifactAppMetadataTable); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := fmt.Sprintf("metadata-%d", time.Now().UnixNano())
	original := store.ArtifactAppMetadata{AppID: id, AgentUID: 42, CreatorUID: 8, Title: "Initial"}
	defer db.DeleteArtifactAppMetadata(ctx, 42, id)
	if err := db.EnsureArtifactAppMetadata(ctx, original); err != nil {
		t.Fatal(err)
	}
	edited := original
	edited.Title, edited.Description, edited.IconURL = "Edited", "Shared description", "/uploads/icon.png"
	edited.CreatorUID = 999
	if err := db.UpdateArtifactAppMetadata(ctx, edited); err != nil {
		t.Fatal(err)
	}
	// A republish (or a racing initialization) cannot replace attribution or edits.
	original.CreatorUID = 7
	if err := db.EnsureArtifactAppMetadata(ctx, original); err != nil {
		t.Fatal(err)
	}
	items, err := db.ListArtifactAppMetadata(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	got := items[id]
	if got.CreatorUID != 8 || got.Title != "Edited" || got.Description != edited.Description || got.IconURL != edited.IconURL {
		t.Fatalf("lost metadata: %+v", got)
	}
	// Agent scope is enforced even if a caller passes a real app ID.
	wrong := edited
	wrong.AgentUID = 43
	if err := db.EnsureArtifactAppMetadata(ctx, wrong); err == nil {
		t.Fatal("cross-Agent initialization accepted")
	}
	if err := db.UpdateArtifactAppMetadata(ctx, wrong); err == nil {
		t.Fatal("cross-Agent update accepted")
	}
	if err := db.DeleteArtifactAppMetadata(ctx, 43, id); err != nil {
		t.Fatal(err)
	}
	items, err = db.ListArtifactAppMetadata(ctx, 42)
	if err != nil || items[id].Title != "Edited" {
		t.Fatalf("cross-Agent deletion changed app: %+v %v", items, err)
	}
	// Saving an identical form works with MySQL's zero affected-row behavior too.
	if err := db.UpdateArtifactAppMetadata(ctx, edited); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteArtifactAppMetadata(ctx, 42, id); err != nil {
		t.Fatal(err)
	}
	items, err = db.ListArtifactAppMetadata(ctx, 42)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := items[id]; exists {
		t.Fatal("metadata not removed")
	}
}
