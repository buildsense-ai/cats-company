package server

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	catsCoDesktopReleaseDefaultBaseURL  = "https://github-release.tos-cn-guangzhou.volces.com/update"
	catsCoDesktopReleaseFallbackVersion = "1.4.1"
	catsCoReleaseManifestMaxBytes       = 64 * 1024
	catsCoReleaseFetchTimeout           = 5 * time.Second
	catsCoReleaseCacheTTL               = 5 * time.Minute
)

type CatsCoDesktopReleasesResponse struct {
	Name      string            `json:"name"`
	Version   string            `json:"version"`
	Downloads map[string]string `json:"downloads"`
	Source    string            `json:"source"`
}

type catsCoReleaseSpec struct {
	Key          string
	ManifestPath string
	Directory    string
	Suffix       string
}

type catsCoReleaseManifest struct {
	Version string
	Files   []string
}

type catsCoReleaseCacheEntry struct {
	BaseURL   string
	Response  CatsCoDesktopReleasesResponse
	ExpiresAt time.Time
}

var catsCoReleaseCache struct {
	sync.Mutex
	entry catsCoReleaseCacheEntry
}

var catsCoReleaseSpecs = []catsCoReleaseSpec{
	{Key: "windows", ManifestPath: "latest.yml", Suffix: "-win.exe"},
	{Key: "mac-arm", ManifestPath: "macos-arm64/latest-mac.yml", Directory: "macos-arm64", Suffix: "-mac-arm64.dmg"},
	{Key: "mac-intel", ManifestPath: "macos-x64/latest-mac.yml", Directory: "macos-x64", Suffix: "-mac-x64.dmg"},
	{Key: "linux-appimage", ManifestPath: "latest-linux.yml", Suffix: "-linux.AppImage"},
	{Key: "linux-deb", ManifestPath: "latest-linux.yml", Suffix: "-linux.deb"},
}

func HandleCatsCoDesktopReleases(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	baseURL := catsCoDesktopReleaseBaseURL()
	response := cachedCatsCoDesktopReleases(r.Context(), baseURL)
	w.Header().Set("Cache-Control", "public, max-age=300")
	writeJSON(w, http.StatusOK, response)
}

func cachedCatsCoDesktopReleases(ctx context.Context, baseURL string) CatsCoDesktopReleasesResponse {
	now := time.Now()
	catsCoReleaseCache.Lock()
	entry := catsCoReleaseCache.entry
	if entry.BaseURL == baseURL && now.Before(entry.ExpiresAt) {
		catsCoReleaseCache.Unlock()
		return entry.Response
	}
	catsCoReleaseCache.Unlock()

	response := buildCatsCoDesktopReleases(ctx, baseURL)

	catsCoReleaseCache.Lock()
	catsCoReleaseCache.entry = catsCoReleaseCacheEntry{
		BaseURL:   baseURL,
		Response:  response,
		ExpiresAt: now.Add(catsCoReleaseCacheTTL),
	}
	catsCoReleaseCache.Unlock()
	return response
}

func buildCatsCoDesktopReleases(ctx context.Context, baseURL string) CatsCoDesktopReleasesResponse {
	response := fallbackCatsCoDesktopReleases(baseURL)
	if baseURL == "" {
		return response
	}

	ctx, cancel := context.WithTimeout(ctx, catsCoReleaseFetchTimeout)
	defer cancel()

	client := &http.Client{Timeout: catsCoReleaseFetchTimeout}
	manifests := map[string]catsCoReleaseManifest{}
	for _, spec := range catsCoReleaseSpecs {
		if _, ok := manifests[spec.ManifestPath]; ok {
			continue
		}
		manifest, err := fetchCatsCoReleaseManifest(ctx, client, joinReleaseURL(baseURL, "", spec.ManifestPath))
		if err != nil {
			continue
		}
		manifests[spec.ManifestPath] = manifest
		if response.Source != "manifest" && strings.TrimSpace(manifest.Version) != "" {
			response.Version = strings.TrimSpace(manifest.Version)
			response.Source = "manifest"
		}
	}

	for _, spec := range catsCoReleaseSpecs {
		manifest, ok := manifests[spec.ManifestPath]
		if !ok {
			continue
		}
		if file := pickCatsCoReleaseFile(manifest.Files, spec.Suffix); file != "" {
			response.Downloads[spec.Key] = joinReleaseURL(baseURL, spec.Directory, file)
			response.Source = "manifest"
		}
		if response.Version == "" && strings.TrimSpace(manifest.Version) != "" {
			response.Version = strings.TrimSpace(manifest.Version)
		}
	}

	return response
}

func fallbackCatsCoDesktopReleases(baseURL string) CatsCoDesktopReleasesResponse {
	version := firstNonEmpty(os.Getenv("CATSCO_DESKTOP_FALLBACK_VERSION"), catsCoDesktopReleaseFallbackVersion)
	return CatsCoDesktopReleasesResponse{
		Name:    "CatsCo",
		Version: version,
		Source:  "fallback",
		Downloads: map[string]string{
			"windows":        joinReleaseURL(baseURL, "", "CatsCo-"+version+"-win.exe"),
			"mac-arm":        joinReleaseURL(baseURL, "macos-arm64", "CatsCo-"+version+"-mac-arm64.dmg"),
			"mac-intel":      joinReleaseURL(baseURL, "macos-x64", "CatsCo-"+version+"-mac-x64.dmg"),
			"linux-appimage": joinReleaseURL(baseURL, "", "CatsCo-"+version+"-linux.AppImage"),
			"linux-deb":      joinReleaseURL(baseURL, "", "CatsCo-"+version+"-linux.deb"),
		},
	}
}

func catsCoDesktopReleaseBaseURL() string {
	raw := firstNonEmpty(os.Getenv("CATSCO_DESKTOP_RELEASE_BASE_URL"), catsCoDesktopReleaseDefaultBaseURL)
	parsed, err := url.Parse(strings.TrimRight(strings.TrimSpace(raw), "/"))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return catsCoDesktopReleaseDefaultBaseURL
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return catsCoDesktopReleaseDefaultBaseURL
	}
	return strings.TrimRight(parsed.String(), "/")
}

func fetchCatsCoReleaseManifest(ctx context.Context, client *http.Client, manifestURL string) (catsCoReleaseManifest, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return catsCoReleaseManifest{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return catsCoReleaseManifest{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return catsCoReleaseManifest{}, http.ErrNoLocation
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, catsCoReleaseManifestMaxBytes))
	if err != nil {
		return catsCoReleaseManifest{}, err
	}
	return parseCatsCoReleaseManifest(string(body)), nil
}

func parseCatsCoReleaseManifest(raw string) catsCoReleaseManifest {
	var manifest catsCoReleaseManifest
	for _, line := range strings.Split(raw, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "version:") && manifest.Version == "" {
			manifest.Version = cleanManifestScalar(strings.TrimPrefix(trimmed, "version:"))
			continue
		}
		if strings.HasPrefix(trimmed, "- url:") {
			if file := cleanManifestScalar(strings.TrimPrefix(trimmed, "- url:")); safeReleaseFileName(file) {
				manifest.Files = append(manifest.Files, file)
			}
			continue
		}
		if strings.HasPrefix(trimmed, "url:") {
			if file := cleanManifestScalar(strings.TrimPrefix(trimmed, "url:")); safeReleaseFileName(file) {
				manifest.Files = append(manifest.Files, file)
			}
		}
	}
	return manifest
}

func cleanManifestScalar(value string) string {
	return strings.Trim(strings.TrimSpace(value), `"'`)
}

func pickCatsCoReleaseFile(files []string, suffix string) string {
	for _, file := range files {
		if strings.HasSuffix(file, suffix) {
			return file
		}
	}
	return ""
}

func safeReleaseFileName(file string) bool {
	return file != "" && !strings.Contains(file, "://") && !strings.HasPrefix(file, "/") && !strings.Contains(file, "..")
}

func joinReleaseURL(baseURL string, directory string, file string) string {
	if baseURL == "" || !safeReleaseFileName(file) {
		return ""
	}
	path := strings.Trim(file, "/")
	if directory = strings.Trim(directory, "/"); directory != "" {
		path = directory + "/" + path
	}
	return strings.TrimRight(baseURL, "/") + "/" + path
}
