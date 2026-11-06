package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store"
)

const publicationDescriptionReleaseStatusPath = "/api/skills/publication-descriptions/internal/release-status"

// The status-only reconciler must acquire its slot before claiming a lease.
// Keep its capacity aligned with this client's shared planning/read budget.
const publicationDescriptionReleaseStatusConcurrency = 2

var (
	ErrSkillHubPublicationDescriptionReleaseStatusDisabled = errors.New("skillhub publication description release status is disabled")
	ErrSkillHubPublicationDescriptionReleaseStatusPeer     = errors.New("skillhub publication description release status peer rejected the request")
)

type publicationDescriptionCloudReleaseStatus struct {
	SchemaVersion      int                                   `json:"schemaVersion"`
	ApplicationID      string                                `json:"applicationId"`
	TaskID             string                                `json:"taskId"`
	PermitID           string                                `json:"permitId"`
	PublicationReceipt publicationCloudReceiptRequest        `json:"publicationReceipt"`
	State              string                                `json:"state"`
	Target             store.SkillHubPublicationCommitTarget `json:"target"`
	Result             *PublicationCommitCloudResult         `json:"result"`
	ErrorCode          *string                               `json:"errorCode"`
}

// ReadRetainedRelease returns historical publication evidence only. It never
// admits, authorizes, retries a worker or refreshes current execution authority.
// A caller must authenticate its current actor before projecting this evidence.
func (c *PublicationDescriptionDerivationClient) ReadRetainedRelease(ctx context.Context,
	app store.SkillHubPublicationDescriptionApplication, intent store.SkillHubPublicationDescriptionReleaseIntent,
	permit publicationDescriptionCloudReleasePermit,
) (*publicationDescriptionCloudReleaseStatus, error) {
	if c == nil || !c.enabled || !c.releaseStatusEnabled {
		return nil, ErrSkillHubPublicationDescriptionReleaseStatusDisabled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	app = snapshotPublicationDescriptionApplication(app)
	if validatePublicationDescriptionCopyApplication(app) != nil || app.Input.Receipt.Query.Issuer != c.issuer ||
		app.Input.Receipt.Query.Audience != c.audience || permit.validateFor(app, intent) != nil ||
		intent.Target.CandidateHash != app.Result.Derivation.DerivedCandidateHash {
		return nil, ErrSkillHubPublicationDescriptionReleaseStatusPeer
	}
	if c.planActive.Add(1) > publicationDescriptionReleaseStatusConcurrency {
		c.planActive.Add(-1)
		return nil, store.ErrSkillHubPublicationBusy
	}
	defer c.planActive.Add(-1)
	body, err := json.Marshal(struct {
		SchemaVersion      int                            `json:"schemaVersion"`
		ApplicationID      string                         `json:"applicationId"`
		PermitID           string                         `json:"permitId"`
		PublicationReceipt publicationCloudReceiptRequest `json:"publicationReceipt"`
	}{1, app.ID, permit.PermitID, publicationControlReceipt(app.Input.Receipt)})
	if err != nil || len(body) > 8192 {
		return nil, ErrSkillHubPublicationDescriptionReleaseStatusPeer
	}
	endpoint := *c.endpoint
	endpoint.Path, endpoint.RawPath = publicationDescriptionReleaseStatusPath, ""
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, ErrSkillHubPublicationDescriptionReleaseStatusPeer
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-store")
	resp, err := c.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, ErrSkillHubPublicationDescriptionReleaseStatusPeer
	}
	defer resp.Body.Close()
	for _, key := range []string{"Content-Type", "Content-Length", "Content-Encoding", "Cache-Control"} {
		if len(resp.Header.Values(key)) > 1 {
			return nil, ErrSkillHubPublicationDescriptionReleaseStatusPeer
		}
	}
	noStore := false
	for _, directive := range strings.Split(resp.Header.Get("Cache-Control"), ",") {
		noStore = noStore || strings.EqualFold(strings.TrimSpace(directive), "no-store")
	}
	if resp.Header.Get("Content-Type") != "application/json; charset=utf-8" ||
		len(resp.Header.Values("Content-Encoding")) != 0 || resp.Uncompressed || !noStore || resp.ContentLength > 8192 {
		return nil, ErrSkillHubPublicationDescriptionReleaseStatusPeer
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8193))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || len(raw) == 0 || len(raw) > 8192 || !utf8.Valid(raw) ||
		(resp.ContentLength >= 0 && resp.ContentLength != int64(len(raw))) || resp.StatusCode != http.StatusOK {
		return nil, ErrSkillHubPublicationDescriptionReleaseStatusPeer
	}
	out, err := decodePublicationDescriptionReleaseStatus(raw)
	if err != nil || out.ApplicationID != app.ID || out.TaskID != app.Input.Receipt.TaskID ||
		out.PermitID != permit.PermitID || out.PublicationReceipt != publicationControlReceipt(app.Input.Receipt) ||
		out.Target != intent.Target || out.Target.CandidateHash != app.Result.Derivation.DerivedCandidateHash {
		return nil, ErrSkillHubPublicationDescriptionReleaseStatusPeer
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func decodePublicationDescriptionReleaseStatus(raw []byte) (*publicationDescriptionCloudReleaseStatus, error) {
	bad := ErrSkillHubPublicationDescriptionReleaseStatusPeer
	// Preserve duplicate-key detection and permit null only for result/errorCode.
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return nil, bad
	}
	allowed := map[string]bool{"schemaVersion": false, "applicationId": false, "taskId": false, "permitId": false,
		"publicationReceipt": false, "state": false, "target": false, "result": true, "errorCode": true}
	fields := map[string]json.RawMessage{}
	for d.More() {
		token, err = d.Token()
		key, ok := token.(string)
		nullable, known := allowed[key]
		if err != nil || !ok || !known || fields[key] != nil {
			return nil, bad
		}
		var value json.RawMessage
		if d.Decode(&value) != nil || (!nullable && bytes.Equal(bytes.TrimSpace(value), []byte("null"))) {
			return nil, bad
		}
		fields[key] = value
	}
	if _, err = d.Token(); err != nil {
		return nil, bad
	}
	if _, err = d.Token(); err != io.EOF || len(fields) != len(allowed) {
		return nil, bad
	}
	var out publicationDescriptionCloudReleaseStatus
	if json.Unmarshal(raw, &out) != nil || out.SchemaVersion != 1 ||
		!oneOf(out.State, "queued", "publishing", "committing", "published", "publish_failed") {
		return nil, bad
	}
	if _, err := decodePublicationCloudReceipt(fields["publicationReceipt"]); err != nil {
		return nil, bad
	}
	if _, err := publicationExactObject(fields["target"], "candidateHash", "manifestHash", "publisherUserId", "publisherProfileId", "namespace", "skillId", "version", "uploadId", "releaseAt", "confirmVersionPublish"); err != nil || out.Target.Validate() != nil {
		return nil, bad
	}
	if out.State == "published" {
		p := out.Result
		if p == nil || out.ErrorCode != nil || p.SkillID != out.Target.SkillID || p.Version != out.Target.Version ||
			p.PublishedAt != out.Target.ReleaseAt || !publicationPackageID.MatchString(p.PackageID) || len(p.Checksum) != 64 || !isLowerHex(p.Checksum) {
			return nil, bad
		}
		if _, err := publicationExactObject(fields["result"], "skillId", "version", "packageId", "checksumSha256", "publishedAt"); err != nil {
			return nil, bad
		}
	} else if out.Result != nil {
		return nil, bad
	}
	if out.State == "publish_failed" {
		if out.ErrorCode == nil {
			return nil, bad
		}
		valid := false
		for _, suffix := range []string{"unavailable", "copy_changed", "rejected", "publisher_changed", "conflict", "artifact_changed", "invalid", "lease_lost"} {
			valid = valid || *out.ErrorCode == "skillhub.publication_description_release_commit_"+suffix
		}
		if !valid {
			return nil, bad
		}
	} else if out.ErrorCode != nil {
		return nil, bad
	}
	return &out, nil
}
