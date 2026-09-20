package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
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

// signedIdentityValue builds a current v1 cookie value the way Issue does, so
// tests can create expired or tampered variants.
func signedIdentityValue(uid int64, exp time.Time, username string) string {
	return signIdentityPayload(identityPayload(uid, strconv.FormatInt(exp.Unix(), 10), username))
}

// legacySignedIdentityValue builds the pre-upgrade "uid:exp" value, which the
// read path has to keep accepting until those cookies expire.
func legacySignedIdentityValue(uid int64, exp time.Time) string {
	return signIdentityPayload(strconv.FormatInt(uid, 10) + ":" + strconv.FormatInt(exp.Unix(), 10))
}

// identityPayload renders an unsigned v1 payload, and signIdentityPayload
// attaches the signature, so tests can assemble values the read path must
// reject on its own checks.
func identityPayload(uid int64, expUnix, username string) string {
	return artifactIdentityPayloadVersion + ":" + strconv.FormatInt(uid, 10) + ":" + expUnix + ":" + url.QueryEscape(username)
}

func signIdentityPayload(payload string) string {
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
	config.Issue(recorder, identityHostRequest("app.catsco.cc"), 363, "saturday")

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
	// The value must be v1:uid:exp:username plus a signature, never the platform
	// credential.
	expires := mustExpiry(t, cookie.Value)
	parts := strings.Split(cookie.Value, ".")
	wantPayload := artifactIdentityPayloadVersion + ":363:" + strconv.FormatInt(expires.Unix(), 10) + ":saturday"
	if len(parts) != 2 || parts[0] != wantPayload {
		t.Errorf("unexpected value shape %q", cookie.Value)
	}
	if strings.Contains(cookie.Value, "oc_token") {
		t.Error("the platform credential must never be placed in this cookie")
	}
	uid, exp, username, ok := config.readCookie(identityRequest("app.catsco.cc", cookie.Value))
	if !ok || uid != 363 || username != "saturday" {
		t.Fatalf("the issued cookie must verify, got uid=%d username=%q ok=%v", uid, username, ok)
	}
	if !exp.Equal(expires) {
		t.Errorf("decoded expiry = %v, want %v", exp, expires)
	}
}

// mustExpiry reads the expiry out of either payload format.
func mustExpiry(t *testing.T, value string) time.Time {
	t.Helper()
	payload, _, _ := strings.Cut(value, ".")
	fields := strings.Split(payload, ":")
	expPart := ""
	switch len(fields) {
	case 2:
		expPart = fields[1]
	case 4:
		expPart = fields[2]
	}
	if expPart == "" {
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
	config.Issue(first, identityHostRequest("app.catsco.cc"), 363, "saturday")
	issued := first.Result().Cookies()
	if len(issued) != 1 {
		t.Fatalf("first issue produced %d cookies", len(issued))
	}

	request := identityHostRequest("app.catsco.cc")
	request.AddCookie(issued[0])
	again := httptest.NewRecorder()
	config.Issue(again, request, 363, "saturday")
	if len(again.Result().Cookies()) != 0 {
		t.Error("a still-fresh cookie must not be reissued")
	}

	// Nearly expired: it must be refreshed.
	stale := identityHostRequest("app.catsco.cc")
	stale.AddCookie(&http.Cookie{
		Name:  artifactIdentityCookieName,
		Value: signedIdentityValue(363, time.Now().Add(config.TTL/4), "saturday"),
	})
	refreshed := httptest.NewRecorder()
	config.Issue(refreshed, stale, 363, "saturday")
	if len(refreshed.Result().Cookies()) != 1 {
		t.Error("a cookie close to expiry must be reissued")
	}

	// A legacy cookie is still valid, so a fresh one must not be reissued either.
	legacy := identityHostRequest("app.catsco.cc")
	legacy.AddCookie(&http.Cookie{
		Name:  artifactIdentityCookieName,
		Value: legacySignedIdentityValue(363, time.Now().Add(config.TTL)),
	})
	kept := httptest.NewRecorder()
	config.Issue(kept, legacy, 363, "saturday")
	if len(kept.Result().Cookies()) != 0 {
		t.Error("a fresh legacy cookie must be left alone until it reaches the renewal point")
	}

	// A different account on the same browser must replace the identity at once,
	// even though the existing cookie is still fresh.
	switched := identityHostRequest("app.catsco.cc")
	switched.AddCookie(issued[0])
	reissued := httptest.NewRecorder()
	config.Issue(reissued, switched, 441, "other")
	replacement := reissued.Result().Cookies()
	if len(replacement) != 1 {
		t.Fatal("a different uid must be written immediately, not after the renewal point")
	}
	uid, _, username, ok := config.readCookie(identityRequest("app.catsco.cc", replacement[0].Value))
	if !ok || uid != 441 || username != "other" {
		t.Fatalf("the reissued cookie must carry the new identity, got uid=%d username=%q ok=%v", uid, username, ok)
	}

	foreign := httptest.NewRecorder()
	config.Issue(foreign, identityHostRequest("example.com"), 363, "saturday")
	if len(foreign.Result().Cookies()) != 0 {
		t.Error("a host outside the configured domains must not receive the cookie")
	}
}

func TestArtifactIdentityRejectsTamperedAndExpiredCookies(t *testing.T) {
	config := testIdentityConfig()
	expUnix := strconv.FormatInt(time.Now().Add(time.Hour).Unix(), 10)
	valid := signedIdentityValue(363, time.Now().Add(time.Hour), "saturday")

	// Re-signing the payload with a different account, or with different
	// content, must fail the signature check.
	_, signature, _ := strings.Cut(valid, ".")
	bad := map[string]string{
		"broken signature":        strings.Replace(valid, signature, signature[:len(signature)-1]+"0", 1),
		"uid swapped":             identityPayload(364, expUnix, "saturday") + "." + signature,
		"username swapped":        identityPayload(363, expUnix, "other") + "." + signature,
		"forged":                  "364:9999999999.deadbeef",
		"empty":                   "",
		"no separator":            "noseparator",
		"non-positive uid":        signIdentityPayload(artifactIdentityPayloadVersion + ":0:" + expUnix + ":saturday"),
		"expired":                 signIdentityPayload(identityPayload(363, strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10), "saturday")),
		"legacy non-positive uid": signIdentityPayload("0:" + expUnix),
		"legacy expired":          signIdentityPayload("363:" + strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10)),
	}
	for name, value := range bad {
		if _, _, _, ok := config.readCookie(identityRequest("app.catsco.cc", value)); ok {
			t.Errorf("%s (%q) must not verify", name, value)
		}
	}

	uid, gotExp, username, ok := config.readCookie(identityRequest("app.catsco.cc", valid))
	if !ok || uid != 363 || username != "saturday" || gotExp.Before(time.Now()) {
		t.Errorf("a valid cookie must verify, got uid=%d username=%q exp=%v ok=%v", uid, username, gotExp, ok)
	}
}

func TestArtifactIdentityReadsBothPayloadFormats(t *testing.T) {
	config := testIdentityConfig()
	exp := time.Now().Add(time.Hour).Truncate(time.Second)
	expUnix := strconv.FormatInt(exp.Unix(), 10)

	legacy := legacySignedIdentityValue(363, exp)
	uid, gotExp, username, ok := config.readCookie(identityRequest("app.catsco.cc", legacy))
	if !ok || uid != 363 || username != "" || !gotExp.Equal(exp) {
		t.Errorf("legacy payload decoded to uid=%d exp=%v username=%q ok=%v", uid, gotExp, username, ok)
	}

	current := signedIdentityValue(363, exp, "saturday")
	uid, gotExp, username, ok = config.readCookie(identityRequest("app.catsco.cc", current))
	if !ok || uid != 363 || username != "saturday" || !gotExp.Equal(exp) {
		t.Errorf("v1 payload decoded to uid=%d exp=%v username=%q ok=%v", uid, gotExp, username, ok)
	}

	// An empty username is a legitimate v1 payload: the field is written empty
	// rather than dropped, because the field count identifies the format.
	empty := signedIdentityValue(363, exp, "")
	if _, _, username, ok := config.readCookie(identityRequest("app.catsco.cc", empty)); !ok || username != "" {
		t.Errorf("an empty username must round-trip, got %q ok=%v", username, ok)
	}

	// Shape malformations must be rejected even though the signature is real.
	unexpected := map[string]string{
		"three fields":       signIdentityPayload(artifactIdentityPayloadVersion + ":363:" + expUnix),
		"two non-numeric":    signIdentityPayload("v1:" + expUnix),
		"missing uid":        signIdentityPayload(artifactIdentityPayloadVersion + ":" + expUnix + ":saturday"),
		"unknown version":    signIdentityPayload("v2:363:" + expUnix + ":saturday"),
		"too many fields":    signIdentityPayload(identityPayload(363, expUnix, "saturday") + ":extra"),
		"broken url escape":  signIdentityPayload(artifactIdentityPayloadVersion + ":363:" + expUnix + ":%zz"),
		"legacy extra field": signIdentityPayload("363:" + expUnix + ":saturday"),
	}
	for name, value := range unexpected {
		if _, _, _, ok := config.readCookie(identityRequest("app.catsco.cc", value)); ok {
			t.Errorf("%s (%q) must not verify", name, value)
		}
	}
}

func TestArtifactIdentityRoundTripsUsernamesWithSeparators(t *testing.T) {
	config := testIdentityConfig()
	// A ':' inside the username must not shift the field boundaries, and any
	// reserved character must survive the escape/unescape pair unchanged.
	// The dot matters most: it is the payload/signature separator and
	// QueryEscape leaves it alone, so an unescaped one silently truncated every
	// cookie signed for a dotted account name.
	for _, username := range []string{"saturday", "team:alpha", "a:b:c", "空格", "up+down", "per%cent", "john.doe", "a.b.c"} {
		recorder := httptest.NewRecorder()
		config.Issue(recorder, identityHostRequest("app.catsco.cc"), 363, username)
		cookies := recorder.Result().Cookies()
		if len(cookies) != 1 {
			t.Fatalf("username %q produced %d cookies", username, len(cookies))
		}
		payload, _, _ := strings.Cut(cookies[0].Value, ".")
		if strings.Count(payload, ":") != 3 {
			t.Errorf("username %q leaked a separator into the payload %q", username, payload)
		}
		uid, _, got, ok := config.readCookie(identityRequest("app.catsco.cc", cookies[0].Value))
		if !ok || uid != 363 || got != username {
			t.Errorf("username %q decoded to %q (uid=%d ok=%v)", username, got, uid, ok)
		}
	}
}

func TestArtifactIdentityHandlerRequiresTheSharedToken(t *testing.T) {
	config := testIdentityConfig()
	handler := &ArtifactIdentityHandler{config: config}
	valid := signedIdentityValue(363, time.Now().Add(time.Hour), "saturday")

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

	// The lookup reports the username of a v1 cookie and leaves the field empty
	// for a legacy cookie that never carried one.
	for name, testCase := range map[string]struct {
		value        string
		wantUsername string
	}{
		"v1":     {valid, `"username":"saturday"`},
		"legacy": {legacySignedIdentityValue(363, time.Now().Add(time.Hour)), `"username":""`},
	} {
		recorder := httptest.NewRecorder()
		handler.HandleIdentity(recorder, withBearer(identityRequest("app.catsco.cc", testCase.value), config.Token))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s lookup = %d (%s)", name, recorder.Code, recorder.Body.String())
		}
		for _, want := range []string{`"authenticated":true`, `"uid":363`, `"expires_at"`, testCase.wantUsername} {
			if !strings.Contains(recorder.Body.String(), want) {
				t.Errorf("%s body %s is missing %s", name, recorder.Body.String(), want)
			}
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
	MaybeIssueArtifactIdentityCookie(off, identityHostRequest("app.catsco.cc"), 363, "saturday")
	if len(off.Result().Cookies()) != 0 {
		t.Fatal("an unconfigured process must not set the cookie")
	}

	ConfigureArtifactIdentity(testIdentityConfig())
	on := httptest.NewRecorder()
	MaybeIssueArtifactIdentityCookie(on, identityHostRequest("app.catsco.cc"), 363, "saturday")
	cookies := on.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("a configured process must set the cookie")
	}
	if _, _, username, ok := artifactIdentity.readCookie(identityRequest("app.catsco.cc", cookies[0].Value)); !ok || username != "saturday" {
		t.Fatalf("the cookie written through the middleware must carry the username, got %q ok=%v", username, ok)
	}
}
