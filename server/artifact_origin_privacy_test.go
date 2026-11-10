package server

import "testing"

func TestArtifactPrivacyCanonicalDefaultPort(t *testing.T) {
	for _, configured := range []string{"https://artifact.example", "https://artifact.example:443"} {
		t.Run(configured, func(t *testing.T) {
			t.Setenv("CATSCO_ARTIFACT_FILE_ORIGINS", configured+",https://[::1]:443")
			for _, origin := range []string{"https://artifact.example", "https://ARTIFACT.example:443", "https://artifact.example:0443"} {
				want := "artifact:https://artifact.example/artifacts/demo"
				if got := canonicalArtifactResource(origin + "/artifacts/demo/v1/"); got != want {
					t.Fatalf("canonical %s: %q, want %q", origin, got, want)
				}
				if got, ok := ArtifactResourceFromPath(origin, "/artifacts/demo/v1/app.js"); !ok || got != want {
					t.Fatalf("callback %s: %q, %v", origin, got, ok)
				}
			}
			if got := canonicalArtifactResource("https://artifact.example:19991/artifacts/demo/v1/"); got != "" {
				t.Fatal("unconfigured non-default port was trusted")
			}
			if got := canonicalArtifactResource("https://[::1]/artifacts/demo/v1/"); got != "artifact:https://[::1]/artifacts/demo" {
				t.Fatalf("IPv6 origin: %q", got)
			}
		})
	}
}

func TestArtifactPrivacyDirectTemplateDefaultPort(t *testing.T) {
	t.Setenv("CATSCO_PUBLIC_BASE_URL", "https://chat.example")
	t.Setenv("CATSCO_ARTIFACT_FILE_ORIGINS", "")
	t.Setenv("CATSCO_ARTIFACT_NODES_JSON", "")
	t.Setenv("CATSCO_ARTIFACT_NODES_FILE", "")
	t.Setenv("CATSCO_DIRECT_ARTIFACT_URL_TEMPLATE", "https://agent-{uid}.artifacts.example:443/artifacts")
	if got := canonicalArtifactResource("https://agent-42.artifacts.example/artifacts/demo/v1/"); got != "artifact:https://agent-42.artifacts.example/artifacts/demo" {
		t.Fatalf("template default port: %q", got)
	}
	if got := canonicalArtifactResource("https://agent-0.artifacts.example/artifacts/demo/v1/"); got != "" {
		t.Fatal("invalid template actor trusted")
	}
}
