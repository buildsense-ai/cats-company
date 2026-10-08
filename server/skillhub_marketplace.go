package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const marketplacePrefix = "/api/skillhub/marketplace"

// Presentation access is independent of selected Bots and device routing.
// The production route MUST use OwnerMiddlewareWithDB (active human JWT).
type SkillHubMarketplaceHandler struct {
	proxy                  *SkillHubProxyHandler
	enabled, writesEnabled bool
	cookieName             string
}

type SkillHubMarketplaceOptions struct {
	Enabled           bool
	WritesEnabled     bool
	SessionCookieName string
}

func NewSkillHubMarketplaceHandler(proxy *SkillHubProxyHandler, opts SkillHubMarketplaceOptions) *SkillHubMarketplaceHandler {
	name := opts.SessionCookieName
	if name == "" {
		name = "catsco_session"
	}
	return &SkillHubMarketplaceHandler{proxy: proxy, enabled: opts.Enabled, writesEnabled: opts.WritesEnabled, cookieName: name}
}

func NewSkillHubMarketplaceHandlerFromEnv(proxy *SkillHubProxyHandler) *SkillHubMarketplaceHandler {
	enabled := func(key string) bool {
		v := strings.TrimSpace(os.Getenv(key))
		return v == "1" || strings.EqualFold(v, "true")
	}
	return NewSkillHubMarketplaceHandler(proxy, SkillHubMarketplaceOptions{
		Enabled:           enabled("CATSCO_SKILLHUB_MARKETPLACE_ENABLED"),
		WritesEnabled:     enabled("CATSCO_SKILLHUB_MARKETPLACE_WRITES_ENABLED"),
		SessionCookieName: strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_SESSION_COOKIE_NAME")),
	})
}

type marketplaceRoute struct {
	upstream string
	methods  string
	private  bool
	image    bool
	upload   bool
	queries  []string
}

var marketplaceAssetPath = regexp.MustCompile(`^/assets/(pa_[a-f0-9]{32})(/preview)?$`)

func resolveMarketplaceRoute(path string) (marketplaceRoute, bool) {
	switch path {
	case "/catalogue/categories":
		return marketplaceRoute{upstream: "/api/catalogue/categories", methods: "GET"}, true
	case "/catalogue/skills":
		return marketplaceRoute{upstream: "/api/catalogue/skills", methods: "GET", queries: []string{"q", "category", "platform", "agent_version", "search_mode", "limit", "cursor"}}, true
	case "/presentations":
		return marketplaceRoute{upstream: "/api/skill-presentations", methods: "GET", queries: []string{"skillId", "version"}}, true
	case "/presentations/draft":
		return marketplaceRoute{upstream: "/api/skill-presentations/draft", methods: "GET, PUT", private: true, queries: []string{"skillId", "version"}}, true
	case "/presentations/publish", "/presentations/unpublish":
		return marketplaceRoute{upstream: "/api/skill-presentations" + strings.TrimPrefix(path, "/presentations"), methods: "POST", private: true}, true
	case "/assets":
		return marketplaceRoute{upstream: "/api/skill-presentations/assets", methods: "GET, POST", private: true, upload: true, queries: []string{"skillId", "version"}}, true
	}
	if match := marketplaceAssetPath.FindStringSubmatch(path); match != nil {
		methods := "GET, DELETE"
		if match[2] != "" {
			methods = "GET"
		}
		return marketplaceRoute{upstream: "/api/skill-presentations" + path, methods: methods, private: match[2] != "", image: true}, true
	}
	return marketplaceRoute{}, false
}

func marketplaceError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Cache-Control", "private, no-store")
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": "SkillHub presentation request could not be completed."}})
}

// Handle never accepts query-string credentials, caller cookies, Bot UID,
// upstream URL, or a client-supplied identity. Only explicit allowlisted paths
// are mapped; legacy SkillHub routes remain untouched.
func (h *SkillHubMarketplaceHandler) Handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "private, no-store")
	uid := UIDFromContext(r.Context())
	token := strings.TrimSpace(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	if uid <= 0 || !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || token == "" {
		marketplaceError(w, http.StatusUnauthorized, "marketplace.human_login_required")
		return
	}
	if !strings.HasPrefix(r.URL.Path, marketplacePrefix+"/") || r.URL.RawPath != "" {
		marketplaceError(w, http.StatusNotFound, "marketplace.route_not_found")
		return
	}
	path := strings.TrimPrefix(r.URL.Path, marketplacePrefix)
	if path == "/capabilities" && r.Method == http.MethodGet {
		writeJSON(w, http.StatusOK, map[string]any{"schemaVersion": 1, "enabled": h.enabled, "writesEnabled": h.enabled && h.writesEnabled})
		return
	}
	route, ok := resolveMarketplaceRoute(path)
	if !ok {
		marketplaceError(w, http.StatusNotFound, "marketplace.route_not_found")
		return
	}
	if !strings.Contains(", "+route.methods+", ", ", "+r.Method+", ") {
		w.Header().Set("Allow", route.methods)
		marketplaceError(w, http.StatusMethodNotAllowed, "marketplace.method_not_allowed")
		return
	}
	write := r.Method != http.MethodGet
	if !h.enabled || (write && !h.writesEnabled) {
		marketplaceError(w, http.StatusServiceUnavailable, "marketplace.disabled")
		return
	}
	if h.proxy == nil || h.proxy.configError != nil || h.proxy.baseURL == nil || h.proxy.baseURL.User != nil {
		marketplaceError(w, http.StatusServiceUnavailable, "marketplace.unavailable")
		return
	}
	query := url.Values{}
	if !write {
		for _, key := range route.queries {
			values := r.URL.Query()[key]
			if len(values) > 1 || (len(values) == 1 && len(values[0]) > 8192) {
				marketplaceError(w, http.StatusBadRequest, "marketplace.query_invalid")
				return
			}
			if len(values) == 1 {
				query.Set(key, values[0])
			}
		}
	}
	var payload []byte
	if write {
		mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "application/json" {
			marketplaceError(w, http.StatusUnsupportedMediaType, "presentation.content_type")
			return
		}
		limit := int64(176 << 10)
		if route.upload && r.Method == http.MethodPost {
			limit = 3 << 20
		}
		payload, err = readLimited(r.Body, limit)
		if err != nil {
			marketplaceError(w, http.StatusRequestEntityTooLarge, "body.too_large")
			return
		}
		if !json.Valid(payload) {
			marketplaceError(w, http.StatusBadRequest, "body.invalid_json")
			return
		}
	}
	// Never inherit a cookie jar or redirect policy from the legacy public proxy.
	// Even a same-origin redirect could forward a temporary editor credential to
	// an unrelated endpoint. No mutation retry: a lost response may have committed.
	client := *h.proxy.client
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	var cookie *http.Cookie
	if route.private || write {
		var err error
		cookie, err = h.exchange(ctx, &client, uid, token)
		if cookie != nil {
			defer h.revoke(&client, cookie)
		}
		if err != nil {
			marketplaceError(w, http.StatusBadGateway, "marketplace.identity_exchange_failed")
			return
		}
	}
	response, err := h.do(ctx, &client, r.Method, route.upstream, query, payload, cookie)
	if err != nil {
		marketplaceError(w, http.StatusBadGateway, "marketplace.upstream_unavailable")
		return
	}
	defer response.Body.Close()
	limit := h.proxy.maxResponseSize
	if route.image && !write {
		limit = 2 << 20
	}
	body, err := readLimited(response.Body, limit)
	if err != nil {
		marketplaceError(w, http.StatusBadGateway, "marketplace.response_invalid")
		return
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		forwardMarketplaceError(w, response.StatusCode, body)
		return
	}
	if route.image && !write {
		mediaType, _, _ := mime.ParseMediaType(response.Header.Get("Content-Type"))
		if mediaType != "image/webp" || len(body) < 12 || string(body[:4]) != "RIFF" || string(body[8:12]) != "WEBP" {
			marketplaceError(w, http.StatusBadGateway, "marketplace.image_invalid")
			return
		}
		w.Header().Set("Content-Type", "image/webp")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; sandbox")
		w.Header().Set("Content-Disposition", "inline; filename=skill-presentation.webp")
	} else {
		if !json.Valid(body) {
			marketplaceError(w, http.StatusBadGateway, "marketplace.response_invalid")
			return
		}
		w.Header().Set("Content-Type", "application/json")
	}
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.WriteHeader(response.StatusCode)
	_, _ = w.Write(body)
}

func (h *SkillHubMarketplaceHandler) do(ctx context.Context, client *http.Client, method, path string, query url.Values, payload []byte, cookie *http.Cookie) (*http.Response, error) {
	target := *h.proxy.baseURL
	target.Path = strings.TrimRight(target.Path, "/") + path
	target.RawPath = ""
	target.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, method, target.String(), bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, image/webp")
	req.Header.Set("User-Agent", "catsco-skillhub-marketplace/1.0")
	if len(payload) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	return client.Do(req)
}

func (h *SkillHubMarketplaceHandler) exchange(ctx context.Context, client *http.Client, uid int64, token string) (*http.Cookie, error) {
	payload, _ := json.Marshal(map[string]string{"token": token})
	response, err := h.do(ctx, client, http.MethodPost, "/api/auth/catsco-exchange", nil, payload, nil)
	if err != nil {
		return nil, errors.New("identity exchange unavailable")
	}
	defer response.Body.Close()
	// Hold a temporary cookie only in this request, never in a cross-account
	// cache/browser cookie. Return it even on invalid bodies so cleanup can run.
	var cookie *http.Cookie
	for _, candidate := range response.Cookies() {
		if candidate.Name == h.cookieName && candidate.Value != "" && candidate.HttpOnly {
			cookie = &http.Cookie{Name: candidate.Name, Value: candidate.Value}
		}
	}
	body, err := readLimited(response.Body, 64<<10)
	if err != nil || response.StatusCode != http.StatusOK || cookie == nil {
		return cookie, errors.New("identity exchange failed")
	}
	var result struct {
		CatsCo struct {
			UID json.RawMessage `json:"uid"`
		} `json:"catsCo"`
	}
	if json.Unmarshal(body, &result) != nil {
		return cookie, errors.New("invalid identity")
	}
	identity := strings.Trim(string(result.CatsCo.UID), "\"")
	if identity != strconv.FormatInt(uid, 10) {
		return cookie, errors.New("identity mismatch")
	}
	return cookie, nil
}

func (h *SkillHubMarketplaceHandler) revoke(client *http.Client, cookie *http.Cookie) {
	// Request cancellation must not skip credential cleanup. This is bounded and
	// does not replay the user's write or report a committed write as failed.
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	response, err := h.do(ctx, client, http.MethodPost, "/api/auth/logout", nil, []byte(`{}`), cookie)
	if err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		_ = response.Body.Close()
	}
	if err != nil || response.StatusCode != http.StatusOK {
		// No JWT, cookie, upstream body or URL in the warning. The credential
		// is discarded locally even if upstream revocation is unavailable.
		log.Print("skillhub-marketplace: temporary session cleanup failed")
	}
}

func forwardMarketplaceError(w http.ResponseWriter, status int, body []byte) {
	switch status {
	case 400, 401, 403, 404, 409, 413, 415, 429, 503:
	default:
		status = http.StatusBadGateway
	}
	var envelope struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &envelope)
	code := "marketplace.upstream_error"
	// Preserve actionable codes, not arbitrary upstream messages/HTML/secrets.
	switch envelope.Error.Code {
	case "presentation.disabled", "presentation.writes_disabled", "presentation.images_disabled", "presentation.schema_unavailable", "presentation.assets_schema_unavailable", "presentation.image_processor_unavailable",
		"presentation.invalid", "presentation.not_found", "presentation.route_not_found", "presentation.revision_conflict", "presentation.draft_required", "presentation.content_type", "presentation.image_invalid", "presentation.image_busy", "presentation.asset_not_found", "presentation.asset_quota", "presentation.asset_in_use",
		"catalogue.cursor_stale", "catalogue.cursor_filters", "catalogue.limit_invalid", "body.too_large", "body.invalid_json", "body.empty", "auth_error":
		code = envelope.Error.Code
	}
	marketplaceError(w, status, code)
}
