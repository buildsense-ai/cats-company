package server

import (
	"os"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

// 共享机（packed_shared）与独享 ECS 必须能在同一个控制台共存：
// 运营选什么后端，脚本就必须收到什么后端，不能靠账号级白名单猜。
func TestPackedSharedProfileSelectsContainerBackend(t *testing.T) {
	h := &CloudWorkerHandler{}
	deployment, err := h.deploymentForProfile(types.CloudWorkerPackedShared)
	if err != nil {
		t.Fatalf("packed_shared profile rejected: %v", err)
	}
	if deployment.Profile != types.CloudWorkerPackedShared {
		t.Fatalf("profile = %q", deployment.Profile)
	}
	if got := deployment.Env["CATSCO_WORKER_BACKEND"]; got != "packed" {
		t.Fatalf("backend env = %q, want packed", got)
	}
	if got := deployment.Env["CTYUN_WORKER_EXT_IP"]; got != "0" {
		t.Fatalf("ext ip = %q, want 0 (shared containers stay private)", got)
	}

	// Dedicated profiles must never leak the shared backend setting.
	for _, profile := range []string{types.CloudWorkerPrivateNAT, types.CloudWorkerPublicIP} {
		h := &CloudWorkerHandler{publicProfileJSON: `{"CTYUN_WORKER_REGION_ID":"r","CTYUN_WORKER_PROJECT_ID":"p","CTYUN_IMAGE_PROJECT_ID":"i","CTYUN_WORKER_AZ_NAME":"az","CTYUN_WORKER_FLAVOR_ID":"f","CTYUN_WORKER_VPC_ID":"v","CTYUN_WORKER_SUBNET_ID":"s","CTYUN_WORKER_SECURITY_GROUP_ID":"g"}`}
		d, err := h.deploymentForProfile(profile)
		if err != nil {
			t.Fatalf("%s: %v", profile, err)
		}
		if got := d.Env["CATSCO_WORKER_BACKEND"]; got != "ecs" {
			t.Fatalf("%s backend env = %q, want ecs", profile, got)
		}
	}
}

func TestDeploymentEnvironmentKeepsPackedBackend(t *testing.T) {
	deployment := types.CloudWorkerDeployment{
		Profile: types.CloudWorkerPackedShared,
		Env:     map[string]string{"CATSCO_WORKER_BACKEND": "packed"},
	}
	env, err := deploymentEnvironment([]string{"PATH=/usr/bin"}, deployment)
	if err != nil {
		t.Fatalf("packed deployment rejected: %v", err)
	}
	found := false
	for _, pair := range env {
		if pair == "CATSCO_WORKER_BACKEND=packed" {
			found = true
		}
	}
	if !found {
		t.Fatalf("CATSCO_WORKER_BACKEND missing from env: %v", env)
	}
}

func TestPackedSharedProfileIsOfferedToOperators(t *testing.T) {
	h := &CloudWorkerHandler{provisionScript: "/bin/true"}
	options := h.deploymentProfileOptions()
	var packed *cloudWorkerProfileOption
	for i := range options {
		if options[i].ID == types.CloudWorkerPackedShared {
			packed = &options[i]
		}
	}
	if packed == nil {
		t.Fatalf("packed_shared missing from operator options: %+v", options)
	}
	if packed.NetworkMode != "shared" || !packed.Available {
		t.Fatalf("packed option = %+v", *packed)
	}
}

func TestPackedSharedRequiresNoPublicProfileConfig(t *testing.T) {
	// 共享机不吃公网 IP 配置；public profile 缺失不能连累它。
	t.Setenv("CTYUN_WORKER_REGION_ID", "")
	os.Unsetenv("CATSCO_WORKER_PUBLIC_PROFILE_JSON")
	h := &CloudWorkerHandler{}
	if _, err := h.deploymentForProfile(types.CloudWorkerPackedShared); err != nil {
		t.Fatalf("packed_shared must not depend on the public profile: %v", err)
	}
}

// 共享机的调度归集群，机器参数（地域/规格/VPC/子网/安全组/跳板）只属于独享实例。
// 老实现把账号级的 CTYUN_* 全量拷进共享机部署记录，于是 bot_config.cloud_deployment
// 里躺着一份"并不存在的机器"，后台总览和导出都得再特判一次把它抹掉。
func TestPackedSharedDeploymentCarriesNoMachineSettings(t *testing.T) {
	for _, key := range []string{
		"CTYUN_WORKER_REGION_ID", "CTYUN_WORKER_PROJECT_ID", "CTYUN_IMAGE_PROJECT_ID",
		"CTYUN_WORKER_AZ_NAME", "CTYUN_WORKER_FLAVOR_ID", "CTYUN_WORKER_VPC_ID",
		"CTYUN_WORKER_SUBNET_ID", "CTYUN_WORKER_SECURITY_GROUP_ID",
		"CTYUN_WORKER_BILLING_MODE", "CTYUN_WORKER_CYCLE_COUNT",
		"CTYUN_JUMP_IP", "CTYUN_JUMP_PORT", "CTYUN_JUMP_USER", "CTYUN_JUMP_KEY",
	} {
		t.Setenv(key, "should-not-leak")
	}
	h := &CloudWorkerHandler{}
	deployment, err := h.deploymentForProfile(types.CloudWorkerPackedShared)
	if err != nil {
		t.Fatalf("packed_shared profile rejected: %v", err)
	}
	for key, value := range deployment.Env {
		switch key {
		case "CATSCO_WORKER_BACKEND", "CTYUN_WORKER_EXT_IP", "CATSCO_WORKER_HTTP_BASE_URL", "CATSCO_WORKER_SERVER_URL":
			continue
		}
		if value != "" {
			t.Fatalf("shared deployment leaked machine setting %s=%q", key, value)
		}
	}
	if got := deployment.Env["CATSCO_WORKER_BACKEND"]; got != "packed" {
		t.Fatalf("backend = %q", got)
	}
}
