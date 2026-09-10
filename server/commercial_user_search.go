package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/openchat/openchat/server/store/types"
)

type commercialUserFieldSearcher interface {
	SearchAdminUsers(query, field string, limit, offset int) ([]*types.User, int, error)
}

func (h *AccountAdminHandler) handleCommercialUserFieldSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	field := r.URL.Query().Get("search_by")
	if query == "" || len(query) > 256 || (field != "uid" && field != "name") ||
		(field == "uid" && (len(query) > 19 || strings.Trim(query, "0123456789") != "")) {
		writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid user search"})
		return
	}
	page := 1
	if raw := r.URL.Query().Get("page"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 10000 {
			writeAccountAdminJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid search page"})
			return
		}
		page = value
	}
	searcher, ok := h.users.(commercialUserFieldSearcher)
	if !ok {
		writeAccountAdminJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "user search unavailable"})
		return
	}
	const pageSize = 20
	users, total, err := searcher.SearchAdminUsers(query, field, pageSize, (page-1)*pageSize)
	if err != nil {
		writeAccountAdminJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to search users"})
		return
	}
	matches := make([]accountUserResponse, 0, len(users))
	for _, user := range users {
		if user != nil {
			matches = append(matches, accountUserPayload(user))
		}
	}
	writeAccountAdminJSON(w, http.StatusOK, map[string]interface{}{
		"users": matches, "total": total, "page": page, "page_size": pageSize,
		"has_more": page*pageSize < total, "search_by": field,
	})
}
