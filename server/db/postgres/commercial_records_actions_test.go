package postgres

import (
	"context"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/openchat/openchat/server/store/types"
)

// recordingQueryMatcher keeps the statement the adapter actually built so a
// pure-SQL test can assert the projection and the bind order without a live
// PostgreSQL (CATS_PG_TEST_DSN is not available in CI here).
type recordingQueryMatcher struct {
	queries []string
}

func (m *recordingQueryMatcher) Match(expectedSQL, actualSQL string) error {
	m.queries = append(m.queries, actualSQL)
	return sqlmock.QueryMatcherRegexp.Match(expectedSQL, actualSQL)
}

// The durable operator-action history is the only place an operator can recover
// the operation_id of a worker that disappeared. It must be listable, and it
// must never echo the raw request/preview JSON (preview_token, fingerprint,
// lease) back to a list endpoint.
func TestCommercialRecordsActionsProjectionAndFilters(t *testing.T) {
	matcher := &recordingQueryMatcher{}
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(matcher))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	adapter := &Adapter{db: db}
	mock.ExpectQuery(`FROM commercial_ops_actions`).
		WithArgs(int64(116), "484efdad", "failed", 20, 40).
		WillReturnRows(sqlmock.NewRows([]string{"total", "records"}).
			AddRow(int64(2), []byte(`[{"operation_id":"484efdad-787e-4b2f-bc99-fcf5b22474cd"}]`)))

	page, err := adapter.ListCommercialRecords(context.Background(), types.CommercialRecordsQuery{
		Kind: "actions", UID: 116, Search: "484efdad", Status: "failed", Limit: 20, Offset: 40,
	})
	if err != nil {
		t.Fatalf("list actions: %v", err)
	}
	if page.Total != 2 || page.HasMore || page.Offset != 40 || page.Limit != 20 {
		t.Fatalf("pagination lost: %+v", page)
	}
	if !strings.Contains(string(page.Records), "484efdad-787e-4b2f-bc99-fcf5b22474cd") {
		t.Fatalf("records: %s", page.Records)
	}
	if len(matcher.queries) != 1 {
		t.Fatalf("statement count: %d", len(matcher.queries))
	}
	sql := matcher.queries[0]
	for _, want := range []string{
		"operation_id AS id, operation_id, uid, intent, status, worker_status, worker_uid",
		"LEFT(error, 500) AS error",
		"LEFT(request->>'note', 500) AS note",
		"COALESCE(NULLIF(preview->>'deployment_profile', ''), request->>'deployment_profile') AS deployment_profile",
		"COALESCE(NULLIF(preview->>'runtime_plan', ''), request->>'runtime_plan') AS runtime_plan",
		"preview->>'worker_expires_at' AS worker_expires_at",
		// operation_id is the primary key, so it has to be the ORDER BY
		// tie-breaker the shared statement already uses.
		"ORDER BY created_at DESC, id DESC",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("projection is missing %q:\n%s", want, sql)
		}
	}
	for _, forbidden := range []string{"preview_token", "fingerprint", " lease", " request,", " preview,", "SELECT request", "SELECT preview"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("projection leaks %q:\n%s", forbidden, sql)
		}
	}

	if _, err := adapter.ListCommercialRecords(context.Background(), types.CommercialRecordsQuery{Kind: "unknown", Limit: 20}); err == nil {
		t.Fatal("unknown kind was accepted")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
