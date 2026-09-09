package types

import "encoding/json"

type CommercialRecordsQuery struct {
	Kind   string
	UID    int64
	Search string
	Status string
	Limit  int
	Offset int
}

type CommercialRecordsPage struct {
	Records json.RawMessage `json:"records"`
	Total   int64           `json:"total"`
	Limit   int             `json:"limit"`
	Offset  int             `json:"offset"`
	HasMore bool            `json:"has_more"`
}
