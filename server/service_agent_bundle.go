package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// 服务 agent 包（云端下发；设计 A 文档 §2/§3"发布=数据包"）。
// 包 = 自包含 JSON：manifest + 人设 + 技能文件 + 知识文件（全文本）。
// package_hash（内容寻址）= 规范化序列化的 sha256；实例按 hash 拉取并复核。
//
// 规范化契约（Go/Node 两侧必须逐字节一致）：
//  1. 行尾归一化：\r\n → \n（文本文件语义不受行尾影响）
//  2. 按 path 升序；对每个文件取 sha256hex(LF 归一化后的 UTF-8 字节)
//  3. canonical = 拼接 "<path>\n<sha256hex>\n"；package_hash = sha256hex(canonical)
const (
	ServiceAgentBundleSchema = "catsco.service-agent-bundle.v1"

	// 上限（防止异常包进入分发链；常规包 KB 级）。
	maxServiceBundleBytes      = 2 << 20 // 2 MiB（整个 bundle JSON）
	maxServiceBundleFileBytes  = 512 << 10
	maxServiceBundleFileCount  = 64
	maxServiceBundlePathLength = 200
)

var (
	ErrServiceBundleInvalid          = errors.New("service agent bundle invalid")
	ErrServiceAgentPackageNotFound   = errors.New("service agent package not found")
	serviceBundleSlugPattern         = regexp.MustCompile(`^[a-z0-9-]+$`)
	serviceBundleManifestSchemaValue = "catsco.service-agent.v1"
)

// ServiceAgentBundleFile 是包内一个文件（全文本；路径为包内相对路径）。
type ServiceAgentBundleFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

// ServiceAgentBundle 是云端下发的自包含包。
type ServiceAgentBundle struct {
	Schema string                   `json:"schema"`
	Slug   string                   `json:"slug"`
	Files  []ServiceAgentBundleFile `json:"files"`
}

// ServiceAgentDerivedDefinition 是从 manifest 派生的平台侧定义（claim 签发服务 token 用）。
// 发布即派生——消除"包内模型 vs 平台定义"的双写裂缝。
type ServiceAgentDerivedDefinition struct {
	AsyncThresholdSeconds int      `json:"async_threshold_seconds"`
	Models                []string `json:"models"`
}

// NormalizeBundleLF 行尾归一化（契约第 1 步）。
func NormalizeBundleLF(content string) string {
	return strings.ReplaceAll(content, "\r\n", "\n")
}

func bundleFileDigest(content string) string {
	sum := sha256.Sum256([]byte(NormalizeBundleLF(content)))
	return hex.EncodeToString(sum[:])
}

// CanonicalServiceBundleHash 计算内容寻址 hash（契约第 2/3 步）。
func CanonicalServiceBundleHash(files []ServiceAgentBundleFile) string {
	sorted := append([]ServiceAgentBundleFile(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	var b strings.Builder
	for _, f := range sorted {
		b.WriteString(f.Path)
		b.WriteByte('\n')
		b.WriteString(bundleFileDigest(f.Content))
		b.WriteByte('\n')
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:])
}

// ValidateServiceAgentBundle 校验包结构并派生平台定义（expectedSlug = 路由上的 slug）。
func ValidateServiceAgentBundle(bundle ServiceAgentBundle, expectedSlug string) (ServiceAgentDerivedDefinition, error) {
	fail := func(format string, args ...any) (ServiceAgentDerivedDefinition, error) {
		return ServiceAgentDerivedDefinition{}, fmt.Errorf("%w: %s", ErrServiceBundleInvalid, fmt.Sprintf(format, args...))
	}
	if bundle.Schema != ServiceAgentBundleSchema {
		return fail("schema must be %s", ServiceAgentBundleSchema)
	}
	if bundle.Slug == "" || !serviceBundleSlugPattern.MatchString(bundle.Slug) {
		return fail("slug invalid")
	}
	if bundle.Slug != expectedSlug {
		return fail("slug mismatch (%s vs %s)", bundle.Slug, expectedSlug)
	}
	if len(bundle.Files) == 0 {
		return fail("files required")
	}
	if len(bundle.Files) > maxServiceBundleFileCount {
		return fail("too many files (%d)", len(bundle.Files))
	}
	seen := map[string]bool{}
	byPath := map[string]string{}
	total := 0
	for _, f := range bundle.Files {
		if err := validateBundlePath(f.Path); err != nil {
			return fail("file path %q: %v", f.Path, err)
		}
		if seen[f.Path] {
			return fail("duplicate file path %q", f.Path)
		}
		seen[f.Path] = true
		if len(f.Content) > maxServiceBundleFileBytes {
			return fail("file %q exceeds %d bytes", f.Path, maxServiceBundleFileBytes)
		}
		total += len(f.Content)
		byPath[f.Path] = f.Content
	}
	if total > maxServiceBundleBytes {
		return fail("bundle exceeds %d bytes", maxServiceBundleBytes)
	}
	manifestRaw, ok := byPath["agent.json"]
	if !ok {
		return fail("agent.json missing")
	}
	var manifest struct {
		Schema string `json:"schema"`
		Slug   string `json:"slug"`
		Prompt struct {
			System string `json:"system"`
		} `json:"prompt"`
		Behavior struct {
			AsyncThresholdSeconds *int `json:"async_threshold_seconds"`
		} `json:"behavior"`
		// 标准包用 model.primary/fallbacks 声明模型；顶层 models 为简化写法（两者兼容）。
		Model struct {
			Primary struct {
				Model string `json:"model"`
			} `json:"primary"`
			Fallbacks []struct {
				Model string `json:"model"`
			} `json:"fallbacks"`
		} `json:"model"`
		Models []string `json:"models"`
	}
	if err := json.Unmarshal([]byte(manifestRaw), &manifest); err != nil {
		return fail("agent.json parse: %v", err)
	}
	if manifest.Schema != serviceBundleManifestSchemaValue {
		return fail("agent.json schema must be %s", serviceBundleManifestSchemaValue)
	}
	if manifest.Slug != bundle.Slug {
		return fail("agent.json slug mismatch (%s)", manifest.Slug)
	}
	promptFile := strings.TrimSpace(manifest.Prompt.System)
	if promptFile == "" {
		return fail("agent.json prompt.system required")
	}
	if _, ok := byPath[promptFile]; !ok {
		return fail("prompt file %q missing from bundle", promptFile)
	}
	threshold := 60
	if manifest.Behavior.AsyncThresholdSeconds != nil {
		threshold = *manifest.Behavior.AsyncThresholdSeconds
	}
	// 模型白名单 = primary + fallbacks（去重保序），再并入顶层 models 兼容写法。
	models := make([]string, 0, 1+len(manifest.Model.Fallbacks)+len(manifest.Models))
	seenModel := map[string]bool{}
	appendModel := func(name string) {
		name = strings.TrimSpace(name)
		if name == "" || seenModel[name] {
			return
		}
		seenModel[name] = true
		models = append(models, name)
	}
	appendModel(manifest.Model.Primary.Model)
	for _, fallback := range manifest.Model.Fallbacks {
		appendModel(fallback.Model)
	}
	for _, name := range manifest.Models {
		appendModel(name)
	}
	return ServiceAgentDerivedDefinition{AsyncThresholdSeconds: threshold, Models: models}, nil
}

func validateBundlePath(p string) error {
	if p == "" {
		return errors.New("empty")
	}
	if len(p) > maxServiceBundlePathLength {
		return fmt.Errorf("longer than %d", maxServiceBundlePathLength)
	}
	if strings.HasPrefix(p, "/") {
		return errors.New("must be relative")
	}
	if strings.Contains(p, "\\") {
		return errors.New("backslash not allowed")
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return errors.New("path segments must be plain names")
		}
	}
	for _, r := range p {
		if r < 0x20 || r == 0x7f {
			return errors.New("control characters not allowed")
		}
	}
	return nil
}
