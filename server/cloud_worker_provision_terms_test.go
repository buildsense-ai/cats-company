package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type provisionTermCredits struct {
	configuredCreditStub
	selection      types.CloudWorkerCreditSelection
	commits, grace int
}

func (s *provisionTermCredits) CloudWorkerCreditSummary(int64) (int, int, error) {
	return 1, 1, nil
}

func (s *provisionTermCredits) ReserveCloudWorkerConfiguredCredit(int64, string, string, string) (types.CloudWorkerCreditSelection, bool, error) {
	return s.selection, true, nil
}

func (s *provisionTermCredits) CommitCloudWorkerCredit(_ int64, _ string, _ int64, _ string, grace int) error {
	s.commits++
	s.grace = grace
	return nil
}

// Exercise the real creation handler: a longer model package must not silently
// extend an independent machine term, and NULL must reach the cluster explicitly.
func TestCloudWorkerProvisionUsesExplicitSharedTerm(t *testing.T) {
	deadline := time.Date(2098, 1, 2, 3, 4, 5, 123456000, time.UTC)
	for _, tc := range []struct {
		name, mode string
		expires    *time.Time
		want       string
	}{
		{"fixed independent of package", "fixed", &deadline, deadline.Format(time.RFC3339)},
		{"finite following package", "follow_plan", &deadline, deadline.Format(time.RFC3339)},
		{"permanent following package", "follow_plan", nil, "none"},
		{"explicit permanent", "permanent", nil, "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			script, argsPath := writeAdminRuntimeScript(t, false)
			h, store := newCloudWorkerTestHandlerCfg(CloudWorkerConfig{ProvisionScript: script})
			h.db = &packageStoreTestDouble{
				deploymentTestStore: &deploymentTestStore{store, map[string]types.CloudWorkerDeployment{}},
				packageExpiresAt:    time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
			}
			credits := &provisionTermCredits{selection: types.CloudWorkerCreditSelection{
				Profile: types.CloudWorkerPackedShared, BillingMode: types.CloudWorkerMonthly,
				ExpiryMode: tc.mode, ExpiresAt: tc.expires, RetentionDays: 9,
			}}
			h.credits = credits
			rec := httptest.NewRecorder()
			h.HandleCreate(rec, cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers", map[string]string{"username": "bot-term"}))
			if rec.Code != http.StatusCreated {
				t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
			}
			args, err := os.ReadFile(argsPath)
			if err != nil || !strings.Contains(string(args), "--expires-at "+tc.want) ||
				!strings.Contains(string(args), "--grace-days 9") || strings.Contains(string(args), "2099-01-01") ||
				strings.Contains(string(args), "--duration") || credits.commits != 1 || credits.grace != 9 {
				t.Fatalf("term lost: args=%s commits=%d grace=%d err=%v", args, credits.commits, credits.grace, err)
			}
		})
	}
}
