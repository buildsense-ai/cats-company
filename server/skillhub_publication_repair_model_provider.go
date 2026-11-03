package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store"
)

var errPublicationRepairModelProvider = errors.New("publication.repair_model_provider_unavailable")

// Trusted constructor configuration only: never load these fields from a task,
// report, model, browser, or source package. There is deliberately no env loader,
// default model, worker registration, or production call site in this slice.
type publicationRepairModelProviderConfig struct {
	Enabled                  bool
	RelayOrigin, AdminOrigin string
	AdminSecret              string `json:"-"`
	Model                    publicationRepairModelConfig
}

// This implements one Anthropic-compatible HTTP request with the original
// human's existing relay key. It cannot control retries/fallbacks INSIDE the
// relay: verifying that deployment policy is a separate rollout prerequisite.
// Only the durable coordinator may grant a paid dispatch; this is not a tool.
type publicationRepairModelProvider struct {
	config publicationRepairModelProviderConfig
	client *http.Client
	active atomic.Int32
}

type publicationRepairProviderMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

func newPublicationRepairModelProvider(cfg publicationRepairModelProviderConfig) (*publicationRepairModelProvider, error) {
	tr := &http.Transport{Proxy: nil, DisableCompression: true, DisableKeepAlives: true, MaxConnsPerHost: 2,
		DialContext:         (&net.Dialer{Timeout: 2 * time.Second}).DialContext,
		TLSHandshakeTimeout: 2 * time.Second, ResponseHeaderTimeout: 50 * time.Second,
		MaxResponseHeaderBytes: 8192, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	return newPublicationRepairModelProviderWithTransport(cfg, tr)
}

// Package-private test seam; production construction always owns the transport.
func newPublicationRepairModelProviderWithTransport(cfg publicationRepairModelProviderConfig, tr http.RoundTripper) (*publicationRepairModelProvider, error) {
	if cfg.Enabled && (!store.ValidSkillHubPublicationOrigin(cfg.RelayOrigin) ||
		!store.ValidSkillHubPublicationOrigin(cfg.AdminOrigin) || !publicationRepairProviderSecret(cfg.AdminSecret) ||
		cfg.Model.Provider != "anthropic-relay" || !store.ValidSkillHubPublicationRepairWorker(cfg.Model.Model) ||
		cfg.Model.MaxOutputTokens < 1 || cfg.Model.MaxOutputTokens > 8192 || tr == nil) {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	return &publicationRepairModelProvider{config: cfg, client: &http.Client{Transport: tr, Timeout: 60 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func publicationRepairProviderSecret(s string) bool {
	if len(s) == 0 || len(s) > 512 {
		return false
	}
	for _, b := range []byte(s) {
		if b < 33 || b > 126 {
			return false
		}
	}
	return true
}

// JSON metadata may be additive, but duplicate keys and ambiguous casing of
// security-relevant keys are never accepted. Never return private decode errors.
func publicationRepairProviderObject(raw []byte, keys ...string) (map[string]json.RawMessage, error) {
	if !utf8.Valid(raw) || validateArtifactRuntimeJSONTokens(raw) != nil {
		return nil, errPublicationRepairModelProvider
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		return nil, errPublicationRepairModelProvider
	}
	for name := range obj {
		for _, key := range keys {
			if strings.EqualFold(name, key) && name != key {
				return nil, errPublicationRepairModelProvider
			}
		}
	}
	return obj, nil
}

func (p *publicationRepairModelProvider) post(ctx context.Context, endpoint, header, secret string, body []byte, limit int64) ([]byte, error) {
	// NopCloser deliberately removes GetBody. Combined with non-idempotent POST,
	// no idempotency headers, fresh connections and no redirects, net/http cannot
	// replay this request. Do not replace with a retrying SDK/shared client.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, io.NopCloser(bytes.NewReader(body)))
	if err != nil {
		return nil, errPublicationRepairModelProvider
	}
	req.ContentLength = int64(len(body))
	req.Header.Set(header, secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-store")
	if header == "x-api-key" {
		req.Header.Set("anthropic-version", "2023-06-01")
	}
	response, err := p.client.Do(req)
	if err != nil {
		return nil, errPublicationRepairModelProvider
	}
	defer response.Body.Close()
	media, _, mediaErr := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if response.StatusCode != http.StatusOK || mediaErr != nil || media != "application/json" ||
		len(response.Header.Values("Content-Type")) != 1 || len(response.Header.Values("Content-Encoding")) != 0 ||
		response.Uncompressed || response.ContentLength > limit {
		return nil, errPublicationRepairModelProvider
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || ctx.Err() != nil || len(raw) == 0 || int64(len(raw)) > limit {
		return nil, errPublicationRepairModelProvider
	}
	return raw, nil
}

func (p *publicationRepairModelProvider) payerKey(ctx context.Context, uid int64) (string, error) {
	// Existing active key ONLY. Never create, rotate, cache, or fall back to a
	// service/owner/Bot credential. The route is derived solely from the payer.
	raw, err := p.post(ctx, p.config.AdminOrigin+"/internal/users/"+strconv.FormatInt(uid, 10)+"/key/reveal",
		"Authorization", "Bearer "+p.config.AdminSecret, nil, 16384)
	if err != nil {
		return "", errPublicationRepairModelProvider
	}
	obj, err := publicationRepairProviderObject(raw, "configured", "key")
	if err != nil || !bytes.Equal(bytes.TrimSpace(obj["configured"]), []byte("true")) {
		return "", errPublicationRepairModelProvider
	}
	key, err := publicationRepairProviderObject(obj["key"], "state", "key", "id")
	if err != nil {
		return "", errPublicationRepairModelProvider
	}
	state, e1 := publicationMaterialString(key["state"])
	value, e2 := publicationMaterialString(key["key"])
	id, e3 := publicationMaterialString(key["id"])
	if e1 != nil || e2 != nil || e3 != nil || state != "active" || id == "" || !publicationRepairProviderSecret(value) {
		return "", errPublicationRepairModelProvider
	}
	return value, nil
}

func (p *publicationRepairModelProvider) invokeOnce(parent context.Context, r publicationRepairModelRequest) (store.SkillHubPublicationRepairModelOutput, error) {
	empty := store.SkillHubPublicationRepairModelOutput{}
	if p == nil || !p.config.Enabled {
		return empty, errPublicationRepairModelProvider
	}
	hash, err := r.Task.IdentityHash()
	if err != nil || r.PayerUID <= 0 || r.TaskIdentityHash != hash || !publicationRepairTaskID.MatchString(r.TaskID) ||
		r.Round.Validate() != nil || r.Round.Evidence != r.Task.Evidence || r.Config != p.config.Model ||
		r.Policy != publicationRepairModelPolicy || r.System != publicationRepairModelSystem ||
		len(r.SourceText) == 0 || len(r.SourceText) > publicationRepairProjectionMaxBytes ||
		!utf8.ValidString(r.SourceText) || strings.ContainsRune(r.SourceText, 0) {
		return empty, store.ErrSkillHubPublicationInvalid
	}
	evidence, err := publicationRepairEvidence(r.Report, r.Task.Receipt.Query.SourceSnapshotHash)
	if err != nil || evidence != r.Task.Evidence {
		return empty, store.ErrSkillHubPublicationInvalid
	}
	if _, err := publicationModelDescriptionProposal(r.Report, "selector eligibility only"); err != nil {
		return empty, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	if ctx.Err() != nil {
		return empty, errPublicationRepairModelProvider
	}
	if p.active.Add(1) > 2 {
		p.active.Add(-1)
		return empty, store.ErrSkillHubPublicationBusy
	}
	defer p.active.Add(-1)
	// Explicit mapping preserves source/report but excludes task IDs, receipts,
	// identity, payer, leases, credentials and arbitrary tool/provider options.
	user, err := json.Marshal(struct {
		Kind   string                      `json:"kind"`
		Source string                      `json:"source"`
		Report PublicationValidationReport `json:"report"`
	}{"untrusted-publication-repair-material", r.SourceText, r.Report})
	if err != nil {
		return empty, store.ErrSkillHubPublicationInvalid
	}
	body, err := json.Marshal(struct {
		Model     string                             `json:"model"`
		MaxTokens int                                `json:"max_tokens"`
		System    string                             `json:"system"`
		Messages  []publicationRepairProviderMessage `json:"messages"`
		Stream    bool                               `json:"stream"`
	}{r.Config.Model, r.Config.MaxOutputTokens, r.System, []publicationRepairProviderMessage{{Role: "user", Content: string(user)}}, false})
	if err != nil || len(body) > 8*1024*1024 {
		return empty, store.ErrSkillHubPublicationInvalid
	}
	key, err := p.payerKey(ctx, r.PayerUID)
	if err != nil || ctx.Err() != nil {
		return empty, errPublicationRepairModelProvider
	}
	raw, err := p.post(ctx, p.config.RelayOrigin+"/anthropic/v1/messages", "x-api-key", key, body, 128*1024)
	if err != nil {
		return empty, errPublicationRepairModelProvider
	}
	obj, err := publicationRepairProviderObject(raw, "type", "role", "model", "content", "stop_reason", "error")
	if err != nil || obj["error"] != nil {
		return empty, errPublicationRepairModelProvider
	}
	for field, expected := range map[string]string{"type": "message", "role": "assistant", "model": r.Config.Model, "stop_reason": "end_turn"} {
		value, err := publicationMaterialString(obj[field])
		if err != nil || value != expected {
			return empty, errPublicationRepairModelProvider
		}
	}
	var blocks []json.RawMessage
	if json.Unmarshal(obj["content"], &blocks) != nil || len(blocks) != 1 {
		return empty, errPublicationRepairModelProvider
	}
	block, err := publicationExactObject(blocks[0], "type", "text")
	if err != nil || string(bytes.TrimSpace(block["type"])) != `"text"` {
		return empty, errPublicationRepairModelProvider
	}
	text, err := publicationMaterialString(block["text"])
	output := store.SkillHubPublicationRepairModelOutput{Text: text}
	if err != nil || output.Validate() != nil || len(text) > store.SkillHubPublicationDescriptionDraftMaxBytes || strings.TrimSpace(text) == "" || ctx.Err() != nil {
		return empty, errPublicationRepairModelProvider
	}
	if _, err := publicationModelDescriptionProposal(r.Report, text); err != nil {
		return empty, errPublicationRepairModelProvider
	}
	return output, nil
}
