package store

import (
	"strings"
	"testing"
)

func TestPublicationDescriptionReleaseTerminalRejectsNonterminalAndUnsafeEvidence(t *testing.T) {
	base := SkillHubPublicationDescriptionReleaseTerminal{ApplicationID: "pubdesc_" + strings.Repeat("a", 64),
		EntryID: "pubentry_" + strings.Repeat("a", 32), PermitID: "pubdescpermit_" + strings.Repeat("b", 32), IntentHash: strings.Repeat("c", 64),
		Target: SkillHubPublicationCommitTarget{CandidateHash: strings.Repeat("d", 64), ManifestHash: strings.Repeat("e", 64), PublisherUserID: "usr_test", PublisherProfileID: "pub_test", Namespace: "publisher", SkillID: "publisher/example", Version: "1.0.0", UploadID: "upl_test", ReleaseAt: "2026-10-28T01:00:00.000Z"},
		State:  "published", Result: &SkillHubPublicationDescriptionReleaseResult{SkillID: "publisher/example", Version: "1.0.0", PackageID: "pkg_12345678-1234-4123-8123-123456789abc", Checksum: strings.Repeat("f", 64), PublishedAt: "2026-10-28T01:00:00.000Z"}}
	if _, err := base.Digest(); err != nil {
		t.Fatal("fixture", err)
	}
	for _, mode := range []string{"queued", "unknown-failure", "result-on-failure", "missing-result", "wrong-version", "wrong-package", "wrong-time", "wrong-hash", "published-error"} {
		t.Run(mode, func(t *testing.T) {
			v := base
			p := *base.Result
			v.Result = &p
			switch mode {
			case "queued":
				v.State = "queued"
			case "unknown-failure":
				v.State = "publish_failed"
				v.Result = nil
				v.ErrorCode = "/private/error"
			case "result-on-failure":
				v.State = "publish_failed"
				v.ErrorCode = "skillhub.publication_description_release_commit_unavailable"
			case "missing-result":
				v.Result = nil
			case "wrong-version":
				v.Result.Version = "2.0.0"
			case "wrong-package":
				v.Result.PackageID = "pkg_private"
			case "wrong-time":
				v.Result.PublishedAt = "2020-01-01T00:00:00Z"
			case "wrong-hash":
				v.Result.Checksum = strings.Repeat("G", 64)
			case "published-error":
				v.ErrorCode = "skillhub.publication_description_release_commit_unavailable"
			}
			if _, err := v.Digest(); err == nil {
				t.Fatal("unsafe terminal accepted")
			}
		})
	}
}
