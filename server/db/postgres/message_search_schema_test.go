package postgres

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/openchat/openchat/server/store/types"
)

func TestMessageSearchExtensionFailurePolicy(t *testing.T) {
	for _, tc := range []struct {
		code     string
		fallback bool
	}{{"42501", true}, {"0A000", true}, {"53300", false}, {"XX000", false}} {
		t.Run(tc.code, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			mock.ExpectQuery("SELECT EXISTS .*pg_extension").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(false))
			mock.ExpectExec(createMessageSearchExtension).WillReturnError(&pgconn.PgError{Code: tc.code})
			if tc.fallback {
				mock.ExpectExec("SELECT pg_advisory_lock").WillReturnResult(sqlmock.NewResult(0, 1))
				mock.ExpectQuery("SELECT count.*pg_index").WithArgs("idx_messages_search_grams_gin").WillReturnRows(sqlmock.NewRows([]string{"exists", "valid"}).AddRow(true, true))
				mock.ExpectExec("CREATE INDEX CONCURRENTLY").WillReturnResult(sqlmock.NewResult(0, 0))
				mock.ExpectExec("SELECT pg_advisory_unlock").WillReturnResult(sqlmock.NewResult(0, 1))
			}
			a := &Adapter{db: db}
			a.messageSearchIndexReady.Store(true)
			err = a.ensureMessageSearchIndex()
			if (err == nil) != tc.fallback {
				t.Fatalf("extension error %s: err=%v want fallback=%v", tc.code, err, tc.fallback)
			}
			if a.messageSearchIndexReady.Load() || a.messageSearchGramIndexReady.Load() != tc.fallback {
				t.Fatal("extension error did not select the expected index implementation")
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMessageSearchPreinstalledExtensionSkipsInstallation(t *testing.T) {
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	// Managed providers can reject CREATE EXTENSION before processing IF NOT
	// EXISTS. A preinstalled extension must never invoke that command.
	mock.ExpectQuery("SELECT EXISTS .*pg_extension").WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(true))
	mock.ExpectExec("SELECT pg_advisory_lock").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery("SELECT count.*pg_index").WillReturnRows(sqlmock.NewRows([]string{"exists", "valid"}).AddRow(true, true))
	mock.ExpectExec("CREATE INDEX CONCURRENTLY").WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("SELECT pg_advisory_unlock").WillReturnResult(sqlmock.NewResult(0, 1))
	a := &Adapter{db: db}
	if err := a.ensureMessageSearchIndex(); err != nil {
		t.Fatal(err)
	}
	if !a.messageSearchIndexReady.Load() {
		t.Fatal("preinstalled extension did not enable indexed search")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func messageSearchSchemaTestAdapter(t *testing.T, dsn string) *Adapter {
	t.Helper()
	base := &Adapter{}
	if err := base.Open(dsn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	schema := fmt.Sprintf("cats_search_%d", time.Now().UnixNano())
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schema)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.db.Exec(`DROP SCHEMA ` + quoteIdent(schema) + ` CASCADE`) })
	db := &Adapter{}
	if err := db.Open(dsnWithSearchPath(t, dsn, schema)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func TestPostgresMessageSearchIndexMigration(t *testing.T) {
	for name, want := range map[string]string{
		"000056_message_search_document.up.sql":   createMessageSearchExtension + createMessageSearchDocument,
		"000057_message_search_index.up.sql":      createMessageSearchIndex,
		"000058_message_search_grams.up.sql":      createMessageSearchGrams,
		"000059_message_search_gram_index.up.sql": createMessageSearchGramIndex,
	} {
		data, err := os.ReadFile(filepath.Join("..", "migrations", "postgres", name))
		if err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(string(data)) != strings.TrimSpace(want) {
			t.Fatalf("startup DDL differs from %s", name)
		}
	}
	dsn := os.Getenv("CATS_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("set CATS_PG_TEST_DSN for historical schema upgrade")
	}
	db := messageSearchSchemaTestAdapter(t, dsn)
	// Historical baseline: the pre-#751 messages table, with no search function
	// or document column. The expression index includes rows already present.
	for _, statement := range []string{
		`CREATE TABLE messages(id bigint PRIMARY KEY, msg_type varchar(16),content text,content_blocks jsonb)`,
		`INSERT INTO messages VALUES(1,'text','historical course2md',NULL)`,
		createMessageSearchExtension, createMessageSearchDocument, createMessageSearchIndex,
		createMessageSearchDocument, createMessageSearchIndex,
	} {
		if _, err := db.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	// A failed concurrent UNIQUE build leaves a real invalid index. Startup
	// must replace it, rather than treating IF NOT EXISTS as proof of readiness.
	for _, statement := range []string{
		`INSERT INTO messages VALUES(2,'text','another historical row',NULL)`,
		`DROP INDEX CONCURRENTLY idx_messages_search_document_trgm`,
	} {
		if _, err := db.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.db.Exec(`CREATE UNIQUE INDEX CONCURRENTLY idx_messages_search_document_trgm ON messages((true))`); err == nil {
		t.Fatal("expected a failed concurrent build")
	}
	if err := db.ensureMessageSearchIndex(); err != nil {
		t.Fatal(err)
	}
	if err := db.ensureMessageSearchIndex(); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(`DELETE FROM messages WHERE id=2`); err != nil {
		t.Fatal(err)
	}
	var valid bool
	if err := db.db.QueryRow(`SELECT indisvalid FROM pg_index WHERE indexrelid='idx_messages_search_document_trgm'::regclass`).Scan(&valid); err != nil || !valid {
		t.Fatalf("index not valid: %v", err)
	}
	var count int
	if err := db.db.QueryRow(`SELECT count(*) FROM messages WHERE message_search_document_v1(msg_type,content,content_blocks) LIKE '%course2md%'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("historical row was not searchable: count=%d err=%v", count, err)
	}
	for _, name := range []string{"000057_message_search_index.down.sql", "000056_message_search_document.down.sql"} {
		data, err := os.ReadFile(filepath.Join("..", "migrations", "postgres", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.db.Exec(string(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.db.QueryRow(`SELECT count(*) FROM messages`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("down migration changed history: count=%d err=%v", count, err)
	}
}

func TestPostgresMessageSearchWithoutExtensionPrivileges(t *testing.T) {
	dsn := os.Getenv("CATS_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("set CATS_PG_TEST_DSN for restricted-role startup")
	}
	base := &Adapter{}
	if err := base.Open(dsn); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Close() })
	name := fmt.Sprintf("cats_search_restricted_%d", time.Now().UnixNano())
	// A separate database is necessary: pg_trgm is shared across schemas, and
	// an extension installed by another test would hide the privilege failure.
	const testPassword = "restricted_search_test"
	if _, err := base.db.Exec(`CREATE ROLE ` + quoteIdent(name) + ` LOGIN PASSWORD '` + testPassword + `'`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.db.Exec(`DROP ROLE ` + quoteIdent(name)) })
	if _, err := base.db.Exec(`CREATE DATABASE ` + quoteIdent(name)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.db.Exec(`DROP DATABASE ` + quoteIdent(name) + ` WITH (FORCE)`) })
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	parsed.Path = "/" + name
	dbDSN := dsnWithSearchPath(t, parsed.String(), "app")
	admin := &Adapter{}
	if err := admin.Open(dbDSN); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close() })
	if _, err := admin.db.Exec(`CREATE SCHEMA app AUTHORIZATION ` + quoteIdent(name)); err != nil {
		t.Fatal(err)
	}
	// Log in as the restricted role so both the app pool and the dedicated
	// schema connection use the same privileges.
	parsed.User = url.UserPassword(name, testPassword)
	db := &Adapter{}
	if err := db.Open(dsnWithSearchPath(t, parsed.String(), "app")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	var canInstall bool
	if err := db.db.QueryRow(`SELECT has_database_privilege(current_user,current_database(),'CREATE')`).Scan(&canInstall); err != nil || canInstall {
		t.Fatalf("test role unexpectedly able to install extensions: allowed=%v err=%v", canInstall, err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := db.CreateSchema(); err != nil {
			t.Fatalf("restricted-role startup %d: %v", attempt, err)
		}
		if db.messageSearchIndexReady.Load() || !db.messageSearchGramIndexReady.Load() {
			t.Fatal("missing extension must enable the built-in gram index")
		}
	}
	uid, err := db.CreateUser(&types.User{Username: "searchviewer", AccountType: types.AccountHuman, PassHash: []byte("hash")})
	if err != nil {
		t.Fatal(err)
	}
	groupID, err := db.CreateGroup("Search fallback", uid)
	if err != nil {
		t.Fatal(err)
	}
	messageID, err := db.SaveMessage(fmt.Sprintf("grp_%d", groupID), uid, "visible course2md", "text")
	if err != nil {
		t.Fatal(err)
	}
	assertSearch := func() {
		t.Helper()
		results, err := db.SearchMessages(context.Background(), uid, "course2md", "message", 20)
		if err != nil || len(results) != 1 || results[0].MessageID != messageID {
			t.Fatalf("search after startup: results=%+v err=%v", results, err)
		}
	}
	assertSearch()
	// The DBA can install the extension without granting the app more rights.
	// The next startup must build the index and enable the accelerated query.
	if _, err := admin.db.Exec(createMessageSearchExtension); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := db.CreateSchema(); err != nil {
			t.Fatal(err)
		}
		if !db.messageSearchIndexReady.Load() {
			t.Fatal("preinstalled extension must enable the index for a restricted role")
		}
		assertSearch()
	}
}

func testPostgresMessageSearchIndexedCandidates(t *testing.T, db *Adapter) {
	t.Helper()
	// Also exercise the SQL pattern escape. Each is a literal substring, including
	// quotes, backslashes, wildcards, the escape character itself, and Unicode.
	needles := []string{`100%_ready!`, `a\b"quoted`, "多语言部署", "line\nbreak"}
	if db.messageSearchGramIndexReady.Load() {
		needles = append(needles, "稀詞", "QZ", "éZ")
	}
	for i, needle := range needles {
		if _, err := db.db.Exec(`INSERT INTO messages(id,topic_id,from_uid,content,msg_type) VALUES($1,'grp_1',2,$2,'text')`, 900000+i, "literal "+needle+" tail"); err != nil {
			t.Fatal(err)
		}
		results, err := db.SearchMessages(context.Background(), 1, needle, "all", 20)
		if err != nil || len(results) != 1 || results[0].MessageID != int64(900000+i) {
			t.Fatalf("literal %q: results=%+v err=%v", needle, results, err)
		}
	}
	for _, statement := range []string{
		`INSERT INTO messages(id,topic_id,from_uid,content,msg_type,content_blocks)
 SELECT 100000+n,'grp_1',2,'','text',
 jsonb_build_array(jsonb_build_object('type','tool_result','content',repeat('hidden ',100)),
 jsonb_build_object('type','assistant_text','text',CASE WHEN n%5000=0 THEN 'selectivecourse2md' ELSE repeat('ordinary visible ',20) END))
 FROM generate_series(1,30000)n`,
		`VACUUM ANALYZE messages`,
	} {
		if _, err := db.db.Exec(statement); err != nil {
			t.Fatal(err)
		}
	}
	searchSQL, indexName := postgresMessageSearchQuery, "idx_messages_search_document_trgm"
	if db.messageSearchGramIndexReady.Load() {
		searchSQL, indexName = postgresMessageSearchGramQuery, "idx_messages_search_grams_gin"
	}
	for _, needle := range []string{"selectivecourse2md", "absent_probe_91be8"} {
		var plan string
		if err := db.db.QueryRow(`EXPLAIN (ANALYZE,FORMAT JSON) `+searchSQL, 1, "all", needle, 200, 0).Scan(&plan); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(plan, `"Index Name": "`+indexName+`"`) {
			t.Fatalf("selective query did not use trigram index: %s", plan)
		}
	}
	// Repeated pgx searches must override a generic session plan locally, then
	// restore the pool connection's setting after commit.
	if _, err := db.db.Exec(`SET plan_cache_mode = force_generic_plan`); err != nil {
		t.Fatal(err)
	}
	defer db.db.Exec(`RESET plan_cache_mode`)
	for attempt := 0; attempt < 7; attempt++ {
		results, err := db.SearchMessages(context.Background(), 1, "selectivecourse2md", "message", 20)
		if err != nil || len(results) != 6 {
			t.Fatalf("prepared search %d: count=%d err=%v", attempt, len(results), err)
		}
	}
	commonCtx, cancelCommon := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelCommon()
	commonResults, err := db.SearchMessages(commonCtx, 1, "ordinary", "message", 20)
	if err != nil || len(commonResults) != 20 {
		t.Fatalf("common term did not use a bounded plan: count=%d err=%v", len(commonResults), err)
	}
	var mode string
	if err := db.db.QueryRow(`SHOW plan_cache_mode`).Scan(&mode); err != nil || mode != "force_generic_plan" {
		t.Fatalf("search leaked plan mode: %s %v", mode, err)
	}
	// Expression indexes track edits and deletes through every write path.
	if _, err := db.db.Exec(`UPDATE messages SET content_blocks='[{"type":"assistant_text","text":"editedcourse2md"}]' WHERE id=105000`); err != nil {
		t.Fatal(err)
	}
	results, err := db.SearchMessages(context.Background(), 1, "editedcourse2md", "message", 20)
	if err != nil || len(results) != 1 || results[0].MessageID != 105000 {
		t.Fatalf("edit not indexed: results=%+v err=%v", results, err)
	}
	if _, err := db.db.Exec(`DELETE FROM messages WHERE id=105000`); err != nil {
		t.Fatal(err)
	}
	results, err = db.SearchMessages(context.Background(), 1, "editedcourse2md", "all", 20)
	if err != nil || len(results) != 0 {
		t.Fatalf("deleted result retained: results=%+v err=%v", results, err)
	}
}
