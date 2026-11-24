package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/openchat/openchat/server/db/wikiinjection"
	"github.com/openchat/openchat/server/store"
)

const feedbackEvent = "5d87b9dc-9e94-4dd0-92d1-06df021a1560:18"

type injectionTestStore struct {
	*wikiMemoryTestStore
	storage *wikiinjection.Storage
}

func (s *injectionTestStore) WikiInjections() *wikiinjection.Storage { return s.storage }

func TestWikiInjectionMarkerCannotBeSubmittedAsChat(t *testing.T) {
	request := SendMessageRequest{TopicID: "p2p_2_50", Type: wikiInjectionType, Content: json.RawMessage(`{"type":"wiki_injection","event_id":"` + feedbackEvent + `"}`)}
	if _, err := normalizeMessageRequest(&request); err == nil {
		t.Fatal("accepted a user-supplied marker")
	}
	request.Type = "text"
	if _, err := normalizeMessageRequest(&request); err == nil {
		t.Fatal("accepted a marker disguised as a text message")
	}
	request.Type = wikiInjectionType
	request.wikiInjectionTrusted = true
	message, err := normalizeMessageRequest(&request)
	if err != nil || len(message.ContentBlocks) != 1 || !isInternalAgentWorkingMessage(message.DisplayType, message.DisplayContent, message.ContentBlocks) {
		t.Fatalf("marker did not become a work record: %v", err)
	}
	if got := inferDisplayTypeFromStoredMessage(message.StoredType, message.StoredContent, message.ContentBlocks); got != wikiInjectionType {
		t.Fatalf("history returned %s", got)
	}
}

func TestWikiInjectionFeedbackJourney(t *testing.T) {
	dsn := os.Getenv("CATS_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("set CATS_PG_TEST_DSN for PostgreSQL integration")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	schema := "wiki_injection_test_" + strconv.FormatInt(time.Now().UnixNano(), 10)
	if _, err := db.Exec("create schema " + schema + "; set search_path to " + schema); err != nil {
		t.Fatal(err)
	}
	defer db.Exec("drop schema " + schema + " cascade")
	up, err := os.ReadFile("db/migrations/postgres/000082_wiki_injection_votes.up.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("create table wiki_injections (" + wikiinjection.InjectionColumns + "); create table wiki_injection_votes (" + wikiinjection.VoteColumns + ")"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := db.Exec(string(up)); err != nil {
			t.Fatal(err)
		}
	}
	storage := wikiinjection.New(db, true)
	base := &wikiMemoryTestStore{memory: []store.WikiCatalogEntry{
		wikiFixture("visible", "p2p_2_50"), wikiFixture("private", "p2p_3_50"),
	}}
	app := &injectionTestStore{base, storage}
	h := NewWikiMemoryHandler(app, &Hub{db: app}, "http://memory.invalid", "service-key")
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/agents/{uid}/wiki-injections/{event}", h.HandleInjection)
	mux.HandleFunc("PUT /api/agents/{uid}/wiki-injections/{event}", h.HandleInjection)
	mux.HandleFunc("POST /internal/memory/wiki-injection-feedback", h.HandleInjectionFeedback)
	if err := storage.Save(t.Context(), 50, feedbackEvent, "p2p_2_50", json.RawMessage(`{
		"event_id":"`+feedbackEvent+`","kind":"initial","summary":"mixed private summary",
		"references":[{"wiki_id":"visible","evidence_level":"wiki_body","contribution":"mixed private contribution"},
		{"wiki_id":"private","evidence_level":"search_snippet"}]
	}`)); err != nil {
		t.Fatal(err)
	}

	send := func(method, path, body, token string, viewer int64, want int) string {
		t.Helper()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", token)
		req = req.WithContext(context.WithValue(req.Context(), uidKey, viewer))
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, req)
		if response.Code != want {
			t.Fatalf("%s %s: got %d, want %d: %s", method, path, response.Code, want, response.Body)
		}
		return response.Body.String()
	}
	view := "/api/agents/50/wiki-injections/" + feedbackEvent
	got := send("GET", view, "", "", 2, 200)
	if !strings.Contains(got, `"title":"visible"`) || !strings.Contains(got, `"restricted":true`) || strings.Contains(got, "private") {
		t.Fatal(got)
	}
	send("GET", view, "", "", 3, 404)
	send("PUT", view, `{"wiki_id":"private","vote":1}`, "", 2, 404)
	send("PUT", view, `{"vote":1}`, "", 2, 204)
	send("PUT", view, `{"wiki_id":"visible","vote":1}`, "", 2, 204)
	send("PUT", view, `{"wiki_id":"visible","vote":-1}`, "", 2, 204)
	got = send("GET", view, "", "", 2, 200)
	if !strings.Contains(got, `"votes":{"":1,"visible":-1}`) {
		t.Fatal(got)
	}
	export := "/internal/memory/wiki-injection-feedback"
	query := `{"bot_uid":"50","event_ids":["` + feedbackEvent + `"]}`
	send("POST", export, query, "Bearer wrong", 0, 401)
	got = send("POST", export, query, "Bearer service-key", 0, 200)
	if !strings.Contains(got, `"wiki_id":"visible","up":0,"down":1`) {
		t.Fatal(got)
	}
	send("PUT", view, `{"wiki_id":"visible","vote":0}`, "", 2, 204)
	got = send("POST", export, query, "Bearer service-key", 0, 200)
	if strings.Contains(got, "visible") || !strings.Contains(got, `"up":1`) {
		t.Fatal(got)
	}
}
