package server

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

type fileMediaTestStore struct {
	store.Store
	store.FileAccessStore
	users       map[int64]*types.User
	records     map[string]store.FileMediaSession
	unavailable bool
	apiKey      string
}

func (s *fileMediaTestStore) GetUser(uid int64) (*types.User, error) { return s.users[uid], nil }
func (s *fileMediaTestStore) GetBotByAPIKey(key string) (int64, error) {
	if key == s.apiKey {
		return 9, nil
	}
	return 0, errors.New("invalid key")
}
func (s *fileMediaTestStore) CreateFileMediaSession(_ context.Context, record store.FileMediaSession) error {
	if s.unavailable {
		return errors.New("offline")
	}
	s.records[record.TokenHash] = record
	return nil
}
func (s *fileMediaTestStore) GetFileMediaSession(_ context.Context, hash string) (*store.FileMediaSession, error) {
	if s.unavailable {
		return nil, errors.New("offline")
	}
	record, ok := s.records[hash]
	if !ok {
		return nil, nil
	}
	return &record, nil
}
func (s *fileMediaTestStore) RevokeFileMediaSessions(_ context.Context, parent string) error {
	if s.unavailable {
		return errors.New("offline")
	}
	for hash, record := range s.records {
		if record.ParentHash == parent || hash == parent {
			delete(s.records, hash)
		}
	}
	return nil
}
func newFileMediaTest(t *testing.T) (*fileMediaTestStore, *FileMediaAuth, string, string) {
	t.Helper()
	db := &fileMediaTestStore{users: map[int64]*types.User{
		7: {ID: 7, Username: "alice", AccountType: types.AccountHuman},
		8: {ID: 8, Username: "bob", AccountType: types.AccountHuman},
		9: {ID: 9, Username: "bot", AccountType: types.AccountBot},
	}, records: make(map[string]store.FileMediaSession), apiKey: GenerateAPIKey(9)}
	alice, err := GenerateToken(7, "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	bob, err := GenerateToken(8, "bob", "")
	if err != nil {
		t.Fatal(err)
	}
	return db, NewFileMediaAuth(db, NewHub(db, nil)), alice, bob
}
func fileMediaMutate(a *FileMediaAuth, method, auth, origin string, cookie *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, "https://chat.example/api/files/media-session", nil)
	req.Header.Set("Authorization", auth)
	req.Header.Set("Origin", origin)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	a.HandleSession(rec, req)
	return rec
}
func fileMediaIssued(t *testing.T, rec *httptest.ResponseRecorder) *http.Cookie {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("session status=%d body=%s", rec.Code, rec.Body.String())
	}
	for _, cookie := range rec.Result().Cookies() {
		if cookie.Name == fileMediaCookieName && cookie.MaxAge > 0 {
			return cookie
		}
	}
	t.Fatal("missing media cookie")
	return nil
}
func fileMediaRead(a *FileMediaAuth, auth string, cookie *http.Cookie) (int, int64) {
	req := httptest.NewRequest(http.MethodGet, "https://chat.example/uploads/file.png", nil)
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	if cookie != nil {
		req.AddCookie(cookie)
	}
	uid := int64(0)
	rec := httptest.NewRecorder()
	a.Middleware(func(w http.ResponseWriter, r *http.Request) {
		uid = UIDFromContext(r.Context())
		w.WriteHeader(http.StatusNoContent)
	})(rec, req)
	return rec.Code, uid
}

func TestFileMediaSessionLifetimeHashesAndActiveActor(t *testing.T) {
	db, a, alice, _ := newFileMediaTest(t)
	// A persistent parent still yields only a 15-minute media credential.
	persistent, err := GeneratePersistentUserToken(7, "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, parent := range []string{alice, persistent} {
		before := time.Now()
		cookie := fileMediaIssued(t, fileMediaMutate(a, http.MethodPost, "Bearer "+parent, "https://chat.example", nil))
		if !cookie.HttpOnly || !cookie.Secure || cookie.Domain != "" || cookie.Path != "/" || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != 900 {
			t.Fatalf("unsafe cookie: %+v", cookie)
		}
		record, ok := db.records[fileMediaHash(cookie.Value)]
		if !ok || record.TokenHash == cookie.Value || record.ParentHash != fileMediaHash(parent) || record.ActorUID != 7 {
			t.Fatalf("bad persisted hashes: %+v", record)
		}
		if record.ExpiresAt.Before(before.Add(15*time.Minute)) || record.ExpiresAt.After(time.Now().Add(15*time.Minute)) {
			t.Fatalf("bad lifetime: %s", record.ExpiresAt)
		}
		if status, uid := fileMediaRead(a, "", cookie); status != 204 || uid != 7 {
			t.Fatalf("read %d/%d", status, uid)
		}
		db.users[7].State = 1
		if status, _ := fileMediaRead(a, "", cookie); status != 403 {
			t.Fatalf("disabled actor read=%d", status)
		}
		db.users[7].State = 0
		record.ExpiresAt = time.Now().Add(-time.Second)
		db.records[record.TokenHash] = record
		if status, _ := fileMediaRead(a, "", cookie); status != 401 {
			t.Fatalf("expired read=%d", status)
		}
	}
}

func TestFileMediaSessionOutlivesParentJWTOnlyWithinMediaTTL(t *testing.T) {
	db, a, _, _ := newFileMediaTest(t)
	// Expiration of the parent is not a second, hidden media-session clock.
	claims := JWTClaims{UID: 7, RegisteredClaims: jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Hour))}}
	expired, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	mediaToken := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	db.records[fileMediaHash(mediaToken)] = store.FileMediaSession{TokenHash: fileMediaHash(mediaToken), ActorUID: 7, ParentHash: fileMediaHash(expired), ExpiresAt: time.Now().Add(time.Minute)}
	if status, uid := fileMediaRead(a, "", &http.Cookie{Name: fileMediaCookieName, Value: mediaToken}); status != 204 || uid != 7 {
		t.Fatalf("media clock %d/%d", status, uid)
	}
	if status, _ := fileMediaRead(a, "Bearer "+expired, &http.Cookie{Name: fileMediaCookieName, Value: mediaToken}); status != 401 {
		t.Fatalf("expired bearer downgraded=%d", status)
	}
}

func TestFileMediaSessionCredentialPrecedenceAndSwitch(t *testing.T) {
	db, a, alice, bob := newFileMediaTest(t)
	cookie := fileMediaIssued(t, fileMediaMutate(a, "POST", "Bearer "+alice, "https://chat.example", nil))
	for _, tc := range []struct {
		auth   string
		status int
		uid    int64
	}{
		{"Bearer invalid", 401, 0}, {"Bearer ", 401, 0}, {"Basic garbage", 401, 0},
		{"Bearer " + bob, 204, 8}, {"ApiKey " + db.apiKey, 204, 9}, {"ApiKey invalid", 401, 0},
	} {
		if status, uid := fileMediaRead(a, tc.auth, cookie); status != tc.status || uid != tc.uid {
			t.Fatalf("precedence %q=%d/%d", tc.auth, status, uid)
		}
	}
	db.users[8].State = 1
	if status, _ := fileMediaRead(a, "Bearer "+bob, cookie); status != 403 {
		t.Fatalf("disabled header downgraded=%d", status)
	}
	db.users[8].State = 0
	switched := fileMediaIssued(t, fileMediaMutate(a, "POST", "Bearer "+bob, "https://chat.example", cookie))
	if status, _ := fileMediaRead(a, "", cookie); status != 401 {
		t.Fatalf("old account retained=%d", status)
	}
	if status, uid := fileMediaRead(a, "", switched); status != 204 || uid != 8 {
		t.Fatalf("new account=%d/%d", status, uid)
	}
}

func TestFileMediaSessionDeleteRevokesParentWithoutCookie(t *testing.T) {
	_, a, alice, _ := newFileMediaTest(t)
	first := fileMediaIssued(t, fileMediaMutate(a, "POST", "Bearer "+alice, "https://chat.example", nil))
	second := fileMediaIssued(t, fileMediaMutate(a, "POST", "Bearer "+alice, "https://chat.example", nil))
	rec := fileMediaMutate(a, "DELETE", "Bearer "+alice, "https://chat.example", nil)
	if rec.Code != 204 {
		t.Fatalf("delete=%d", rec.Code)
	}
	for _, cookie := range []*http.Cookie{first, second} {
		if status, _ := fileMediaRead(a, "", cookie); status != 401 {
			t.Fatalf("revoked session=%d", status)
		}
	}
	if cookies := rec.Result().Cookies(); len(cookies) != 1 || cookies[0].MaxAge != -1 {
		t.Fatalf("delete cookie=%v", cookies)
	}
}

func TestFileMediaSessionCSRFAndFailureClosed(t *testing.T) {
	db, a, alice, _ := newFileMediaTest(t)
	for _, method := range []string{"POST", "DELETE"} {
		for _, origin := range []string{"", "null", "https://evil.example", "http://chat.example", "https://chat.example:443", "https://chat.example/", "https://chat.example.evil", "https://chat.example?x"} {
			if rec := fileMediaMutate(a, method, "Bearer "+alice, origin, nil); rec.Code != 403 {
				t.Fatalf("origin %q %s=%d", origin, method, rec.Code)
			}
		}
	}
	cookie := fileMediaIssued(t, fileMediaMutate(a, "POST", "Bearer "+alice, "https://chat.example", nil))
	if rec := fileMediaMutate(a, "POST", "", "https://chat.example", cookie); rec.Code != 401 {
		t.Fatalf("cookie minted session=%d", rec.Code)
	}
	db.unavailable = true
	if status, _ := fileMediaRead(a, "", cookie); status != 503 {
		t.Fatalf("offline read=%d", status)
	}
	if rec := fileMediaMutate(a, "POST", "Bearer "+alice, "https://chat.example", cookie); rec.Code != 503 {
		t.Fatalf("offline create=%d", rec.Code)
	}
	if status, _ := fileMediaRead(NewFileMediaAuth(nil, nil), "Bearer "+alice, nil); status != 503 {
		t.Fatalf("nil store=%d", status)
	}
	noSessions := NewFileMediaAuth(&deviceHandlerStore{users: db.users}, nil)
	if status, _ := fileMediaRead(noSessions, "", cookie); status != 503 {
		t.Fatalf("missing session store=%d", status)
	}
	if rec := fileMediaMutate(noSessions, "POST", "Bearer "+alice, "https://chat.example", nil); rec.Code != 503 {
		t.Fatalf("missing create store=%d", rec.Code)
	}
}

func TestFileMediaLateLogoutCannotClearNewAccount(t *testing.T) {
	_, a, alice, bob := newFileMediaTest(t)
	first := fileMediaIssued(t, fileMediaMutate(a, "POST", "Bearer "+alice, "https://chat.example", nil))
	second := fileMediaIssued(t, fileMediaMutate(a, "POST", "Bearer "+bob, "https://chat.example", first))
	rec := fileMediaMutate(a, "DELETE", "Bearer "+alice, "https://chat.example", second)
	if rec.Code != 204 || len(rec.Result().Cookies()) != 0 {
		t.Fatalf("late delete status=%d cookies=%v", rec.Code, rec.Result().Cookies())
	}
	if status, uid := fileMediaRead(a, "", second); status != 204 || uid != 8 {
		t.Fatalf("late logout changed current actor=%d/%d", status, uid)
	}
}

func TestFileMediaAPIKeySessionAndNoQueryCredentials(t *testing.T) {
	db, a, alice, _ := newFileMediaTest(t)
	cookie := fileMediaIssued(t, fileMediaMutate(a, "POST", "ApiKey "+db.apiKey, "https://chat.example", nil))
	if status, uid := fileMediaRead(a, "", cookie); status != 204 || uid != 9 {
		t.Fatalf("api key cookie=%d/%d", status, uid)
	}
	for _, query := range []string{"token=" + alice, "api_key=" + db.apiKey} {
		req := httptest.NewRequest("GET", "https://chat.example/uploads/a?"+query, nil)
		rec := httptest.NewRecorder()
		a.Middleware(func(w http.ResponseWriter, r *http.Request) { t.Error("query credential authenticated") })(rec, req)
		if rec.Code != 401 {
			t.Fatalf("query status=%d", rec.Code)
		}
	}
	db.users[9].State = 1
	if status, _ := fileMediaRead(a, "", cookie); status != 403 {
		t.Fatalf("disabled API actor=%d", status)
	}
}
