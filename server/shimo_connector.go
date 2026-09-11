package server

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	shimoConnectorAudience = "catsco-shimo-connector"
	shimoConnectorIssuer   = "catscompany"
	shimoConnectorRevision = "catsco-shimo-mvp-1"
	defaultShimoAttemptTTL = 5 * time.Minute
	maxShimoActorTokenTTL  = 10 * time.Minute
	maxShimoRequestBytes   = 1 << 20
	defaultShimoResumeTTL  = 20 * time.Minute
)

var shimoCellRangePattern = regexp.MustCompile(`^[A-Z]{1,3}[1-9][0-9]{0,6}:[A-Z]{1,3}[1-9][0-9]{0,6}$`)

// ShimoActorClaims is a short-lived capability issued for one trusted CatsCo
// chat actor and virtual employee. It is never accepted from a request body.
type ShimoActorClaims struct {
	AgentUID   string `json:"agent_uid"`
	ActorUID   string `json:"actor_user_id"`
	TaskRef    string `json:"task_ref,omitempty"`
	SkillID    string `json:"skill_id"`
	Capability string `json:"capability"`
	jwt.RegisteredClaims
}

type shimoActor struct {
	agentUID string
	actorUID string
	taskRef  string
}

type shimoConnectionKey struct {
	agentUID string
	actorUID string
}

type shimoConnection struct {
	state       string
	accountHint string
	verifiedAt  time.Time
	attemptID   string
}

type shimoLoginAttempt struct {
	id        string
	key       shimoConnectionKey
	taskRef   string
	tokenHash string
	expiresAt time.Time
	used      bool
	// claimed marks the window in which a request has taken the one-time
	// login link and is starting the isolated browser session. It is set
	// under h.mu before StartLogin runs so concurrent requests cannot open a
	// second session or deliver a duplicate resume, and it is rolled back if
	// StartLogin fails so the user can retry the same link.
	claimed bool
}

type shimoLoginResume struct {
	key       shimoConnectionKey
	taskRef   string
	topicID   string
	messageID int64
	tokenHash string
	expiresAt time.Time
}

// ShimoLoginResume identifies the trusted live CatsCo turn that should resume
// after an isolated Shimo browser session has been saved.
type ShimoLoginResume struct {
	AgentUID  int64
	ActorUID  int64
	TopicID   string
	MessageID int64
}

type ShimoConnectorBackend interface {
	ListSheets(ctx context.Context, actor shimoActor, sourceURL string) (map[string]any, error)
	ReadSheet(ctx context.Context, actor shimoActor, sourceURL, sheetName, cellRange string) (map[string]any, error)
	ReadDocument(ctx context.Context, actor shimoActor, sourceURL string, maxChars int) (map[string]any, error)
}

type ShimoConnectorOptions struct {
	ActorSecret []byte
	PublicURL   string
	AttemptTTL  time.Duration
	Revision    string
	Backend     ShimoConnectorBackend
	WorkerToken string
	Now         func() time.Time
}

type ShimoConnectorHandler struct {
	actorSecret     []byte
	publicURL       string
	attemptTTL      time.Duration
	revision        string
	backend         ShimoConnectorBackend
	workerToken     string
	now             func() time.Time
	resumePublisher func(ShimoLoginResume) bool

	mu          sync.Mutex
	connections map[shimoConnectionKey]shimoConnection
	attempts    map[string]*shimoLoginAttempt
	resumes     map[string]*shimoLoginResume
}

func NewShimoConnectorHandler(options ShimoConnectorOptions) *ShimoConnectorHandler {
	attemptTTL := options.AttemptTTL
	if attemptTTL <= 0 || attemptTTL > 15*time.Minute {
		attemptTTL = defaultShimoAttemptTTL
	}
	revision := strings.TrimSpace(options.Revision)
	if revision == "" {
		revision = shimoConnectorRevision
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &ShimoConnectorHandler{
		actorSecret: append([]byte(nil), options.ActorSecret...),
		publicURL:   strings.TrimRight(strings.TrimSpace(options.PublicURL), "/"),
		attemptTTL:  attemptTTL,
		revision:    revision,
		backend:     options.Backend,
		workerToken: strings.TrimSpace(options.WorkerToken),
		now:         now,
		connections: make(map[shimoConnectionKey]shimoConnection),
		attempts:    make(map[string]*shimoLoginAttempt),
		resumes:     make(map[string]*shimoLoginResume),
	}
}

func NewShimoConnectorHandlerFromEnv() *ShimoConnectorHandler {
	var backend ShimoConnectorBackend
	if envBoolValue("CATSCO_SHIMO_MOCK_ENABLED") {
		backend = mockShimoConnectorBackend{}
	} else if strings.TrimSpace(os.Getenv("CATSCO_SHIMO_WORKER_URL")) != "" || strings.TrimSpace(os.Getenv("CATSCO_SHIMO_WORKER_TOKEN")) != "" {
		worker, err := newHTTPShimoWorkerBackend(
			os.Getenv("CATSCO_SHIMO_WORKER_URL"),
			os.Getenv("CATSCO_SHIMO_WORKER_TOKEN"),
			os.Getenv("CATSCO_SHIMO_WORKER_CALLBACK_URL"),
			[]byte(strings.TrimSpace(os.Getenv("CATSCO_SHIMO_ACTOR_SECRET"))),
		)
		if err == nil {
			backend = worker
		}
	}
	return NewShimoConnectorHandler(ShimoConnectorOptions{
		ActorSecret: []byte(strings.TrimSpace(os.Getenv("CATSCO_SHIMO_ACTOR_SECRET"))),
		PublicURL:   strings.TrimSpace(os.Getenv("CATSCO_SHIMO_PUBLIC_BASE_URL")),
		Revision:    strings.TrimSpace(os.Getenv("CATSCO_SHIMO_CONNECTOR_REVISION")),
		Backend:     backend,
		WorkerToken: strings.TrimSpace(os.Getenv("CATSCO_SHIMO_WORKER_TOKEN")),
	})
}

// SetLoginResumePublisher installs the delivery hook after the Hub exists.
func (h *ShimoConnectorHandler) SetLoginResumePublisher(publisher func(ShimoLoginResume) bool) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.resumePublisher = publisher
	h.mu.Unlock()
}

// GenerateShimoActorToken creates the per-invocation capability consumed by
// the connector. Callers must pass identities already verified by CatsCo.
func GenerateShimoActorToken(secret []byte, agentUID, actorUID, taskRef, skillID string, ttl time.Duration) (string, error) {
	if len(secret) < 32 {
		return "", errors.New("shimo actor secret must contain at least 32 bytes")
	}
	agentUID = strings.TrimSpace(agentUID)
	actorUID = strings.TrimSpace(actorUID)
	taskRef = strings.TrimSpace(taskRef)
	skillID = strings.TrimSpace(skillID)
	if agentUID == "" || actorUID == "" || taskRef == "" || skillID == "" {
		return "", errors.New("agent_uid, actor_user_id, task_ref, and skill_id are required")
	}
	if ttl <= 0 || ttl > maxShimoActorTokenTTL {
		return "", errors.New("shimo actor token ttl must be between zero and ten minutes")
	}
	now := time.Now().UTC()
	jti, err := shimoRandomHex(16)
	if err != nil {
		return "", err
	}
	claims := ShimoActorClaims{
		AgentUID:   agentUID,
		ActorUID:   actorUID,
		TaskRef:    taskRef,
		SkillID:    skillID,
		Capability: "shimo:connect shimo:read",
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    shimoConnectorIssuer,
			Audience:  jwt.ClaimStrings{shimoConnectorAudience},
			Subject:   actorUID,
			ID:        jti,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(secret)
}

func (h *ShimoConnectorHandler) HandleConnection(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	key := shimoConnectionKey{agentUID: actor.agentUID, actorUID: actor.actorUID}
	binding := h.connectionBinding(key)
	switch r.Method {
	case http.MethodGet:
		if lifecycle, remote := h.backend.(shimoConnectionLifecycleBackend); remote {
			status, err := lifecycle.ConnectionStatus(r.Context(), actor)
			if err != nil {
				h.writeBackendError(w, err)
				return
			}
			data := map[string]any{"state": status.State}
			if status.AccountHint != "" {
				data["account_hint"] = status.AccountHint
			}
			if status.VerifiedAt != "" {
				data["verified_at"] = status.VerifiedAt
			}
			h.writeOK(w, http.StatusOK, binding, data)
			return
		}
		h.mu.Lock()
		connection, exists := h.connections[key]
		h.mu.Unlock()
		data := map[string]any{"state": "disconnected"}
		if exists {
			data["state"] = connection.state
			if connection.accountHint != "" {
				data["account_hint"] = connection.accountHint
			}
			if !connection.verifiedAt.IsZero() {
				data["verified_at"] = connection.verifiedAt.UTC().Format(time.RFC3339)
			}
		}
		h.writeOK(w, http.StatusOK, binding, data)
	case http.MethodDelete:
		if lifecycle, remote := h.backend.(shimoConnectionLifecycleBackend); remote {
			if err := lifecycle.Disconnect(r.Context(), actor); err != nil {
				h.writeBackendError(w, err)
				return
			}
		}
		h.mu.Lock()
		delete(h.connections, key)
		for _, attempt := range h.attempts {
			if attempt.key == key {
				attempt.used = true
			}
		}
		h.mu.Unlock()
		h.writeOK(w, http.StatusOK, binding, map[string]any{"state": "disconnected"})
	default:
		h.writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "请求方法不受支持", 0)
	}
}

func (h *ShimoConnectorHandler) HandleConnectionLink(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "请求方法不受支持", 0)
		return
	}
	actor, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if h.backend == nil {
		h.writeError(w, http.StatusServiceUnavailable, "CONNECTOR_NOT_CONFIGURED", "石墨连接器读取后端尚未配置", 30)
		return
	}
	if h.publicURL == "" {
		h.writeError(w, http.StatusServiceUnavailable, "CONNECTOR_NOT_CONFIGURED", "石墨连接器公网地址尚未配置", 30)
		return
	}
	if r.ContentLength > 0 {
		h.writeError(w, http.StatusBadRequest, "INVALID_ARGUMENTS", "连接链接请求不接受请求体", 0)
		return
	}
	attemptID, err := shimoRandomHex(12)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "无法创建石墨连接请求", 0)
		return
	}
	rawToken, err := shimoRandomHex(32)
	if err != nil {
		h.writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "无法创建石墨连接请求", 0)
		return
	}
	now := h.now().UTC()
	key := shimoConnectionKey{agentUID: actor.agentUID, actorUID: actor.actorUID}
	attempt := &shimoLoginAttempt{
		id: attemptID, key: key, taskRef: actor.taskRef,
		tokenHash: shimoSHA256Hex(rawToken), expiresAt: now.Add(h.attemptTTL),
	}
	h.mu.Lock()
	h.pruneAttemptsLocked(now)
	h.attempts[attempt.tokenHash] = attempt
	h.connections[key] = shimoConnection{state: "connecting", attemptID: attemptID}
	h.mu.Unlock()
	connectionURL := h.publicURL + "/connect/shimo/" + url.PathEscape(rawToken)
	h.writeOK(w, http.StatusOK, h.connectionBinding(key), map[string]any{
		"connection_url": connectionURL,
		"attempt_id":     attemptID,
		"expires_at":     attempt.expiresAt.Format(time.RFC3339),
	})
}

func (h *ShimoConnectorHandler) HandleListSheets(w http.ResponseWriter, r *http.Request) {
	actor, sourceURL, ok := h.authorizedReadRequest(w, r, http.MethodPost)
	if !ok {
		return
	}
	data, err := h.backend.ListSheets(r.Context(), actor, sourceURL)
	if err != nil {
		h.writeBackendError(w, err)
		return
	}
	h.writeOK(w, http.StatusOK, h.connectionBinding(shimoConnectionKey{agentUID: actor.agentUID, actorUID: actor.actorUID}), data)
}

func (h *ShimoConnectorHandler) HandleReadSheet(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.authenticateMethodAndConnection(w, r, http.MethodPost)
	if !ok {
		return
	}
	var body struct {
		URL       string `json:"url"`
		SheetName string `json:"sheet_name"`
		Range     string `json:"range"`
	}
	if !h.decodeJSON(w, r, &body) {
		return
	}
	sourceURL, err := validateShimoSourceURL(body.URL)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "INVALID_URL", err.Error(), 0)
		return
	}
	sheetName := strings.TrimSpace(body.SheetName)
	if sheetName == "" || len([]rune(sheetName)) > 200 {
		h.writeError(w, http.StatusBadRequest, "INVALID_ARGUMENTS", "工作表名称不能为空或过长", 0)
		return
	}
	cellRange := strings.ToUpper(strings.TrimSpace(body.Range))
	if !shimoCellRangePattern.MatchString(cellRange) {
		h.writeError(w, http.StatusBadRequest, "INVALID_RANGE", "单元格范围应类似 A1:Z1000", 0)
		return
	}
	data, err := h.backend.ReadSheet(r.Context(), actor, sourceURL, sheetName, cellRange)
	if err != nil {
		h.writeBackendError(w, err)
		return
	}
	h.writeOK(w, http.StatusOK, h.connectionBinding(shimoConnectionKey{agentUID: actor.agentUID, actorUID: actor.actorUID}), data)
}

func (h *ShimoConnectorHandler) HandleReadDocument(w http.ResponseWriter, r *http.Request) {
	actor, ok := h.authenticateMethodAndConnection(w, r, http.MethodPost)
	if !ok {
		return
	}
	var body struct {
		URL      string `json:"url"`
		MaxChars int    `json:"max_chars"`
	}
	if !h.decodeJSON(w, r, &body) {
		return
	}
	sourceURL, err := validateShimoSourceURL(body.URL)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "INVALID_URL", err.Error(), 0)
		return
	}
	if body.MaxChars < 1000 || body.MaxChars > 500000 {
		h.writeError(w, http.StatusBadRequest, "INVALID_ARGUMENTS", "max_chars 必须介于 1000 与 500000", 0)
		return
	}
	data, err := h.backend.ReadDocument(r.Context(), actor, sourceURL, body.MaxChars)
	if err != nil {
		h.writeBackendError(w, err)
		return
	}
	h.writeOK(w, http.StatusOK, h.connectionBinding(shimoConnectionKey{agentUID: actor.agentUID, actorUID: actor.actorUID}), data)
}

// HandleLoginAttempt is the temporary user-facing connection page. The MVP
// backend exposes a mock completion button; the real backend will replace it
// with an isolated browser surface while preserving the URL contract.
func (h *ShimoConnectorHandler) HandleLoginAttempt(w http.ResponseWriter, r *http.Request) {
	rawToken := strings.TrimPrefix(r.URL.Path, "/connect/shimo/")
	if rawToken == "" || strings.Contains(rawToken, "/") {
		h.writeLoginPage(w, http.StatusNotFound, "连接链接无效", false)
		return
	}
	tokenHash := shimoSHA256Hex(rawToken)
	now := h.now().UTC()
	h.mu.Lock()
	h.pruneAttemptsLocked(now)
	attempt := h.attempts[tokenHash]
	if attempt == nil || attempt.used || attempt.claimed || !hmac.Equal([]byte(attempt.tokenHash), []byte(tokenHash)) {
		h.mu.Unlock()
		h.writeLoginPage(w, http.StatusGone, "连接链接已失效，请回到聊天重新发起", false)
		return
	}
	if r.Method == http.MethodGet {
		if lifecycle, remote := h.backend.(shimoConnectionLifecycleBackend); remote {
			actor := shimoActor{agentUID: attempt.key.agentUID, actorUID: attempt.key.actorUID, taskRef: attempt.taskRef}
			attempt.claimed = true
			completionToken, resume := h.newLoginResumeLocked(attempt, now)
			h.mu.Unlock()
			loginURL, err := lifecycle.StartLogin(r.Context(), actor, completionToken)
			if err != nil {
				h.mu.Lock()
				if resume != nil {
					delete(h.resumes, resume.tokenHash)
				}
				if current := h.attempts[tokenHash]; current == attempt {
					attempt.claimed = false
				}
				h.mu.Unlock()
				h.writeLoginPage(w, http.StatusBadGateway, "暂时无法打开石墨登录，请返回聊天后重试", false)
				return
			}
			h.mu.Lock()
			if current := h.attempts[tokenHash]; current == attempt {
				current.used = true
			}
			h.mu.Unlock()
			http.Redirect(w, r, loginURL, http.StatusSeeOther)
			return
		}
		h.mu.Unlock()
		h.writeLoginPage(w, http.StatusOK, "石墨连接器本地验证", true)
		return
	}
	if r.Method != http.MethodPost {
		h.mu.Unlock()
		h.writeLoginPage(w, http.StatusMethodNotAllowed, "请求方法不受支持", false)
		return
	}
	if _, mock := h.backend.(mockShimoConnectorBackend); !mock {
		h.mu.Unlock()
		h.writeLoginPage(w, http.StatusNotImplemented, "真实石墨登录界面尚未接入", false)
		return
	}
	attempt.used = true
	h.connections[attempt.key] = shimoConnection{
		state: "connected", accountHint: "本地模拟石墨账号", verifiedAt: now,
	}
	h.mu.Unlock()
	h.writeLoginPage(w, http.StatusOK, "连接成功，可以返回 CatsCo 聊天", false)
}

// HandleLoginComplete consumes a Worker-only one-time completion token and
// resumes the original bot turn without persisting a synthetic chat message.
func (h *ShimoConnectorHandler) HandleLoginComplete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		h.writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "请求方法不受支持", 0)
		return
	}
	if len(h.workerToken) < 32 || !secureBearerMatch(r.Header.Get("Authorization"), h.workerToken) {
		h.writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "浏览器 Worker 回调凭证无效", 0)
		return
	}
	var body struct {
		CompletionToken string `json:"completion_token"`
	}
	if !h.decodeJSON(w, r, &body) {
		return
	}
	rawToken := strings.TrimSpace(body.CompletionToken)
	if len(rawToken) != 64 {
		h.writeError(w, http.StatusBadRequest, "INVALID_ARGUMENTS", "登录完成票据无效", 0)
		return
	}
	hash := shimoSHA256Hex(rawToken)
	now := h.now().UTC()
	h.mu.Lock()
	h.pruneResumesLocked(now)
	resume := h.resumes[hash]
	publisher := h.resumePublisher
	if resume == nil || publisher == nil || !hmac.Equal([]byte(resume.tokenHash), []byte(hash)) {
		h.mu.Unlock()
		h.writeError(w, http.StatusGone, "RESUME_EXPIRED", "登录恢复票据已失效", 0)
		return
	}
	delete(h.resumes, hash)
	h.mu.Unlock()
	delivered := publisher(ShimoLoginResume{
		AgentUID: parseFormattedUID(resume.key.agentUID), ActorUID: parseFormattedUID(resume.key.actorUID),
		TopicID: resume.topicID, MessageID: resume.messageID,
	})
	if !delivered {
		h.mu.Lock()
		if resume.expiresAt.After(h.now().UTC()) {
			h.resumes[hash] = resume
		}
		h.mu.Unlock()
		h.writeError(w, http.StatusConflict, "AGENT_OFFLINE", "虚拟员工当前不在线，请回到聊天继续任务", 3)
		return
	}
	h.writeOK(w, http.StatusAccepted, h.connectionBinding(resume.key), map[string]any{"state": "resumed"})
}

func (h *ShimoConnectorHandler) authenticateMethodAndConnection(w http.ResponseWriter, r *http.Request, method string) (shimoActor, bool) {
	if r.Method != method {
		h.writeError(w, http.StatusMethodNotAllowed, "METHOD_NOT_ALLOWED", "请求方法不受支持", 0)
		return shimoActor{}, false
	}
	actor, ok := h.authenticate(w, r)
	if !ok {
		return shimoActor{}, false
	}
	if h.backend == nil {
		h.writeError(w, http.StatusServiceUnavailable, "CONNECTOR_NOT_CONFIGURED", "石墨连接器读取后端尚未配置", 30)
		return shimoActor{}, false
	}
	key := shimoConnectionKey{agentUID: actor.agentUID, actorUID: actor.actorUID}
	if lifecycle, remote := h.backend.(shimoConnectionLifecycleBackend); remote {
		status, err := lifecycle.ConnectionStatus(r.Context(), actor)
		if err != nil {
			h.writeBackendError(w, err)
			return shimoActor{}, false
		}
		if status.State != "connected" {
			h.writeError(w, http.StatusConflict, "LOGIN_REQUIRED", "当前用户尚未连接石墨", 0)
			return shimoActor{}, false
		}
		return actor, true
	}
	h.mu.Lock()
	connection, connected := h.connections[key]
	h.mu.Unlock()
	if !connected || connection.state != "connected" {
		h.writeError(w, http.StatusConflict, "LOGIN_REQUIRED", "当前用户尚未连接石墨", 0)
		return shimoActor{}, false
	}
	return actor, true
}

func (h *ShimoConnectorHandler) authorizedReadRequest(w http.ResponseWriter, r *http.Request, method string) (shimoActor, string, bool) {
	actor, ok := h.authenticateMethodAndConnection(w, r, method)
	if !ok {
		return shimoActor{}, "", false
	}
	var body struct {
		URL string `json:"url"`
	}
	if !h.decodeJSON(w, r, &body) {
		return shimoActor{}, "", false
	}
	sourceURL, err := validateShimoSourceURL(body.URL)
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "INVALID_URL", err.Error(), 0)
		return shimoActor{}, "", false
	}
	return actor, sourceURL, true
}

func (h *ShimoConnectorHandler) authenticate(w http.ResponseWriter, r *http.Request) (shimoActor, bool) {
	if len(h.actorSecret) < 32 {
		h.writeError(w, http.StatusServiceUnavailable, "CONNECTOR_NOT_CONFIGURED", "石墨连接器身份密钥尚未配置", 30)
		return shimoActor{}, false
	}
	authorization := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(authorization, "Bearer ") {
		h.writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "缺少石墨连接器短时令牌", 0)
		return shimoActor{}, false
	}
	tokenText := strings.TrimSpace(strings.TrimPrefix(authorization, "Bearer "))
	claims := &ShimoActorClaims{}
	token, err := jwt.ParseWithClaims(tokenText, claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("unexpected signing method")
		}
		return h.actorSecret, nil
	}, jwt.WithAudience(shimoConnectorAudience), jwt.WithIssuer(shimoConnectorIssuer), jwt.WithExpirationRequired(), jwt.WithValidMethods([]string{"HS256"}))
	issuedAtOK := claims.IssuedAt != nil && !claims.IssuedAt.Time.After(h.now().UTC().Add(time.Minute))
	expiresAtOK := claims.ExpiresAt != nil && claims.IssuedAt != nil && claims.ExpiresAt.Time.Sub(claims.IssuedAt.Time) <= maxShimoActorTokenTTL
	requestedSkillID := strings.TrimSpace(r.Header.Get("X-CatsCo-Skill-ID"))
	identityOK := strings.TrimSpace(claims.AgentUID) != "" && strings.TrimSpace(claims.ActorUID) != "" && strings.TrimSpace(claims.TaskRef) != "" && strings.TrimSpace(claims.SkillID) != "" && claims.Subject == strings.TrimSpace(claims.ActorUID)
	if err != nil || !token.Valid || !issuedAtOK || !expiresAtOK || !identityOK || requestedSkillID == "" || requestedSkillID != strings.TrimSpace(claims.SkillID) || claims.Capability != "shimo:connect shimo:read" {
		h.writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "石墨连接器短时令牌无效或已过期", 0)
		return shimoActor{}, false
	}
	return shimoActor{agentUID: strings.TrimSpace(claims.AgentUID), actorUID: strings.TrimSpace(claims.ActorUID), taskRef: strings.TrimSpace(claims.TaskRef)}, true
}

func (h *ShimoConnectorHandler) decodeJSON(w http.ResponseWriter, r *http.Request, target any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxShimoRequestBytes)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		h.writeError(w, http.StatusBadRequest, "INVALID_ARGUMENTS", "请求 JSON 无效或包含未知字段", 0)
		return false
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		h.writeError(w, http.StatusBadRequest, "INVALID_ARGUMENTS", "请求体只能包含一个 JSON 对象", 0)
		return false
	}
	return true
}

func (h *ShimoConnectorHandler) connectionBinding(key shimoConnectionKey) string {
	mac := hmac.New(sha256.New, h.actorSecret)
	mac.Write([]byte("shimo-binding\x00" + key.agentUID + "\x00" + key.actorUID))
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

func (h *ShimoConnectorHandler) pruneAttemptsLocked(now time.Time) {
	for hash, attempt := range h.attempts {
		if attempt.used || !attempt.expiresAt.After(now) {
			delete(h.attempts, hash)
			if connection, ok := h.connections[attempt.key]; ok && connection.state == "connecting" && connection.attemptID == attempt.id {
				delete(h.connections, attempt.key)
			}
		}
	}
}

func (h *ShimoConnectorHandler) newLoginResumeLocked(attempt *shimoLoginAttempt, now time.Time) (string, *shimoLoginResume) {
	if attempt == nil || len(h.workerToken) < 32 || h.resumePublisher == nil {
		return "", nil
	}
	topicID, messageID, ok := parseShimoTaskRef(attempt.taskRef)
	if !ok {
		return "", nil
	}
	agentUID := parseFormattedUID(attempt.key.agentUID)
	actorUID := parseFormattedUID(attempt.key.actorUID)
	if agentUID <= 0 || actorUID <= 0 || extractPeerUID(topicID, actorUID) != agentUID {
		return "", nil
	}
	rawToken, err := shimoRandomHex(32)
	if err != nil {
		return "", nil
	}
	resume := &shimoLoginResume{
		key: attempt.key, taskRef: attempt.taskRef, topicID: topicID, messageID: messageID,
		tokenHash: shimoSHA256Hex(rawToken), expiresAt: now.Add(defaultShimoResumeTTL),
	}
	h.pruneResumesLocked(now)
	h.resumes[resume.tokenHash] = resume
	return rawToken, resume
}

func (h *ShimoConnectorHandler) pruneResumesLocked(now time.Time) {
	for hash, resume := range h.resumes {
		if resume == nil || !resume.expiresAt.After(now) {
			delete(h.resumes, hash)
		}
	}
}

func parseShimoTaskRef(taskRef string) (string, int64, bool) {
	value := strings.TrimPrefix(strings.TrimSpace(taskRef), "catsco:")
	separator := strings.LastIndex(value, ":")
	if separator <= 0 || separator == len(value)-1 {
		return "", 0, false
	}
	topicID := value[:separator]
	messageID, err := strconv.ParseInt(value[separator+1:], 10, 64)
	if err != nil || messageID <= 0 || !strings.HasPrefix(topicID, "p2p_") {
		return "", 0, false
	}
	return topicID, messageID, true
}

func secureBearerMatch(header, expected string) bool {
	header = strings.TrimSpace(header)
	provided := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
	return strings.HasPrefix(header, "Bearer ") && len(provided) == len(expected) && hmac.Equal([]byte(provided), []byte(expected))
}

func (h *ShimoConnectorHandler) writeOK(w http.ResponseWriter, status int, binding string, data map[string]any) {
	writeJSON(w, status, map[string]any{
		"ok": true, "request_id": newRequestID(), "connection_binding": binding,
		"connector_revision": h.revision, "data": data,
	})
}

func (h *ShimoConnectorHandler) writeError(w http.ResponseWriter, status int, code, message string, retryAfter int) {
	writeJSON(w, status, map[string]any{
		"ok": false, "request_id": newRequestID(),
		"error": map[string]any{"code": code, "message": message, "retry_after_seconds": retryAfter},
	})
}

type shimoBackendError struct {
	code       string
	message    string
	status     int
	retryAfter int
}

func (e shimoBackendError) Error() string { return e.message }

func (h *ShimoConnectorHandler) writeBackendError(w http.ResponseWriter, err error) {
	var backendError shimoBackendError
	if errors.As(err, &backendError) {
		h.writeError(w, backendError.status, backendError.code, backendError.message, backendError.retryAfter)
		return
	}
	h.writeError(w, http.StatusBadGateway, "UPSTREAM_CHANGED", "石墨读取结果不符合预期结构", 30)
}

func (h *ShimoConnectorHandler) writeLoginPage(w http.ResponseWriter, status int, message string, showButton bool) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_ = shimoLoginTemplate.Execute(w, map[string]any{"Message": message, "ShowButton": showButton})
}

var shimoLoginTemplate = template.Must(template.New("shimo-login").Parse(`<!doctype html>
<html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>连接石墨</title><style>body{font-family:system-ui,sans-serif;max-width:520px;margin:64px auto;padding:24px;color:#17211b}main{border:1px solid #dfe7e1;border-radius:16px;padding:28px}button{border:0;border-radius:8px;padding:12px 18px;background:#2f7d42;color:#fff;font-size:16px}</style></head>
<body><main><h1>连接石墨</h1><p>{{.Message}}</p>{{if .ShowButton}}<form method="post"><button type="submit">模拟登录成功</button></form>{{end}}</main></body></html>`))

func validateShimoSourceURL(value string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || (parsed.Hostname() != "shimo.im" && parsed.Hostname() != "www.shimo.im") || strings.Trim(parsed.Path, "/") == "" {
		return "", errors.New("只允许完整的 HTTPS shimo.im 文档链接")
	}
	parsed.Fragment = ""
	return parsed.String(), nil
}

func shimoRandomHex(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func shimoSHA256Hex(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func newRequestID() string {
	value, err := shimoRandomHex(12)
	if err != nil {
		return "unavailable"
	}
	return value
}

type mockShimoConnectorBackend struct{}

func (mockShimoConnectorBackend) ListSheets(_ context.Context, _ shimoActor, _ string) (map[string]any, error) {
	return map[string]any{
		"extracted_at": time.Now().UTC().Format(time.RFC3339),
		"sheets":       []map[string]any{{"name": "工作表1", "index": 0}, {"name": "开票回款", "index": 1}},
	}, nil
}

func (mockShimoConnectorBackend) ReadSheet(_ context.Context, _ shimoActor, _ string, _, _ string) (map[string]any, error) {
	return map[string]any{
		"extracted_at": time.Now().UTC().Format(time.RFC3339),
		"values":       [][]any{{"客户", "金额"}, {"张三", 8500}},
	}, nil
}

func (mockShimoConnectorBackend) ReadDocument(_ context.Context, _ shimoActor, _ string, maxChars int) (map[string]any, error) {
	text := "石墨连接器本地模拟文档"
	truncated := len([]rune(text)) > maxChars
	if truncated {
		text = string([]rune(text)[:maxChars])
	}
	return map[string]any{"extracted_at": time.Now().UTC().Format(time.RFC3339), "text": text, "truncated": truncated}, nil
}
