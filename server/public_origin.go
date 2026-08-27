package server

import (
	"errors"
	"net/url"
	"os"
	"strings"
)

// configuredPublicBaseURL returns the canonical public HTTPS origin for this
// deployment. Callers should treat an error as an absent/invalid configuration
// rather than deriving a public URL from request headers.
func configuredPublicBaseURL() (string, error) {
	raw := strings.TrimSpace(os.Getenv("CATSCO_PUBLIC_BASE_URL"))
	parsed, err := url.Parse(raw)
	if err != nil || raw == "" || parsed.Scheme != "https" || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" ||
		(parsed.Path != "" && parsed.Path != "/") {
		return "", errors.New("CATSCO_PUBLIC_BASE_URL must be a public HTTPS origin")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}
