package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestPostgresStartupSchemaLocks(t *testing.T) {
	dsn := os.Getenv("CATS_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("set CATS_PG_TEST_DSN to run PostgreSQL integration tests")
	}
	var base Adapter
	if err := base.Open(dsn); err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := fmt.Sprintf("cats_startup_lock_%d", time.Now().UnixNano())
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schema)); err != nil {
		t.Fatal(err)
	}
	defer base.db.Exec(`DROP SCHEMA ` + quoteIdent(schema) + ` CASCADE`)

	var db Adapter
	if err := db.Open(dsnWithSearchPath(t, dsn, schema)); err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("fresh schema: %v", err)
	}
	startReader := func() *sql.Tx {
		t.Helper()
		tx, err := db.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
		if err != nil {
			t.Fatal(err)
		}
		var count int
		if err := tx.QueryRow(`SELECT count(*) FROM users`).Scan(&count); err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		return tx
	}

	reader := startReader()
	err := db.CreateSchema()
	defer reader.Rollback()
	_ = reader.Rollback()
	if err != nil {
		t.Fatalf("up-to-date schema should not need an exclusive users lock: %v", err)
	}

	if _, err := db.db.Exec(`ALTER TABLE users DROP COLUMN bot_disclose`); err != nil {
		t.Fatal(err)
	}
	reader = startReader()
	var before, after string
	defer reader.Rollback()
	if err := reader.QueryRow(`SHOW lock_timeout`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	err = db.CreateSchema()
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != "55P03" || !strings.Contains(err.Error(), "bot_disclose") || time.Since(started) > 10*time.Second {
		t.Fatalf("expected bounded users migration lock failure, got %v after %s", err, time.Since(started))
	}
	if err := reader.QueryRow(`SHOW lock_timeout`).Scan(&after); err != nil || after != before {
		t.Fatalf("reader's timeout changed: %q -> %q (%v)", before, after, err)
	}
	_ = reader.Rollback()
	if err := db.db.QueryRow(`SHOW lock_timeout`).Scan(&after); err != nil || after != before {
		t.Fatalf("migration timeout leaked into app pool: %q -> %q (%v)", before, after, err)
	}
	for range 2 {
		if err := db.CreateSchema(); err != nil {
			t.Fatalf("retry historical schema upgrade: %v", err)
		}
	}
	var nullable string
	if err := db.db.QueryRow(`SELECT is_nullable FROM information_schema.columns
		WHERE table_schema=current_schema() AND table_name='users' AND column_name='bot_disclose'`).Scan(&nullable); err != nil || nullable != "NO" {
		t.Fatalf("expected bot_disclose NOT NULL after upgrade, got %q (%v)", nullable, err)
	}

	t.Run("concurrent search index waits for an old snapshot", func(t *testing.T) {
		if _, err := db.db.Exec(`DROP INDEX idx_messages_search_document_trgm`); err != nil {
			t.Fatal(err)
		}
		snapshot, err := db.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true, Isolation: sql.LevelRepeatableRead})
		if err != nil {
			t.Fatal(err)
		}
		defer snapshot.Rollback()
		var count int
		if err := snapshot.QueryRow(`SELECT count(*) FROM users`).Scan(&count); err != nil {
			t.Fatal(err)
		}

		result := make(chan error, 1)
		go func() { result <- db.CreateSchema() }()
		finished := false
		defer func() {
			_ = snapshot.Rollback()
			if !finished {
				<-result
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for {
			select {
			case err := <-result:
				finished = true
				t.Fatalf("schema finished before waiting for the snapshot: %v", err)
			default:
			}
			var waiting bool
			if err := db.db.QueryRowContext(ctx, `SELECT EXISTS (
				SELECT 1 FROM pg_stat_progress_create_index
				WHERE relid = 'messages'::regclass AND phase = 'waiting for old snapshots')`).Scan(&waiting); err != nil {
				t.Fatal(err)
			}
			if waiting {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		select {
		case err := <-result:
			finished = true
			t.Fatalf("search index used the short DDL lock timeout: %v", err)
		case <-time.After(1250 * time.Millisecond):
		}
		if err := snapshot.Rollback(); err != nil {
			t.Fatal(err)
		}
		err = <-result
		finished = true
		if err != nil || !db.messageSearchIndexReady.Load() {
			t.Fatalf("search index did not finish after releasing the snapshot: %v", err)
		}
	})
}
