package server

import (
	"encoding/json"
	"io"
	"net/http"
	"regexp"
	"strings"
)

var memorySourcePin = regexp.MustCompile(`^ref_[a-f0-9]{64}$`)
var memorySourceRevision = regexp.MustCompile(`^[a-f0-9]{64}$`)
var memorySourceNodeID = regexp.MustCompile(`^(output-)?[a-f0-9]{64}$`)

type memorySourceReader struct {
	Kind          string                 `json:"kind"`
	ProjectionRef string                 `json:"projection_ref,omitempty"`
	Revision      string                 `json:"revision,omitempty"`
	MemberRef     string                 `json:"member_ref,omitempty"`
	NodeID        string                 `json:"node_id,omitempty"`
	Anchor        map[string]interface{} `json:"anchor,omitempty"`
}

// HandleMemoryRead reuses the existing owner check, account bearer forwarding,
// time/response bounds and private response policy. No device capability or
// operator credential is minted for the browser.
func (h *DailyNotesHandler) HandleMemoryRead(w http.ResponseWriter, r *http.Request) {
	reader := *h
	reader.queryPath = "/catsco/user/knowledge/v1/memory/read"
	reader.buildQuery = memorySourcesUpstreamQuery
	reader.HandleQuery(w, r)
}
func memorySourcesUpstreamQuery(r *http.Request, agentUID int64) ([]byte, bool) {
	var q memorySourceReader
	dec := json.NewDecoder(io.LimitReader(r.Body, 8<<10))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if dec.Decode(&q) != nil || dec.Decode(&struct{}{}) != io.EOF || !validMemorySourceReader(q) {
		return nil, false
	}
	out := map[string]interface{}{"agent_id": formatUID(agentUID)}
	switch q.Kind {
	case "projection":
		out["operation"] = "memory_projection"
		out["projection_ref"] = q.ProjectionRef
	case "assertion":
		out["operation"] = "memory_assertion"
		out["assertion"] = map[string]interface{}{"projection_ref": q.ProjectionRef, "member_ref": q.MemberRef, "node_id": q.NodeID, "max_bytes": 65536, "max_nodes": 64}
	case "source":
		out["operation"] = "source"
		out["source"] = map[string]interface{}{"anchor": q.Anchor, "before": 0, "after": 0, "max_bytes": 65536}
	}
	body, e := json.Marshal(out)
	return body, e == nil
}
func validMemorySourceReader(q memorySourceReader) bool {
	switch q.Kind {
	case "projection":
		return memorySourcePin.MatchString(q.ProjectionRef) && (q.Revision == "" || memorySourceRevision.MatchString(q.Revision)) && q.MemberRef == "" && q.NodeID == "" && q.Anchor == nil
	case "assertion":
		return memorySourcePin.MatchString(q.ProjectionRef) && memorySourcePin.MatchString(q.MemberRef) && memorySourceNodeID.MatchString(q.NodeID) && q.Revision == "" && q.Anchor == nil
	case "source":
		if q.ProjectionRef != "" || q.Revision != "" || q.MemberRef != "" || q.NodeID != "" || q.Anchor == nil {
			return false
		}
		for key, value := range q.Anchor {
			switch key {
			case "byte_offset", "byte_length":
				number, ok := value.(json.Number)
				if !ok {
					return false
				}
				n, e := number.Int64()
				if e != nil || n < 0 {
					return false
				}
			case "kind", "id", "revision", "session_id", "session_type", "stream_id", "source_projection_ref":
				text, ok := value.(string)
				if !ok || !boundedMemoryID(text) {
					return false
				}
			default:
				return false
			}
		}
		id, _ := q.Anchor["id"].(string)
		if !boundedMemoryID(id) {
			return false
		}
		kind, _ := q.Anchor["kind"].(string)
		if kind == "source_memory_projection_node" {
			ref, _ := q.Anchor["source_projection_ref"].(string)
			rev, _ := q.Anchor["revision"].(string)
			return memorySourcePin.MatchString(ref) && memorySourceRevision.MatchString(rev) && memorySourceNodeID.MatchString(id)
		}
		return kind == "session_query" || kind == "session_result" || kind == "learning_node"
	}
	return false
}
func boundedMemoryID(value string) bool {
	return value != "" && len(value) <= 256 && !strings.ContainsAny(value, "\x00\r\n\t")
}
