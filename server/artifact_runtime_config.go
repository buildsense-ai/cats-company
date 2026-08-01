package server

import (
	"net/http"
	"os"
	"strconv"
	"strings"
)

const artifactRuntimeConfigContract = "cloud-html-artifact.runtime-config.v1"

type ArtifactRuntimeConfigHandler struct {
	accessKey  string
	secretKey  string
	dnsZone    string
	hostSuffix string
}

type artifactRuntimeConfigResponse struct {
	ContractVersion string                           `json:"contract_version"`
	AgentUID        string                           `json:"agent_uid"`
	DNSProvider     string                           `json:"dns_provider"`
	DNSZone         string                           `json:"dns_zone"`
	HostSuffix      string                           `json:"host_suffix"`
	Credentials     artifactRuntimeConfigCredentials `json:"credentials"`
}

type artifactRuntimeConfigCredentials struct {
	AccessKey string `json:"access_key"`
	SecretKey string `json:"secret_key"`
}

func NewArtifactRuntimeConfigHandlerFromEnv() *ArtifactRuntimeConfigHandler {
	return &ArtifactRuntimeConfigHandler{
		accessKey: strings.TrimSpace(firstNonEmpty(
			os.Getenv("CATSCO_ARTIFACT_DNS_ACCESS_KEY"),
			os.Getenv("VOLC_ACCESSKEY"),
		)),
		secretKey: strings.TrimSpace(firstNonEmpty(
			os.Getenv("CATSCO_ARTIFACT_DNS_SECRET_KEY"),
			os.Getenv("VOLC_SECRETKEY"),
		)),
		dnsZone: strings.TrimSpace(firstNonEmpty(
			os.Getenv("CATSCO_ARTIFACT_DNS_ZONE"),
			"catsco.fun",
		)),
		hostSuffix: strings.TrimSpace(firstNonEmpty(
			os.Getenv("CATSCO_ARTIFACT_HOST_SUFFIX"),
			"artifacts.catsco.fun",
		)),
	}
}

func (h *ArtifactRuntimeConfigHandler) Handle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Pragma", "no-cache")
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}

	uid := UIDFromContext(r.Context())
	if uid <= 0 {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}
	if h == nil || h.accessKey == "" || h.secretKey == "" || h.dnsZone == "" || h.hostSuffix == "" {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"error": "artifact runtime configuration is unavailable",
		})
		return
	}

	writeJSON(w, http.StatusOK, artifactRuntimeConfigResponse{
		ContractVersion: artifactRuntimeConfigContract,
		AgentUID:        strconv.FormatInt(uid, 10),
		DNSProvider:     "volcengine",
		DNSZone:         h.dnsZone,
		HostSuffix:      h.hostSuffix,
		Credentials: artifactRuntimeConfigCredentials{
			AccessKey: h.accessKey,
			SecretKey: h.secretKey,
		},
	})
}
