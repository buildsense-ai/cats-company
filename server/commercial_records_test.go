package server

import (
	"context"
	"github.com/openchat/openchat/server/store/types"
	"net/http"
	"net/http/httptest"
	"testing"
)

type recordsTestStore struct {
	*commercialOpsTestStore
	query types.CommercialRecordsQuery
	calls int
}

func (s *recordsTestStore) ListCommercialRecords(_ context.Context, q types.CommercialRecordsQuery) (*types.CommercialRecordsPage, error) {
	s.calls++
	s.query = q
	return &types.CommercialRecordsPage{Records: []byte(`[]`), Total: 205, Offset: q.Offset, Limit: q.Limit, HasMore: true}, nil
}
func TestCommercialRecordsAuthAndPagination(t *testing.T) {
	store := &recordsTestStore{commercialOpsTestStore: newCommercialOpsTestStore()}
	h := NewCommercialOpsHandler(nil, commercialOpsTestVerifier{service: AccountService{Slug: "readonly", Scopes: []string{commercialOpsReadScope}}}, store)
	for _, tc := range []struct {
		query  string
		status int
	}{
		{"kind=orders&offset=100&limit=20&uid=38&q=order&status=fulfilled", 200},
		{"kind=unknown", 400}, {"kind=orders&offset=-1", 400}, {"kind=orders&limit=0", 400}, {"kind=orders&limit=101", 400}, {"kind=orders&uid=bad", 400},
	} {
		r := httptest.NewRecorder()
		h.HandleRecords(r, commercialOpsRequest(http.MethodGet, "/api/account/commercial-ops/records?"+tc.query, ""))
		if r.Code != tc.status {
			t.Fatalf("%s: %d %s", tc.query, r.Code, r.Body.String())
		}
	}
	if store.calls != 1 || store.query.Offset != 100 || store.query.UID != 38 || store.query.Status != "fulfilled" {
		t.Fatalf("query lost: %#v", store)
	}
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		r := httptest.NewRecorder()
		h.HandleRecords(r, commercialOpsRequest(method, "/api/account/commercial-ops/records?kind=orders", ""))
		if r.Code != 405 {
			t.Fatal(r.Code)
		}
	}
	req := commercialOpsRequest(http.MethodGet, "/api/account/commercial-ops/records?kind=orders", "")
	req.Header.Del("Authorization")
	r := httptest.NewRecorder()
	h.HandleRecords(r, req)
	if r.Code != 401 {
		t.Fatal(r.Code)
	}
	req = commercialOpsRequest(http.MethodGet, "/api/account/commercial-ops/records?kind=orders", "")
	req.RemoteAddr = "203.0.113.3:1234"
	r = httptest.NewRecorder()
	h.HandleRecords(r, req)
	if r.Code != 403 {
		t.Fatal(r.Code)
	}
	if store.calls != 1 {
		t.Fatal("unauthorized request reached store")
	}
}
