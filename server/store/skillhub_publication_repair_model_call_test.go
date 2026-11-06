package store

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func modelCallFixture() SkillHubPublicationRepairModelCall {
	now := time.Now().UTC()
	return SkillHubPublicationRepairModelCall{
		CallID: "pubmodel_" + strings.Repeat("1", 32), CreatedAt: now,
		Input: SkillHubPublicationRepairModelCallInput{RequestHash: strings.Repeat("a", 64)},
		DispatchLease: SkillHubPublicationRepairLease{
			EntryID: "pubentry_" + strings.Repeat("1", 32), TaskID: "pubrepair_" + strings.Repeat("2", 32),
			Round: 1, WorkerID: "worker", Token: strings.Repeat("3", 32), Generation: 1, Until: now.Add(time.Minute),
			Input: SkillHubPublicationRepairRoundInput{RequestID: "request-1", Evidence: SkillHubPublicationRepairEvidence{
				CandidateHash: strings.Repeat("a", 64), ReportHash: strings.Repeat("b", 64), IssueSetHash: strings.Repeat("c", 64)}},
		},
	}
}

func TestSkillHubPublicationRepairModelCallValidation(t *testing.T) {
	valid := modelCallFixture()
	if valid.Validate() != nil {
		t.Fatal("valid pending call rejected")
	}
	for name, mutate := range map[string]func(*SkillHubPublicationRepairModelCall){
		"id":           func(c *SkillHubPublicationRepairModelCall) { c.CallID = "pubmodel_" + strings.Repeat("X", 32) },
		"hash":         func(c *SkillHubPublicationRepairModelCall) { c.Input.RequestHash = "bad" },
		"lease":        func(c *SkillHubPublicationRepairModelCall) { c.DispatchLease.Round = 4 },
		"created":      func(c *SkillHubPublicationRepairModelCall) { c.CreatedAt = time.Time{} },
		"expired":      func(c *SkillHubPublicationRepairModelCall) { c.CreatedAt = c.DispatchLease.Until },
		"partial_hash": func(c *SkillHubPublicationRepairModelCall) { c.OutputHash = strings.Repeat("a", 64) },
		"partial_time": func(c *SkillHubPublicationRepairModelCall) { c.CompletedAt = &c.CreatedAt },
		"partial_output": func(c *SkillHubPublicationRepairModelCall) {
			c.Output = &SkillHubPublicationRepairModelOutput{Text: "text"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := valid
			mutate(&c)
			if c.Validate() == nil {
				t.Fatal("invalid call accepted")
			}
		})
	}
	output := SkillHubPublicationRepairModelOutput{Text: "未确认的草稿\n<script>not executable</script>"}
	valid.Output, valid.OutputHash = &output, output.Hash()
	completed := valid.CreatedAt.Add(time.Second)
	valid.CompletedAt = &completed
	if valid.Validate() != nil {
		t.Fatal("valid complete call rejected")
	}
	for name, mutate := range map[string]func(*SkillHubPublicationRepairModelCall){
		"empty": func(c *SkillHubPublicationRepairModelCall) { c.Output = &SkillHubPublicationRepairModelOutput{} },
		"null": func(c *SkillHubPublicationRepairModelCall) {
			c.Output = &SkillHubPublicationRepairModelOutput{Text: "a\x00b"}
		},
		"utf8": func(c *SkillHubPublicationRepairModelCall) {
			c.Output = &SkillHubPublicationRepairModelOutput{Text: string([]byte{0xff})}
		},
		"oversize": func(c *SkillHubPublicationRepairModelCall) {
			c.Output = &SkillHubPublicationRepairModelOutput{Text: strings.Repeat("x", SkillHubPublicationRepairModelOutputMaxBytes+1)}
		},
		"hash_mismatch": func(c *SkillHubPublicationRepairModelCall) { c.OutputHash = strings.Repeat("a", 64) },
		"missing_time":  func(c *SkillHubPublicationRepairModelCall) { c.CompletedAt = nil },
		"early":         func(c *SkillHubPublicationRepairModelCall) { at := c.CreatedAt.Add(-time.Second); c.CompletedAt = &at },
		"late":          func(c *SkillHubPublicationRepairModelCall) { c.CompletedAt = &c.DispatchLease.Until },
	} {
		t.Run(name, func(t *testing.T) {
			c := valid
			mutate(&c)
			if c.Validate() == nil {
				t.Fatal("invalid output accepted")
			}
		})
	}
	for _, value := range []any{valid, output} {
		raw, err := json.Marshal(value)
		if err != nil || strings.Contains(string(raw), "not executable") {
			t.Fatal("private model text serialized", err)
		}
	}
	if (SkillHubPublicationRepairModelOutput{Text: strings.Repeat("x", SkillHubPublicationRepairModelOutputMaxBytes)}).Validate() != nil {
		t.Fatal("bounded exact limit rejected")
	}
}
