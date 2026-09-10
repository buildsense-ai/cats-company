package postgres

import (
	"fmt"
	"os"
	"testing"
)

func TestPostgresAdminUserSearch(t *testing.T) {
	dsn := os.Getenv("CATS_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("database integration DSN is not configured")
	}
	db := &Adapter{}
	if err := db.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.db.SetMaxOpenConns(1)
	// A connection-local table keeps this fixture separate from every other test.
	_, err := db.db.Exec(`CREATE TEMPORARY TABLE users (
 id BIGINT PRIMARY KEY, username VARCHAR(255), email VARCHAR(255), display_name VARCHAR(255),
 avatar_url VARCHAR(255), account_type VARCHAR(16), state INT,
 created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP, updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP)`)
	if err != nil {
		t.Fatal(err)
	}
	insert := func(id int64, name, email string) {
		t.Helper()
		_, err := db.db.Exec(`INSERT INTO users (id,username,email,display_name,avatar_url,account_type,state)
  VALUES ($1,$2,$3,$2,'','human',0)`, id, name, email)
		if err != nil {
			t.Fatal(err)
		}
	}
	insert(61, "customer", "client@example.com")
	insert(161, "second", "second@example.com")
	insert(610, "third", "third@example.com")
	for i := int64(0); i < 25; i++ {
		insert(6100+i, fmt.Sprintf("fragment-%d", i), "")
		insert(2000+i, fmt.Sprintf("bot-61-%d", i), "")
	}
	insert(8000, "100%_literal", "literal@example.com")
	insert(8001, "100XXliteral", "other@example.com")
	first, total, err := db.SearchAdminUsers("61", "uid", 20, 0)
	if err != nil || total != 28 || len(first) != 20 || first[0].ID != 61 || first[1].ID != 161 {
		t.Fatalf("UID search total=%d len=%d err=%v", total, len(first), err)
	}
	seen := map[int64]bool{}
	for _, u := range first {
		seen[u.ID] = true
		if u.ID >= 2000 && u.ID < 2025 {
			t.Fatal("UID mode matched a username")
		}
	}
	second, total, err := db.SearchAdminUsers("61", "uid", 20, 20)
	if err != nil || total != 28 || len(second) != 8 {
		t.Fatalf("second page total=%d len=%d err=%v", total, len(second), err)
	}
	for _, u := range second {
		if seen[u.ID] {
			t.Fatal("duplicate across pages")
		}
	}
	names, total, err := db.SearchAdminUsers("bot-61", "name", 20, 0)
	if err != nil || total != 25 || len(names) != 20 {
		t.Fatalf("name search total=%d len=%d err=%v", total, len(names), err)
	}
	exact, _, err := db.SearchAdminUsers("CLIENT@example.com", "name", 20, 0)
	if err != nil || len(exact) != 1 || exact[0].ID != 61 || exact[0].Email != "client@example.com" {
		t.Fatalf("email search=%+v err=%v", exact, err)
	}
	literal, _, err := db.SearchAdminUsers("100%_", "name", 20, 0)
	if err != nil || len(literal) != 1 || literal[0].ID != 8000 {
		t.Fatalf("literal search=%+v err=%v", literal, err)
	}
	empty, total, err := db.SearchAdminUsers("999999", "uid", 20, 0)
	if err != nil || total != 0 || len(empty) != 0 {
		t.Fatalf("empty search total=%d err=%v", total, err)
	}
	beyond, total, err := db.SearchAdminUsers("61", "uid", 20, 100)
	if err != nil || total != 28 || len(beyond) != 0 {
		t.Fatalf("out of range total=%d err=%v", total, err)
	}
	if _, _, err := db.SearchAdminUsers("61", "invalid", 20, 0); err == nil {
		t.Fatal("invalid field accepted")
	}
}
