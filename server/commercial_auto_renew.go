package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const (
	commercialAutoRenewRunApplied      = "applied"
	commercialAutoRenewRunFailed       = "failed"
	commercialAutoRenewActionExtend    = "extend"
	commercialAutoRenewRunListLimit    = 20
	commercialAutoRenewFrontendMessage = "已自动顺延一个周期（30 天）"
)

// CommercialAutoRenewStore persists the opt-in switch and the detailed run
// log. The Postgres adapter implements it; deployments without the tables
// simply do not start the runner or expose the console surface.
type CommercialAutoRenewStore interface {
	GetCommercialAutoRenewConfig(uid int64) (*types.CommercialAutoRenewConfig, error)
	SetCommercialAutoRenewConfig(uid int64, enabled bool, note string) (*types.CommercialAutoRenewConfig, error)
	ListDueCommercialAutoRenew(now time.Time, within time.Duration) ([]*types.CommercialAutoRenewDue, error)
	RecordCommercialAutoRenewRun(run *types.CommercialAutoRenewRun) error
	ListCommercialAutoRenewRuns(uid int64, limit int) ([]*types.CommercialAutoRenewRun, error)
}

// CommercialAutoRenewExecutor applies one renewal extension. It is implemented
// by AccountAdminHandler and faked in tests.
type CommercialAutoRenewExecutor interface {
	ApplyCommercialAutoRenewExtend(ctx context.Context, uid int64, expectedExpiry time.Time, operationID, note string) (*types.CommercialAccountAdjustmentResult, error)
}

// CommercialAutoRenewRunner is the internal scheduled renewer: every pass it
// extends the paid window of opted-in personal/pro accounts that fall inside
// the renewal lead time, reusing the exact ledger path of the manual
// 「顺延续费」 button (operator extension + Relay cycle sync + cloud-worker
// resume). Every attempt is recorded for the relay-admin console.
type CommercialAutoRenewRunner struct {
	store       CommercialAutoRenewStore
	executor    CommercialAutoRenewExecutor
	interval    time.Duration
	renewBefore time.Duration
}

// NewCommercialAutoRenewRunner builds the runner; renewBefore is the lead
// time before expiry at which a window is extended.
func NewCommercialAutoRenewRunner(store CommercialAutoRenewStore, executor CommercialAutoRenewExecutor, renewBefore time.Duration) *CommercialAutoRenewRunner {
	if renewBefore <= 0 {
		renewBefore = 7 * 24 * time.Hour
	}
	return &CommercialAutoRenewRunner{store: store, executor: executor, interval: time.Hour, renewBefore: renewBefore}
}

// Start launches the scheduler loop. The first pass runs shortly after
// startup so a restart heals an account that already slipped inside the lead
// time; later passes run hourly and never stop on failure.
func (r *CommercialAutoRenewRunner) Start(ctx context.Context) {
	if r == nil || r.store == nil || r.executor == nil {
		return
	}
	go func() {
		timer := time.NewTimer(2 * time.Minute)
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-timer.C:
				r.RunOnce(ctx)
				timer.Reset(r.interval)
			}
		}
	}()
}

// RunOnce extends every enabled account whose paid window is due and returns
// how many renewals were applied or failed.
func (r *CommercialAutoRenewRunner) RunOnce(ctx context.Context) (applied, failed int) {
	if r == nil || r.store == nil || r.executor == nil {
		return 0, 0
	}
	now := time.Now().UTC()
	due, err := r.store.ListDueCommercialAutoRenew(now, r.renewBefore)
	if err != nil {
		log.Printf("[commercial-auto-renew] list due accounts failed: %v", err)
		return 0, 0
	}
	for _, item := range due {
		if item == nil || item.UID <= 0 {
			continue
		}
		if r.renewOne(ctx, item) {
			applied++
		} else {
			failed++
		}
	}
	return applied, failed
}

func (r *CommercialAutoRenewRunner) renewOne(ctx context.Context, due *types.CommercialAutoRenewDue) bool {
	expiry := due.ExpiresAt.UTC()
	operationID := fmt.Sprintf("auto-renew-%d-%d", due.UID, expiry.Unix())
	run := &types.CommercialAutoRenewRun{UID: due.UID, Action: commercialAutoRenewActionExtend, OperationID: operationID}
	previous := expiry
	run.PreviousExpiry = &previous
	result, err := r.executor.ApplyCommercialAutoRenewExtend(ctx, due.UID, expiry, operationID, "内部自动续费")
	if err == nil && result == nil {
		err = fmt.Errorf("renewal result is unavailable")
	}
	if err == nil && result.Applied {
		run.Status = commercialAutoRenewRunApplied
		run.NewExpiry = result.ExpiresAt
		run.Message = commercialAutoRenewFrontendMessage
	} else if err == nil {
		// The deterministic operation id already exists: this window was
		// renewed by an earlier pass whose run-log write did not land.
		run.Status = commercialAutoRenewRunApplied
		run.NewExpiry = result.ExpiresAt
		run.Message = "该周期已顺延（重复执行已忽略）"
	} else {
		run.Status = commercialAutoRenewRunFailed
		run.Message = err.Error()
	}
	if recordErr := r.store.RecordCommercialAutoRenewRun(run); recordErr != nil {
		log.Printf("[commercial-auto-renew] record run failed uid=%d: %v", due.UID, recordErr)
	}
	if run.Status == commercialAutoRenewRunApplied {
		log.Printf("[commercial-auto-renew] uid=%d extended %s -> %s", due.UID, previous.Format(time.RFC3339), commercialAutoRenewExpiryText(result.ExpiresAt))
	} else {
		log.Printf("[commercial-auto-renew] uid=%d failed: %s", due.UID, run.Message)
	}
	return run.Status == commercialAutoRenewRunApplied
}

func commercialAutoRenewExpiryText(expiresAt *time.Time) string {
	if expiresAt == nil {
		return "(unknown)"
	}
	return expiresAt.UTC().Format(time.RFC3339)
}

// ApplyCommercialAutoRenewExtend runs the same renewal transaction as the
// manual 「顺延续费」 action for one account, with a deterministic operation id
// so scheduler retries stay idempotent.
func (h *AccountAdminHandler) ApplyCommercialAutoRenewExtend(ctx context.Context, uid int64, expectedExpiry time.Time, operationID, note string) (*types.CommercialAccountAdjustmentResult, error) {
	if h == nil || h.commercial == nil {
		return nil, fmt.Errorf("commercial store unavailable")
	}
	if operationID == "" {
		return nil, fmt.Errorf("operation id is required")
	}
	now := time.Now().UTC()
	preview, err := h.buildCommercialAdjustmentPreview(ctx, h.commercial, &commercialAdjustmentRequest{UID: uid, Action: commercialAdjustmentExtend}, now)
	if err != nil {
		return nil, err
	}
	if !preview.CanApply {
		return nil, fmt.Errorf("auto renew cannot apply: %s", strings.Join(preview.Warnings, "；"))
	}
	if preview.PreviousExpiresAt == nil || !preview.PreviousExpiresAt.UTC().Equal(expectedExpiry.UTC()) {
		return nil, fmt.Errorf("package expiry changed before the renewal ran")
	}
	adjustmentStore, ok := h.commercial.(commercialAccountAdjustmentStore)
	if !ok {
		return nil, fmt.Errorf("commercial adjustment store unavailable")
	}
	expected := expectedExpiry.UTC()
	result, err := adjustmentStore.ApplyCommercialAccountAdjustment(&types.CommercialAccountAdjustment{
		UID: uid, Action: commercialAdjustmentExtend, ExpectedExpiresAt: &expected,
		OperationID: operationID, Note: note, EffectiveAt: now,
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, fmt.Errorf("commercial adjustment result is unavailable")
	}
	if h.cloudWorkerRenewer != nil && commercialAdjustmentRenewsCloudWorkers(commercialAdjustmentExtend, result, preview) {
		go h.cloudWorkerRenewer(uid)
	}
	if err := h.applyCommercialAdjustmentToRelay(ctx, uid, commercialAdjustmentExtend, result); err != nil {
		// The ledger is committed; Relay sync stays recoverable through the
		// normal retry path so the renewal itself is never rolled back.
		if h.commercialRelaySyncer != nil {
			h.commercialRelaySyncer.Enqueue(uid)
		}
		log.Printf("[commercial-auto-renew] uid=%d renewed but relay sync is pending: %v", uid, err)
	}
	return result, nil
}

// HandleCommercialAutoRenew serves the internal console surface: GET returns
// the opt-in state plus recent runs, POST toggles the switch.
func (h *AccountAdminHandler) HandleCommercialAutoRenew(w http.ResponseWriter, r *http.Request) {
	store, ok := h.requireCommercialStore(w, r)
	if !ok {
		return
	}
	autoRenew, ok := store.(CommercialAutoRenewStore)
	if !ok {
		writeAccountAdminJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "commercial auto renew store unavailable"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		uid, err := strconvParsePositiveInt64(r.URL.Query().Get("uid"))
		if err != nil {
			writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid uid"})
			return
		}
		config, err := autoRenew.GetCommercialAutoRenewConfig(uid)
		if err != nil {
			writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load auto renew config"})
			return
		}
		if config == nil {
			config = &types.CommercialAutoRenewConfig{UID: uid}
		}
		runs, err := autoRenew.ListCommercialAutoRenewRuns(uid, commercialAutoRenewRunListLimit)
		if err != nil {
			writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load auto renew runs"})
			return
		}
		writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{"config": config, "runs": runs})
	case http.MethodPost:
		var req struct {
			UID     int64  `json:"uid"`
			Enabled bool   `json:"enabled"`
			Note    string `json:"note"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid auto renew request"})
			return
		}
		if req.UID <= 0 {
			writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "uid is required"})
			return
		}
		note := strings.TrimSpace(req.Note)
		if len(note) > 256 {
			writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "note is too long"})
			return
		}
		config, err := autoRenew.SetCommercialAutoRenewConfig(req.UID, req.Enabled, note)
		if err != nil {
			writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to save auto renew config"})
			return
		}
		writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{"ok": true, "config": config})
	default:
		writeAccountAdminJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}
