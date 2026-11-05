package server

import (
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store/types"
)

// 共享容器集群的租户名上限来自 incus 的实例名限制：1-63 字符。
// 容器名是 packed-<tenant>，故障切换还会建 packed-<tenant>-sb，
// 所以 "packed-" (7) + "-sb" (3) 之后只剩 53 给租户名。
//
// 2026-11-05 在 prod-1 用 `incus init --empty` 逐档实测：63 建得出，
// 64 直接报 "Invalid instance name: Name must be 1-63 characters long"。
func TestValidatePackedTenantName(t *testing.T) {
	cases := []struct {
		name    string
		profile string
		tenant  string
		wantErr bool
	}{
		{"租户名 53 字符通过", types.CloudWorkerPackedShared, "bot-" + strings.Repeat("a", 49), false},
		{"租户名 54 字符拒绝", types.CloudWorkerPackedShared, "bot-" + strings.Repeat("a", 50), true},
		{"平台 varchar(64) 允许的 68 字符租户名拒绝", types.CloudWorkerPackedShared, "bot-" + strings.Repeat("a", 64), true},
		{"独享 ECS 不受容器名限制", types.CloudWorkerPublicIP, "bot-" + strings.Repeat("a", 64), false},
		{"现网租户名通过", types.CloudWorkerPackedShared, "bot-bot-1446-53215df3", false},
	}
	for _, c := range cases {
		err := validatePackedTenantName(c.profile, c.tenant)
		if (err != nil) != c.wantErr {
			t.Fatalf("%s: err=%v wantErr=%v (tenant=%q len=%d)", c.name, err, c.wantErr, c.tenant, len(c.tenant))
		}
	}
}

// 边界必须对齐：username 49 → 租户名 53，换算出的容器名刚好 ≤ 63。
func TestCloudWorkerTenantNameBudget(t *testing.T) {
	if got := len(cloudWorkerTenantName(strings.Repeat("a", 49))); got != packedTenantNameMaxLen {
		t.Fatalf("username 49 字符应得到租户名 %d 字符，实际 %d", packedTenantNameMaxLen, got)
	}
	if got := len(cloudWorkerTenantName(strings.Repeat("a", 50))); got != packedTenantNameMaxLen+1 {
		t.Fatalf("username 50 字符应得到 %d 字符，实际 %d", packedTenantNameMaxLen+1, got)
	}
	for _, u := range []string{strings.Repeat("a", packedTenantNameMaxLen-4), "live-38", "bot-1446-53215df3"} {
		tenant := cloudWorkerTenantName(u)
		if n := len("packed-" + tenant); n > 63 {
			t.Fatalf("username %q → 容器名 %d 字符，超 incus 上限 63", u, n)
		}
		if n := len("packed-" + tenant + "-sb"); n > 63 {
			t.Fatalf("username %q → 备副本容器名 %d 字符，超 incus 上限 63", u, n)
		}
	}
}