package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store"
)

const publicationDescriptionReleaseAuthorizePath = "/api/skills/publication-descriptions/internal/authorize"
const publicationDescriptionReleaseCommitPath = "/api/skills/publication-descriptions/internal/commit"

type publicationDescriptionCloudReleaseAdmission struct {
	SchemaVersion int    `json:"schemaVersion"`
	ApplicationID string `json:"applicationId"`
	TaskID        string `json:"taskId"`
	PermitID      string `json:"permitId"`
	State         string `json:"state"`
	Replayed      *bool  `json:"replayed"`
}

var (
	ErrSkillHubPublicationDescriptionReleaseAuthorizeDisabled = errors.New("skillhub publication description release authorization is disabled")
	ErrSkillHubPublicationDescriptionReleaseAuthorizePeer     = errors.New("skillhub publication description release authorization peer rejected the request")
	ErrSkillHubPublicationDescriptionReleaseCommitDisabled    = errors.New("skillhub publication description release commit is disabled")
	ErrSkillHubPublicationDescriptionReleaseCommitPeer        = errors.New("skillhub publication description release commit peer rejected the request")
)

type publicationDescriptionCloudReleasePermit struct {
	SchemaVersion int                                   `json:"schemaVersion"`
	ApplicationID string                                `json:"applicationId"`
	EntryID       string                                `json:"entryId"`
	Target        store.SkillHubPublicationCommitTarget `json:"target"`
	IntentHash    string                                `json:"intentHash"`
	PermitID      string                                `json:"permitId"`
	AuthorizedAt  string                                `json:"authorizedAt"`
	Replayed      *bool                                 `json:"replayed"`
}

func (p publicationDescriptionCloudReleasePermit) validateFor(app store.SkillHubPublicationDescriptionApplication, intent store.SkillHubPublicationDescriptionReleaseIntent) error {
	digest, err := intent.Digest()
	at, timeErr := time.Parse(time.RFC3339Nano, p.AuthorizedAt)
	permit := store.SkillHubPublicationDescriptionReleasePermit{Intent: intent, ID: p.PermitID, AuthorizedAt: at}
	if err != nil || timeErr != nil || !strings.HasSuffix(p.AuthorizedAt, "Z") || validatePublicationDescriptionCopyApplication(app) != nil || intent.ApplicationID != app.ID ||
		intent.EntryID != app.Input.Receipt.Query.EntryID || intent.IdentityHash != digest ||
		p.SchemaVersion != 1 || p.ApplicationID != app.ID || p.EntryID != intent.EntryID || p.Target != intent.Target ||
		p.IntentHash != intent.IdentityHash || p.Replayed == nil || permit.Validate() != nil {
		return store.ErrSkillHubPublicationConflict
	}
	return nil
}

// AuthorizeRetainedRelease asks Cloud to recover/consume the exact durable
// CatsCo release intent. Cloud still only stores a permit; it does not commit
// or publish this derived candidate.
func (c *PublicationDescriptionDerivationClient) AuthorizeRetainedRelease(ctx context.Context,
	app store.SkillHubPublicationDescriptionApplication, intent store.SkillHubPublicationDescriptionReleaseIntent,
) (*publicationDescriptionCloudReleasePermit, error) {
	if c == nil || !c.enabled || !c.planEnabled || !c.releaseAuthorizeEnabled {
		return nil, ErrSkillHubPublicationDescriptionReleaseAuthorizeDisabled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	app = snapshotPublicationDescriptionApplication(app)
	if validatePublicationDescriptionCopyApplication(app) != nil || app.Input.Receipt.Query.Issuer != c.issuer ||
		app.Input.Receipt.Query.Audience != c.audience || intent.ApplicationID != app.ID ||
		intent.EntryID != app.Input.Receipt.Query.EntryID || intent.Target.CandidateHash != app.Result.Derivation.DerivedCandidateHash {
		return nil, ErrSkillHubPublicationDescriptionDerivationInput
	}
	digest, err := intent.Digest()
	if err != nil || digest != intent.IdentityHash {
		return nil, ErrSkillHubPublicationDescriptionDerivationInput
	}
	if c.planActive.Add(1) > 2 {
		c.planActive.Add(-1)
		return nil, store.ErrSkillHubPublicationBusy
	}
	defer c.planActive.Add(-1)
	body, err := json.Marshal(struct {
		SchemaVersion      int                            `json:"schemaVersion"`
		ApplicationID      string                         `json:"applicationId"`
		PublicationReceipt publicationCloudReceiptRequest `json:"publicationReceipt"`
	}{1, app.ID, publicationControlReceipt(app.Input.Receipt)})
	if err != nil || len(body) > 8192 {
		return nil, ErrSkillHubPublicationDescriptionDerivationInput
	}
	endpoint := *c.endpoint
	endpoint.Path, endpoint.RawPath = publicationDescriptionReleaseAuthorizePath, ""
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, ErrSkillHubPublicationDescriptionDerivationInput
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
		return nil, ErrSkillHubPublicationDescriptionReleaseAuthorizePeer
	}
	defer resp.Body.Close()
	for _, key := range []string{"Content-Type", "Content-Length", "Content-Encoding", "Cache-Control"} {
		if len(resp.Header.Values(key)) > 1 {
			return nil, ErrSkillHubPublicationDescriptionReleaseAuthorizePeer
		}
	}
	noStore := false
	for _, directive := range strings.Split(resp.Header.Get("Cache-Control"), ",") {
		noStore = noStore || strings.EqualFold(strings.TrimSpace(directive), "no-store")
	}
	if resp.Header.Get("Content-Type") != "application/json; charset=utf-8" ||
		len(resp.Header.Values("Content-Encoding")) != 0 || resp.Uncompressed || !noStore || resp.ContentLength > 8192 {
		return nil, ErrSkillHubPublicationDescriptionReleaseAuthorizePeer
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8193))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || len(raw) == 0 || len(raw) > 8192 || !utf8.Valid(raw) ||
		(resp.ContentLength >= 0 && resp.ContentLength != int64(len(raw))) {
		return nil, ErrSkillHubPublicationDescriptionReleaseAuthorizePeer
	}
	if resp.StatusCode != http.StatusOK {
		parts, parseErr := publicationExactObject(raw, "error")
		if parseErr == nil {
			e, parseErr := publicationExactObject(parts["error"], "code", "message")
			var code, message string
			if parseErr == nil && json.Unmarshal(e["code"], &code) == nil && json.Unmarshal(e["message"], &message) == nil &&
				resp.StatusCode == http.StatusForbidden && code == "skillhub.publication_description_derivation_denied" {
				return nil, store.ErrSkillHubPublicationDenied
			}
		}
		return nil, ErrSkillHubPublicationDescriptionReleaseAuthorizePeer
	}
	parts, err := publicationExactObject(raw, "schemaVersion", "applicationId", "permit", "replayed")
	if err != nil {
		return nil, ErrSkillHubPublicationDescriptionReleaseAuthorizePeer
	}
	permit, err := publicationExactObject(parts["permit"], "schemaVersion", "applicationId", "entryId", "target", "intentHash", "permitId", "authorizedAt")
	if err != nil {
		return nil, ErrSkillHubPublicationDescriptionReleaseAuthorizePeer
	}
	if _, err := publicationExactObject(permit["target"], "candidateHash", "manifestHash", "publisherUserId", "publisherProfileId", "namespace", "skillId", "version", "uploadId", "releaseAt", "confirmVersionPublish"); err != nil {
		return nil, ErrSkillHubPublicationDescriptionReleaseAuthorizePeer
	}
	var out publicationDescriptionCloudReleasePermit
	var schemaVersion int
	var applicationID string
	if decodeUniqueJSON(parts["permit"], &out) != nil || json.Unmarshal(parts["schemaVersion"], &schemaVersion) != nil ||
		json.Unmarshal(parts["applicationId"], &applicationID) != nil || json.Unmarshal(parts["replayed"], &out.Replayed) != nil ||
		schemaVersion != 1 || applicationID != app.ID || out.validateFor(app, intent) != nil {
		return nil, ErrSkillHubPublicationDescriptionReleaseAuthorizePeer
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &out, nil
}

// AdmitRetainedRelease submits only an already durable, exact Cloud permit to
// the isolated derived-candidate queue. The Cloud operation is idempotent, so
// retrying after a lost response recovers the same admission rather than
// creating a second publication.
func (c *PublicationDescriptionDerivationClient) AdmitRetainedRelease(ctx context.Context,
	app store.SkillHubPublicationDescriptionApplication, intent store.SkillHubPublicationDescriptionReleaseIntent,
	permit publicationDescriptionCloudReleasePermit,
) (*publicationDescriptionCloudReleaseAdmission, error) {
	if c == nil || !c.enabled || !c.planEnabled || !c.releaseAuthorizeEnabled || !c.releaseCommitEnabled {
		return nil, ErrSkillHubPublicationDescriptionReleaseCommitDisabled
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	app = snapshotPublicationDescriptionApplication(app)
	if validatePublicationDescriptionCopyApplication(app) != nil || app.Input.Receipt.Query.Issuer != c.issuer ||
		app.Input.Receipt.Query.Audience != c.audience || permit.validateFor(app, intent) != nil {
		return nil, ErrSkillHubPublicationDescriptionReleaseCommitPeer
	}
	if c.planActive.Add(1) > 2 {
		c.planActive.Add(-1)
		return nil, store.ErrSkillHubPublicationBusy
	}
	defer c.planActive.Add(-1)
	body, err := json.Marshal(struct {
		SchemaVersion int    `json:"schemaVersion"`
		ApplicationID string `json:"applicationId"`
		PermitID      string `json:"permitId"`
	}{1, app.ID, permit.PermitID})
	if err != nil || len(body) > 1024 {
		return nil, ErrSkillHubPublicationDescriptionReleaseCommitPeer
	}
	endpoint := *c.endpoint
	endpoint.Path, endpoint.RawPath = publicationDescriptionReleaseCommitPath, ""
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, ErrSkillHubPublicationDescriptionReleaseCommitPeer
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
		return nil, ErrSkillHubPublicationDescriptionReleaseCommitPeer
	}
	defer resp.Body.Close()
	for _, key := range []string{"Content-Type", "Content-Length", "Content-Encoding", "Cache-Control"} {
		if len(resp.Header.Values(key)) > 1 {
			return nil, ErrSkillHubPublicationDescriptionReleaseCommitPeer
		}
	}
	noStore := false
	for _, directive := range strings.Split(resp.Header.Get("Cache-Control"), ",") {
		noStore = noStore || strings.EqualFold(strings.TrimSpace(directive), "no-store")
	}
	if resp.Header.Get("Content-Type") != "application/json; charset=utf-8" ||
		len(resp.Header.Values("Content-Encoding")) != 0 || resp.Uncompressed || !noStore || resp.ContentLength > 4096 {
		return nil, ErrSkillHubPublicationDescriptionReleaseCommitPeer
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4097))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || len(raw) == 0 || len(raw) > 4096 || !utf8.Valid(raw) ||
		(resp.ContentLength >= 0 && resp.ContentLength != int64(len(raw))) {
		return nil, ErrSkillHubPublicationDescriptionReleaseCommitPeer
	}
	if resp.StatusCode != http.StatusOK {
		parts, parseErr := publicationExactObject(raw, "error")
		if parseErr == nil {
			e, parseErr := publicationExactObject(parts["error"], "code", "message")
			var code, message string
			if parseErr == nil && json.Unmarshal(e["code"], &code) == nil && json.Unmarshal(e["message"], &message) == nil &&
				resp.StatusCode == http.StatusForbidden && code == "skillhub.publication_description_derivation_denied" {
				return nil, store.ErrSkillHubPublicationDenied
			}
		}
		return nil, ErrSkillHubPublicationDescriptionReleaseCommitPeer
	}
	parts, err := publicationExactObject(raw, "schemaVersion", "applicationId", "taskId", "permitId", "state", "replayed")
	if err != nil {
		return nil, ErrSkillHubPublicationDescriptionReleaseCommitPeer
	}
	var out publicationDescriptionCloudReleaseAdmission
	var schema int
	if decodeUniqueJSON(raw, &out) != nil || json.Unmarshal(parts["schemaVersion"], &schema) != nil || schema != 1 ||
		out.SchemaVersion != 1 || out.ApplicationID != app.ID || out.TaskID != app.Input.Receipt.TaskID ||
		out.PermitID != permit.PermitID || out.Replayed == nil ||
		!oneOf(out.State, "queued", "publishing", "committing", "published", "publish_failed") {
		return nil, ErrSkillHubPublicationDescriptionReleaseCommitPeer
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &out, nil
}
