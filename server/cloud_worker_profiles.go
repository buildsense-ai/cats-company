package server

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

type cloudWorkerProfileContextKey struct{}
type cloudWorkerBillingContextKey struct{}

type cloudWorkerBillingCredits interface {
	CloudWorkerConfiguredCreditSummary(int64, string, string) (int, int, error)
	ReserveCloudWorkerConfiguredCredit(int64, string, string, string) (types.CloudWorkerCreditSelection, bool, error)
	GrantCloudWorkerConfiguredCredits(int64, int, string, *time.Time, string, string) (int, error)
}

type cloudWorkerDeploymentStore interface {
	SetCloudWorkerDeployment(int64, types.CloudWorkerDeployment) error
	GetCloudWorkerDeployment(string) (*types.CloudWorkerDeployment, error)
	ListCloudWorkerDeployments() (map[string]types.CloudWorkerDeployment, error)
}

type cloudWorkerProfileCredits interface {
	CloudWorkerProfileCreditSummary(int64, string) (int, int, error)
	ReserveCloudWorkerProfileCredit(int64, string, string) (string, bool, error)
	GrantCloudWorkerProfileCredits(int64, int, string, *time.Time, string) (int, error)
}

// Explicit allowlist: deployment records never carry AK/SK, login tokens or keys.
var cloudWorkerProfileEnvKeys = []string{
	"CTYUN_WORKER_REGION_ID", "CTYUN_WORKER_PROJECT_ID", "CTYUN_IMAGE_PROJECT_ID",
	"CTYUN_WORKER_AZ_NAME", "CTYUN_WORKER_FLAVOR_ID", "CTYUN_WORKER_VPC_ID",
	"CTYUN_WORKER_SUBNET_ID", "CTYUN_WORKER_SECURITY_GROUP_ID", "CTYUN_WORKER_EXT_IP",
	"CTYUN_WORKER_BILLING_MODE", "CTYUN_WORKER_CYCLE_COUNT",
	"CTYUN_JUMP_IP", "CTYUN_JUMP_PORT", "CTYUN_JUMP_USER", "CTYUN_JUMP_KEY",
	"CATSCO_WORKER_HTTP_BASE_URL", "CATSCO_WORKER_SERVER_URL",
}

type cloudWorkerProfileOption struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	RegionID    string `json:"region_id"`
	ProjectID   string `json:"project_id"`
	NetworkMode string `json:"network_mode"`
	Available   bool   `json:"available"`
	Reason      string `json:"reason,omitempty"`
}

func (h *CloudWorkerHandler) deploymentForProfile(profile string) (types.CloudWorkerDeployment, error) {
	profile, valid := types.NormalizeCloudWorkerProfile(profile)
	value := types.CloudWorkerDeployment{Profile: profile, Env: map[string]string{}}
	if !valid {
		return value, fmt.Errorf("unsupported cloud worker profile")
	}
	for _, key := range cloudWorkerProfileEnvKeys {
		value.Env[key] = os.Getenv(key)
	}
	value.Env["CTYUN_WORKER_EXT_IP"] = "0"
	if profile == types.CloudWorkerPrivateNAT {
		return value, nil
	}
	if strings.TrimSpace(h.publicProfileJSON) == "" {
		return value, fmt.Errorf("public deployment profile is not configured")
	}
	var overrides map[string]string
	if err := json.Unmarshal([]byte(h.publicProfileJSON), &overrides); err != nil {
		return value, fmt.Errorf("invalid public deployment configuration")
	}
	for key, entry := range overrides {
		if _, allowed := value.Env[key]; !allowed {
			return value, fmt.Errorf("unsupported deployment configuration field %s", key)
		}
		if strings.ContainsAny(entry, "\x00\r\n") {
			return value, fmt.Errorf("invalid deployment configuration field %s", key)
		}
		value.Env[key] = entry
	}
	// Never borrow another region's resource IDs when the public profile is incomplete.
	for _, key := range cloudWorkerProfileEnvKeys[:8] {
		if strings.TrimSpace(overrides[key]) == "" {
			return value, fmt.Errorf("public deployment requires %s", key)
		}
	}
	value.Env["CTYUN_WORKER_EXT_IP"] = "1"
	value.Env["CTYUN_JUMP_IP"] = ""
	value.Env["CTYUN_JUMP_KEY"] = ""
	if overrides["CATSCO_WORKER_HTTP_BASE_URL"] == "" {
		value.Env["CATSCO_WORKER_HTTP_BASE_URL"] = "https://app.catsco.cn"
	}
	if overrides["CATSCO_WORKER_SERVER_URL"] == "" {
		value.Env["CATSCO_WORKER_SERVER_URL"] = "wss://app.catsco.cn/v0/channels"
	}
	return value, nil
}

func (h *CloudWorkerHandler) deploymentForTenant(tenant string) (types.CloudWorkerDeployment, error) {
	if data, ok := h.db.(cloudWorkerDeploymentStore); ok && tenant != "" {
		value, err := data.GetCloudWorkerDeployment(tenant)
		if err != nil {
			return types.CloudWorkerDeployment{}, fmt.Errorf("load worker deployment: %w", err)
		}
		if value != nil {
			return *value, nil
		}
	}
	return h.deploymentForProfile(types.CloudWorkerPrivateNAT)
}

func deploymentEnvironment(base []string, deployment types.CloudWorkerDeployment) ([]string, error) {
	allowed := map[string]bool{}
	for _, key := range cloudWorkerProfileEnvKeys {
		allowed[key] = true
	}
	result := map[string]string{}
	for _, pair := range base {
		if key, value, ok := strings.Cut(pair, "="); ok {
			result[key] = value
		}
	}
	// Artifact gateway SSH is independent of the worker's SSH route.
	for _, suffix := range []string{"IP", "PORT", "USER", "KEY"} {
		key := "CATSCO_ARTIFACT_GATEWAY_SSH_" + suffix
		if result[key] == "" {
			result[key] = result["CTYUN_JUMP_"+suffix]
		}
	}
	for _, suffix := range []string{"REGION_ID", "PROJECT_ID"} {
		result["CATSCO_WORKER_DEFAULT_"+suffix] = result["CTYUN_WORKER_"+suffix]
	}
	for key, value := range deployment.Env {
		if !allowed[key] || strings.ContainsAny(value, "\x00\r\n") {
			return nil, fmt.Errorf("invalid persisted deployment environment")
		}
		result[key] = value
	}
	if deployment.Profile == types.CloudWorkerPublicIP {
		result["CTYUN_WORKER_EXT_IP"] = "1"
		result["CTYUN_JUMP_IP"] = ""
	} else if deployment.Profile != types.CloudWorkerPrivateNAT {
		return nil, fmt.Errorf("invalid persisted deployment profile")
	}
	raw, err := json.Marshal(deployment)
	if err != nil {
		return nil, err
	}
	result["CATSCO_WORKER_DEPLOYMENT_JSON"] = string(raw)
	var env []string
	for key, value := range result {
		env = append(env, key+"="+value)
	}
	sort.Strings(env)
	return env, nil
}

// Query every persisted deployment independently. A failed resource pool must
// never make its workers appear deleted, or erase another pool's fresh status.
func (h *CloudWorkerHandler) collectDeploymentStatus() (map[string]cloudInstanceInfo, error) {
	legacy, err := h.deploymentForProfile(types.CloudWorkerPrivateNAT)
	if err != nil {
		return nil, err
	}
	deployments := map[string]types.CloudWorkerDeployment{}
	if data, ok := h.db.(cloudWorkerDeploymentStore); ok {
		deployments, err = data.ListCloudWorkerDeployments()
		if err != nil {
			return nil, err
		}
	}
	type group struct {
		deployment types.CloudWorkerDeployment
		tenants    []string
	}
	groups := map[string]*group{}
	add := func(tenant string, deployment types.CloudWorkerDeployment) {
		if deployment.Profile == "" {
			deployment = legacy
		}
		encoded, _ := json.Marshal(deployment)
		key := string(encoded)
		if groups[key] == nil {
			groups[key] = &group{deployment: deployment}
		}
		if tenant != "" {
			groups[key].tenants = append(groups[key].tenants, tenant)
		}
	}
	add("", legacy)
	for tenant, deployment := range deployments {
		add(tenant, deployment)
	}
	type result struct {
		group *group
		out   string
		err   error
	}
	results := make(chan result, len(groups))
	limit := make(chan struct{}, 2)
	for _, g := range groups {
		go func(g *group) {
			limit <- struct{}{}
			out, err := h.runDeploymentScript(cloudWorkerStatusProbeTimeout, g.deployment, h.statusScript)
			<-limit
			results <- result{g, out, err}
		}(g)
	}
	infos := map[string]cloudInstanceInfo{}
	var failed []string
	// Include only the tenants belonging to each group when DB inventory exists.
	for range groups {
		r := <-results
		if r.err != nil {
			failed = append(failed, r.group.deployment.Profile)
			for _, tenant := range r.group.tenants {
				infos[tenant] = cloudInstanceInfo{Status: "unavailable"}
			}
			continue
		}
		parsed := parseCloudWorkerStatusTSV(r.out)
		if len(deployments) == 0 {
			for tenant, info := range parsed {
				infos[tenant] = info
			}
		}
		for _, tenant := range r.group.tenants {
			info, found := parsed[tenant]
			if !found {
				info.Status = "missing"
			}
			infos[tenant] = info
		}
	}
	if len(failed) == len(groups) {
		return nil, fmt.Errorf("all cloud status pools unavailable")
	}
	return infos, nil
}

func (h *CloudWorkerHandler) deploymentProfileOptions() []cloudWorkerProfileOption {
	var options []cloudWorkerProfileOption
	for _, profile := range []string{types.CloudWorkerPrivateNAT, types.CloudWorkerPublicIP} {
		value, err := h.deploymentForProfile(profile)
		option := cloudWorkerProfileOption{ID: profile, Label: "华南 2 · 无公网 IP", NetworkMode: "private", Available: err == nil && h.provisionScript != "", RegionID: value.Env["CTYUN_WORKER_REGION_ID"], ProjectID: value.Env["CTYUN_WORKER_PROJECT_ID"]}
		if profile == types.CloudWorkerPublicIP {
			option.Label, option.NetworkMode = "佛山 7 · 公网 IP", "public"
		}
		if err != nil {
			option.Reason = err.Error()
		}
		options = append(options, option)
	}
	return options
}
