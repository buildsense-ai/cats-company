package server

import (
	"encoding/json"
	"math"
	"os"
	"strconv"
	"strings"
)

// 计费（设计 §4.5）：**谁调用谁付费、按量给**。
//
// agent 可在 definition 里声明单价；未声明 = 免费（金额记 0，但用量与调用关系照样记账，
// 便于后续定价回溯重算）。
//
//	{"pricing": {"unit": "token", "per_1k_tokens": 0.02, "currency": "CNY"}}
//	{"pricing": {"unit": "run",   "per_run": 0.05}}
//	{"pricing": {"unit": "second","per_second": 0.001}}
//
// 金额一律用「微元」(1e-6 货币单位) 存整数，避免浮点累加误差。
const (
	ServicePricingUnitToken  = "token"
	ServicePricingUnitRun    = "run"
	ServicePricingUnitSecond = "second"

	servicePlatformFeeBpsEnv = "CATS_SERVICE_PLATFORM_FEE_BPS"
	microsPerUnit            = 1_000_000.0
	defaultLedgerCurrency    = "CNY"
)

// ServiceAgentPricing 是 agent 的计费声明（definition.pricing）。
type ServiceAgentPricing struct {
	Unit        string  `json:"unit"`
	Per1KTokens float64 `json:"per_1k_tokens,omitempty"`
	PerRun      float64 `json:"per_run,omitempty"`
	PerSecond   float64 `json:"per_second,omitempty"`
	Currency    string  `json:"currency,omitempty"`
}

// ParseServiceAgentPricing 从 definition JSON 读 pricing（缺失/非法 → nil = 免费）。
func ParseServiceAgentPricing(definition json.RawMessage) *ServiceAgentPricing {
	if len(definition) == 0 {
		return nil
	}
	var parsed struct {
		Pricing *ServiceAgentPricing `json:"pricing"`
	}
	if err := json.Unmarshal(definition, &parsed); err != nil {
		return nil
	}
	if parsed.Pricing == nil {
		return nil
	}
	pricing := *parsed.Pricing
	pricing.Unit = strings.ToLower(strings.TrimSpace(pricing.Unit))
	if pricing.Unit == "" {
		return nil
	}
	if pricing.Currency == "" {
		pricing.Currency = defaultLedgerCurrency
	}
	return &pricing
}

// ServiceRunCharge 是一次 run 的计费结果（金额单位：微元）。
type ServiceRunCharge struct {
	Currency string
	Gross    int64 // 调用方应付
	Fee      int64 // 平台抽成
	Net      int64 // agent owner 应得
}

// PlatformFeeBps 读平台抽成（万分比；未配置/非法 → 0 = 全额归 owner，internal agent 归平台）。
func PlatformFeeBps() int {
	raw := strings.TrimSpace(os.Getenv(servicePlatformFeeBpsEnv))
	if raw == "" {
		return 0
	}
	bps, err := strconv.Atoi(raw)
	if err != nil || bps < 0 {
		return 0
	}
	if bps > 10000 {
		bps = 10000
	}
	return bps
}

// ComputeServiceRunCharge 按 pricing + 实际用量算钱。
// usage 为 0 或未声明 pricing → 金额 0（仍返回 currency，便于账本落行）。
func ComputeServiceRunCharge(pricing *ServiceAgentPricing, promptTokens, completionTokens int64, durationMs int64, feeBps int) ServiceRunCharge {
	charge := ServiceRunCharge{Currency: defaultLedgerCurrency}
	if pricing == nil {
		return charge
	}
	if pricing.Currency != "" {
		charge.Currency = pricing.Currency
	}
	var amount float64
	switch pricing.Unit {
	case ServicePricingUnitToken:
		tokens := promptTokens + completionTokens
		if tokens > 0 && pricing.Per1KTokens > 0 {
			amount = float64(tokens) / 1000.0 * pricing.Per1KTokens
		}
	case ServicePricingUnitRun:
		if pricing.PerRun > 0 {
			amount = pricing.PerRun
		}
	case ServicePricingUnitSecond:
		if durationMs > 0 && pricing.PerSecond > 0 {
			amount = float64(durationMs) / 1000.0 * pricing.PerSecond
		}
	}
	if amount <= 0 {
		return charge
	}
	charge.Gross = int64(math.Round(amount * microsPerUnit))
	if feeBps > 0 {
		charge.Fee = int64(math.Round(float64(charge.Gross) * float64(feeBps) / 10000.0))
	}
	charge.Net = charge.Gross - charge.Fee
	if charge.Net < 0 {
		charge.Net = 0
	}
	return charge
}
