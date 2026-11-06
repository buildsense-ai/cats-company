package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/openchat/openchat/server/store"
)

func releaseStatusFixture(app store.SkillHubPublicationDescriptionApplication, p publicationDescriptionCloudReleasePermit) publicationDescriptionCloudReleaseStatus {
	return publicationDescriptionCloudReleaseStatus{SchemaVersion: 1, ApplicationID: app.ID, TaskID: app.Input.Receipt.TaskID,
		PermitID: p.PermitID, PublicationReceipt: publicationControlReceipt(app.Input.Receipt), State: "queued", Target: p.Target}
}

func TestPublicationDescriptionReleaseStatusHTTPS(t *testing.T) {
	var app store.SkillHubPublicationDescriptionApplication
	var intent store.SkillHubPublicationDescriptionReleaseIntent
	var permit publicationDescriptionCloudReleasePermit
	var want publicationDescriptionCloudReleaseStatus
	calls := 0
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != http.MethodPost || r.URL.Path != publicationDescriptionReleaseStatusPath || r.Header.Get("Authorization") != "Bearer pubderive_"+strings.Repeat("a", 64) ||
			r.Header.Get("Cookie") != "" || r.Header.Get("Origin") != "" || r.Header.Get("Accept-Encoding") != "identity" {
			t.Error("unsafe request")
		}
		raw, _ := io.ReadAll(r.Body)
		fields, err := publicationExactObject(raw, "schemaVersion", "applicationId", "permitId", "publicationReceipt")
		var receipt publicationCloudReceiptRequest
		if err != nil || json.Unmarshal(fields["publicationReceipt"], &receipt) != nil || receipt != publicationControlReceipt(app.Input.Receipt) ||
			string(fields["applicationId"]) != `"`+app.ID+`"` || string(fields["permitId"]) != `"`+permit.PermitID+`"` {
			t.Error("receipt/permit changed")
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		_ = json.NewEncoder(w).Encode(want)
	}))
	defer s.Close()
	app, intent, permit = releaseAuthorizeClientFixture(t, s.URL)
	cfg := derivationClientConfig(s)
	cfg.ReleaseStatusEnabled = true // No plan, authorization, admission or copy-read gate needed.
	c, err := NewPublicationDescriptionDerivationClient(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, state := range []string{"queued", "publishing", "committing", "published", "publish_failed"} {
		want = releaseStatusFixture(app, permit)
		want.State = state
		if state == "published" {
			want.Result = &PublicationCommitCloudResult{SkillID: permit.Target.SkillID, Version: permit.Target.Version,
				PackageID: "pkg_12345678-1234-4123-8123-123456789abc", Checksum: strings.Repeat("a", 64), PublishedAt: permit.Target.ReleaseAt}
		}
		if state == "publish_failed" {
			code := "skillhub.publication_description_release_commit_unavailable"
			want.ErrorCode = &code
		}
		got, err := c.ReadRetainedRelease(context.Background(), app, intent, permit)
		if err != nil || got == nil || !reflect.DeepEqual(*got, want) {
			t.Fatalf("%s: got=%+v err=%v", state, got, err)
		}
	}
	if calls != 5 {
		t.Fatal("unexpected requests", calls)
	}
	before := calls
	c.releaseStatusEnabled = false
	if got, err := c.ReadRetainedRelease(context.Background(), app, intent, permit); got != nil || !errors.Is(err, ErrSkillHubPublicationDescriptionReleaseStatusDisabled) || calls != before {
		t.Fatal("default gate ignored", got, err)
	}
	c.releaseStatusEnabled = true
	foreign := permit
	foreign.Target.Version = "99.0.0"
	if got, err := c.ReadRetainedRelease(context.Background(), app, intent, foreign); got != nil || !errors.Is(err, ErrSkillHubPublicationDescriptionReleaseStatusPeer) || calls != before {
		t.Fatal("invalid permit sent", got, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.ReadRetainedRelease(ctx, app, intent, permit); !errors.Is(err, context.Canceled) || calls != before {
		t.Fatal("cancelled read sent", err)
	}
	c.planActive.Store(2)
	if _, err := c.ReadRetainedRelease(context.Background(), app, intent, permit); !errors.Is(err, store.ErrSkillHubPublicationBusy) || calls != before {
		t.Fatal("concurrency cap ignored", err)
	}
}

func TestPublicationDescriptionReleaseStatusRejectsUnsafeResponses(t *testing.T) {
	for _, mode := range []string{"application", "task", "permit", "receipt", "target", "null-state", "missing-result", "unknown", "duplicate", "nested-duplicate", "trailing", "early-result", "missing-result-published", "wrong-package", "wrong-version", "wrong-time", "wrong-checksum", "unknown-failure", "missing-failure", "transient-failure", "cacheable", "encoding", "duplicate-header", "redirect", "oversized", "lost-response", "http-error"} {
		t.Run(mode, func(t *testing.T) {
			var app store.SkillHubPublicationDescriptionApplication
			var intent store.SkillHubPublicationDescriptionReleaseIntent
			var permit publicationDescriptionCloudReleasePermit
			calls := 0
			s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if mode == "lost-response" {
					conn, _, _ := w.(http.Hijacker).Hijack()
					_ = conn.Close()
					return
				}
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set("Cache-Control", "no-store")
				out := releaseStatusFixture(app, permit)
				if mode == "application" {
					out.ApplicationID += "f"
				}
				if mode == "task" {
					out.TaskID += "f"
				}
				if mode == "permit" {
					out.PermitID += "f"
				}
				if mode == "receipt" {
					out.PublicationReceipt.SourceSnapshotHash = app.Result.Derivation.DerivedCandidateHash
				}
				if mode == "target" {
					out.Target.ManifestHash = strings.Repeat("f", 64)
				}
				if mode == "unknown" {
					out.State = "publish_queued"
				}
				if strings.HasPrefix(mode, "wrong-") || mode == "early-result" {
					out.State = "published"
					out.Result = &PublicationCommitCloudResult{SkillID: out.Target.SkillID, Version: out.Target.Version, PackageID: "pkg_12345678-1234-4123-8123-123456789abc", Checksum: strings.Repeat("a", 64), PublishedAt: out.Target.ReleaseAt}
					if mode == "early-result" {
						out.State = "committing"
					}
					if mode == "wrong-package" {
						out.Result.PackageID = "pkg_private-path"
					}
					if mode == "wrong-version" {
						out.Result.Version = "99.0.0"
					}
					if mode == "wrong-time" {
						out.Result.PublishedAt = "2000-01-01T00:00:00Z"
					}
					if mode == "wrong-checksum" {
						out.Result.Checksum = strings.Repeat("G", 64)
					}
				}
				if mode == "missing-result-published" {
					out.State = "published"
				}
				if mode == "missing-failure" {
					out.State = "publish_failed"
				}
				if mode == "unknown-failure" || mode == "transient-failure" {
					code := "/private/checkpoint"
					out.ErrorCode = &code
					if mode == "unknown-failure" {
						out.State = "publish_failed"
					}
				}
				if mode == "cacheable" {
					w.Header().Set("Cache-Control", "public")
				}
				if mode == "encoding" {
					w.Header().Set("Content-Encoding", "gzip")
				}
				if mode == "duplicate-header" {
					w.Header().Add("Content-Type", "application/json; charset=utf-8")
				}
				if mode == "redirect" {
					w.Header().Set("Location", "https://attacker.invalid")
					w.WriteHeader(302)
				}
				if mode == "http-error" {
					w.WriteHeader(503)
				}
				raw, _ := json.Marshal(out)
				text := string(raw)
				if mode == "null-state" {
					text = strings.Replace(text, `"state":"queued"`, `"state":null`, 1)
				}
				if mode == "missing-result" {
					text = strings.Replace(text, `,"result":null`, "", 1)
				}
				if mode == "duplicate" {
					text = strings.Replace(text, `"schemaVersion":1`, `"schemaVersion":1,"schemaVersion":1`, 1)
				}
				if mode == "nested-duplicate" {
					text = strings.Replace(text, `"candidateHash":`, `"candidateHash":"`+out.Target.CandidateHash+`","candidateHash":`, 1)
				}
				if mode == "trailing" {
					text += `{}`
				}
				if mode == "oversized" {
					text += strings.Repeat(" ", 8192)
				}
				_, _ = io.WriteString(w, text)
			}))
			defer s.Close()
			app, intent, permit = releaseAuthorizeClientFixture(t, s.URL)
			cfg := derivationClientConfig(s)
			cfg.ReleaseStatusEnabled = true
			c, err := NewPublicationDescriptionDerivationClient(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := c.ReadRetainedRelease(context.Background(), app, intent, permit); got != nil || !errors.Is(err, ErrSkillHubPublicationDescriptionReleaseStatusPeer) || calls != 1 {
				t.Fatalf("unsafe response accepted or retried: %+v %v %d", got, err, calls)
			}
		})
	}
}

func TestPublicationDescriptionReleaseStatusEnvironmentGate(t *testing.T) {
	const prefix = "CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_DERIVATION_"
	t.Setenv(prefix+"ENABLED", "true")
	t.Setenv(prefix+"URL", "https://hub.example"+publicationDescriptionDerivationPath)
	t.Setenv(prefix+"ISSUER", "https://cats.example")
	t.Setenv(prefix+"AUDIENCE", "https://hub.example")
	t.Setenv(prefix+"PEER_SECRET", "pubderive_"+strings.Repeat("a", 64))
	t.Setenv(prefix+"TIMEOUT_MS", "1000")
	const gate = "CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_RELEASE_STATUS_ENABLED"
	for _, value := range []string{"", "false", "invalid", "1", " TRUE "} {
		t.Setenv(gate, value)
		c, err := NewPublicationDescriptionDerivationClientFromEnv()
		if err != nil || c.releaseStatusEnabled != (value == "1" || value == " TRUE ") {
			t.Fatalf("gate %q: %+v %v", value, c, err)
		}
	}
	t.Setenv(prefix+"ENABLED", "false")
	if c, err := NewPublicationDescriptionDerivationClientFromEnv(); err != nil || c.releaseStatusEnabled {
		t.Fatal("status flag bypassed base gate", err)
	}
}
