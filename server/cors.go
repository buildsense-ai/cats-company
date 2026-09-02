package server

import (
	"net/http"
	"net/url"
	"strings"
)

const allowedCORSHeaders = "Content-Type, Authorization, " + rawUploadFileNameHeader + ", " + rawUploadFileSizeHeader

// The .cc origin remains the canonical public address.  .cn is an equivalent
// compatibility entry point while the domain migration is in progress.
var allowedCORSOrigins = map[string]struct{}{
	"https://app.catsco.cc": {},
	"https://app.catsco.cn": {},
}

func normalizeCORSOrigin(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.User != nil || parsed.Scheme == "" || parsed.Host == "" ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if scheme != "https" || host == "" {
		return ""
	}
	port := parsed.Port()
	if port != "" && port != "443" {
		return ""
	}
	return scheme + "://" + host
}

func isAllowedCORSOrigin(raw string) (string, bool) {
	origin := normalizeCORSOrigin(raw)
	if origin == "" {
		return "", false
	}
	if _, ok := allowedCORSOrigins[origin]; !ok {
		return "", false
	}
	return origin, true
}

func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Origin")
		if origin, ok := isAllowedCORSOrigin(r.Header.Get("Origin")); ok {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", allowedCORSHeaders)
		w.Header().Set("Access-Control-Allow-Credentials", "true")

		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusOK)
			return
		}

		next.ServeHTTP(w, r)
	})
}
