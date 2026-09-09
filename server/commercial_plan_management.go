package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/openchat/openchat/server/store/types"
)

type commercialPlanManagementStore interface {
	ListCommercialPlanUsage([]int64, string) ([]*types.CommercialPlanUsage, error)
	ListCommercialPlanUsers(int64, int64, int) (*types.CommercialPlanUsers, error)
	DeleteCommercialPlan(int64, string, string) error
}

func (h *AccountAdminHandler) handleCommercialPlanManagement(w http.ResponseWriter, r *http.Request, store CommercialStore) {
	management, ok := store.(commercialPlanManagementStore)
	if !ok {
		writeAccountAdminJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "套餐管理暂不可用"})
		return
	}
	trialSlug := strings.TrimSpace(os.Getenv("CATS_COMMERCIAL_TRIAL_PLAN_SLUG"))
	if r.Method == http.MethodPost && r.URL.Query().Get("action") == "delete" {
		var request struct {
			ID   int64  `json:"id"`
			Slug string `json:"slug"`
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.ID <= 0 || !commercialSlugPattern.MatchString(request.Slug) {
			writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "请确认要删除的套餐"})
			return
		}
		err := management.DeleteCommercialPlan(request.ID, request.Slug, trialSlug)
		if err != nil {
			status, message := http.StatusInternalServerError, "删除套餐失败"
			if errors.Is(err, types.ErrCommercialPlanNotFound) {
				status, message = http.StatusNotFound, err.Error()
			}
			if errors.Is(err, types.ErrCommercialPlanDeleteConflict) {
				status, message = http.StatusConflict, err.Error()
			}
			writeAccountAdminJSON(w, status, map[string]string{"error": message})
			return
		}
		writeAccountAdminJSON(w, http.StatusOK, map[string]any{"ok": true, "id": request.ID})
		return
	}
	if r.Method == http.MethodGet && r.URL.Query().Get("view") == "usage" {
		parts := strings.Split(r.URL.Query().Get("ids"), ",")
		if len(parts) > 50 {
			writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "每次最多查询 50 个套餐"})
			return
		}
		ids := make([]int64, 0, len(parts))
		for _, part := range parts {
			id, err := strconv.ParseInt(part, 10, 64)
			if err != nil || id <= 0 {
				writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "套餐 ID 无效"})
				return
			}
			ids = append(ids, id)
		}
		usage, err := management.ListCommercialPlanUsage(ids, trialSlug)
		if err != nil {
			writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "读取套餐使用情况失败"})
			return
		}
		writeAccountAdminJSON(w, http.StatusOK, map[string]any{"usage": usage})
		return
	}
	if r.Method == http.MethodGet && r.URL.Query().Get("view") == "users" {
		id, err := strconv.ParseInt(r.URL.Query().Get("plan_id"), 10, 64)
		after, afterErr := strconv.ParseInt(firstNonEmpty(r.URL.Query().Get("after_uid"), "0"), 10, 64)
		limit, limitErr := strconv.Atoi(firstNonEmpty(r.URL.Query().Get("limit"), "25"))
		if err != nil || id <= 0 || afterErr != nil || after < 0 || limitErr != nil || limit < 1 || limit > 100 {
			writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "分页参数无效"})
			return
		}
		users, err := management.ListCommercialPlanUsers(id, after, limit)
		if err != nil {
			writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "读取套餐用户失败"})
			return
		}
		writeAccountAdminJSON(w, http.StatusOK, users)
		return
	}
	writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "不支持的套餐操作"})
}
