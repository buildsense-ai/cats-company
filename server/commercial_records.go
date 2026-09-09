package server

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/openchat/openchat/server/store/types"
)

type commercialRecordsStore interface {
	ListCommercialRecords(context.Context, types.CommercialRecordsQuery) (*types.CommercialRecordsPage, error)
}

func (h *CommercialOpsHandler) HandleRecords(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if _, ok := h.requireService(w, r, false); !ok {
		return
	}
	query := r.URL.Query()
	q := types.CommercialRecordsQuery{Kind: query.Get("kind"), Search: strings.TrimSpace(query.Get("q")), Status: strings.TrimSpace(query.Get("status")), Limit: 20}
	switch q.Kind {
	case "orders", "invites", "payment-events", "entitlements", "operator-events":
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid record kind"})
		return
	}
	for name, target := range map[string]*int{"limit": &q.Limit, "offset": &q.Offset} {
		if raw := query.Get(name); raw != "" {
			value, err := strconv.Atoi(raw)
			if err != nil || value < 0 {
				writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid pagination"})
				return
			}
			*target = value
		}
	}
	if raw := query.Get("uid"); raw != "" {
		value, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || value < 1 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid uid"})
			return
		}
		q.UID = value
	}
	if q.Limit < 1 || q.Limit > 100 || q.Offset > 1000000 || len(q.Search) > 200 || len(q.Status) > 64 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid record filters"})
		return
	}
	store, ok := h.store.(commercialRecordsStore)
	if !ok {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "record pagination unavailable"})
		return
	}
	page, err := store.ListCommercialRecords(r.Context(), q)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to load records"})
		return
	}
	writeJSON(w, http.StatusOK, page)
}
