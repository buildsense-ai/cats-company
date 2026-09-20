package server

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

func testIdentityConfig() ArtifactIdentityConfig {
	return ArtifactIdentityConfig{
		OptIn:   true,
		Domains: []string{".catsco.cc", ".catsco.cn"},
		TTL:     time.Hour,
		Secure:  true,
		Token:   "test-control-token-0123456789abcdef",
	}
}

// signedIdentityValue builds a cookie value the way Issue does, so tests can
// create expired or tampered variants.
func signedIdentityValue(uid int64, exp time.Time) string {
	payload := strconv.FormatInt(uid, 10) + ":" + strconv.FormatInt(exp.Unix(), 10)
	return payload + "." + artifactIdentitySign(payload)
}

func identityHostRequest(host string) *http.Request {
	request := httptest.NewRequest(http.MethodGet, "/api/artifacts/identity", nil)
	request.Host = host
	return request
}

func identityRequest(host, cookie string) *http.Request {
	request := identityHostRequest(host)
	if cookie != "" {
		request.AddCookie(&http.Cookie{Name: artifactIdentityCookieName, Value: cookie})
	}
	return request
}

// normalizeDomain strips the leading dot so both ".catsco.cc" (as written) and
// "catsco.cc" (as Go parses it back) compare equal.
func normalizeDomain(value string) string { return strings.TrimPrefix(value, ".") }

func TestArtifactIdentityConfigDefaultsAndEnablement(t *testing.T) {
	t.Setenv("CATSCO_ARTIFACT_IDENTITY_ENABLED", "")
	t.Setenv("CATSCO_ARTIFACT_IDENTITY_DOMAINS", "")
	t.Setenv("CATSCO_ARTIFACT_GATEWAY_TOKEN", "")
	t.Setenv("CATSCO_ARTIFACT_IDENTITY_TTL", "")
	config := ArtifactIdentityConfigFromEnv()
	if config.Enabled() {
		t.Fatal("nothing configured must stay off")
	}
	if len(config.Domains) != 2 || config.Domains[0] != ".catsco.cc" || config.Domains[1] != ".catsco.cn" {
		t.Fatalf("default domains = %v, want both public domains", config.Domains)
	}
	if !config.Secure {
		t.Fatal("Secure must default to true")
	}

	// A shared token alone must not switch a cross-subdomain identity cookie on:
	// that token is also configured for the unrelated launch endpoint.
	t.Setenv("CATSCO_ARTIFACT_GATEWAY_TOKEN", "token")
	if ArtifactIdentityConfigFromEnv().Enabled() {
		t.Fatal("the shared token alone must not enable the identity cookie")
	}

	t.Setenv("CATSCO_ARTIFACT_IDENTITY_ENABLED", "1")
	if !ArtifactIdentityConfigFromEnv().Enabled() {
		t.Fatal("with the explicit opt-in and the token the feature must be enabled")
	}
	t.Setenv("CATSCO_ARTIFACT_IDENTITY_DOMAINS", ".example.test")
	t.Setenv("CATSCO_ARTIFACT_IDENTITY_TTL", "30m")
	configured := ArtifactIdentityConfigFromEnv()
	if len(configured.Domains) != 1 || configured.Domains[0] != ".example.test" || configured.TTL != 30*time.Minute {
		t.Fatalf("configured = %v / %v", configured.Domains, configured.TTL)
	}
	t.Setenv("CATSCO_ARTIFACT_IDENTITY_TTL", "nonsense")
	if ArtifactIdentityConfigFromEnv().TTL != artifactIdentityDefaultTTL {
		t.Fatal("a bad duration must fall back to the default")
	}
}

func TestArtifactIdentityPicksTheDomainForTheRequestHost(t *testing.T) {
	config := testIdentityConfig()
	cases := map[string]string{
		"app.catsco.cc":     ".catsco.cc",
		"app.catsco.cc:443": ".catsco.cc",
		"APP.CATSCO.CC":     ".catsco.cc",
		"app.catsco.cn":     ".catsco.cn",
		"catsco.cc":         ".catsco.cc",
		"example.com":       "",
		"":                  "",
	}
	for host, want := range cases {
		if got := config.domainForHost(host); got != want {
			t.Errorf("domainForHost(%q) = %q, want %q", host, got, want)
		}
	}
}

func TestArtifactIdentityIssueSetsANarrowCookie(t *testing.T) {
	config := testIdentityConfig()
	recorder := httptest.NewRecorder()
	config.Issue(recorder, identityHostRequest("app.catsco.cc"), 363)

	cookies := recorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("cookies = %d, want exactly 1", len(cookies))
	}
	cookie := cookies[0]
	if cookie.Name != artifactIdentityCookieName {
		t.Errorf("name = %q", cookie.Name)
	}
	if normalizeDomain(cookie.Domain) != "catsco.cc" {
		t.Errorf("domain = %q, want a catsco.cc domain cookie", cookie.Domain)
	}
	if !cookie.HttpOnly || !cookie.Secure || cookie.SameSite != http.SameSiteLaxMode || cookie.Path != "/" {
		t.Errorf("attributes = HttpOnly:%v Secure:%v SameSite:%v Path:%q",
			cookie.HttpOnly, cookie.Secure, cookie.SameSite, cookie.Path)
	}
	if !strings.Contains(recorder.Header().Get("Set-Cookie"), "Domain=") {
		t.Error("the response must actually carry a Domain attribute, or the cookie cannot reach the Artifact origin")
	}
	if cookie.MaxAge != int(time.Hour.Seconds()) {
		t.Errorf("MaxAge = %d", cookie.MaxAge)
	}
	// The value must be uid:exp plus a signature, never the platform credential.
	parts := strings.Split(cookie.Value, ".")
	if len(parts) != 2 || parts[0] != strconv.FormatInt(363, 10)+":"+strconv.FormatInt(mustExpiry(t, cookie.Value).Unix(), 10) {
		t.Errorf("unexpected value shape %q", cookie.Value)
	}
	if strings.Contains(cookie.Value, "oc_token") {
		t.Error("the platform credential must never be placed in this cookie")
	}
	if _, _, ok := config.readCookie(identityRequest("app.catsco.cc", cookie.Value)); !ok {
		t.Fatal("the issued cookie must verify")
	}
}

func mustExpiry(t *testing.T, value string) time.Time {
	t.Helper()
	payload, _, _ := strings.Cut(value, ".")
	_, expPart, ok := strings.Cut(payload, ":")
	if !ok {
		t.Fatalf("malformed payload %q", payload)
	}
	expUnix, err := strconv.ParseInt(expPart, 10, 64)
	if err != nil {
		t.Fatalf("bad expiry in %q", payload)
	}
	return time.Unix(expUnix, 0)
}

func TestArtifactIdentityIssueSkipsFreshCookiesAndForeignHosts(t *testing.T) {
	config := testIdentityConfig()

	first := httptest.NewRecorder()
	config.Issue(first, identityHostRequest("app.catsco.cc"), 363)
	issued := first.Result().Cookies()
	if len(issued) != 1 {
		t.Fatalf("first issue produced %d cookies", len(issued))
	}

	request := identityHostRequest("app.catsco.cc")
	request.AddCookie(issued[0])
	again := httptest.NewRecorder()
	config.Issue(again, request, 363)
	if len(again.Result().Cookies()) != 0 {
		t.Error("a still-fresh cookie must not be reissued")
	}

	// Nearly expired: it must be refreshed.
	stale := identityHostRequest("app.catsco.cc")
	stale.AddCookie(&http.Cookie{
		Name:  artifactIdentityCookieName,
		Value: signedIdentityValue(363, time.Now().Add(config.TTL/4)),
	})
	refreshed := httptest.NewRecorder()
	config.Issue(refreshed, stale, 363)
	if len(refreshed.Result().Cookies()) != 1 {
		t.Error("a cookie close to expiry must be reissued")
	}

	// A different account on the same browser must replace the identity at once,
	// even though the existing cookie is still fresh.
	switched := identityHostRequest("app.catsco.cc")
	switched.AddCookie(issued[0])
	reissued := httptest.NewRecorder()
	config.Issue(reissued, switched, 441)
	replacement := reissued.Result().Cookies()
	if len(replacement) != 1 {
		t.Fatal("a different uid must be written immediately, not after the renewal point")
	}
	uid, _, ok := config.readCookie(identityRequest("app.catsco.cc", replacement[0].Value))
	if !ok || uid != 441 {
		t.Fatalf("the reissued cookie must carry the new uid, got uid=%d ok=%v", uid, ok)
	}

	foreign := httptest.NewRecorder()
	config.Issue(foreign, identityHostRequest("example.com"), 363)
	if len(foreign.Result().Cookies()) != 0 {
		t.Error("a host outside the configured domains must not receive the cookie")
	}
}

func TestArtifactIdentityRejectsTamperedAndExpiredCookies(t *testing.T) {
	config := testIdentityConfig()
	valid := signedIdentityValue(363, time.Now().Add(time.Hour))

	payload, signature, _ := strings.Cut(valid, ".")
	swapped := strings.Replace(payload, "363:", "364:", 1) + "." + signature
	bad := map[string]string{
		"broken signature": strings.Replace(valid, signature, signature[:len(signature)-1]+"0", 1),
		"uid swapped":      swapped,
		"forged":           "364:9999999999.deadbeef",
		"empty":            "",
		"no separator":     "noseparator",
		"non-positive uid": "0:" + strings.Split(payload, ":")[1] + "." + artifactIdentitySign("0:"+strings.Split(payload, ":")[1]),
		"expired":          signedIdentityValue(363, time.Now().Add(-time.Minute)),
	}
	for name, value := range bad {
		if _, _, ok := config.readCookie(identityRequest("app.catsco.cc", value)); ok {
			t.Errorf("%s (%q) must not verify", name, value)
		}
	}

	uid, exp, ok := config.readCookie(identityRequest("app.catsco.cc", valid))
	if !ok || uid != 363 || exp.Before(time.Now()) {
		t.Errorf("a valid cookie must verify, got uid=%d exp=%v ok=%v", uid, exp, ok)
	}
}

func TestArtifactIdentityHandlerRequiresTheSharedToken(t *testing.T) {
	config := testIdentityConfig()
	handler := &ArtifactIdentityHandler{config: config}
	valid := signedIdentityValue(363, time.Now().Add(time.Hour))

	post := httptest.NewRecorder()
	handler.HandleIdentity(post, httptest.NewRequest(http.MethodPost, "/api/artifacts/identity", nil))
	if post.Code != http.StatusMethodNotAllowed {
		t.Errorf("method guard = %d", post.Code)
	}

	for name, request := range map[string]*http.Request{
		"no token":    identityRequest("app.catsco.cc", valid),
		"wrong token": withBearer(identityRequest("app.catsco.cc", valid), strings.Repeat("a", 33)),
	} {
		recorder := httptest.NewRecorder()
		handler.HandleIdentity(recorder, request)
		if recorder.Code != http.StatusForbidden {
			t.Errorf("%s = %d, want 403", name, recorder.Code)
		}
	}

	noCookie := httptest.NewRecorder()
	handler.HandleIdentity(noCookie, withBearer(identityRequest("app.catsco.cc", ""), config.Token))
	if noCookie.Code != http.StatusUnauthorized {
		t.Errorf("without a cookie = %d, want 401", noCookie.Code)
	}

	ok := httptest.NewRecorder()
	handler.HandleIdentity(ok, withBearer(identityRequest("app.catsco.cc", valid), config.Token))
	if ok.Code != http.StatusOK {
		t.Fatalf("valid lookup = %d (%s)", ok.Code, ok.Body.String())
	}
	for _, want := range []string{`"authenticated":true`, `"uid":363`, `"expires_at"`} {
		if !strings.Contains(ok.Body.String(), want) {
			t.Errorf("body %s is missing %s", ok.Body.String(), want)
		}
	}
}

func withBearer(request *http.Request, token string) *http.Request {
	request.Header.Set("Authorization", "Bearer "+token)
	return request
}

func TestArtifactIdentityHandlerUnavailableWhenUnconfigured(t *testing.T) {
	handler := &ArtifactIdentityHandler{config: ArtifactIdentityConfig{}}
	if handler.Enabled() {
		t.Fatal("a zero config must be disabled")
	}
	recorder := httptest.NewRecorder()
	handler.HandleIdentity(recorder, identityHostRequest("app.catsco.cc"))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured = %d, want 503", recorder.Code)
	}
}

func TestMaybeIssueArtifactIdentityCookieStaysOffWhenUnconfigured(t *testing.T) {
	previous := artifactIdentity
	defer func() { artifactIdentity = previous }()

	ConfigureArtifactIdentity(ArtifactIdentityConfig{})
	off := httptest.NewRecorder()
	MaybeIssueArtifactIdentityCookie(off, identityHostRequest("app.catsco.cc"), 363)
	if len(off.Result().Cookies()) != 0 {
		t.Fatal("an unconfigured process must not set the cookie")
	}

	ConfigureArtifactIdentity(testIdentityConfig())
	on := httptest.NewRecorder()
	MaybeIssueArtifactIdentityCookie(on, identityHostRequest("app.catsco.cc"), 363)
	if len(on.Result().Cookies()) != 1 {
		t.Fatal("a configured process must set the cookie")
	}
}
