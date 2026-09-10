package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

type commercialSearchLookup struct {
	accountTestUserLookup
	fuzzyResults []*types.User
	fuzzyCalls   int
}

type commercialFieldSearchLookup struct {
	accountTestUserLookup
	query, field         string
	limit, offset, calls int
	err                  error
}

func (s *commercialFieldSearchLookup) SearchAdminUsers(query, field string, limit, offset int) ([]*types.User, int, error) {
	s.query, s.field, s.limit, s.offset = query, field, limit, offset
	s.calls++
	return []*types.User{{ID: 61, Username: "customer", AccountType: types.AccountHuman}}, 45, s.err
}

func TestCommercialUserFieldSearch(t *testing.T) {
	for _, tc := range []struct {
		query  string
		status int
		field  string
		offset int
	}{
		{"search_by=uid&q=61", 200, "uid", 0},
		{"search_by=name&q=customer&page=2", 200, "name", 20},
		{"search_by=uid&q=bot-61", 400, "", 0},
		{"search_by=uid&q=61&page=-1", 400, "", 0},
		{"search_by=uid&q=61&page=10001", 400, "", 0},
		{"search_by=name&q=customer&page=wrong", 400, "", 0},
		{"search_by=other&q=61", 400, "", 0},
		{"search_by=uid&q=", 400, "", 0},
	} {
		t.Run(tc.query, func(t *testing.T) {
			lookup := &commercialFieldSearchLookup{}
			handler := NewAccountAdminHandler(lookup, nil, nil, newCommercialTestStore())
			req := httptest.NewRequest(http.MethodGet, "/local/account-admin/commercial/users?"+tc.query, nil)
			req.RemoteAddr = "127.0.0.1:40200"
			rec := httptest.NewRecorder()
			handler.HandleCommercialUserSummary(rec, req)
			if rec.Code != tc.status {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			if tc.status != 200 {
				if lookup.calls != 0 {
					t.Fatal("invalid search reached database")
				}
				return
			}
			if lookup.field != tc.field || lookup.offset != tc.offset || lookup.limit != 20 {
				t.Fatalf("unexpected lookup: %+v", lookup)
			}
			var result struct {
				Total, Page int
				PageSize    int  `json:"page_size"`
				HasMore     bool `json:"has_more"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Total != 45 || result.Page != tc.offset/20+1 || result.PageSize != 20 || !result.HasMore {
				t.Fatalf("bad pagination: %+v", result)
			}
		})
	}
}

func TestCommercialUserFieldSearchDoesNotExposeStoreError(t *testing.T) {
	lookup := &commercialFieldSearchLookup{err: errors.New("private database failure")}
	handler := NewAccountAdminHandler(lookup, nil, nil, newCommercialTestStore())
	req := httptest.NewRequest(http.MethodGet, "/local/account-admin/commercial/users?search_by=uid&q=61", nil)
	req.RemoteAddr = "127.0.0.1:40200"
	rec := httptest.NewRecorder()
	handler.HandleCommercialUserSummary(rec, req)
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "private") {
		t.Fatalf("unexpected error response: %s", rec.Body.String())
	}
}

func (s *commercialSearchLookup) SearchUsers(_ string, limit int) ([]*types.User, error) {
	s.fuzzyCalls++
	if len(s.fuzzyResults) > limit {
		return s.fuzzyResults[:limit], nil
	}
	return s.fuzzyResults, nil
}

func TestCommercialUserSearchUsesAccountLookup(t *testing.T) {
	for _, tc := range []struct {
		name       string
		query      string
		wantUIDs   []int64
		fuzzyCalls int
	}{
		{name: "exact UID bypasses a full page of unrelated matches", query: " 61 ", wantUIDs: []int64{61}},
		{name: "missing UID does not select a similarly named account", query: "9999", wantUIDs: []int64{}},
		{name: "exact email resolves an account whose username differs", query: "customer@example.com", wantUIDs: []int64{61, 62}, fuzzyCalls: 1},
		{name: "exact username precedes fuzzy matches without duplicates", query: "customer", wantUIDs: []int64{61, 62}, fuzzyCalls: 1},
		{name: "fuzzy matches retain full account metadata", query: "helper", wantUIDs: []int64{62}, fuzzyCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			users := map[int64]*types.User{
				61: {ID: 61, Username: "customer", Email: "customer@example.com", DisplayName: "Customer", AccountType: types.AccountHuman},
				62: {ID: 62, Username: "customer-helper", Email: "helper@example.com", DisplayName: "Helper", AccountType: types.AccountHuman},
			}
			lookup := &commercialSearchLookup{accountTestUserLookup: accountTestUserLookup{users: users}}
			if tc.fuzzyCalls == 0 {
				for i := int64(100); i < 125; i++ {
					// Public/fuzzy search can fill its 20-result limit before UID61.
					lookup.fuzzyResults = append(lookup.fuzzyResults, &types.User{ID: i, Username: fmt.Sprintf("bot-61-9999-%d", i)})
				}
			} else {
				lookup.fuzzyResults = []*types.User{{ID: 62, Username: "customer-helper"}}
				if tc.query != "helper" {
					lookup.fuzzyResults = append(lookup.fuzzyResults, &types.User{ID: 61, Username: "customer"})
				}
			}
			handler := NewAccountAdminHandler(lookup, nil, nil, newCommercialTestStore())
			req := httptest.NewRequest(http.MethodGet, "/local/account-admin/commercial/users?q="+url.QueryEscape(tc.query), nil)
			req.RemoteAddr = "127.0.0.1:40200"
			rec := httptest.NewRecorder()
			handler.HandleCommercialUserSummary(rec, req)
			if rec.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
			}
			var body struct {
				Users []accountUserResponse `json:"users"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Users == nil || len(body.Users) != len(tc.wantUIDs) {
				t.Fatalf("expected UIDs %v, got %+v", tc.wantUIDs, body.Users)
			}
			for i, uid := range tc.wantUIDs {
				if body.Users[i].UID != uid || body.Users[i].Email != users[uid].Email || body.Users[i].AccountType != types.AccountHuman {
					t.Fatalf("result %d must contain complete account %d, got %+v", i, uid, body.Users[i])
				}
			}
			if lookup.fuzzyCalls != tc.fuzzyCalls {
				t.Fatalf("fuzzy search calls=%d, want %d", lookup.fuzzyCalls, tc.fuzzyCalls)
			}
		})
	}
}
