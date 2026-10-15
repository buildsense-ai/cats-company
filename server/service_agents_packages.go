package server

import (
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"strings"

	"github.com/openchat/openchat/server/store"
)

// 云端包分发内部 API（设计 A §2；实例/发布脚本用实例 token 鉴权）。
//
//	POST /internal/service-agents/{slug}/publish   —— 发布包（写新版本行；同 hash 幂等）
//	GET  /internal/service-agent-packages/{hash}   —— 按内容 hash 拉取包
//
// 发布即派生：definition_json 从包 manifest 计算（async_threshold_seconds/models），
// 平台不再手工维护定义，消除"包内模型 vs 平台定义"双写裂缝。

const serviceBundleHTTPLimit = 4 << 20 // 传输上限（内容上限在 ValidateServiceAgentBundle）

func (o *ServiceRunOrchestrator) handlePublishServiceAgentPackage(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimSpace(r.PathValue("slug"))
	var bundle ServiceAgentBundle
	body := http.MaxBytesReader(w, r.Body, serviceBundleHTTPLimit)
	if err := json.NewDecoder(body).Decode(&bundle); err != nil {
		writeServiceJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_body"})
		return
	}
	definition, err := ValidateServiceAgentBundle(bundle, slug)
	if err != nil {
		writeServiceJSON(w, http.StatusBadRequest, map[string]any{"error": "invalid_bundle", "detail": err.Error()})
		return
	}
	definitionJSON, err := json.Marshal(definition)
	if err != nil {
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal"})
		return
	}
	bundleJSON, err := json.Marshal(bundle)
	if err != nil {
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal"})
		return
	}
	packageHash := CanonicalServiceBundleHash(bundle.Files)
	version, err := o.store.PublishServiceAgentPackage(r.Context(), slug, definitionJSON, packageHash, bundleJSON)
	if errors.Is(err, store.ErrServiceAgentNotFound) {
		writeServiceJSON(w, http.StatusNotFound, map[string]any{"error": "agent_not_found"})
		return
	}
	if err != nil {
		log.Printf("service package publish slug=%s: %v", slug, err)
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "publish_failed"})
		return
	}
	log.Printf("service package published slug=%s version=%d hash=%s", slug, version, packageHash)
	writeServiceJSON(w, http.StatusOK, map[string]any{
		"slug":               slug,
		"version":            version,
		"package_hash":       packageHash,
		"definition":         definition,
		"async_threshold":    definition.AsyncThresholdSeconds,
		"knowledge_files":    countBundleFiles(bundle.Files, "knowledge/"),
		"skill_files":        countBundleFiles(bundle.Files, "skills/"),
	})
}

func (o *ServiceRunOrchestrator) handleGetServiceAgentPackage(w http.ResponseWriter, r *http.Request) {
	packageHash := strings.TrimSpace(r.PathValue("packageHash"))
	if packageHash == "" {
		writeServiceJSON(w, http.StatusBadRequest, map[string]any{"error": "hash_required"})
		return
	}
	bundle, err := o.store.GetServiceAgentPackage(r.Context(), packageHash)
	if errors.Is(err, store.ErrServicePackageNotFound) {
		writeServiceJSON(w, http.StatusNotFound, map[string]any{"error": "package_not_found"})
		return
	}
	if err != nil {
		log.Printf("service package get hash=%s: %v", packageHash, err)
		writeServiceJSON(w, http.StatusInternalServerError, map[string]any{"error": "get_failed"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = io.Copy(w, strings.NewReader(string(bundle)))
}

func countBundleFiles(files []ServiceAgentBundleFile, prefix string) int {
	count := 0
	for _, f := range files {
		if strings.HasPrefix(f.Path, prefix) {
			count++
		}
	}
	return count
}
