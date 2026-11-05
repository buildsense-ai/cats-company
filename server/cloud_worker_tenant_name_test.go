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

// 错误措辞要能直接给用户/运营看：不能出现 packed-*、incus 这类内部概念，
// 但要报出实际长度和上限，否则运营没法解释"为什么开不了"。
func TestValidatePackedTenantNameMessage(t *testing.T) {
	err := validatePackedTenantName(types.CloudWorkerPackedShared, "bot-"+strings.Repeat("a", 50))
	if err == nil {
		t.Fatal("54 字符租户名应当被拒绝")
	}
	msg := err.Error()
	if !strings.Contains(msg, "用户名过长") {
		t.Fatalf("措辞应面向用户，实际: %s", msg)
	}
	if !strings.Contains(msg, "50") || !strings.Contains(msg, "49") {
		t.Fatalf("应同时报出实际长度 50 和上限 49，实际: %s", msg)
	}
	if strings.Contains(msg, "packed-") || strings.Contains(msg, "incus") {
		t.Fatalf("不应暴露内部概念，实际: %s", msg)
	}
	if strings.Contains(msg, "缩短") || strings.Contains(msg, "重试") {
		t.Fatalf("username 注册后改不了，不能建议用户自己改: %s", msg)
	}
	if !strings.Contains(msg, "管理员") {
		t.Fatalf("应给出可执行的下一步（联系管理员），实际: %s", msg)
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

// 常量之间必须自洽：任何一处单独改数字都会立刻红。
func TestUsernameLimitConstants(t *testing.T) {
	if maxUsernameLen != 49 {
		t.Fatalf("maxUsernameLen 应为 49（incus 63 − packed- 7 − -sb 3 − bot- 4），实际 %d", maxUsernameLen)
	}
	if packedTenantNameMaxLen != len("bot-")+maxUsernameLen {
		t.Fatalf("packedTenantNameMaxLen(%d) 必须 = len(bot-)+maxUsernameLen(%d)",
			packedTenantNameMaxLen, len("bot-")+maxUsernameLen)
	}
	// 最长的容器名形态：packed-<tenant>-sb
	if n := len("packed-") + packedTenantNameMaxLen + len("-sb"); n > 63 {
		t.Fatalf("最长容器名 %d 字符，超 incus 上限 63", n)
	}
}

// 自动生成的名字也不能超：注册时没填 username 会从 email 推导，
// 历史上限是 54（base）和 64（candidate），都超过现在的 49。
func TestRegistrationUsernameBaseFitsLimit(t *testing.T) {
	cases := []string{
		strings.Repeat("a", 80) + "@example.com",
		strings.Repeat("verylonglocalpart", 5) + "@example.com",
		"ab@example.com",
		"user_name.with.dots@example.com",
	}
	for _, email := range cases {
		base := registrationUsernameBase(email)
		if len(base) > maxUsernameLen {
			t.Fatalf("email %q → base %q 长 %d，超上限 %d", email, base, len(base), maxUsernameLen)
		}
		if len(base) < 3 {
			t.Fatalf("email %q → base %q 太短，注册会失败", email, base)
		}
	}
}

// cloud worker 的用户名正则要和 maxUsernameLen 对齐。
func TestWorkerUsernameReBoundary(t *testing.T) {
	if !workerUsernameRe.MatchString(strings.Repeat("a", maxUsernameLen)) {
		t.Fatalf("%d 字符应通过", maxUsernameLen)
	}
	if workerUsernameRe.MatchString(strings.Repeat("a", maxUsernameLen+1)) {
		t.Fatalf("%d 字符应被拒绝", maxUsernameLen+1)
	}
	if !workerUsernameRe.MatchString("bot-1446-53215df3") {
		t.Fatal("现网 bot 名应通过")
	}
}
