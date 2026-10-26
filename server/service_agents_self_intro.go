package server

import (
	"context"
	"encoding/json"
	"log"
	"strings"
)

// 自述（self_intro）：应用跑完一次之后，可以在结果里「建议」自己的对外描述。
//
// 为什么是「建议」而不是直接写：
//
//	写这段的是模型。它可以提出「我应该被这样描述」，但不能替人决定对外文案 ——
//	那句话决定了别人的 Agent 会不会调用它、以及会不会因此产生费用。
//	所以建议先落 service_agents.self_intro_*，等 owner 在面板确认后才进 definition。
//
// 位置约定（任一即可）：
//
//	{"output": {"text": "...", "self_intro": {"description": "...", "input_schema": {...}}}}
//	{"self_intro": {"description": "...", "input_schema": {...}}}
//
// 只认这两个字段。执行方式 / 价格 / 可见性 / slug 一律不看不存 —— 那是发布动作，
// 必须由人来做（也因此 agent 永远拿不到"发布"能力，不存在误操作空间）。

const (
	// 建议描述的落库上限，与作者侧同一口径（超长按 rune 截断，不报错）。
	maxSelfIntroDescriptionRunes = maxServiceAgentDescriptionRunes
	// 建议里 input_schema 的上限，与作者侧同源。
	maxSelfIntroInputSchemaBytes = maxServiceAgentInputSchemaBytes
)

// serviceRunAgentResolver 从 run 反查它服务的是哪个应用（自述落地要知道写给谁）。
// 可选能力：未实现时跳过自述提取（老 store / 测试替身不受影响）。
type serviceRunAgentResolver interface {
	ServiceRunAgentSlug(ctx context.Context, runID string) (string, error)
}

// ServiceAgentSelfIntroStore 是自述建议的落库能力（主库实现）。
type ServiceAgentSelfIntroStore interface {
	SetServiceAgentSelfIntro(ctx context.Context, slug, description, inputSchema string) error
	ClearServiceAgentSelfIntro(ctx context.Context, slug string) error
}

// selfIntroSuggestion 是应用在结果里给出的自我描述建议。
type selfIntroSuggestion struct {
	Description string
	InputSchema string
}

// extractSelfIntro 从 run 结果里取出自述建议。
//
// 找不到（没写 / 格式不对 / 空描述）时返回 ok=false —— 这是常态，不是错误：
// 绝大多数应用不会自述，平台不该因此报警或失败。
func extractSelfIntro(result json.RawMessage) (selfIntroSuggestion, bool) {
	if len(result) == 0 {
		return selfIntroSuggestion{}, false
	}
	var envelope struct {
		SelfIntro json.RawMessage `json:"self_intro"`
		Output    struct {
			SelfIntro json.RawMessage `json:"self_intro"`
		} `json:"output"`
	}
	if err := json.Unmarshal(result, &envelope); err != nil {
		return selfIntroSuggestion{}, false
	}
	raw := envelope.SelfIntro
	if len(raw) == 0 {
		raw = envelope.Output.SelfIntro
	}
	if len(raw) == 0 || string(raw) == "null" {
		return selfIntroSuggestion{}, false
	}
	var view struct {
		Description string          `json:"description"`
		InputSchema json.RawMessage `json:"input_schema"`
	}
	if err := json.Unmarshal(raw, &view); err != nil {
		return selfIntroSuggestion{}, false
	}
	description := strings.TrimSpace(view.Description)
	if description == "" {
		return selfIntroSuggestion{}, false
	}
	if runes := []rune(description); len(runes) > maxSelfIntroDescriptionRunes {
		description = string(runes[:maxSelfIntroDescriptionRunes])
	}
	// schema 写错不连累描述：描述才是这一段的主体，schema 丢了只意味着退回通用写法。
	// （作者侧写错要报错，因为那是人主动提交的；这里是模型顺手给的，不该因此整条丢弃。）
	schema := ""
	if normalized, err := normalizeServiceAgentInputSchema(view.InputSchema); err == nil && len(normalized) > 0 {
		schema = string(normalized)
	}
	return selfIntroSuggestion{Description: description, InputSchema: schema}, true
}

// recordSelfIntro 把结果里的自述建议落库（没有建议时什么也不做）。
//
// 失败只记日志：自述是锦上添花，不能因为它让一次正常的 run 收尾失败 ——
// 调用方等的是任务结果，不是这段文案。
func (o *ServiceRunOrchestrator) recordSelfIntro(ctx context.Context, runID string, result json.RawMessage) {
	suggestion, ok := extractSelfIntro(result)
	if !ok {
		return
	}
	resolver, ok := o.store.(serviceRunAgentResolver)
	if !ok {
		return
	}
	slug, err := resolver.ServiceRunAgentSlug(ctx, runID)
	if err != nil || strings.TrimSpace(slug) == "" {
		if err != nil {
			logSelfIntro(runID, "resolve agent: "+err.Error())
		}
		return
	}
	store, ok := o.store.(ServiceAgentSelfIntroStore)
	if !ok {
		return
	}
	if err := store.SetServiceAgentSelfIntro(ctx, slug, suggestion.Description, suggestion.InputSchema); err != nil {
		logSelfIntro(runID, "persist: "+err.Error())
		return
	}
	logSelfIntro(runID, "recorded suggestion slug="+slug)
}

func logSelfIntro(runID, detail string) {
	log.Printf("service self-intro run=%s %s", runID, detail)
}
