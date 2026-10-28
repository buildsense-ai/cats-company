package server

import (
	"encoding/json"
	"fmt"
	"strings"
)

// serviceAgentInputProblem 用应用声明的 input_schema 做一次最低限度的入参体检。
//
// 只检查 required 里的键在不在 —— 不引入 JSON Schema 校验器。目标不是当校验网关，
// 而是「用户传错字段名时立刻告诉他」。
//
// 为什么需要：input_schema 以前只用来对客户端宣告，服务端从不校验。后果很具体 ——
// 调用方把字段名写成 query / prompt / text_input（很常见，因为每个应用的 schema 都
// 不一样），调用会「成功」受理，然后 bot 收到一段纯 JSON 噪音（botRunnerPromptText
// 在没有 text 字段时会把整个 JSON 当 prompt 下发），或者等到 bot_empty_input 才失败。
// 用户白等几十秒，额度照烧，而且完全不知道自己传错了。
//
// fail-open 的边界：schema 为空、解析失败、没有 required → 一律不拦。老应用没声明
// schema 是常态，不能因为「声明得不完整」把正常调用挡掉。
func serviceAgentInputProblem(schema string, input json.RawMessage) string {
	required := serviceAgentRequiredFields(schema)
	if len(required) == 0 {
		return ""
	}
	// input 不是对象就不做 required 检查：bot 型应用支持把一句话直接当 input 传
	// （botRunnerPromptText 认字符串），这条兼容路径不能因为 schema 存在就被堵死。
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(input, &fields); err != nil || fields == nil {
		return ""
	}
	var missing []string
	for _, key := range required {
		if raw, ok := fields[key]; ok && !serviceAgentEmptyJSON(raw) {
			continue
		}
		missing = append(missing, key)
	}
	if len(missing) == 0 {
		return ""
	}
	// 把用户实际传的键名带上：拼错时（text 写成 text_input）这一条就能让他自己发现。
	// 不附带完整 schema —— 调用方 Agent 手上已经有 tools/list 里的那份。
	var got []string
	for key := range fields {
		got = append(got, key)
	}
	sortStrings(got)
	hint := "没有传任何字段"
	if len(got) > 0 {
		hint = "你传的是 " + strings.Join(got, "、")
	}
	return fmt.Sprintf(
		"invalid_input: 这个应用要求 input 里有 %s，但这次缺了（%s）。"+
			"请在 input 里补上这些字段；如果字段名不一样，多半是写错了 —— 核对一下这个应用的入参说明再调用。",
		strings.Join(missing, "、"), hint)
}

// serviceAgentRequiredFields 解析 schema 里的 required（解析不出就返回空 = 不校验）。
func serviceAgentRequiredFields(schema string) []string {
	trimmed := strings.TrimSpace(schema)
	if trimmed == "" || trimmed == "null" {
		return nil
	}
	var parsed struct {
		Required []string `json:"required"`
	}
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		return nil
	}
	out := make([]string, 0, len(parsed.Required))
	for _, key := range parsed.Required {
		if key = strings.TrimSpace(key); key != "" {
			out = append(out, key)
		}
	}
	return out
}

// serviceAgentEmptyJSON 判断一个字段值算不算「没给」。
// 空串 / null / 空数组 / 空对象都算 —— 它们不会让 bot 收到任何有意义的内容，
// 与其放过去让它跑一趟空转，不如在这里说清楚。
func serviceAgentEmptyJSON(raw json.RawMessage) bool {
	trimmed := strings.TrimSpace(string(raw))
	switch trimmed {
	case "", "null", `""`, "[]", "{}":
		return true
	}
	return false
}

// sortStrings 稳定排序（避免 map 遍历顺序让提示文案和测试抖动）。
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
