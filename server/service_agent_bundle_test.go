package server

import (
	"encoding/json"
	"strings"
	"testing"
)

func validBundleFixture() ServiceAgentBundle {
	return ServiceAgentBundle{
		Schema: ServiceAgentBundleSchema,
		Slug:   "weekly-report",
		Files: []ServiceAgentBundleFile{
			{Path: "agent.json", Content: `{
  "schema": "catsco.service-agent.v1",
  "slug": "weekly-report",
  "prompt": {"system": "system-prompt.md"},
  "behavior": {"async_threshold_seconds": 60},
  "models": ["claude-sonnet-5-5"]
}`},
			{Path: "system-prompt.md", Content: "你是周报助手。\n"},
			{Path: "skills/company-style.md", Content: "# 文风\n正式。\n"},
			{Path: "knowledge/glossary.md", Content: "| 术语 |\n| --- |\n"},
		},
	}
}

// 作者（往往是 Agent 自己）在 agent.json 里声明「做什么」和「入参长什么样」，
// 平台派生时必须原样带出来——这是调用方模型唯一能看见的契约。
func TestServiceAgentBundleDerivesAuthorContract(t *testing.T) {
	bundle := validBundleFixture()
	bundle.Files[0].Content = `{
  "schema": "catsco.service-agent.v1",
  "slug": "weekly-report",
  "name": "周报助手",
  "description": "把一周的零散记录整理成对外周报，含进展、风险、下周计划。当用户明确要周报时使用；闲聊或单纯问日期时不要用。",
  "input_schema": {
    "type": "object",
    "properties": {
      "notes": {"type": "string", "description": "本周记录原文"},
      "audience": {"type": "string", "enum": ["老板", "全员"], "description": "给谁看"}
    },
    "required": ["notes"]
  },
  "prompt": {"system": "system-prompt.md"},
  "behavior": {"async_threshold_seconds": 60},
  "models": ["claude-sonnet-5-5"]
}`
	definition, err := ValidateServiceAgentBundle(bundle, "weekly-report")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if !strings.Contains(definition.Description, "整理成对外周报") {
		t.Fatalf("description = %q", definition.Description)
	}
	if len(definition.InputSchema) == 0 {
		t.Fatal("input_schema should survive derivation")
	}
	// 紧凑化后仍是合法 JSON，且保留了作者写的 required
	var schema struct {
		Required   []string       `json:"required"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(definition.InputSchema, &schema); err != nil {
		t.Fatalf("input_schema not valid json: %v", err)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "notes" {
		t.Fatalf("required = %v", schema.Required)
	}
	if _, ok := schema.Properties["audience"]; !ok {
		t.Fatalf("properties = %v", schema.Properties)
	}
}

// 作者没写就留空（面板会退回通用写法），但写错了必须拒绝——静默丢弃等于让工具变哑巴。
func TestServiceAgentBundleRejectsBadInputSchema(t *testing.T) {
	cases := []struct {
		name   string
		schema string
	}{
		{name: "not an object", schema: `"just a string"`},
		{name: "wrong top-level type", schema: `{"type":"string"}`},
		{name: "empty object", schema: `{}`},
	}
	for _, tc := range cases {
		bundle := validBundleFixture()
		bundle.Files[0].Content = `{
  "schema": "catsco.service-agent.v1",
  "slug": "weekly-report",
  "prompt": {"system": "system-prompt.md"},
  "behavior": {"async_threshold_seconds": 60},
  "models": ["claude-sonnet-5-5"],
  "input_schema": ` + tc.schema + `
}`
		if _, err := ValidateServiceAgentBundle(bundle, "weekly-report"); err == nil {
			t.Fatalf("%s: expected error", tc.name)
		}
	}
}

func TestServiceAgentBundleValidateAndHash(t *testing.T) {
	bundle := validBundleFixture()
	definition, err := ValidateServiceAgentBundle(bundle, "weekly-report")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if definition.AsyncThresholdSeconds != 60 {
		t.Fatalf("threshold = %d", definition.AsyncThresholdSeconds)
	}
	if len(definition.Models) != 1 || definition.Models[0] != "claude-sonnet-5-5" {
		t.Fatalf("models = %v", definition.Models)
	}

	hash := CanonicalServiceBundleHash(bundle.Files)
	if len(hash) != 64 {
		t.Fatalf("hash length = %d", len(hash))
	}

	// 稳定性：文件顺序不影响 hash；CRLF 归一化后同 hash；内容变化改变 hash。
	reordered := append([]ServiceAgentBundleFile(nil), bundle.Files...)
	reordered[0], reordered[1] = reordered[1], reordered[0]
	if got := CanonicalServiceBundleHash(reordered); got != hash {
		t.Fatalf("order sensitivity: %s vs %s", got, hash)
	}
	crlf := append([]ServiceAgentBundleFile(nil), bundle.Files...)
	for i := range crlf {
		crlf[i].Content = strings.ReplaceAll(crlf[i].Content, "\n", "\r\n")
	}
	if got := CanonicalServiceBundleHash(crlf); got != hash {
		t.Fatalf("CRLF normalization broke stability: %s vs %s", got, hash)
	}
	tampered := append([]ServiceAgentBundleFile(nil), bundle.Files...)
	tampered[2].Content += "改动"
	if got := CanonicalServiceBundleHash(tampered); got == hash {
		t.Fatal("content change must change hash")
	}
}

func TestServiceAgentBundleValidateRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ServiceAgentBundle)
	}{
		{"bad schema", func(b *ServiceAgentBundle) { b.Schema = "x" }},
		{"slug mismatch", func(b *ServiceAgentBundle) { b.Slug = "other" }},
		{"no files", func(b *ServiceAgentBundle) { b.Files = nil }},
		{"traversal", func(b *ServiceAgentBundle) { b.Files[1].Path = "../system-prompt.md" }},
		{"absolute", func(b *ServiceAgentBundle) { b.Files[1].Path = "/etc/passwd" }},
		{"duplicate", func(b *ServiceAgentBundle) { b.Files = append(b.Files, b.Files[1]) }},
		{"no manifest", func(b *ServiceAgentBundle) { b.Files = b.Files[1:] }},
		{"manifest bad json", func(b *ServiceAgentBundle) { b.Files[0].Content = "{" }},
		{"prompt missing", func(b *ServiceAgentBundle) {
			b.Files = append(b.Files[:1], b.Files[2:]...)
		}},
		{"manifest slug mismatch", func(b *ServiceAgentBundle) {
			b.Files[0].Content = strings.ReplaceAll(b.Files[0].Content, `"slug": "weekly-report"`, `"slug": "hello"`)
		}},
		{"manifest schema mismatch", func(b *ServiceAgentBundle) {
			b.Files[0].Content = strings.ReplaceAll(b.Files[0].Content, "catsco.service-agent.v1", "x")
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bundle := validBundleFixture()
			tc.mutate(&bundle)
			if _, err := ValidateServiceAgentBundle(bundle, bundle.Slug); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

// 跨语言契约钉定：与 catsco-agents tests/service-agent-bundle.test.ts 同 fixture、同期望值。
// 两侧算法若有任何漂移（归一化/排序/编码），双仓 CI 同时红。
func TestServiceAgentBundleGoldenHash(t *testing.T) {
	files := []ServiceAgentBundleFile{
		{Path: "agent.json", Content: `{"schema":"catsco.service-agent.v1","slug":"weekly-report","prompt":{"system":"system-prompt.md"},"behavior":{"async_threshold_seconds":60},"models":["claude-sonnet-5-5"]}`},
		{Path: "system-prompt.md", Content: "你是周报助手。\n"},
		{Path: "skills/company-style.md", Content: "# 文风\n正式。\n"},
		{Path: "knowledge/glossary.md", Content: "| 术语 |\n| --- |\n"},
	}
	const golden = "2d5b78ac17ef250323346e330d75b760fffaf9a2f05563bbd387b7fe86f44b48"
	if got := CanonicalServiceBundleHash(files); got != golden {
		t.Fatalf("golden hash drift: %s", got)
	}
}

// 真实包用 model.primary/fallbacks 声明模型——派生必须覆盖（2026-10-15 生产事故：
// 只读顶层 models 导致服务 token 白名单为空 → relay 400 service_model_not_allowed）。
func TestServiceAgentBundleDerivesModelsFromModelBinding(t *testing.T) {
	bundle := validBundleFixture()
	bundle.Files[0].Content = `{
  "schema": "catsco.service-agent.v1",
  "slug": "weekly-report",
  "prompt": {"system": "system-prompt.md"},
  "model": {
    "primary": {"provider": "anthropic", "model": "claude-sonnet-5-5"},
    "fallbacks": [{"provider": "anthropic", "model": "claude-haiku-4-5"}]
  },
  "behavior": {"async_threshold_seconds": 60}
}`
	definition, err := ValidateServiceAgentBundle(bundle, "weekly-report")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	want := []string{"claude-sonnet-5-5", "claude-haiku-4-5"}
	if len(definition.Models) != len(want) {
		t.Fatalf("models = %v, want %v", definition.Models, want)
	}
	for i := range want {
		if definition.Models[i] != want[i] {
			t.Fatalf("models = %v, want %v", definition.Models, want)
		}
	}

	// 顶层 models 与 model.primary 混用：去重保序（primary 在前）。
	bundle.Files[0].Content = strings.Replace(bundle.Files[0].Content,
		`"behavior"`, `"models": ["claude-haiku-4-5", "extra-model"], "behavior"`, 1)
	definition, err = ValidateServiceAgentBundle(bundle, "weekly-report")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	want = []string{"claude-sonnet-5-5", "claude-haiku-4-5", "extra-model"}
	if len(definition.Models) != len(want) {
		t.Fatalf("mixed models = %v, want %v", definition.Models, want)
	}
	for i := range want {
		if definition.Models[i] != want[i] {
			t.Fatalf("mixed models = %v, want %v", definition.Models, want)
		}
	}
}

func TestServiceAgentBundleDefaults(t *testing.T) {
	bundle := validBundleFixture()
	bundle.Files[0].Content = `{"schema":"catsco.service-agent.v1","slug":"weekly-report","prompt":{"system":"system-prompt.md"}}`
	definition, err := ValidateServiceAgentBundle(bundle, "weekly-report")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if definition.AsyncThresholdSeconds != 60 {
		t.Fatalf("default threshold = %d", definition.AsyncThresholdSeconds)
	}
	if definition.Models == nil {
		t.Fatal("models must default to empty slice, not nil")
	}
	// 自动注册的展示名：manifest 无 name → 回退 slug。
	if definition.Name != "weekly-report" {
		t.Fatalf("default name = %q, want slug fallback", definition.Name)
	}
}

// manifest name → 展示名（strip + VARCHAR(128) 列宽按 rune 截断）；中文名不被切坏。
func TestServiceAgentBundleDerivesDisplayName(t *testing.T) {
	bundle := validBundleFixture()
	bundle.Files[0].Content = `{"schema":"catsco.service-agent.v1","slug":"weekly-report","name":"  周报助手  ","prompt":{"system":"system-prompt.md"}}`
	definition, err := ValidateServiceAgentBundle(bundle, "weekly-report")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if definition.Name != "周报助手" {
		t.Fatalf("name = %q, want 周报助手", definition.Name)
	}

	longName := strings.Repeat("超", 200)
	bundle.Files[0].Content = `{"schema":"catsco.service-agent.v1","slug":"weekly-report","name":"` + longName + `","prompt":{"system":"system-prompt.md"}}`
	definition, err = ValidateServiceAgentBundle(bundle, "weekly-report")
	if err != nil {
		t.Fatalf("validate: %v", err)
	}
	if runes := []rune(definition.Name); len(runes) != maxServiceAgentNameRunes {
		t.Fatalf("truncated name runes = %d, want %d", len(runes), maxServiceAgentNameRunes)
	}
}
