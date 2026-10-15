package server

import (
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
}
