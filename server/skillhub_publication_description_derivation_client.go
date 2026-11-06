package server

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/openchat/openchat/server/store"
)

const publicationDescriptionDerivationPath = "/api/skills/publication-descriptions/internal/derive"

var (
	ErrSkillHubPublicationDescriptionDerivationDisabled    = errors.New("skillhub publication description derivation is disabled")
	ErrSkillHubPublicationDescriptionDerivationConfig      = errors.New("invalid skillhub publication description derivation configuration")
	ErrSkillHubPublicationDescriptionDerivationPeer        = errors.New("skillhub publication description derivation peer rejected the request")
	ErrSkillHubPublicationDescriptionDerivationInput       = errors.New("invalid skillhub publication description derivation application")
	ErrSkillHubPublicationDescriptionDerivationUnsupported = errors.New("skillhub publication description derivation supports only SKILL.md")
)

// PublicationDescriptionDerivationClientConfig is deliberately separate from
// the HTTP server configuration. A client is created, but never registered as
// a publication executor, unless a future rollout explicitly wires it in.
type PublicationDescriptionDerivationClientConfig struct {
	Enabled                 bool
	ReadEnabled             bool
	ValidationEnabled       bool
	PlanEnabled             bool
	ReleaseAuthorizeEnabled bool
	ReleaseCommitEnabled    bool
	ReleaseStatusEnabled    bool
	URL                     string
	Issuer                  string
	Audience                string
	PeerSecret              string
	Timeout                 time.Duration
	HTTPClient              *http.Client
}

type PublicationDescriptionDerivationClient struct {
	enabled                 bool
	readEnabled             bool
	validationEnabled       bool
	planEnabled             bool
	releaseAuthorizeEnabled bool
	releaseCommitEnabled    bool
	releaseStatusEnabled    bool
	planActive              atomic.Int32
	endpoint                *url.URL
	secret                  string
	issuer                  string
	audience                string
	client                  *http.Client
}

func NewPublicationDescriptionDerivationClient(cfg PublicationDescriptionDerivationClientConfig) (*PublicationDescriptionDerivationClient, error) {
	// Copy the client so timeout/redirect policy cannot mutate a shared caller.
	client := &http.Client{}
	if cfg.HTTPClient != nil {
		*client = *cfg.HTTPClient
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	// This service-to-service request must not inherit browser/session cookies.
	client.Jar = nil
	if !cfg.Enabled {
		return &PublicationDescriptionDerivationClient{client: client}, nil
	}
	if cfg.Timeout < time.Second || cfg.Timeout > time.Minute || !strings.HasPrefix(cfg.PeerSecret, "pubderive_") || len(cfg.PeerSecret) != len("pubderive_")+64 {
		return nil, ErrSkillHubPublicationDescriptionDerivationConfig
	}
	if !isLowerHex(cfg.PeerSecret[len("pubderive_"):]) || cfg.Issuer == "" || cfg.Audience == "" {
		return nil, ErrSkillHubPublicationDescriptionDerivationConfig
	}
	endpoint, err := url.Parse(strings.TrimSpace(cfg.URL))
	if err != nil || endpoint.Scheme != "https" || endpoint.User != nil || endpoint.Host == "" || endpoint.RawQuery != "" || endpoint.Fragment != "" || endpoint.Path != publicationDescriptionDerivationPath {
		return nil, ErrSkillHubPublicationDescriptionDerivationConfig
	}
	if !store.ValidSkillHubPublicationOrigin(cfg.Issuer) || !store.ValidSkillHubPublicationOrigin(cfg.Audience) {
		return nil, ErrSkillHubPublicationDescriptionDerivationConfig
	}
	audience, err := url.Parse(cfg.Audience)
	if err != nil || endpoint.Host != audience.Host {
		return nil, ErrSkillHubPublicationDescriptionDerivationConfig
	}
	client.Timeout = cfg.Timeout
	return &PublicationDescriptionDerivationClient{enabled: true, readEnabled: cfg.ReadEnabled, validationEnabled: cfg.ValidationEnabled,
		planEnabled: cfg.PlanEnabled, releaseAuthorizeEnabled: cfg.ReleaseAuthorizeEnabled,
		releaseCommitEnabled: cfg.ReleaseCommitEnabled, releaseStatusEnabled: cfg.ReleaseStatusEnabled, endpoint: endpoint,
		secret: cfg.PeerSecret, issuer: cfg.Issuer, audience: cfg.Audience, client: client}, nil
}

func NewPublicationDescriptionDerivationClientFromEnv() (*PublicationDescriptionDerivationClient, error) {
	rawEnabled := strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_DERIVATION_ENABLED"))
	enabled := strings.EqualFold(rawEnabled, "true") || rawEnabled == "1"
	if !enabled {
		return NewPublicationDescriptionDerivationClient(PublicationDescriptionDerivationClientConfig{})
	}
	timeout := 15 * time.Second
	if raw := strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_DERIVATION_TIMEOUT_MS")); raw != "" {
		ms, err := strconv.Atoi(raw)
		if err != nil || ms < 1000 || ms > 60000 {
			return nil, ErrSkillHubPublicationDescriptionDerivationConfig
		}
		timeout = time.Duration(ms) * time.Millisecond
	}
	return NewPublicationDescriptionDerivationClient(PublicationDescriptionDerivationClientConfig{
		Enabled:                 enabled,
		ReadEnabled:             strings.EqualFold(strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_READ_ENABLED")), "true") || strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_READ_ENABLED")) == "1",
		ValidationEnabled:       strings.EqualFold(strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_VALIDATION_ENABLED")), "true") || strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_VALIDATION_ENABLED")) == "1",
		PlanEnabled:             strings.EqualFold(strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_PLAN_ENABLED")), "true") || strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_PLAN_ENABLED")) == "1",
		ReleaseAuthorizeEnabled: strings.EqualFold(strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_RELEASE_AUTHORIZE_ENABLED")), "true") || strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_RELEASE_AUTHORIZE_ENABLED")) == "1",
		ReleaseCommitEnabled:    strings.EqualFold(strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_RELEASE_COMMIT_ENABLED")), "true") || strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_RELEASE_COMMIT_ENABLED")) == "1",
		ReleaseStatusEnabled:    strings.EqualFold(strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_RELEASE_STATUS_ENABLED")), "true") || strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_RELEASE_STATUS_ENABLED")) == "1",
		URL:                     strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_DERIVATION_URL")),
		Issuer:                  strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_DERIVATION_ISSUER")),
		Audience:                strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_DERIVATION_AUDIENCE")),
		PeerSecret:              strings.TrimSpace(os.Getenv("CATSCO_SKILLHUB_PUBLICATION_DESCRIPTION_DERIVATION_PEER_SECRET")),
		Timeout:                 timeout,
	})
}

func (c *PublicationDescriptionDerivationClient) DeriveAndRetain(ctx context.Context, app store.SkillHubPublicationDescriptionApplication) (store.SkillHubPublicationDescriptionApplicationResult, error) {
	if c == nil || !c.enabled {
		return store.SkillHubPublicationDescriptionApplicationResult{}, ErrSkillHubPublicationDescriptionDerivationDisabled
	}
	if err := ctx.Err(); err != nil {
		return store.SkillHubPublicationDescriptionApplicationResult{}, err
	}
	proposal := app.Input.Draft.Proposal
	identity, identityErr := app.Input.IdentityHash()
	if identityErr != nil || app.IdentityHash != identity || app.ID != "pubdesc_"+identity || app.Input.Receipt.Query.Issuer != c.issuer || app.Input.Receipt.Query.Audience != c.audience {
		return store.SkillHubPublicationDescriptionApplicationResult{}, ErrSkillHubPublicationDescriptionDerivationInput
	}
	// Cloud's current deterministic transformation implements SKILL.md only.
	// Draft admission also supports skill.json, but that is not a derivation.
	if proposal.RelativePath != "SKILL.md" || proposal.EntryIndex == nil {
		return store.SkillHubPublicationDescriptionApplicationResult{}, ErrSkillHubPublicationDescriptionDerivationUnsupported
	}
	request := struct {
		SchemaVersion int    `json:"schemaVersion"`
		ApplicationID string `json:"applicationId"`
		TaskID        string `json:"taskId"`
		RequestHash   string `json:"requestHash"`
		CandidateHash string `json:"candidateHash"`
		Proposal      struct {
			RelativePath string `json:"relativePath"`
			Field        string `json:"field"`
			EntryIndex   int64  `json:"entryIndex"`
			Text         string `json:"text"`
		} `json:"proposal"`
	}{SchemaVersion: 1, ApplicationID: app.ID, TaskID: app.Input.Receipt.TaskID,
		RequestHash: app.Input.Receipt.Query.RequestHash, CandidateHash: app.Input.Draft.Evidence.CandidateHash}
	request.Proposal = struct {
		RelativePath string `json:"relativePath"`
		Field        string `json:"field"`
		EntryIndex   int64  `json:"entryIndex"`
		Text         string `json:"text"`
	}{proposal.RelativePath, proposal.Field, *proposal.EntryIndex, proposal.Text}
	body, err := json.Marshal(request)
	if err != nil {
		return store.SkillHubPublicationDescriptionApplicationResult{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return store.SkillHubPublicationDescriptionApplicationResult{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.secret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return store.SkillHubPublicationDescriptionApplicationResult{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64*1024+1))
	if err != nil || len(raw) > 64*1024 {
		return store.SkillHubPublicationDescriptionApplicationResult{}, ErrSkillHubPublicationDescriptionDerivationPeer
	}
	if resp.StatusCode != http.StatusOK {
		return store.SkillHubPublicationDescriptionApplicationResult{}, fmt.Errorf("%w: status %d", ErrSkillHubPublicationDescriptionDerivationPeer, resp.StatusCode)
	}
	var out cloudPublicationDescriptionDerivationResponse
	if err := decodeUniqueJSON(raw, &out); err != nil || out.Replayed == nil || out.ApplicationID != app.ID || out.TaskID != app.Input.Receipt.TaskID || out.DerivedBytes < 1 || out.DerivedBytes > store.SkillHubPublicationCaptureMaxBytes || out.DerivedRequestHash != out.Receipt.DerivedRequestHash || out.DerivedCandidateHash != out.Receipt.DerivedCandidateHash || out.Receipt.OriginalRequestHash != app.Input.Receipt.Query.RequestHash || out.Receipt.OriginalCandidateHash != app.Input.Draft.Evidence.CandidateHash || out.Receipt.DerivedRequestHash == out.Receipt.OriginalRequestHash || out.Receipt.DerivedCandidateHash == out.Receipt.OriginalCandidateHash || out.Receipt.ProposalDigest != publicationDescriptionProposalDigest(proposal) {
		return store.SkillHubPublicationDescriptionApplicationResult{}, ErrSkillHubPublicationDescriptionDerivationPeer
	}
	result := store.SkillHubPublicationDescriptionApplicationResult{ApplicationID: out.ApplicationID, RequestBytes: out.DerivedBytes, Derivation: out.Receipt}
	if err := result.ValidateFor(app); err != nil {
		return store.SkillHubPublicationDescriptionApplicationResult{}, err
	}
	return result, nil
}

type cloudPublicationDescriptionDerivationResponse struct {
	ApplicationID        string                                                `json:"applicationId"`
	TaskID               string                                                `json:"taskId"`
	DerivedBytes         int64                                                 `json:"derivedBytes"`
	DerivedRequestHash   string                                                `json:"derivedRequestHash"`
	DerivedCandidateHash string                                                `json:"derivedCandidateHash"`
	Receipt              store.SkillHubPublicationDescriptionDerivationReceipt `json:"receipt"`
	// Either replay state is valid for the same immutable application identity.
	Replayed *bool `json:"replayed"`
}

func publicationDescriptionProposalDigest(p store.SkillHubPublicationDescriptionProposal) string {
	entryIndex := "null"
	if p.EntryIndex != nil {
		entryIndex = strconv.FormatInt(*p.EntryIndex, 10)
	}
	return publicationDescriptionSHA256Hex("skillhub-description-derivation-v1:proposal\x00{" +
		`"entryIndex":` + entryIndex + `,"field":` + cloudDescriptionJSONString(p.Field) + `,"relativePath":` + cloudDescriptionJSONString(p.RelativePath) + `,"text":` + cloudDescriptionJSONString(p.Text) + "}")
}

// Match Cloud JSON.stringify, including literal <>& and U+2028/U+2029.
// Go's JSON encoder escapes the latter even with SetEscapeHTML(false).
// Proposal validation guarantees valid UTF-8 before network dispatch.
func cloudDescriptionJSONString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if r < 0x20 {
				const digits = "0123456789abcdef"
				b.WriteString(`\u00`)
				b.WriteByte(digits[r>>4])
				b.WriteByte(digits[r&15])
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
func publicationDescriptionSHA256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}
func isLowerHex(s string) bool {
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return false
		}
	}
	return true
}

// decodeUniqueJSON rejects duplicate object keys as well as unknown fields.
func decodeUniqueJSON(raw []byte, target any) error {
	var seen any
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := decodeJSONValue(dec, &seen, 0); err != nil {
		return err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		return errors.New("trailing json")
	}
	strict := json.NewDecoder(bytes.NewReader(raw))
	strict.DisallowUnknownFields()
	if err := strict.Decode(target); err != nil {
		return err
	}
	if err := strict.Decode(&trailing); err != io.EOF {
		return err
	}
	return nil
}

func decodeJSONValue(dec *json.Decoder, out *any, depth int) error {
	if depth > 16 {
		return errors.New("json depth")
	}
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	switch v := tok.(type) {
	case json.Delim:
		switch v {
		case '{':
			obj := map[string]any{}
			for dec.More() {
				keyToken, err := dec.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("json key")
				}
				if _, exists := obj[key]; exists {
					return errors.New("duplicate json key")
				}
				var value any
				if err := decodeJSONValue(dec, &value, depth+1); err != nil {
					return err
				}
				obj[key] = value
			}
			if _, err := dec.Token(); err != nil {
				return err
			}
			*out = obj
		case '[':
			arr := []any{}
			for dec.More() {
				var value any
				if err := decodeJSONValue(dec, &value, depth+1); err != nil {
					return err
				}
				arr = append(arr, value)
			}
			if _, err := dec.Token(); err != nil {
				return err
			}
			*out = arr
		default:
			return errors.New("json delimiter")
		}
	default:
		*out = v
	}
	return nil
}
