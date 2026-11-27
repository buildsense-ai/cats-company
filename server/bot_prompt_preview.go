package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const previewModel = "glm-5.3-flash"

// Generate text only. This route does not open a topic, dispatch to a bot,
// invoke a tool or modify the bot definition.
func (h *BotDefinitionHandler) HandleOwnerPromptPreview(w http.ResponseWriter, r *http.Request) {
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	h.handleOwnerPromptPreview(w, r, client)
}

func (h *BotDefinitionHandler) handleOwnerPromptPreview(w http.ResponseWriter, r *http.Request, client *http.Client) {
	if r.Method != http.MethodPost {
		writeJSON(w, 405, map[string]string{"error": "method not allowed"})
		return
	}
	owner, _, ok := h.authorizeOwner(w, r)
	if !ok {
		return
	}
	var input struct {
		Prompt   string `json:"prompt"`
		Question string `json:"question"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&input) != nil || decoder.Decode(&struct{}{}) != io.EOF || strings.TrimSpace(input.Question) == "" || len(input.Question) > 8000 || len(input.Prompt) > 64000 {
		writeJSON(w, 400, map[string]string{"error": "问题或设定过长，请精简后再试。"})
		return
	}
	fail := func(status int, message string) { writeJSON(w, status, map[string]string{"error": message}) }
	if h.modelConfig == nil || h.modelConfig.relayAdmin == nil {
		fail(503, "模型试答暂时不可用")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 55*time.Second)
	defer cancel()
	allowed, err := h.modelConfig.catalogModelAllowed(ctx, owner, previewModel)
	if err != nil {
		fail(503, "暂时无法读取模型额度")
		return
	}
	if !allowed {
		fail(403, "当前账号没有试答模型的可用额度")
		return
	}
	endpoint, err := url.Parse(relayEnv("CATS_RELAY_ANTHROPIC_BASE_URL", relayBaseURL()+"/anthropic") + "/v1/messages")
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil {
		fail(503, "试答服务尚未配置")
		return
	}
	var key relayKeyResponse
	err = h.modelConfig.relayAdmin.Do(ctx, http.MethodPost, fmt.Sprintf("/internal/users/%d/key/reveal", owner), nil, &key)
	if err != nil || !key.Configured || key.Key == nil || key.Key.State != "active" || key.Key.Key == "" {
		fail(503, "当前账号的模型服务不可用")
		return
	}
	system := "你正在进行助手回复试答。请直接以助手身份回答用户的问题。本次仅能生成文字。"
	if input.Prompt != "" {
		system += "\n\n助手设定如下：\n" + effectivePersonaPrompt(input.Prompt)
	}
	body, _ := json.Marshal(map[string]any{"model": previewModel, "max_tokens": 1024, "stream": false, "system": system, "messages": []map[string]string{{"role": "user", "content": input.Question}}})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		fail(503, "试答服务尚未配置")
		return
	}
	req.Header.Set("x-api-key", key.Key.Key)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("anthropic-version", "2023-06-01")
	resp, err := client.Do(req)
	if err != nil {
		fail(502, "模型暂未返回，请重试")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode == 429 {
		fail(429, "模型繁忙或额度不足，请稍后重试")
		return
	}
	if resp.StatusCode != 200 {
		fail(502, "模型暂时无法试答")
		return
	}
	var answer struct {
		Content []struct {
			Type string
			Text string
		}
	}
	if json.NewDecoder(io.LimitReader(resp.Body, 256<<10)).Decode(&answer) != nil {
		fail(502, "试答内容读取失败")
		return
	}
	var texts []string
	for _, block := range answer.Content {
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			texts = append(texts, block.Text)
		}
	}
	if len(texts) == 0 {
		fail(502, "模型没有返回文字，请重试")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, map[string]string{"answer": strings.Join(texts, "\n")})
}
