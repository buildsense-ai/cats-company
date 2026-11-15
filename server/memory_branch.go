package server

import (
	"bytes"
	"encoding/json"
	"io"
)

type memoryBranchNode struct {
	ID     string             `json:"id"`
	Kind   string             `json:"kind"`
	Label  string             `json:"label"`
	Ref    string             `json:"ref"`
	Reader memorySourceReader `json:"reader"`
}
type memoryBranchEdge struct {
	ID     string `json:"id"`
	From   string `json:"from"`
	To     string `json:"to"`
	Kind   string `json:"kind"`
	LinkID string `json:"link_id,omitempty"`
}
type memoryBranchSnapshot struct {
	Schema         string             `json:"schema"`
	BranchRunID    string             `json:"branch_run_id"`
	OriginTurnID   string             `json:"origin_turn_id"`
	Sequence       int64              `json:"sequence"`
	Status         string             `json:"status"`
	ConsumedTurnID string             `json:"consumed_turn_id,omitempty"`
	ObservationID  string             `json:"observation_id,omitempty"`
	Nodes          []memoryBranchNode `json:"nodes"`
	Edges          []memoryBranchEdge `json:"edges"`
	ReadRefs       []string           `json:"read_refs"`
	InjectedRefs   []string           `json:"injected_refs"`
	AnswerRefs     []string           `json:"answer_refs"`
	Truncated      bool               `json:"truncated,omitempty"`
}

// Validate the public contract at the shared ingestion choke point. In
// particular, raw bodies, generation proofs, tokens and caller scopes cannot
// hitch a ride in an extension field. Labels come from node kinds alone.
func normalizedMemoryBranch(content interface{}) (map[string]interface{}, bool) {
	data := []byte(normalizeContentText(content))
	if len(data) > 48<<10 {
		return nil, false
	}
	var event memoryBranchSnapshot
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	dec.UseNumber()
	if dec.Decode(&event) != nil || dec.Decode(&struct{}{}) != io.EOF {
		return nil, false
	}
	if event.Schema != "catsco.memory_branch.v1" || !boundedMemoryID(event.BranchRunID) || !boundedMemoryID(event.OriginTurnID) || event.Sequence < 1 || len(event.Nodes) > 64 || len(event.Edges) > 128 {
		return nil, false
	}
	switch event.Status {
	case "running", "ready", "empty", "failed", "cancelled", "expired", "injected", "answered", "deferred", "unavailable":
	default:
		return nil, false
	}
	read, ok := memoryRefSet(event.ReadRefs)
	if !ok {
		return nil, false
	}
	injected, ok := memoryRefSet(event.InjectedRefs)
	if !ok {
		return nil, false
	}
	answered, ok := memoryRefSet(event.AnswerRefs)
	if !ok {
		return nil, false
	}
	for ref := range injected {
		if !read[ref] {
			return nil, false
		}
	}
	for ref := range answered {
		if !injected[ref] {
			return nil, false
		}
	}
	if len(injected) > 0 && (!boundedMemoryID(event.ConsumedTurnID) || !boundedMemoryID(event.ObservationID)) {
		return nil, false
	}
	ids := map[string]bool{}
	for i, node := range event.Nodes {
		if !boundedMemoryID(node.ID) || !boundedMemoryID(node.Kind) || ids[node.ID] || !read[node.Ref] || !validMemorySourceReader(node.Reader) {
			return nil, false
		}
		ids[node.ID] = true
		label := map[string]string{"assertion": "记忆断言", "condition": "适用条件", "omission": "已知缺口", "correction": "更正", "session_query": "历史提问", "session_result": "历史回答", "learning_node": "学习记录", "message": "原始消息", "tool_call": "工具调用"}[node.Kind]
		if label == "" {
			label = "记忆节点"
		}
		event.Nodes[i].Label = label
	}
	edgeIDs := map[string]bool{}
	for _, edge := range event.Edges {
		if !boundedMemoryID(edge.ID) || !boundedMemoryID(edge.Kind) || !ids[edge.From] || !ids[edge.To] || edgeIDs[edge.ID] || edge.LinkID != "" && !boundedMemoryID(edge.LinkID) {
			return nil, false
		}
		edgeIDs[edge.ID] = true
	}
	clean, e := json.Marshal(event)
	if e != nil {
		return nil, false
	}
	var out map[string]interface{}
	if json.Unmarshal(clean, &out) != nil {
		return nil, false
	}
	return out, true
}
func memoryRefSet(refs []string) (map[string]bool, bool) {
	if refs == nil || len(refs) > 64 {
		return nil, false
	}
	out := map[string]bool{}
	for _, ref := range refs {
		if !boundedMemoryID(ref) || out[ref] {
			return nil, false
		}
		out[ref] = true
	}
	return out, true
}
