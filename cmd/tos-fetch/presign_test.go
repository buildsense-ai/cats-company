package main

import (
	"net/url"
	"strings"
	"testing"
)

func TestPresignDelegatesOneObjectForTenMinutes(t *testing.T) {
	t.Setenv("CATSCO_WORKER_ARTIFACT_ACCESS_KEY_ID", "test-ak")
	t.Setenv("CATSCO_WORKER_ARTIFACT_SECRET_ACCESS_KEY", "test-secret")
	t.Setenv("CATSCO_WORKER_ARTIFACT_SECURITY_TOKEN", "")
	signed, err := presignObject("https://tos-cn-guangzhou.volces.com", "cn-guangzhou", "worker-cache", "update/worker/1.6.6/package.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(signed)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Scheme != "https" || parsed.Host != "worker-cache.tos-cn-guangzhou.volces.com" || parsed.Path != "/update/worker/1.6.6/package.tar.gz" {
		t.Fatalf("unexpected object coordinates: %s", parsed.Redacted())
	}
	if parsed.Query().Get("X-Tos-Expires") != "600" || parsed.Query().Get("X-Tos-Signature") == "" {
		t.Fatal("expected a signed 600-second URL")
	}
	if strings.Contains(signed, "test-secret") {
		t.Fatal("secret must never appear in URL")
	}
}

func TestPresignRequiresCoordinatesAndCredentials(t *testing.T) {
	if _, err := presignObject("", "region", "bucket", "key"); err == nil {
		t.Fatal("missing endpoint accepted")
	}
	if _, err := presignObject("https://tos-cn-guangzhou.volces.com", "cn-guangzhou", "bucket", "key\nother"); err == nil {
		t.Fatal("newline in key accepted")
	}
	t.Setenv("CATSCO_WORKER_ARTIFACT_ACCESS_KEY_ID", "")
	t.Setenv("CATSCO_WORKER_ARTIFACT_SECRET_ACCESS_KEY", "")
	if _, err := presignObject("https://tos-cn-guangzhou.volces.com", "cn-guangzhou", "bucket", "key"); err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("expected credential error, got %v", err)
	}
}
