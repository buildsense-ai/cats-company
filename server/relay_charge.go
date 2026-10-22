package server

import (
	"context"
	"fmt"
	"net/http"
	"strings"
)

// 平台 → relay 的用户代扣通道（设计 §4.5 闭环）。
//
// 外部执行后端（http / mcp / worker）自报用量后，账本按发布者定价算出调用方
// 应付金额，落到 service_run_ledger（billing_state=pending_credit）；补扣扫描
// 再调 relay 的 /internal/users/<uid>/charge 把金额记进调用方当月 current_usage。
// request_id 用 run_id：relay 侧幂等（相同 request_id 只记一次），平台侧重复
// 补扣（例如扣费成功但状态没写回）只会拿到 duplicate，不会重复扣钱。

// ServiceUserCharger 是「给某个 uid 记一笔平台代扣」的最小接口
// （*RelayAdminClient 实现；测试注入替身）。
type ServiceUserCharger interface {
	// ChargeUser 记一笔代扣。duplicate=true 表示 relay 已有同 request_id 的
	// 记录（幂等重放，等同成功）。
	ChargeUser(ctx context.Context, uid int64, requestID string, amountCNY float64, note string) (duplicate bool, err error)
}

// relayChargeResponse 对应 relay handle_internal_charge 的响应体。
type relayChargeResponse struct {
	OK                   bool    `json:"ok"`
	UID                  int64   `json:"uid"`
	RequestID            string  `json:"request_id"`
	AmountCNY            float64 `json:"amount_cny"`
	Duplicate            bool    `json:"duplicate"`
	ManualChargeTotalCNY float64 `json:"manual_charge_total_cny"`
}

// ChargeUser 调 relay 内部代扣接口（request_id 幂等）。
func (c *RelayAdminClient) ChargeUser(ctx context.Context, uid int64, requestID string, amountCNY float64, note string) (bool, error) {
	if c == nil {
		return false, relayAdminError{status: http.StatusServiceUnavailable, message: "relay admin is not configured"}
	}
	requestID = strings.TrimSpace(requestID)
	if uid <= 0 {
		return false, relayAdminError{status: http.StatusBadRequest, message: "uid is required"}
	}
	if requestID == "" {
		return false, relayAdminError{status: http.StatusBadRequest, message: "request_id is required"}
	}
	if amountCNY <= 0 {
		return false, relayAdminError{status: http.StatusBadRequest, message: "amount_cny must be positive"}
	}
	body := map[string]interface{}{
		"request_id": requestID,
		"amount_cny": amountCNY,
	}
	if note = strings.TrimSpace(note); note != "" {
		body["note"] = note
	}
	var out relayChargeResponse
	if err := c.Do(ctx, http.MethodPost, fmt.Sprintf("/internal/users/%d/charge", uid), body, &out); err != nil {
		return false, err
	}
	if !out.OK {
		return false, relayAdminError{status: http.StatusBadGateway, message: "relay charge was not acknowledged"}
	}
	return out.Duplicate, nil
}
