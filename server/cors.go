package server

import (
	"net/http"
	"net/url"
	"os"
	"strings"
)

const allowedCORSHeaders = "Content-Type, Authorization, " + rawUploadFileNameHeader + ", " + rawUploadFileSizeHeader

func CORSMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := strings.TrimSpace(r.Header.Get("Origin"))
		allowOrigin := "https://app.catsco.cc"
		if origin != "" && isAllowedCORSOrigin(origin) {
			allowOrigin = origin
		}
		w.Header().Set("Access-Control-Allow-Origin", allowOrigin)
		w.Header().Add("Vary", "Origin")
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

func isAllowedCORSOrigin(origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil {
		return false
	}
	allowed := map[string]struct{}{
		"https://app.catsco.cc": {},
		"https://app.catsco.cn": {},
	}
	for _, raw := range strings.Split(os.Getenv("CATSCO_ALLOWED_CORS_ORIGINS"), ",") {
		candidate := strings.TrimRight(strings.TrimSpace(raw), "/")
		if candidate != "" {
			allowed[candidate] = struct{}{}
		}
	}
	_, ok := allowed[strings.TrimRight(origin, "/")]
	return ok
}
