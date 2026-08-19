package main

import (
	"io"
	"strings"
	"testing"
)

func TestFetchRequiresPrivateCredentials(t *testing.T) {
	t.Setenv("CATSCO_WORKER_ARTIFACT_ACCESS_KEY_ID", "")
	t.Setenv("CATSCO_WORKER_ARTIFACT_SECRET_ACCESS_KEY", "")
	err := fetch("https://tos-cn-guangzhou.volces.com", "cn-guangzhou", "bucket", "key", "output")
	if err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("expected credential error, got %v", err)
	}
}

func TestFetchRequiresObjectCoordinates(t *testing.T) {
	t.Setenv("CATSCO_WORKER_ARTIFACT_ACCESS_KEY_ID", "ak")
	t.Setenv("CATSCO_WORKER_ARTIFACT_SECRET_ACCESS_KEY", "sk")
	err := fetch("", "cn-guangzhou", "bucket", "key", "output")
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("expected required field error, got %v", err)
	}
}

func TestListRequiresPrivateCredentials(t *testing.T) {
	t.Setenv("CATSCO_WORKER_ARTIFACT_ACCESS_KEY_ID", "")
	t.Setenv("CATSCO_WORKER_ARTIFACT_SECRET_ACCESS_KEY", "")
	err := listObjects("https://tos-cn-guangzhou.volces.com", "cn-guangzhou", "bucket", "update/worker/", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "credentials") {
		t.Fatalf("expected credential error, got %v", err)
	}
}

func TestListRequiresObjectCoordinates(t *testing.T) {
	t.Setenv("CATSCO_WORKER_ARTIFACT_ACCESS_KEY_ID", "ak")
	t.Setenv("CATSCO_WORKER_ARTIFACT_SECRET_ACCESS_KEY", "sk")
	err := listObjects("", "cn-guangzhou", "bucket", "update/worker/", io.Discard)
	if err == nil || !strings.Contains(err.Error(), "required") {
		t.Fatalf("expected required field error, got %v", err)
	}
}
