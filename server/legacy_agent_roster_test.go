package server

// Before implementation: the complete admin roster must carry actual owner
// IDs, preserve a successful empty snapshot, and withhold its contract on DB
// failure. Client-uploaded owner claims never enter this handler.
import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/openchat/openchat/server/store"
)

type legacyOwnerRosterStore struct {
	store.Store
	rows []map[string]interface{}
	err  error
}

func (s *legacyOwnerRosterStore) ListBots() ([]map[string]interface{}, error) { return s.rows, s.err }
func TestLegacyAgentOwnershipRosterHTTP(t *testing.T) {
	for _, tc := range []struct {
		name   string
		rows   []map[string]interface{}
		err    error
		status int
	}{
		{"owners", []map[string]interface{}{{"id": int64(564), "owner_id": int64(94)}}, nil, 200},
		{"empty", nil, nil, 200},
		{"unavailable", nil, errors.New("fixture database unavailable"), 500},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := NewBotHandler(&legacyOwnerRosterStore{rows: tc.rows, err: tc.err})
			w := httptest.NewRecorder()
			h.HandleListBots(w, httptest.NewRequest(http.MethodGet, "/api/admin/bots", nil))
			var body struct {
				Contract string                   `json:"ownership_contract"`
				Bots     []map[string]interface{} `json:"bots"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if w.Code != tc.status {
				t.Fatal(w.Code, w.Body.String())
			}
			if tc.status == 200 {
				if body.Contract != "catslog-agent-owners/1" || body.Bots == nil {
					t.Fatalf("incomplete snapshot: %s", w.Body.String())
				}
			} else if body.Contract != "" {
				t.Fatal("failed snapshot advertised as complete")
			}
		})
	}
}
