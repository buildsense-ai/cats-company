package server

import (
	"net/http"

	"github.com/openchat/openchat/server/db/mysql"
)

type UsageHandler struct {
	db *mysql.Adapter
}

func NewUsageHandler(db *mysql.Adapter) *UsageHandler {
	return &UsageHandler{db: db}
}

func (h *UsageHandler) HandleReportUsage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *UsageHandler) HandleGetUsage(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]interface{}{"usage": []interface{}{}})
}
