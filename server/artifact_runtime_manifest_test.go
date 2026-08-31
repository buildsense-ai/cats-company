package server

import (
	"strings"
	"testing"
	"time"
)

func TestParseArtifactRuntimeManifest(t *testing.T) {
	manifest, err := parseArtifactRuntimeManifest([]byte(`{
		"contract_version":"catsco.artifact-manifest.v4",
		"purpose":"Maintain a risk register",
		"runtime":{
			"version":"0.1",
			"surfaces":[{"id":"risk-list","title":"Risks"},{"id":"risk-detail"}],
			"state":[{"namespace":"risks","mode":"read-write"}]
		}
	}`))
	if err != nil {
		t.Fatalf("parse runtime manifest: %v", err)
	}
	if manifest.Version != "0.1" || len(manifest.Surfaces) != 2 || len(manifest.State) != 1 {
		t.Fatalf("unexpected manifest: %#v", manifest)
	}
	if !manifest.allowsSurface("risk-list") || !manifest.allowsNamespace("risks", true) {
		t.Fatalf("manifest declarations were not preserved: %#v", manifest)
	}
}

func TestParseArtifactRuntimeManifestAcceptsRuntime02(t *testing.T) {
	manifest, err := parseArtifactRuntimeManifest([]byte(`{
		"contract_version":"catsco.artifact-manifest.v4",
		"runtime":{
			"version":"0.2",
			"surfaces":[{"id":"task-board"}],
			"state":[{"namespace":"project_tasks","mode":"read-write"}]
		}
	}`))
	if err != nil {
		t.Fatalf("parse Runtime 0.2 manifest: %v", err)
	}
	if manifest.Version != "0.2" || !manifest.allowsNamespace("project_tasks", true) {
		t.Fatalf("unexpected Runtime 0.2 manifest: %#v", manifest)
	}
}

func TestArtifactRuntimeManifestCacheIsBounded(t *testing.T) {
	handler := &CloudArtifactHandler{
		artifactRuntimeManifestTTL: time.Minute,
		artifactRuntimeManifestCache: make(
			map[string]artifactRuntimeManifestCacheEntry,
			artifactRuntimeManifestCacheMax,
		),
	}
	expiresAt := time.Now().Add(time.Minute)
	for index := 0; index < artifactRuntimeManifestCacheMax; index++ {
		handler.artifactRuntimeManifestCache[string(rune(index+1))] = artifactRuntimeManifestCacheEntry{
			expiresAt: expiresAt.Add(time.Duration(index) * time.Second),
		}
	}
	handler.storeArtifactRuntimeManifest("new", ArtifactRuntimeManifest{Version: "0.1"})
	if len(handler.artifactRuntimeManifestCache) != artifactRuntimeManifestCacheMax {
		t.Fatalf("cache size=%d, want %d", len(handler.artifactRuntimeManifestCache), artifactRuntimeManifestCacheMax)
	}
	if _, ok := handler.artifactRuntimeManifestCache["new"]; !ok {
		t.Fatal("new manifest was not cached")
	}
}

func TestParseArtifactRuntimeManifestRejectsNonV4AndDuplicates(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "v3",
			body: `{"contract_version":"catsco.artifact-manifest.v3","runtime":{"version":"0.1","surfaces":[{"id":"main"}],"state":[{"namespace":"data","mode":"read-write"}]}}`,
			want: "requires catsco.artifact-manifest.v4",
		},
		{
			name: "duplicate namespace",
			body: `{"contract_version":"catsco.artifact-manifest.v4","runtime":{"version":"0.1","surfaces":[{"id":"main"}],"state":[{"namespace":"data","mode":"read-write"},{"namespace":"data","mode":"read-write"}]}}`,
			want: "invalid or duplicated",
		},
		{
			name: "read only mode",
			body: `{"contract_version":"catsco.artifact-manifest.v4","runtime":{"version":"0.1","surfaces":[{"id":"main"}],"state":[{"namespace":"data","mode":"read-only"}]}}`,
			want: "invalid or duplicated",
		},
		{
			name: "duplicate runtime field",
			body: `{"contract_version":"catsco.artifact-manifest.v4","runtime":{"version":"0.1","version":"0.2","surfaces":[{"id":"main"}],"state":[{"namespace":"data","mode":"read-write"}]}}`,
			want: "duplicate field",
		},
		{
			name: "null surface title",
			body: `{"contract_version":"catsco.artifact-manifest.v4","runtime":{"version":"0.1","surfaces":[{"id":"main","title":null}],"state":[{"namespace":"data","mode":"read-write"}]}}`,
			want: "title is invalid",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := parseArtifactRuntimeManifest([]byte(test.body))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want substring %q", err, test.want)
			}
		})
	}
}
