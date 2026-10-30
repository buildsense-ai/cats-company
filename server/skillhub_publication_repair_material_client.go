package server

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/openchat/openchat/server/store"
)

const publicationRepairMaterialPath = "/api/skills/publication-repair/internal/material"

var errPublicationRepairMaterialDisabled = errors.New("publication.repair_material_disabled")
var errPublicationRepairMaterialPeer = errors.New("publication.repair_material_unavailable")

type publicationRepairMaterialConfig struct {
	Enabled                      bool
	Issuer, Audience, PeerSecret string
}

// Internal read-only client. No environment loader, Hub registration, public
// tool, model call or repair worker is added. Production remains unwired/off.
type publicationRepairMaterialClient struct {
	config publicationRepairMaterialConfig
	client *http.Client
	active atomic.Int32
}

func newPublicationRepairMaterialClient(cfg publicationRepairMaterialConfig) (*publicationRepairMaterialClient, error) {
	transport := &http.Transport{Proxy: nil, DisableCompression: true, DisableKeepAlives: true, MaxConnsPerHost: 2,
		DialContext: (&net.Dialer{Timeout: 2 * time.Second}).DialContext, TLSHandshakeTimeout: 2 * time.Second,
		ResponseHeaderTimeout: 12 * time.Second, MaxResponseHeaderBytes: 8192,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	return newPublicationRepairMaterialClientWithTransport(cfg, transport)
}

// Test-only transport injection is package-private. Runtime uses the strict
// constructor above, not caller-supplied clients/cookie jars/proxy settings.
func newPublicationRepairMaterialClientWithTransport(cfg publicationRepairMaterialConfig, transport http.RoundTripper) (*publicationRepairMaterialClient, error) {
	if cfg.Enabled && (!store.ValidSkillHubPublicationOrigin(cfg.Issuer) || !store.ValidSkillHubPublicationOrigin(cfg.Audience) ||
		cfg.Issuer == cfg.Audience || !strings.HasPrefix(cfg.PeerSecret, "pubmaterial_") || len(cfg.PeerSecret) != len("pubmaterial_")+64 ||
		!isLowerHex(strings.TrimPrefix(cfg.PeerSecret, "pubmaterial_")) || transport == nil) {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	return &publicationRepairMaterialClient{config: cfg, client: &http.Client{Transport: transport, Timeout: 15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

// readOriginal returns verified private wire bytes, NOT model material or
// execution authority. The trusted caller must load current task/lease and
// recheck Runtime source before AND after reading. Never accept browser/model
// receipts here, expose raw bytes, or parse/execute Skill code in this client.
func (c *publicationRepairMaterialClient) readOriginal(parent context.Context, receipt store.SkillHubPublicationCloudReceipt) ([]byte, error) {
	if c == nil || !c.config.Enabled {
		return nil, errPublicationRepairMaterialDisabled
	}
	if receipt.Validate() != nil || receipt.Query.Issuer != c.config.Issuer || receipt.Query.Audience != c.config.Audience ||
		receipt.Query.RequestBytes > publicationCaptureRuntimeMaxBytes {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if c.active.Add(1) > 2 {
		c.active.Add(-1)
		return nil, store.ErrSkillHubPublicationBusy
	}
	defer c.active.Add(-1)
	body, err := json.Marshal(struct {
		SchemaVersion int                            `json:"schemaVersion"`
		Receipt       publicationCloudReceiptRequest `json:"receipt"`
	}{1, publicationControlReceipt(receipt)})
	if err != nil || len(body) > 4096 {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	// Origin/path are constructor-pinned; no receipt can provide a destination.
	endpoint, err := url.Parse(c.config.Audience + publicationRepairMaterialPath)
	if err != nil {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, store.ErrSkillHubPublicationInvalid
	}
	req.Header.Set("Authorization", "Bearer "+c.config.PeerSecret)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/octet-stream")
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-store")
	response, err := c.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errPublicationRepairMaterialPeer
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || response.ContentLength != receipt.Query.RequestBytes ||
		len(response.Header.Values("Content-Type")) != 1 || response.Header.Get("Content-Type") != "application/octet-stream" ||
		len(response.Header.Values("Content-Length")) != 1 || response.Header.Get("Content-Length") != strconv.FormatInt(receipt.Query.RequestBytes, 10) ||
		len(response.Header.Values("Content-Encoding")) != 0 || response.Uncompressed || len(response.TransferEncoding) != 0 ||
		len(response.Header.Values("Cache-Control")) != 1 || response.Header.Get("Cache-Control") != "no-store" ||
		len(response.Header.Values("X-Content-Type-Options")) != 1 || response.Header.Get("X-Content-Type-Options") != "nosniff" {
		return nil, errPublicationRepairMaterialPeer
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, receipt.Query.RequestBytes+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil || int64(len(raw)) != receipt.Query.RequestBytes || publicationDescriptionSHA256Hex(string(raw)) != receipt.Query.RequestHash {
		return nil, errPublicationRepairMaterialPeer
	}
	return raw, nil
}
