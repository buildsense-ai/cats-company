package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store"
)

type ModelCreditReader interface {
	GetModelCreditAccount(ctx context.Context, uid int64) (*store.ModelCreditAccount, error)
	ListModelCreditLedgerEvents(ctx context.Context, uid int64, limit, offset int) ([]store.ModelCreditLedgerEvent, error)
}

type modelCreditLedgerMutator interface {
	ReserveModelCredits(context.Context, store.ModelCreditReserveInput) (*store.ModelCreditReservation, *store.ModelCreditAccount, error)
	SettleModelCredits(context.Context, store.ModelCreditSettleInput) (*store.ModelCreditReservation, *store.ModelCreditAccount, error)
	MarkModelCreditNoUsage(context.Context, store.ModelCreditNoUsageConfirmation) (*store.ModelCreditReservation, error)
	ReleaseModelCredits(context.Context, store.ModelCreditReleaseInput) (*store.ModelCreditReservation, *store.ModelCreditAccount, error)
}

type ModelCreditHandler struct {
	store  ModelCreditReader
	ledger modelCreditLedgerMutator
}

func NewModelCreditHandler(reader ModelCreditReader) *ModelCreditHandler {
	ledger, _ := reader.(modelCreditLedgerMutator)
	return &ModelCreditHandler{store: reader, ledger: ledger}
}

var modelCreditPeerSecretPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)

// modelCreditAccountResponse 是 GET /api/relay/credits 的响应。
//
// enabled / allowed 是这一轮新增的显式身份字段（旧字段全部保留，向后兼容）：
//
//   - enabled 是 credit 身份：这个账户是否进过 credit 体系。真正的 legacy 老用户是
//     false，前端据此显示老额度说明而不是 credit 余额页。
//   - allowed 是当期权限：套餐过期后 enabled 仍为 true、allowed 变成 false，这样前端
//     和 Relay 都不会把「过期」误当成「legacy 免费」。
type modelCreditAccountResponse struct {
	UID            int64 `json:"uid"`
	Enabled        bool  `json:"enabled"`
	Allowed        bool  `json:"allowed"`
	MonthlyUnits   int64 `json:"monthly_units"`
	GiftUnits      int64 `json:"gift_units"`
	PaidUnits      int64 `json:"paid_units"`
	DebtUnits      int64 `json:"debt_units"`
	UnitsPerCredit int64 `json:"units_per_credit"`
}

func (h *ModelCreditHandler) HandleAccount(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	uid := UIDFromContext(r.Context())
	if uid <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if h == nil || h.store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "credit account unavailable"})
		return
	}
	response := modelCreditAccountResponse{UID: uid, UnitsPerCredit: store.ModelCreditUnitsPerCredit}
	// The monthly bucket is only valid for the period `now` falls in: reconcile
	// before answering so the user never sees a stale period's allowance as
	// spendable. PostgreSQL implements the hook; other stores just skip it.
	//
	// 资格读取顺带完成月额度对账，所以能做到「一次事务既给身份又给当期余额」；没有
	// 这个能力的存储退回单独对账，身份字段保持 false（=未进 credit 体系）。身份读
	// 失败不能吞掉余额：退回单独对账并留下日志，别让一次只读把用户的套餐状态说错。
	if identity, ok := h.store.(ModelCreditEligibilityReader); ok && identity != nil {
		if enabled, _, allowance, identityErr := identity.GetModelCreditEligibility(r.Context(), uid); identityErr != nil {
			log.Printf("model credit identity read failed: uid=%d err=%v", uid, identityErr)
			reconcileModelCreditMonthlyBeforeRead(r.Context(), h.store, uid)
		} else {
			response.Enabled = enabled
			response.Allowed = enabled && allowance > 0
		}
	} else {
		reconcileModelCreditMonthlyBeforeRead(r.Context(), h.store, uid)
	}
	account, err := h.store.GetModelCreditAccount(r.Context(), uid)
	if err != nil && !errors.Is(err, store.ErrModelCreditNotFound) {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load credit account"})
		return
	}
	if account != nil {
		response.MonthlyUnits = account.MonthlyUnits
		response.GiftUnits = account.GiftUnits
		response.PaidUnits = account.PaidUnits
		response.DebtUnits = account.DebtUnits
	}
	writeJSON(w, http.StatusOK, response)
}

type modelCreditLedgerEventResponse struct {
	Bucket         string `json:"bucket"`
	EventType      string `json:"event_type"`
	Units          int64  `json:"units"`
	MonthlyBalance int64  `json:"monthly_balance"`
	GiftBalance    int64  `json:"gift_balance"`
	PaidBalance    int64  `json:"paid_balance"`
	DebtBalance    int64  `json:"debt_balance"`
	CreatedAt      string `json:"created_at"`
}

func (h *ModelCreditHandler) HandleLedger(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	uid := UIDFromContext(r.Context())
	if uid <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if h == nil || h.store == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "credit ledger unavailable"})
		return
	}
	limit, offset := 50, 0
	var err error
	if value := strings.TrimSpace(r.URL.Query().Get("limit")); value != "" {
		limit, err = strconv.Atoi(value)
	}
	if err == nil {
		if value := strings.TrimSpace(r.URL.Query().Get("offset")); value != "" {
			offset, err = strconv.Atoi(value)
		}
	}
	if err != nil || limit < 1 || limit > 100 || offset < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid pagination"})
		return
	}
	events, err := h.store.ListModelCreditLedgerEvents(r.Context(), uid, limit, offset)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load credit ledger"})
		return
	}
	response := make([]modelCreditLedgerEventResponse, 0, len(events))
	for _, event := range events {
		response = append(response, modelCreditLedgerEventResponse{
			Bucket: event.Bucket, EventType: event.EventType, Units: event.Units,
			MonthlyBalance: event.MonthlyBalance, GiftBalance: event.GiftBalance,
			PaidBalance: event.PaidBalance, DebtBalance: event.DebtBalance,
			CreatedAt: event.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	writeJSON(w, http.StatusOK, map[string]interface{}{"events": response, "limit": limit, "offset": offset})
}

type modelCreditMutationRequest struct {
	UID       int64  `json:"uid"`
	RequestID string `json:"request_id"`
	Units     int64  `json:"units"`
	// RootTaskID 是这笔调用所属的原始任务。reserve 必填（缺了就是硬错误，不能绕过
	// 任务门禁）；settle/release/confirm-no-usage 不用带。
	RootTaskID string `json:"root_task_id"`
}

type modelCreditMutationResponse struct {
	Reservation *store.ModelCreditReservation `json:"reservation"`
	Account     *modelCreditAccountResponse   `json:"account,omitempty"`
}

func (h *ModelCreditHandler) HandleInternalMutation(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	secret := strings.TrimSpace(os.Getenv("CATSCO_RELAY_CREDIT_PEER_SECRET"))
	auth := r.Header.Values("Authorization")
	if !modelCreditPeerSecretPattern.MatchString(secret) || len(auth) != 1 || subtle.ConstantTimeCompare([]byte(auth[0]), []byte("Bearer "+secret)) != 1 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if h == nil || h.ledger == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "credit settlement unavailable"})
		return
	}
	var input modelCreditMutationRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	if decoder.Decode(new(any)) != io.EOF {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		return
	}
	var reservation *store.ModelCreditReservation
	var account *store.ModelCreditAccount
	var err error
	switch strings.Trim(strings.TrimSpace(r.URL.Path), "/") {
	case "internal/relay/credits/reserve":
		reservation, account, err = h.ledger.ReserveModelCredits(r.Context(), store.ModelCreditReserveInput{
			UID: input.UID, RequestID: input.RequestID, RootTaskID: input.RootTaskID, Units: input.Units,
		})
	case "internal/relay/credits/settle":
		reservation, account, err = h.ledger.SettleModelCredits(r.Context(), store.ModelCreditSettleInput{UID: input.UID, RequestID: input.RequestID, ActualUnits: input.Units})
	case "internal/relay/credits/release":
		if input.Units != 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
			return
		}
		reservation, account, err = h.ledger.ReleaseModelCredits(r.Context(), store.ModelCreditReleaseInput{UID: input.UID, RequestID: input.RequestID})
	case "internal/relay/credits/confirm-no-usage":
		if input.Units != 0 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
			return
		}
		reservation, err = h.ledger.MarkModelCreditNoUsage(r.Context(), store.ModelCreditNoUsageConfirmation{UID: input.UID, RequestID: input.RequestID})
	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if err != nil {
		switch {
		case errors.Is(err, store.ErrModelCreditInvalid):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request"})
		case errors.Is(err, store.ErrModelCreditInsufficient):
			writeJSON(w, http.StatusPaymentRequired, map[string]string{"error": "insufficient credit"})
		case errors.Is(err, store.ErrModelCreditConflict):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "request conflict"})
		case errors.Is(err, store.ErrModelCreditNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "reservation not found"})
		case errors.Is(err, store.ErrModelCreditTaskRequired):
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "root task id required"})
		case errors.Is(err, store.ErrModelCreditTaskNotFound):
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "root task not found"})
		case errors.Is(err, store.ErrModelCreditTaskNotRunning):
			writeJSON(w, http.StatusConflict, map[string]string{"error": "root task is not running"})
		default:
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "credit mutation failed"})
		}
		return
	}
	response := modelCreditMutationResponse{Reservation: reservation}
	if account != nil {
		// 能走到这里的用户一定在 credit 模型里（预留/结算都过账本），所以身份是 true，
		// allowed 表示结算之后还有可花额度。
		response.Account = &modelCreditAccountResponse{
			UID: account.UID, Enabled: true, Allowed: account.SpendableUnits() > 0,
			MonthlyUnits: account.MonthlyUnits, GiftUnits: account.GiftUnits,
			PaidUnits: account.PaidUnits, DebtUnits: account.DebtUnits,
			UnitsPerCredit: store.ModelCreditUnitsPerCredit,
		}
	}
	writeJSON(w, http.StatusOK, response)
}
