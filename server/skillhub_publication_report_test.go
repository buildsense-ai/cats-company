package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func publicationReportFixture(r store.SkillHubPublicationCloudReceipt) *PublicationCloudValidationReport {
	index := int64(0)
	return &PublicationCloudValidationReport{SchemaVersion: 1, Receipt: publicationControlReceipt(r), Validation: PublicationValidationReport{
		SchemaVersion: "1", RuleVersion: "skillhub-publish-v1.0.1", Profile: "skillhub-publish-v1", ReportID: "12345678-1234-4123-8123-123456789abc",
		Outcome: "rejected", Coverage: "complete", CandidateHash: r.Query.SourceSnapshotHash, LimitsFingerprint: strings.Repeat("b", 64),
		NextAction: "review_report", Issues: []PublicationValidationIssue{{Code: "metadata.description_missing", Category: "format", Blocking: true,
			Location: PublicationValidationLocation{EntryIndex: &index, RelativePath: "SKILL.md", Field: "description"},
			Message:  "入口缺少 description；可生成草稿，但确认后才能写入发布副本。", RepairPolicy: "confirmation_required"}},
	}}
}

func TestSkillHubPublicationCommitClientReportStrictTransport(t *testing.T) {
	for _, mode := range []string{"success", "automatic_name", "large_valid", "passed", "wrong_receipt", "wrong_candidate", "duplicate_report", "duplicate_issue", "duplicate_location",
		"private_field", "private_path", "empty_location_field", "automatic_description", "null", "wrong_type", "wrong_schema", "unknown_coverage", "unknown_action", "lost_rejection",
		"automatic_wrong_code", "automatic_wrong_category", "automatic_wrong_category_package_safety", "automatic_nonblocking", "automatic_wrong_field", "automatic_wrong_path_skill_json", "automatic_wrong_path_empty",
		"too_many_issues", "issue_budget", "report_budget", "transport_budget", "redirect", "encoding", "no_cache", "lost_reply", "outage"} {
		t.Run(mode, func(t *testing.T) {
			expected := cancellationTestLease("https://hub.example").Receipt
			peer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "POST" || r.URL.Path != publicationCommitCloudPath || r.Header.Get("Authorization") != "Bearer "+strings.Repeat("a", 64) || r.Header.Get("Cookie") != "" {
					t.Error("wrong transport")
				}
				var in struct {
					SchemaVersion int                            `json:"schemaVersion"`
					Action        string                         `json:"action"`
					Receipt       publicationCloudReceiptRequest `json:"receipt"`
				}
				if json.NewDecoder(r.Body).Decode(&in) != nil || in.SchemaVersion != 1 || in.Action != "report" || in.Receipt != publicationControlReceipt(expected) {
					t.Error("request scope")
				}
				w.Header().Set("Content-Type", "application/json")
				w.Header().Set("Cache-Control", "no-store")
				out := publicationReportFixture(expected)
				switch mode {
				case "automatic_name":
					entryIndex := int64(0)
					out.Validation.Issues[0] = automaticNameReportIssue(&entryIndex)
				case "automatic_wrong_code":
					issue := automaticNameReportIssue(nil)
					issue.Code = "metadata.name_invalid"
					out.Validation.Issues[0] = issue
				case "automatic_wrong_category":
					issue := automaticNameReportIssue(nil)
					issue.Category = "compatibility"
					out.Validation.Issues[0] = issue
				case "automatic_wrong_category_package_safety":
					issue := automaticNameReportIssue(nil)
					issue.Category = "package_safety"
					out.Validation.Issues[0] = issue
				case "automatic_nonblocking":
					issue := automaticNameReportIssue(nil)
					issue.Blocking = false
					out.Validation.Issues[0] = issue
					out.Validation.Truncated = true
				case "automatic_wrong_field":
					issue := automaticNameReportIssue(nil)
					issue.Location.Field = "description"
					out.Validation.Issues[0] = issue
				case "automatic_wrong_path_skill_json":
					issue := automaticNameReportIssue(nil)
					issue.Location.RelativePath = "skill.json"
					out.Validation.Issues[0] = issue
				case "automatic_wrong_path_empty":
					issue := automaticNameReportIssue(nil)
					issue.Location.RelativePath = ""
					out.Validation.Issues[0] = issue
				case "large_valid", "report_budget":
					i := out.Validation.Issues[0]
					i.Message = strings.Repeat("x", 900)
					n := 10
					if mode == "report_budget" {
						n = 50
					}
					for len(out.Validation.Issues) < n {
						out.Validation.Issues = append(out.Validation.Issues, i)
					}
				case "passed":
					out.Validation.Outcome, out.Validation.NextAction = "passed", "none"
					out.Validation.Issues = []PublicationValidationIssue{}
				case "wrong_receipt":
					out.Receipt.EventID = "pubevent_00000000-0000-4000-8000-000000000000"
				case "wrong_candidate":
					out.Validation.CandidateHash = strings.Repeat("f", 64)
				case "private_path":
					out.Validation.Issues[0].Location.RelativePath = "private-secret/file.txt"
				case "automatic_description":
					out.Validation.Issues[0].RepairPolicy = "manual"
				case "wrong_schema":
					out.Validation.SchemaVersion = "2"
				case "unknown_coverage":
					out.Validation.Coverage = "partial"
				case "unknown_action":
					out.Validation.NextAction = "repair_now"
				case "lost_rejection":
					out.Validation.Issues = []PublicationValidationIssue{}
				case "too_many_issues":
					for len(out.Validation.Issues) < 51 {
						out.Validation.Issues = append(out.Validation.Issues, out.Validation.Issues[0])
					}
				case "issue_budget":
					out.Validation.Issues[0].Message = strings.Repeat("x", 1025)
				case "transport_budget":
					w.Write([]byte(strings.Repeat("x", 64*1024+1)))
					return
				case "redirect":
					w.Header().Set("Location", "https://other.invalid")
					w.WriteHeader(302)
					return
				case "encoding":
					w.Header().Set("Content-Encoding", "gzip")
				case "no_cache":
					w.Header().Del("Cache-Control")
				case "lost_reply":
					c, _, _ := w.(http.Hijacker).Hijack()
					c.Close()
					return
				case "outage":
					w.WriteHeader(503)
					w.Write([]byte(`{"error":{"code":"upstream","message":"private SQL password"}}`))
					return
				}
				b, _ := json.Marshal(out)
				text := string(b)
				switch mode {
				case "duplicate_report":
					text = strings.Replace(text, `"coverage":"complete"`, `"coverage":"complete","coverage":"complete"`, 1)
				case "duplicate_issue":
					text = strings.Replace(text, `"blocking":true`, `"blocking":true,"blocking":false`, 1)
				case "duplicate_location":
					text = strings.Replace(text, `"entryIndex":0`, `"entryIndex":0,"entryIndex":1`, 1)
				case "private_field":
					text = strings.Replace(text, `"location":{`, `"location":{"source":"private",`, 1)
				case "empty_location_field":
					text = strings.Replace(text, `"field":"description"`, `"field":""`, 1)
				case "null":
					text = strings.Replace(text, `"blocking":true`, `"blocking":null`, 1)
				case "wrong_type":
					text = strings.Replace(text, `"blocking":true`, `"blocking":"true"`, 1)
				}
				w.Write([]byte(text))
			}))
			defer peer.Close()
			expected.Query.Audience = peer.URL
			c, err := newPublicationCommitCloudClient(PublicationCommitCloudConfig{Enabled: true, Issuer: expected.Query.Issuer, Audience: peer.URL, PeerSecret: strings.Repeat("a", 64)}, peer.Client().Transport)
			if err != nil {
				t.Fatal(err)
			}
			out, err := c.Report(context.Background(), expected)
			success := mode == "success" || mode == "automatic_name" || mode == "large_valid" || mode == "passed"
			if success != (err == nil) {
				t.Fatalf("success=%v err=%v", success, err)
			}
			if success && out.Validation.CandidateHash != expected.Query.SourceSnapshotHash {
				t.Fatal("wrong report")
			}
		})
	}
}

func automaticNameReportIssue(entryIndex *int64) PublicationValidationIssue {
	return PublicationValidationIssue{Code: "metadata.name_whitespace", Category: "format", Blocking: true,
		Location: PublicationValidationLocation{EntryIndex: entryIndex, RelativePath: "SKILL.md", Field: "name"},
		Message:  "入口 name 不能包含首尾空白。", RepairPolicy: "automatic"}
}

type humanReportCloud struct {
	*humanCommitCloud
	report func(context.Context, store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error)
}

func (c *humanReportCloud) Report(ctx context.Context, r store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
	return c.report(ctx, r)
}
func humanReportRequest(t *testing.T, uid int64) *http.Request {
	r := humanCommitRequest(t, "report", "", uid)
	r.Method = "GET"
	return r
}
func TestSkillHubPublicationHumanCommitReportPrivateReadOnly(t *testing.T) {
	for _, tc := range []struct {
		uid    int64
		status int
	}{{0, 401}, {1, 200}, {2, 404}, {3, 403}, {4, 403}, {5, 403}, {6, 401}} {
		t.Run(string(rune('0'+tc.uid)), func(t *testing.T) {
			db, c, h := humanCommitFixture()
			calls := 0
			c.plan = func(context.Context) error { t.Fatal("report must not plan"); return nil }
			c.read = func(context.Context) error { t.Fatal("report must not read execution status"); return nil }
			c.authorize = func(context.Context) error { t.Fatal("report must not authorize"); return nil }
			h.cloud = &humanReportCloud{c, func(_ context.Context, r store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
				calls++
				return publicationReportFixture(r), nil
			}}
			w := humanCommitRun(t, h, humanReportRequest(t, tc.uid), tc.status)
			if tc.status == 200 {
				if calls != 1 || !strings.Contains(w.Body.String(), `"repairPolicy":"confirmation_required"`) {
					t.Fatal("missing original report")
				}
				for _, secret := range []string{"receipt", "pubtask_", "pubgrant_", "pubcandidate_", "requestHash", "publisherUserId"} {
					if strings.Contains(w.Body.String(), secret) {
						t.Fatal("private proof leak", secret)
					}
				}
			} else if calls != 0 {
				t.Fatal("unauthorized report fetch")
			}
			if db.records != 0 {
				t.Fatal("read recorded human intent")
			}
		})
	}
}
func TestSkillHubPublicationHumanCommitReportFailureAndHistory(t *testing.T) {
	for _, mode := range []string{"cancelled", "outage", "wrong_receipt", "unsafe_report", "authority_changed", "no_receipt", "post", "body", "query", "disabled"} {
		t.Run(mode, func(t *testing.T) {
			db, c, h := humanCommitFixture()
			calls := 0
			h.cloud = &humanReportCloud{c, func(_ context.Context, r store.SkillHubPublicationCloudReceipt) (*PublicationCloudValidationReport, error) {
				calls++
				out := publicationReportFixture(r)
				switch mode {
				case "outage":
					return nil, errors.New("private SQL token")
				case "wrong_receipt":
					out.Receipt.AcceptanceEpoch++
				case "unsafe_report":
					out.Validation.Issues[0].Location.RelativePath = "private-file"
				case "authority_changed":
					db.denied = true
				}
				return out, nil
			}}
			r := humanReportRequest(t, 1)
			want := 503
			switch mode {
			case "cancelled":
				db.entry.State = "cancelled"
				want = 200
			case "no_receipt":
				db.receipt.Query.EntryID = "bad" // Invalid persisted evidence.
			case "post":
				r.Method = "POST"
				want = 405
			case "body":
				r.ContentLength = 1
				want = 400
			case "query":
				r.URL.RawQuery = "actor=2"
				want = 400
			case "disabled":
				h.enabled = false
				want = 404
			}
			w := humanCommitRun(t, h, r, want)
			if db.records != 0 || strings.Contains(w.Body.String(), "private SQL") || strings.Contains(w.Body.String(), "private-file") {
				t.Fatal("write or secret leak")
			}
			if (mode == "post" || mode == "body" || mode == "query" || mode == "disabled" || mode == "no_receipt") && calls != 0 {
				t.Fatal("invalid request reached Cloud")
			}
		})
	}
}
