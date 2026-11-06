package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
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

// TestCloudWorkerDeploymentSnapshotGuardMatchesGoKeys keeps
// cloudWorkerProfileEnvKeys (this package) and the jq allowlist in
// deploy/prod/ops/worker-deployment-profile.sh in lockstep.
//
// The shell guard is a hard copy, so a key that exists on only one side makes
// every lifecycle call (provision, deploy, renew, reset, rollback, destroy,
// billing) fail with "invalid worker deployment snapshot". That is exactly how
// the 2026-10-27 dedicated-ECS update outage happened: CATSCO_WORKER_BACKEND
// was introduced so the console could hand out shared-cluster containers and
// dedicated instances from one fleet, but only the Go list learned the key.
func TestCloudWorkerDeploymentSnapshotGuardMatchesGoKeys(t *testing.T) {
	path := filepath.Join("..", "deploy", "prod", "ops", "worker-deployment-profile.sh")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	guard := string(raw)

	shellKeys := deploymentSnapshotShellKeys(t, guard)
	goKeys := make(map[string]bool, len(cloudWorkerProfileEnvKeys))
	for _, key := range cloudWorkerProfileEnvKeys {
		goKeys[key] = true
		if !shellKeys[key] {
			t.Errorf("key %q is in cloudWorkerProfileEnvKeys but missing from the shell guard in %s", key, path)
		}
	}
	for key := range shellKeys {
		if !goKeys[key] {
			t.Errorf("key %q is in the shell guard in %s but not in cloudWorkerProfileEnvKeys", key, path)
		}
	}

	// Every profile the server can emit must survive the guard, otherwise a
	// worker created from that profile could never be updated, renewed or
	// reset without an operator hand-editing its deployment snapshot.
	for _, profile := range []string{
		types.CloudWorkerPrivateNAT,
		types.CloudWorkerPublicIP,
		types.CloudWorkerPackedShared,
	} {
		if !strings.Contains(guard, `"`+profile+`"`) {
			t.Errorf("profile %q is not accepted by the shell guard in %s", profile, path)
		}
	}
}

// deploymentSnapshotShellKeys extracts the jq allowlist keys so the contract
// test above can compare them with cloudWorkerProfileEnvKeys.
func deploymentSnapshotShellKeys(t *testing.T, script string) map[string]bool {
	t.Helper()
	const anchor = ".env | to_entries | all( .key | IN("
	start := strings.Index(script, anchor)
	if start < 0 {
		t.Fatalf("jq snapshot allowlist not found (looked for %q)", anchor)
	}
	rest := script[start+len(anchor):]
	end := strings.Index(rest, ")))")
	if end < 0 {
		t.Fatal("unterminated jq snapshot allowlist")
	}
	keys := map[string]bool{}
	for _, match := range regexp.MustCompile(`"([A-Z][A-Z0-9_]+)"`).FindAllStringSubmatch(rest[:end], -1) {
		keys[match[1]] = true
	}
	if len(keys) == 0 {
		t.Fatal("jq snapshot allowlist parsed to zero keys")
	}
	return keys
}
