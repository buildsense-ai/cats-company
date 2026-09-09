package server

import (
	"encoding/json"
	"github.com/openchat/openchat/server/store/types"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCommercialInviteAutomaticallyGeneratesCode(t *testing.T) {
	store := newCommercialTestStore()
	handler := NewAccountAdminHandler(accountTestUserLookup{users: map[int64]*types.User{}}, nil, nil, store)
	seen := map[string]bool{}
	for i := 0; i < 10; i++ {
		req := httptest.NewRequest(http.MethodPost, "/local/account-admin/commercial/invites", strings.NewReader(`{"plan_id":1,"max_redemptions":3}`))
		req.RemoteAddr = "127.0.0.1:32000"
		rec := httptest.NewRecorder()
		handler.HandleCommercialInvites(rec, req)
		if rec.Code != 200 {
			t.Fatalf("status=%d %s", rec.Code, rec.Body.String())
		}
		var body struct {
			Code string `json:"code"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if !commercialCodePattern.MatchString(body.Code) || len(body.Code) != 27 || seen[body.Code] {
			t.Fatal("invalid or repeated generated code")
		}
		seen[body.Code] = true
		if !store.invites[i].CreateOnly || store.invites[i].Code != body.Code {
			t.Fatal("generated code was not protected from overwrite")
		}
	}
}
