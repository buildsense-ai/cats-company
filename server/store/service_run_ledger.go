package store

import (
	"encoding/json"
	"time"
)

// run 级账本（设计 §4.5）：谁调用谁付费、按量计费。
// 金额口径：微元（1e-6 货币单位）整数，避免浮点累计误差。

// ServiceRunBillable 是一条待记账的终态 run（含归属与定义快照）。
type ServiceRunBillable struct {
	RunID            string
	SessionID        string
	AgentID          int64
	AgentSlug        string
	OwnerKind        string
	OwnerUID         int64
	CallerUID        int64
	CallerBotUID     int64
	RunnerType       string
	Status           string
	PromptTokens     int64
	CompletionTokens int64
	DurationMs       int64
	// DefinitionJSON 是会话钉定版本的定义（含 pricing 快照）。
	DefinitionJSON json.RawMessage
	FinishedAt     time.Time
}

// ServiceRunLedgerEntry 是写入账本的一行（金额已按当时 pricing 算好）。
type ServiceRunLedgerEntry struct {
	ServiceRunBillable
	PricingJSON json.RawMessage
	Currency    string
	GrossMicros int64
	FeeMicros   int64
	NetMicros   int64
	// PayerUID = 调用者（谁调用谁给钱）；PayeeUID = agent owner（internal → 0 = 平台自营）。
	PayerUID int64
	PayeeUID int64
}
