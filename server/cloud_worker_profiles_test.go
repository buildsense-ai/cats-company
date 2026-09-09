package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func testPublicProfile(t *testing.T) string {
	t.Helper()
	values := map[string]string{}
	for _, key := range cloudWorkerProfileEnvKeys[:8] {
		values[key] = "public-" + key
	}
	raw, _ := json.Marshal(values)
	return string(raw)
}

type deploymentTestStore struct {
	*cloudWorkerTestStore
	deployments map[string]types.CloudWorkerDeployment
}

func (s *deploymentTestStore) SetCloudWorkerDeployment(uid int64, value types.CloudWorkerDeployment) error {
	s.deployments[s.tenantNames[uid]] = value
	return nil
}
func (s *deploymentTestStore) GetCloudWorkerDeployment(tenant string) (*types.CloudWorkerDeployment, error) {
	value, ok := s.deployments[tenant]
	if !ok {
		return nil, nil
	}
	return &value, nil
}
func (s *deploymentTestStore) ListCloudWorkerDeployments() (map[string]types.CloudWorkerDeployment, error) {
	return s.deployments, nil
}

type profileCreditStub struct {
	quotaCreditStub
	profile, requested string
	released           bool
}

func (s *profileCreditStub) ReserveCloudWorkerProfileCredit(_ int64, _, profile string) (string, bool, error) {
	s.requested = profile
	return s.profile, s.available > 0, nil
}
func (s *profileCreditStub) CloudWorkerProfileCreditSummary(int64, string) (int, int, error) {
	return s.total, s.available, nil
}
func (s *profileCreditStub) GrantCloudWorkerProfileCredits(int64, int, string, *time.Time, string) (int, error) {
	return 1, nil
}
func (s *profileCreditStub) ReleaseCloudWorkerCredit(int64, string) error {
	s.released = true
	return nil
}

func TestCloudWorkerProfileEnvironmentIsolation(t *testing.T) {
	t.Setenv("CTYUN_WORKER_REGION_ID", "nat-region")
	t.Setenv("CTYUN_JUMP_IP", "nat-jump")
	t.Setenv("CTYUN_AK", "fake-secret-not-a-real-key")
	h, _ := newCloudWorkerTestHandler("")
	h.publicProfileJSON = testPublicProfile(t)
	deployment, err := h.deploymentForProfile(types.CloudWorkerPublicIP)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(deployment)
	if strings.Contains(string(raw), "fake-secret") {
		t.Fatal("credential persisted")
	}
	env, err := deploymentEnvironment([]string{"CTYUN_JUMP_IP=nat-jump", "CTYUN_JUMP_KEY=/keys/gateway", "CTYUN_WORKER_REGION_ID=nat-region"}, deployment)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(env, "\n")
	for _, want := range []string{"CTYUN_WORKER_EXT_IP=1", "CATSCO_ARTIFACT_GATEWAY_SSH_IP=nat-jump", "CATSCO_WORKER_DEFAULT_REGION_ID=nat-region", "CATSCO_WORKER_HTTP_BASE_URL=https://app.catsco.cn"} {
		if !strings.Contains(joined, want) {
			t.Errorf("missing %s", want)
		}
	}
	if os.Getenv("CTYUN_JUMP_IP") != "nat-jump" {
		t.Fatal("process configuration mutated")
	}
	h.publicProfileJSON = `{"CTYUN_WORKER_REGION_ID":"only-region"}`
	if _, err = h.deploymentForProfile(types.CloudWorkerPublicIP); err == nil {
		t.Fatal("incomplete cross-region config accepted")
	}
	deployment.Env["CTYUN_SK"] = "fake"
	if _, err = deploymentEnvironment(nil, deployment); err == nil {
		t.Fatal("credential field accepted")
	}
}

func TestCloudWorkerFrontendCannotSelectDeploymentProfile(t *testing.T) {
	h, store := newCloudWorkerTestHandler("")
	db := &deploymentTestStore{store, map[string]types.CloudWorkerDeployment{}}
	h.db = db
	credits := &profileCreditStub{quotaCreditStub: quotaCreditStub{total: 1, available: 1}, profile: types.CloudWorkerPrivateNAT}
	h.credits = credits
	// Missing provision script intentionally stops after the selected snapshot
	// has been persisted, without any provider resource creation.
	request := cloudWorkerRequest(7, http.MethodPost, "/api/cloud-workers", map[string]interface{}{"username": "bot-test", "deployment_profile": "public_ip"})
	recorder := httptest.NewRecorder()
	h.HandleCreate(recorder, request)
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status %d", recorder.Code)
	}
	if credits.requested != "" {
		t.Fatal("public JSON became trusted profile selector")
	}
	if len(db.deployments) != 1 {
		t.Fatalf("snapshots %d", len(db.deployments))
	}
	for _, value := range db.deployments {
		if value.Profile != types.CloudWorkerPrivateNAT {
			t.Fatal("paid credit upgraded to public")
		}
	}
	if !credits.released {
		t.Fatal("failed create did not release credit")
	}
}

func TestCloudWorkerPublicAdminSSHMetadata(t *testing.T) {
	h, store := newCloudWorkerTestHandler("")
	h.publicProfileJSON = testPublicProfile(t)
	deployment, _ := h.deploymentForProfile(types.CloudWorkerPublicIP)
	h.db = &deploymentTestStore{store, map[string]types.CloudWorkerDeployment{"bot-public": deployment}}
	store.adminRecords = []types.CloudWorkerAdminRecord{{WorkerUID: 42, OwnerUID: 7, TenantName: "bot-public"}}
	h.sshHostStateRoot = "/state/workers"
	h.sshJumpHost = "nat-jump"
	h.statusLoaded = true
	h.statusUpdatedAt = time.Now()
	h.statusSnapshot = map[string]cloudInstanceInfo{"bot-public": {Status: "running", PublicIP: "8.8.8.8", PrivateIP: "172.27.7.9"}}
	overview, err := h.CloudWorkerAdminOverview(time.Now())
	if err != nil {
		t.Fatal(err)
	}
	item := overview.Workers[0]
	if item.DeploymentProfile != types.CloudWorkerPublicIP || item.SSHJumpHost != "" || item.SSHKeyPath != "/state/workers/bot-public/id_rsa" || item.SSHExecutionHost != "cats-ctyun" {
		t.Fatalf("invalid SSH metadata: %+v", item)
	}
}

func TestCloudWorkerStatusPoolFailureDoesNotDeleteOtherWorkers(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX script contract is executed on Linux")
	}
	t.Setenv("CTYUN_WORKER_REGION_ID", "nat-region")
	h, store := newCloudWorkerTestHandler("")
	h.publicProfileJSON = testPublicProfile(t)
	public, _ := h.deploymentForProfile(types.CloudWorkerPublicIP)
	h.db = &deploymentTestStore{store, map[string]types.CloudWorkerDeployment{"bot-private": {}, "bot-public": public}}
	script := filepath.Join(t.TempDir(), "status.sh")
	if err := os.WriteFile(script, []byte("#!/bin/sh\n[ \"$CTYUN_WORKER_EXT_IP\" = 1 ] && exit 1\nprintf 'worker-bot-private\\trunning\\timage\\tv1\\t1\\t10.0.0.9\\t\\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	h.statusScript = script
	infos, err := h.collectDeploymentStatus()
	if err != nil {
		t.Fatal(err)
	}
	if infos["bot-private"].Status != "running" || infos["bot-public"].Status != "unavailable" {
		t.Fatalf("wrong pool results: %+v", infos)
	}
}
