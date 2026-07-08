package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func resetCatsCoReleaseCacheForTest() {
	catsCoReleaseCache.Lock()
	catsCoReleaseCache.entry = catsCoReleaseCacheEntry{}
	catsCoReleaseCache.Unlock()
}

func TestCatsCoDesktopReleasesReadsUpdaterManifests(t *testing.T) {
	resetCatsCoReleaseCacheForTest()
	t.Cleanup(resetCatsCoReleaseCacheForTest)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest.yml":
			_, _ = w.Write([]byte("version: 1.5.0\nfiles:\n  - url: CatsCo-1.5.0-win.exe\n"))
		case "/macos-arm64/latest-mac.yml":
			_, _ = w.Write([]byte("version: 1.5.0\nfiles:\n  - url: CatsCo-1.5.0-mac-arm64.dmg\n"))
		case "/macos-x64/latest-mac.yml":
			_, _ = w.Write([]byte("version: 1.5.0\nfiles:\n  - url: CatsCo-1.5.0-mac-x64.dmg\n"))
		case "/latest-linux.yml":
			_, _ = w.Write([]byte("version: 1.5.0\nfiles:\n  - url: CatsCo-1.5.0-linux.AppImage\n  - url: CatsCo-1.5.0-linux.deb\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	t.Setenv("CATSCO_DESKTOP_RELEASE_BASE_URL", upstream.URL)

	req := httptest.NewRequest(http.MethodGet, "/api/catsco/desktop-releases", nil)
	rec := httptest.NewRecorder()
	HandleCatsCoDesktopReleases(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp CatsCoDesktopReleasesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Version != "1.5.0" || resp.Source != "manifest" {
		t.Fatalf("version/source=%q/%q, want manifest 1.5.0", resp.Version, resp.Source)
	}
	want := map[string]string{
		"windows":        upstream.URL + "/CatsCo-1.5.0-win.exe",
		"mac-arm":        upstream.URL + "/macos-arm64/CatsCo-1.5.0-mac-arm64.dmg",
		"mac-intel":      upstream.URL + "/macos-x64/CatsCo-1.5.0-mac-x64.dmg",
		"linux-appimage": upstream.URL + "/CatsCo-1.5.0-linux.AppImage",
		"linux-deb":      upstream.URL + "/CatsCo-1.5.0-linux.deb",
	}
	for key, href := range want {
		if resp.Downloads[key] != href {
			t.Fatalf("download %s=%q, want %q", key, resp.Downloads[key], href)
		}
	}
}

func TestCatsCoDesktopReleasesFallsBackWhenManifestUnavailable(t *testing.T) {
	resetCatsCoReleaseCacheForTest()
	t.Cleanup(resetCatsCoReleaseCacheForTest)

	upstream := httptest.NewServer(http.NotFoundHandler())
	defer upstream.Close()
	t.Setenv("CATSCO_DESKTOP_RELEASE_BASE_URL", upstream.URL)

	req := httptest.NewRequest(http.MethodGet, "/api/catsco/desktop-releases", nil)
	rec := httptest.NewRecorder()
	HandleCatsCoDesktopReleases(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	var resp CatsCoDesktopReleasesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Version != "1.4.1" || resp.Source != "fallback" {
		t.Fatalf("version/source=%q/%q, want fallback 1.4.1", resp.Version, resp.Source)
	}
	if resp.Downloads["windows"] != upstream.URL+"/CatsCo-1.4.1-win.exe" {
		t.Fatalf("fallback windows=%q", resp.Downloads["windows"])
	}
}
