package server

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxShimoWorkerResponseBytes = 32 << 20

type shimoConnectionStatus struct {
	State       string
	AccountHint string
	VerifiedAt  string
}

type shimoConnectionLifecycleBackend interface {
	ConnectionStatus(ctx context.Context, actor shimoActor) (shimoConnectionStatus, error)
	StartLogin(ctx context.Context, actor shimoActor, completionToken string) (string, error)
	Disconnect(ctx context.Context, actor shimoActor) error
}

type httpShimoWorkerBackend struct {
	baseURL     string
	token       string
	bindingKey  []byte
	callbackURL string
	client      *http.Client
}

func newHTTPShimoWorkerBackend(baseURL, token, callbackURL string, bindingKey []byte) (*httpShimoWorkerBackend, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	parsed, err := url.Parse(baseURL)
	internalHTTP := parsed.Scheme == "http" && (parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "localhost" || !strings.Contains(parsed.Hostname(), "."))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "https" && !internalHTTP) {
		return nil, errors.New("shimo worker URL must use HTTPS or loopback HTTP")
	}
	if len(strings.TrimSpace(token)) < 32 {
		return nil, errors.New("shimo worker token must contain at least 32 characters")
	}
	if len(bindingKey) < 32 {
		return nil, errors.New("shimo worker binding key must contain at least 32 bytes")
	}
	callbackURL = strings.TrimSpace(callbackURL)
	if callbackURL != "" {
		callback, callbackErr := url.Parse(callbackURL)
		internalCallback := callbackErr == nil && callback.Scheme == "http" && (callback.Hostname() == "127.0.0.1" || callback.Hostname() == "localhost" || !strings.Contains(callback.Hostname(), "."))
		if callbackErr != nil || callback.Scheme == "" || callback.Host == "" || callback.User != nil || callback.RawQuery != "" || callback.Fragment != "" || (callback.Scheme != "https" && !internalCallback) {
			return nil, errors.New("shimo worker callback URL must use HTTPS or internal HTTP")
		}
	}
	return &httpShimoWorkerBackend{
		baseURL:     baseURL,
		token:       strings.TrimSpace(token),
		bindingKey:  append([]byte(nil), bindingKey...),
		callbackURL: callbackURL,
		client:      &http.Client{Timeout: 75 * time.Second},
	}, nil
}

func (b *httpShimoWorkerBackend) ConnectionStatus(ctx context.Context, actor shimoActor) (shimoConnectionStatus, error) {
	var response struct {
		State       string `json:"state"`
		AccountHint string `json:"account_hint"`
		VerifiedAt  string `json:"verified_at"`
	}
	if err := b.call(ctx, "/v1/sessions/status", map[string]any{"connection_binding": b.binding(actor)}, &response); err != nil {
		return shimoConnectionStatus{}, err
	}
	return shimoConnectionStatus{State: response.State, AccountHint: response.AccountHint, VerifiedAt: response.VerifiedAt}, nil
}

func (b *httpShimoWorkerBackend) StartLogin(ctx context.Context, actor shimoActor, completionToken string) (string, error) {
	var response struct {
		LoginURL string `json:"login_url"`
	}
	request := map[string]any{"connection_binding": b.binding(actor)}
	if b.callbackURL != "" && strings.TrimSpace(completionToken) != "" {
		request["completion_callback_url"] = b.callbackURL
		request["completion_token"] = strings.TrimSpace(completionToken)
	}
	if err := b.call(ctx, "/v1/sessions/login", request, &response); err != nil {
		return "", err
	}
	parsed, err := url.Parse(strings.TrimSpace(response.LoginURL))
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || (parsed.Scheme != "https" && !(parsed.Scheme == "http" && parsed.Hostname() == "127.0.0.1")) {
		return "", shimoBackendError{code: "UPSTREAM_CHANGED", message: "浏览器 Worker 返回了无效的登录地址", status: http.StatusBadGateway, retryAfter: 30}
	}
	return parsed.String(), nil
}

func (b *httpShimoWorkerBackend) Disconnect(ctx context.Context, actor shimoActor) error {
	return b.call(ctx, "/v1/sessions/disconnect", map[string]any{"connection_binding": b.binding(actor)}, &struct{}{})
}

func (b *httpShimoWorkerBackend) ListSheets(ctx context.Context, actor shimoActor, sourceURL string) (map[string]any, error) {
	var response map[string]any
	err := b.call(ctx, "/v1/sheets/list", map[string]any{"connection_binding": b.binding(actor), "url": sourceURL}, &response)
	return response, err
}

func (b *httpShimoWorkerBackend) ReadSheet(ctx context.Context, actor shimoActor, sourceURL, sheetName, cellRange string) (map[string]any, error) {
	var response map[string]any
	err := b.call(ctx, "/v1/sheets/read", map[string]any{
		"connection_binding": b.binding(actor), "url": sourceURL, "sheet_name": sheetName, "range": cellRange,
	}, &response)
	return response, err
}

func (b *httpShimoWorkerBackend) ReadDocument(ctx context.Context, actor shimoActor, sourceURL string, maxChars int) (map[string]any, error) {
	var response map[string]any
	err := b.call(ctx, "/v1/documents/read", map[string]any{
		"connection_binding": b.binding(actor), "url": sourceURL, "max_chars": maxChars,
	}, &response)
	return response, err
}

func (b *httpShimoWorkerBackend) binding(actor shimoActor) string {
	mac := hmac.New(sha256.New, b.bindingKey)
	mac.Write([]byte("shimo-binding\x00" + actor.agentUID + "\x00" + actor.actorUID))
	return hex.EncodeToString(mac.Sum(nil)[:16])
}

func (b *httpShimoWorkerBackend) call(ctx context.Context, path string, requestBody any, output any) error {
	body, err := json.Marshal(requestBody)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, b.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+b.token)
	request.Header.Set("Content-Type", "application/json")
	response, err := b.client.Do(request)
	if err != nil {
		return shimoBackendError{code: "WORKER_UNAVAILABLE", message: "石墨浏览器 Worker 暂时不可用", status: http.StatusBadGateway, retryAfter: 30}
	}
	defer response.Body.Close()
	limited := io.LimitReader(response.Body, maxShimoWorkerResponseBytes+1)
	responseBytes, err := io.ReadAll(limited)
	if err != nil || len(responseBytes) > maxShimoWorkerResponseBytes {
		return shimoBackendError{code: "UPSTREAM_CHANGED", message: "浏览器 Worker 返回内容过大或无法读取", status: http.StatusBadGateway, retryAfter: 30}
	}
	var envelope struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(responseBytes, &envelope); err != nil {
		return shimoBackendError{code: "UPSTREAM_CHANGED", message: "浏览器 Worker 返回格式无效", status: http.StatusBadGateway, retryAfter: 30}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 || !envelope.OK {
		code := strings.TrimSpace(envelope.Error.Code)
		message := strings.TrimSpace(envelope.Error.Message)
		if code == "" {
			code = "WORKER_ERROR"
		}
		if message == "" {
			message = "石墨浏览器 Worker 处理失败"
		}
		status := response.StatusCode
		if status < 400 || status > 599 {
			status = http.StatusBadGateway
		}
		return shimoBackendError{code: code, message: message, status: status, retryAfter: 0}
	}
	if output == nil {
		return nil
	}
	if err := json.Unmarshal(envelope.Data, output); err != nil {
		return fmt.Errorf("decode shimo worker data: %w", err)
	}
	return nil
}
