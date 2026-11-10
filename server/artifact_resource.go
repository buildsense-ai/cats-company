package server

import (
	"net"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
)

// Artifact keys include origin and namespace. IDs alone are not globally unique.
func canonicalArtifactResource(raw string) string {
	raw = strings.TrimPrefix(strings.TrimSpace(raw), "artifact:")
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawPath != "" || parsed.Opaque != "" {
		return ""
	}
	root, ok := artifactResourceRoot(parsed.Path)
	if !ok || !artifactFileOriginAllowed(parsed) {
		return ""
	}
	return "artifact:" + artifactFileOrigin(parsed) + root
}

// Match browser/worker URL.origin, including explicit HTTPS default ports.
func artifactFileOrigin(u *url.URL) string {
	if u == nil || u.Scheme != "https" || u.User != nil || u.Hostname() == "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	port := u.Port()
	if port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number <= 0 || number > 65535 {
			return ""
		}
		if number != 443 {
			return "https://" + net.JoinHostPort(host, strconv.Itoa(number))
		}
	}
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	return "https://" + host
}

func artifactResourceRoot(p string) (string, bool) {
	if strings.ContainsAny(p, "\\%?#\x00\r\n") {
		return "", false
	}
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(p, "/"), "/"), "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", false
		}
	}
	start := 0
	if len(parts) > 0 && parts[0] == "artifacts" {
		start = 1
	}
	if len(parts) <= start {
		return "", false
	}
	if parts[start] == "by-agent" {
		if len(parts) <= start+2 {
			return "", false
		}
		uid, err := strconv.ParseInt(parts[start+1], 10, 64)
		if err != nil || uid <= 0 || strconv.FormatInt(uid, 10) != parts[start+1] {
			return "", false
		}
		start += 2
	}
	if !validArtifactID(parts[start]) || parts[start] == "artifacts-index.json" || parts[start] == "by-agent" {
		return "", false
	}
	// Require an artifact namespace. A root /id path is only meaningful on a
	// configured dedicated node; deployments use /artifacts or /by-agent.
	if parts[0] != "artifacts" && parts[0] != "by-agent" {
		return "", false
	}
	return "/" + strings.Join(parts[:start+1], "/"), true
}

func artifactFileOriginAllowed(u *url.URL) bool {
	origin := artifactFileOrigin(u)
	if origin == "" {
		return false
	}
	bases := strings.Split(os.Getenv("CATSCO_ARTIFACT_FILE_ORIGINS"), ",")
	bases = append(bases, firstNonEmpty(strings.TrimSpace(os.Getenv("CLOUD_ARTIFACTS_INDEX_URL")), defaultArtifactIndexURL))
	for _, raw := range bases {
		base, err := url.Parse(strings.TrimSpace(raw))
		if err == nil && artifactFileOrigin(base) == origin {
			return true
		}
	}
	if registry, err := loadArtifactNodeRegistryFromEnv(); err == nil && registry != nil {
		for _, node := range registry.nodes {
			base, err := url.Parse(node.publicBaseURL)
			if err == nil && artifactFileOrigin(base) == origin && strings.HasPrefix(u.Path, strings.TrimRight(base.Path, "/")+"/") {
				return true
			}
		}
	}
	if template, err := loadArtifactDirectURLTemplateFromEnv(); err == nil && template != nil {
		const placeholder = "catscoartifactuidplaceholder"
		base, err := url.Parse(strings.Replace(template.value, artifactDirectUIDToken, placeholder, 1))
		if err != nil {
			return false
		}
		pattern := "^" + strings.Replace(regexp.QuoteMeta(artifactFileOrigin(base)+base.Path), placeholder, "[1-9][0-9]*", 1) + "(?:/|$)"
		matched, _ := regexp.MatchString(pattern, origin+u.Path)
		if matched {
			return true
		}
	}
	return false
}

// Only canonical URLs under configured artifact origins are recognized in text.
// All new messages still undergo source-use validation before persistence.
var artifactMessageURLPattern = regexp.MustCompile(`https://[^\s<>"'` + "`" + `()\[\]{}]+`)

func messageArtifactURLs(text string) []string {
	out := []string{}
	for _, candidate := range artifactMessageURLPattern.FindAllString(text, -1) {
		candidate = strings.TrimRight(candidate, ",;。")
		if key := canonicalArtifactResource(candidate); key != "" {
			out = append(out, key)
		}
	}
	return out
}
