package server

import (
	"encoding/json"
	"errors"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// These tests pin the rule that a durable "ensure" operation may only adopt a
// machine of the deployment class it was previewed for. A shared-container
// worker and a dedicated ECS instance are different products, so the account's
// first machine is not an answer: the operation has to classify every machine it
// finds and either adopt one of its own class or provision one.

type commercialActionTestMachine struct {
	UID     int64
	Tenant  string
	Profile string
	// State and BillingAction simulate the lifecycle row the sweeper or a billing
	// pass already owns for this machine. Empty is the historical no-row/active
	// reading, so only the cases that need it seed a row.
	State         string
	BillingAction string
}

// commercialActionWorkerFixture builds an owner (uid 42) with the given machines
// and the deployment profiles the deployment store reports for them.
func commercialActionWorkerFixture(t *testing.T, requested string, machines []commercialActionTestMachine) (*CloudWorkerHandler, *deploymentTestStore, *commercialWorkerActionTestCredits, *types.CommercialAction) {
	t.Helper()
	h, db := newCloudWorkerTestHandlerCfg(CloudWorkerConfig{CreateQuota: "42=99"})
	store := &deploymentTestStore{cloudWorkerTestStore: db, deployments: map[string]types.CloudWorkerDeployment{}}
	db.ownerBots = nil
	for _, machine := range machines {
		db.ownerBots = append(db.ownerBots, map[string]interface{}{
			"id": machine.UID, "tenant_name": machine.Tenant, "username": machine.Tenant,
		})
		store.deployments[machine.Tenant] = types.CloudWorkerDeployment{Profile: machine.Profile, Env: map[string]string{}}
	}
	h.db = store
	credits := &commercialWorkerActionTestCredits{}
	for _, machine := range machines {
		if machine.State == "" && machine.BillingAction == "" {
			continue
		}
		credits.lifecycles = append(credits.lifecycles, CloudWorkerLifecycle{
			ID: machine.UID, TenantName: machine.Tenant, State: machine.State, BillingAction: machine.BillingAction,
		})
	}
	h.credits = credits
	expiry := time.Now().UTC().Add(time.Hour)
	v := &types.CommercialAction{
		Operation: types.CommercialActionOperation{UID: 42, OperationID: "profile"},
		Request:   types.CommercialActionRequest{WorkerAction: "ensure"},
		Preview: types.CommercialActionPreview{
			DeploymentProfile: requested, BillingMode: types.CloudWorkerMonthly, WorkerExpiresAt: &expiry,
		},
	}
	return h, store, credits, v
}

func TestCommercialActionEnsureAdoptsOnlyItsDeploymentClass(t *testing.T) {
	ecs := commercialActionTestMachine{UID: 1001, Tenant: "bot-ecs", Profile: types.CloudWorkerPrivateNAT}
	publicIP := commercialActionTestMachine{UID: 1002, Tenant: "bot-public", Profile: types.CloudWorkerPublicIP}
	shared := commercialActionTestMachine{UID: 1003, Tenant: "bot-shared", Profile: types.CloudWorkerPackedShared}
	for _, tc := range []struct {
		name        string
		requested   string
		machines    []commercialActionTestMachine
		wantHandled bool
		wantKept    int64
	}{
		// The reported bug: an ECS-only account used to have its ECS instance
		// adopted by a shared operation, so the shared credit paid for nothing.
		{"shared operation on an ECS-only account provisions one", types.CloudWorkerPackedShared, []commercialActionTestMachine{ecs}, false, 0},
		{"shared operation on a shared-only account keeps it", types.CloudWorkerPackedShared, []commercialActionTestMachine{shared}, true, shared.UID},
		// Mixed accounts keep their own class instead of the first machine.
		{"shared operation on a mixed account keeps the shared machine", types.CloudWorkerPackedShared, []commercialActionTestMachine{ecs, shared}, true, shared.UID},
		{"dedicated operation on a mixed account keeps the ECS machine", types.CloudWorkerPrivateNAT, []commercialActionTestMachine{ecs, shared}, true, ecs.UID},
		{"dedicated operation on a shared-only account provisions one", types.CloudWorkerPrivateNAT, []commercialActionTestMachine{shared}, false, 0},
		// Both ECS profiles are one class (the rule the paid renewal path uses),
		// and the first machine of that class in roster order is the one adopted.
		{"public-IP machine is the same class as private NAT", types.CloudWorkerPrivateNAT, []commercialActionTestMachine{publicIP, shared}, true, publicIP.UID},
		{"first same-class machine wins", types.CloudWorkerPrivateNAT, []commercialActionTestMachine{ecs, publicIP}, true, ecs.UID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, store, credits, v := commercialActionWorkerFixture(t, tc.requested, tc.machines)
			rec := httptest.NewRecorder()
			handled := h.prepareCommercialActionWorker(rec, 42, v)
			if handled != tc.wantHandled || credits.keptWorker != tc.wantKept || credits.reserved != 0 {
				t.Fatalf("handled=%v kept=%d reserved=%d body=%s", handled, credits.keptWorker, credits.reserved, rec.Body)
			}
			if tc.wantKept == 0 {
				if credits.kept != 0 || rec.Body.Len() != 0 {
					t.Fatalf("a machine of another class was adopted: kept=%d body=%s", credits.kept, rec.Body)
				}
				return
			}
			if rec.Code != 200 || credits.kept != 1 {
				t.Fatalf("keep did not complete: %d %s", rec.Code, rec.Body)
			}
			var body struct {
				Status string `json:"worker_status"`
				UID    int64  `json:"uid"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Status != "kept" || body.UID != tc.wantKept {
				t.Fatalf("kept the wrong machine: %s", rec.Body)
			}
			// Nothing about the adopted machine is rewritten by the server: the
			// deployment record it was classified from must be untouched.
			if value := store.deployments[tc.machines[len(tc.machines)-1].Tenant]; value.Profile == "" {
				t.Fatal("deployment record was erased")
			}
		})
	}
}

func TestCommercialActionEnsureNeverAdoptsAcrossShutdownOrUnknownState(t *testing.T) {
	shared := commercialActionTestMachine{UID: 1003, Tenant: "bot-shared", Profile: types.CloudWorkerPackedShared}
	ecs := commercialActionTestMachine{UID: 1001, Tenant: "bot-ecs", Profile: types.CloudWorkerPrivateNAT}

	t.Run("additional always appends one machine", func(t *testing.T) {
		h, _, credits, v := commercialActionWorkerFixture(t, types.CloudWorkerPackedShared, []commercialActionTestMachine{shared})
		v.Request.WorkerAction = "additional"
		if handled := h.prepareCommercialActionWorker(httptest.NewRecorder(), 42, v); handled || credits.kept != 0 {
			t.Fatalf("additional adopted an existing machine: handled=%v credits=%+v", handled, credits)
		}
	})

	t.Run("a same-class machine whose destroy is running stops the operation", func(t *testing.T) {
		h, _, credits, v := commercialActionWorkerFixture(t, types.CloudWorkerPackedShared, []commercialActionTestMachine{shared})
		credits.lifecycles = []CloudWorkerLifecycle{{ID: 7, TenantName: shared.Tenant, State: "delete_running"}}
		rec := httptest.NewRecorder()
		if handled := h.prepareCommercialActionWorker(rec, 42, v); !handled {
			t.Fatal("a machine being destroyed was silently ignored")
		}
		if rec.Code != 409 || credits.kept != 0 || credits.reserved != 0 || commercialActionResponseCode(rec.Body.Bytes()) != "cloud_worker_reconciliation_required" {
			t.Fatalf("destroy in flight not reported: %d %s credits=%+v", rec.Code, rec.Body, credits)
		}
	})

	t.Run("an unreadable machine profile fails closed", func(t *testing.T) {
		h, store, credits, v := commercialActionWorkerFixture(t, types.CloudWorkerPrivateNAT, []commercialActionTestMachine{ecs})
		h.db = &commercialActionFailingDeploymentStore{deploymentTestStore: store, err: errors.New("deployment store down")}
		rec := httptest.NewRecorder()
		if handled := h.prepareCommercialActionWorker(rec, 42, v); !handled {
			t.Fatal("an identification error was swallowed and a machine would be created")
		}
		if rec.Code != 409 || credits.kept != 0 || credits.reserved != 0 || commercialActionResponseCode(rec.Body.Bytes()) != "cloud_worker_reconciliation_required" {
			t.Fatalf("identification error not reported: %d %s", rec.Code, rec.Body)
		}
	})

	t.Run("an unsupported recorded profile fails closed", func(t *testing.T) {
		unknown := commercialActionTestMachine{UID: 1004, Tenant: "bot-unknown", Profile: "mystery_fleet"}
		h, _, credits, v := commercialActionWorkerFixture(t, types.CloudWorkerPrivateNAT, []commercialActionTestMachine{unknown})
		rec := httptest.NewRecorder()
		if handled := h.prepareCommercialActionWorker(rec, 42, v); !handled || credits.kept != 0 {
			t.Fatalf("unknown profile was defaulted: handled=%v credits=%+v", handled, credits)
		}
		if rec.Code != 409 || commercialActionResponseCode(rec.Body.Bytes()) != "cloud_worker_reconciliation_required" {
			t.Fatalf("unknown profile not reported: %d %s", rec.Code, rec.Body)
		}
	})

	t.Run("the database rejecting the adoption never becomes a second machine", func(t *testing.T) {
		h, _, credits, v := commercialActionWorkerFixture(t, types.CloudWorkerPackedShared, []commercialActionTestMachine{shared})
		credits.keepError = errors.New("operation keep target 1003 is a shared worker but the operation credit is dedicated")
		rec := httptest.NewRecorder()
		if handled := h.prepareCommercialActionWorker(rec, 42, v); !handled {
			t.Fatal("a rejected adoption fell through to provisioning")
		}
		if rec.Code != 409 || credits.reserved != 0 || commercialActionResponseCode(rec.Body.Bytes()) != "cloud_worker_keep_rejected" {
			t.Fatalf("rejected adoption not reported: %d %s", rec.Code, rec.Body)
		}
	})
}

// commercialActionFailingDeploymentStore makes the deployment lookup itself fail.
type commercialActionFailingDeploymentStore struct {
	*deploymentTestStore
	err error
}

func (s *commercialActionFailingDeploymentStore) GetCloudWorkerDeployment(string) (*types.CloudWorkerDeployment, error) {
	return nil, s.err
}

// commercialActionResponseCode reads the machine-readable code of a refused
// operator action response.
func commercialActionResponseCode(body []byte) string {
	var value struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &value)
	return value.Code
}

// Which machine an ensure adopts must not depend on the order the roster happened
// to arrive in. The roster is sorted by username, so an account whose
// lowest-numbered machine sorts last is the case that proves the operation sorts
// by identity before it keeps one.
func TestCommercialActionEnsurePicksTheLowestUIDOfItsClass(t *testing.T) {
	shared := func(uid int64, tenant string) commercialActionTestMachine {
		return commercialActionTestMachine{UID: uid, Tenant: tenant, Profile: types.CloudWorkerPackedShared}
	}
	// Username order is bot-a, bot-m; UID order is 1004, 1007.
	h, _, credits, v := commercialActionWorkerFixture(t, types.CloudWorkerPackedShared,
		[]commercialActionTestMachine{shared(1007, "bot-a"), shared(1004, "bot-m")})
	rec := httptest.NewRecorder()
	if handled := h.prepareCommercialActionWorker(rec, 42, v); !handled {
		t.Fatal("an account with two same-class machines did not keep one")
	}
	if credits.keptWorker != 1004 || credits.kept != 1 || credits.reserved != 0 {
		t.Fatalf("kept %d (kept=%d reserved=%d body=%s): the operation did not pick the lowest identity", credits.keptWorker, credits.kept, credits.reserved, rec.Body)
	}
}

// The refusals that must not become a silent second machine, and the skips that
// must: a machine being destroyed or already claimed for billing stops the
// operation for a human, a same-class machine that is merely being destroyed next
// to an adoptable one does not, and a deleted row is not a machine at all.
func TestCommercialActionEnsureNeverProvisionBehindAFailedMachineCheck(t *testing.T) {
	shared := func(uid int64, tenant, state, billing string) commercialActionTestMachine {
		return commercialActionTestMachine{UID: uid, Tenant: tenant, Profile: types.CloudWorkerPackedShared, State: state, BillingAction: billing}
	}

	t.Run("an in-flight billing claim stops the operation", func(t *testing.T) {
		h, _, credits, v := commercialActionWorkerFixture(t, types.CloudWorkerPackedShared,
			[]commercialActionTestMachine{shared(1003, "bot-shared", "active", "expire")})
		rec := httptest.NewRecorder()
		if handled := h.prepareCommercialActionWorker(rec, 42, v); !handled {
			t.Fatal("a machine another pass is already re-terming was adopted")
		}
		if rec.Code != 409 || credits.kept != 0 || credits.reserved != 0 || commercialActionResponseCode(rec.Body.Bytes()) != "cloud_worker_reconciliation_required" {
			t.Fatalf("billing claim not reported: %d %s credits=%+v", rec.Code, rec.Body, credits)
		}
	})

	t.Run("a deleted machine is skipped instead of blocking", func(t *testing.T) {
		h, _, credits, v := commercialActionWorkerFixture(t, types.CloudWorkerPackedShared,
			[]commercialActionTestMachine{shared(1003, "bot-shared", "deleted", "")})
		rec := httptest.NewRecorder()
		if handled := h.prepareCommercialActionWorker(rec, 42, v); handled || credits.kept != 0 || credits.reserved != 0 {
			t.Fatalf("a deleted machine was treated as live: handled=%v body=%s credits=%+v", handled, rec.Body, credits)
		}
	})

	t.Run("a machine being destroyed does not hide an adoptable one", func(t *testing.T) {
		h, _, credits, v := commercialActionWorkerFixture(t, types.CloudWorkerPackedShared,
			[]commercialActionTestMachine{shared(1004, "bot-going", "delete_running", ""), shared(1005, "bot-staying", "active", "")})
		rec := httptest.NewRecorder()
		if handled := h.prepareCommercialActionWorker(rec, 42, v); !handled {
			t.Fatal("an adoptable same-class machine was not kept")
		}
		if credits.keptWorker != 1005 || credits.kept != 1 || rec.Code != 200 {
			t.Fatalf("kept %d (kept=%d code=%d body=%s)", credits.keptWorker, credits.kept, rec.Code, rec.Body)
		}
	})

	t.Run("an unreadable lifecycle does not fall back to provisioning", func(t *testing.T) {
		h, _, credits, v := commercialActionWorkerFixture(t, types.CloudWorkerPackedShared,
			[]commercialActionTestMachine{shared(1003, "bot-shared", "active", "")})
		credits.lifecycleError = errors.New("lifecycle store down")
		rec := httptest.NewRecorder()
		if handled := h.prepareCommercialActionWorker(rec, 42, v); !handled {
			t.Fatal("an unreadable lifecycle was read as \"this account has no machine\"")
		}
		if rec.Code != 409 || credits.kept != 0 || credits.reserved != 0 || commercialActionResponseCode(rec.Body.Bytes()) != "cloud_worker_reconciliation_required" {
			t.Fatalf("lifecycle read failure not reported: %d %s credits=%+v", rec.Code, rec.Body, credits)
		}
	})
}

// The adoption set both the keep transaction and the preview share. delete_running
// and deleted are decisions already taken (a destroy started or finished), and any
// billing claim in flight means another pass owns the machine's next term.
func TestCloudWorkerCanBeAdoptedRule(t *testing.T) {
	for _, tc := range []struct {
		state, billing string
		want           bool
	}{
		{"", "", true},
		{"active", "", true},
		{"delete_pending", "", true},
		{"delete_failed", "", true},
		{"delete_running", "", false},
		{"deleted", "", false},
		// A state a future schema may add is not adoptable by default.
		{"migrating", "", false},
		{"active", "expire", false},
		{"active", "sync", false},
		{"delete_pending", "convert", false},
		{"", "suspend", false},
	} {
		if got := types.CloudWorkerCanBeAdopted(tc.state, tc.billing); got != tc.want {
			t.Fatalf("CloudWorkerCanBeAdopted(%q, %q) = %v, want %v", tc.state, tc.billing, got, tc.want)
		}
	}
}
