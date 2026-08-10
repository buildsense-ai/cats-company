package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

const (
	artifactRefMetadataKey           = "artifact_ref"
	artifactContextMetadataKey       = "artifact_context"
	artifactPageContextMetadataKey   = "artifact_page_context"
	artifactRefContract              = "catsco.artifact-ref.v1"
	artifactContextContract          = "catsco.artifact-context.v1"
	artifactPageContextContract      = "catsco.artifact-page-context.v1"
	artifactContextResolutionMaxErr  = 240
	artifactContextResolutionTimeout = 1500 * time.Millisecond
	artifactContextCacheTTLDefault   = 2 * time.Second
	artifactPageContextMaxBytes      = 16 * 1024
	artifactPageContextMaxControls   = 24
	artifactSemanticContextMaxBytes  = 8 * 1024
	artifactSemanticContextMaxDepth  = 6
	artifactSemanticContextMaxItems  = 50
	artifactSemanticContextMaxKeys   = 50
	artifactSemanticContextMaxKeyLen = 128
	artifactSemanticContextMaxString = 1000
	artifactSemanticContextMaxVisits = 4096
)

var artifactPageContextTagPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

var artifactPageContextControlTypes = map[string]struct{}{
	"checkbox":        {},
	"radio":           {},
	"select-one":      {},
	"select-multiple": {},
	"text":            {},
	"search":          {},
	"number":          {},
	"range":           {},
	"textarea":        {},
}

var artifactSemanticUnsafeKeys = map[string]struct{}{
	"__proto__":   {},
	"constructor": {},
	"prototype":   {},
}

type artifactContextCacheKey struct {
	agentUID   int64
	artifactID string
}

type artifactContextCacheEntry struct {
	record    ArtifactContextRecord
	expiresAt time.Time
}

type artifactContextCacheMutationToken struct {
	exact    uint64
	wildcard uint64
}

// ArtifactContextRecord is the server-validated identity of one active
// Artifact. It deliberately excludes user identity and authorization claims.
type ArtifactContextRecord struct {
	ID             string
	Title          string
	Kind           string
	URL            string
	PublishVersion int
}

// ArtifactContextResolver resolves an exact Artifact ID within one Agent's
// configured Artifact node.
type ArtifactContextResolver interface {
	ResolveActiveArtifact(ctx context.Context, agentUID int64, artifactID string) (ArtifactContextRecord, error)
}

type artifactRefCandidate struct {
	ID               string
	DisplayedVersion int64
}

// SetArtifactContextResolver installs the server-side Artifact lookup used by
// both HTTP and WebSocket message ingestion.
func (h *Hub) SetArtifactContextResolver(resolver ArtifactContextResolver) {
	if h == nil {
		return
	}
	h.artifactContextResolver = resolver
}

func (h *Hub) canonicalizeArtifactMessageMetadata(ctx context.Context, actorUID int64, topicID string, metadata map[string]interface{}) map[string]interface{} {
	next := metadataWithoutArtifactContext(metadata)
	candidate, ok := parseArtifactRefCandidate(metadata)
	if !ok || h == nil || h.artifactContextResolver == nil {
		return next
	}

	agentUID, ok := h.artifactAgentForTopic(actorUID, topicID)
	if !ok {
		return next
	}
	resolutionCtx, cancel := context.WithTimeout(ctx, artifactContextResolutionTimeout)
	defer cancel()
	record, err := h.artifactContextResolver.ResolveActiveArtifact(resolutionCtx, agentUID, candidate.ID)
	if err != nil {
		message := strings.TrimSpace(err.Error())
		if len(message) > artifactContextResolutionMaxErr {
			message = message[:artifactContextResolutionMaxErr]
		}
		logArtifactContextDrop(topicID, actorUID, agentUID, candidate.ID, message)
		return next
	}
	if record.ID != candidate.ID || !validArtifactID(record.ID) ||
		strings.TrimSpace(record.Title) == "" || (record.Kind != "html" && record.Kind != "mini_app") ||
		!validArtifactContextURL(record.URL) {
		logArtifactContextDrop(topicID, actorUID, agentUID, candidate.ID, "resolver returned an invalid artifact")
		return next
	}

	contextValue := map[string]interface{}{
		"contract_version":  artifactContextContract,
		"id":                record.ID,
		"agent_uid":         strconv.FormatInt(agentUID, 10),
		"title":             strings.TrimSpace(record.Title),
		"kind":              record.Kind,
		"url":               strings.TrimSpace(record.URL),
		"currently_visible": true,
		"topic_id":          topicID,
	}
	if record.PublishVersion > 0 {
		contextValue["latest_version"] = record.PublishVersion
		if candidate.DisplayedVersion > 0 && candidate.DisplayedVersion <= int64(record.PublishVersion) {
			contextValue["displayed_version"] = candidate.DisplayedVersion
		}
	}
	if pageContext, ok := parseArtifactPageContextCandidate(metadata); ok {
		contextValue["page_context"] = pageContext
	}
	if next == nil {
		next = make(map[string]interface{}, 1)
	}
	next[artifactContextMetadataKey] = contextValue
	return next
}

func (h *Hub) artifactAgentForTopic(actorUID int64, topicID string) (int64, bool) {
	if h == nil || h.db == nil || actorUID <= 0 || strings.TrimSpace(topicID) == "" {
		return 0, false
	}
	actor, err := h.db.GetUser(actorUID)
	if err != nil || actor == nil || actor.AccountType == types.AccountBot || actor.AccountType == types.AccountService {
		return 0, false
	}

	if !isGroupTopic(topicID) {
		peerUID := extractPeerUID(topicID, actorUID)
		if peerUID <= 0 || !h.isBotUser(peerUID) {
			return 0, false
		}
		return peerUID, true
	}

	groupID := extractGroupID(topicID)
	if groupID <= 0 {
		return 0, false
	}
	group, err := h.db.GetGroup(groupID)
	if err == nil && group != nil && len(group.AgentIDs) == 1 && h.isBotUser(group.AgentIDs[0]) {
		return group.AgentIDs[0], true
	}

	members, err := h.db.GetGroupMembers(groupID)
	if err != nil {
		return 0, false
	}
	var agentUID int64
	for _, member := range members {
		if member == nil || member.UserID <= 0 || member.UserID == actorUID {
			continue
		}
		isBot := member.IsBot || h.isBotUser(member.UserID)
		if !isBot {
			continue
		}
		if agentUID != 0 && agentUID != member.UserID {
			return 0, false
		}
		agentUID = member.UserID
	}
	return agentUID, agentUID > 0
}

func parseArtifactRefCandidate(metadata map[string]interface{}) (artifactRefCandidate, bool) {
	if metadata == nil {
		return artifactRefCandidate{}, false
	}
	raw, ok := metadata[artifactRefMetadataKey]
	if !ok {
		return artifactRefCandidate{}, false
	}
	ref, ok := raw.(map[string]interface{})
	if !ok || !metadataBool(ref, "currently_visible") {
		return artifactRefCandidate{}, false
	}
	contractVersion, ok := exactArtifactMetadataString(ref, "contract_version")
	if !ok || contractVersion != artifactRefContract {
		return artifactRefCandidate{}, false
	}
	id, ok := exactArtifactMetadataString(ref, "id")
	if !ok || !validArtifactID(id) {
		return artifactRefCandidate{}, false
	}
	displayedVersion := firstMetadataInt64(ref, "displayed_version")
	if displayedVersion < 0 {
		return artifactRefCandidate{}, false
	}
	return artifactRefCandidate{ID: id, DisplayedVersion: displayedVersion}, true
}

func exactArtifactMetadataString(metadata map[string]interface{}, key string) (string, bool) {
	value, ok := metadata[key].(string)
	return value, ok && value != "" && value == strings.TrimSpace(value)
}

func metadataWithoutArtifactContext(metadata map[string]interface{}) map[string]interface{} {
	if metadata == nil {
		return nil
	}
	next := make(map[string]interface{}, len(metadata))
	for key, value := range metadata {
		if key == artifactRefMetadataKey || key == artifactContextMetadataKey || key == artifactPageContextMetadataKey {
			continue
		}
		next[key] = value
	}
	if len(next) == 0 {
		return nil
	}
	return next
}

func artifactMetadataForRecipient(metadata map[string]interface{}, recipientUID int64) map[string]interface{} {
	if metadata == nil {
		return nil
	}
	next := make(map[string]interface{}, len(metadata))
	for key, value := range metadata {
		if key == artifactRefMetadataKey || key == artifactContextMetadataKey || key == artifactPageContextMetadataKey {
			continue
		}
		next[key] = value
	}
	if raw, ok := metadata[artifactContextMetadataKey]; ok && recipientUID > 0 {
		if contextValue, ok := raw.(map[string]interface{}); ok &&
			firstMetadataInt64(contextValue, "agent_uid") == recipientUID &&
			firstMetadataString(contextValue, "contract_version") == artifactContextContract {
			next[artifactContextMetadataKey] = contextValue
		}
	}
	if len(next) == 0 {
		return nil
	}
	return next
}

func parseArtifactPageContextCandidate(metadata map[string]interface{}) (map[string]interface{}, bool) {
	if metadata == nil {
		return nil, false
	}
	raw, ok := metadata[artifactPageContextMetadataKey]
	if !ok {
		return nil, false
	}
	contextValue, ok := raw.(map[string]interface{})
	if !ok || firstMetadataString(contextValue, "contract_version") != artifactPageContextContract {
		return nil, false
	}
	contextWithoutSemantic := make(map[string]interface{}, len(contextValue))
	for key, value := range contextValue {
		if key != "semantic_context" {
			contextWithoutSemantic[key] = value
		}
	}
	encoded, err := json.Marshal(contextWithoutSemantic)
	if err != nil || len(encoded) == 0 || len(encoded) > artifactPageContextMaxBytes {
		return nil, false
	}
	observedAt := strings.TrimSpace(firstMetadataString(contextValue, "observed_at"))
	if observedAt == "" {
		return nil, false
	}
	if _, err := time.Parse(time.RFC3339Nano, observedAt); err != nil {
		return nil, false
	}

	next := map[string]interface{}{
		"contract_version": artifactPageContextContract,
		"observed_at":      truncateUTF8(observedAt, 64),
	}
	if title := artifactPageContextString(contextValue, "title", 256); title != "" {
		next["title"] = title
	}
	if location := artifactPageContextLocation(contextValue["location"]); location != nil {
		next["location"] = location
	}
	if selectedText := artifactPageContextString(contextValue, "selected_text", 2000); selectedText != "" {
		next["selected_text"] = selectedText
	}
	if interaction := artifactPageContextInteraction(contextValue["last_interaction"]); interaction != nil {
		next["last_interaction"] = interaction
	}
	if controls := artifactPageContextControls(contextValue["controls"]); len(controls) > 0 {
		next["controls"] = controls
	}
	if rawSemantic, exists := contextValue["semantic_context"]; exists {
		if semanticContext, ok := artifactPageContextSemantic(rawSemantic); ok {
			next["semantic_context"] = semanticContext
		}
	}
	if len(next) == 2 {
		return nil, false
	}
	encoded, err = json.Marshal(next)
	if err != nil {
		return nil, false
	}
	if len(encoded) > artifactPageContextMaxBytes {
		delete(next, "semantic_context")
		if len(next) == 2 {
			return nil, false
		}
		encoded, err = json.Marshal(next)
		if err != nil || len(encoded) > artifactPageContextMaxBytes {
			return nil, false
		}
	}
	return next, true
}

func artifactPageContextSemantic(value interface{}) (interface{}, bool) {
	remainingVisits := artifactSemanticContextMaxVisits
	sanitized, ok := sanitizeArtifactSemanticValue(value, 0, &remainingVisits)
	if !ok || !artifactSemanticHasContent(sanitized) {
		return nil, false
	}
	encoded, err := json.Marshal(sanitized)
	if err != nil || len(encoded) == 0 || len(encoded) > artifactSemanticContextMaxBytes {
		return nil, false
	}
	return sanitized, true
}

func sanitizeArtifactSemanticValue(value interface{}, depth int, remainingVisits *int) (interface{}, bool) {
	if remainingVisits == nil || depth > artifactSemanticContextMaxDepth || *remainingVisits <= 0 {
		return nil, false
	}
	*remainingVisits--
	switch typed := value.(type) {
	case nil:
		return nil, true
	case string:
		return truncateUTF8(typed, artifactSemanticContextMaxString), true
	case bool:
		return typed, true
	case float64:
		return typed, !math.IsNaN(typed) && !math.IsInf(typed, 0)
	case float32:
		value := float64(typed)
		return typed, !math.IsNaN(value) && !math.IsInf(value, 0)
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return typed, true
	case json.Number:
		value, err := typed.Float64()
		return typed, err == nil && !math.IsNaN(value) && !math.IsInf(value, 0)
	case []interface{}:
		limit := len(typed)
		if limit > artifactSemanticContextMaxItems {
			limit = artifactSemanticContextMaxItems
		}
		result := make([]interface{}, 0, limit)
		for _, item := range typed[:limit] {
			if *remainingVisits <= 0 {
				break
			}
			if sanitized, ok := sanitizeArtifactSemanticValue(item, depth+1, remainingVisits); ok {
				result = append(result, sanitized)
			}
		}
		return result, true
	case map[string]interface{}:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			if len([]rune(key)) > artifactSemanticContextMaxKeyLen {
				continue
			}
			if _, unsafe := artifactSemanticUnsafeKeys[key]; unsafe {
				continue
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		if len(keys) > artifactSemanticContextMaxKeys {
			keys = keys[:artifactSemanticContextMaxKeys]
		}
		result := make(map[string]interface{}, len(keys))
		for _, key := range keys {
			if *remainingVisits <= 0 {
				break
			}
			if sanitized, ok := sanitizeArtifactSemanticValue(typed[key], depth+1, remainingVisits); ok {
				result[key] = sanitized
			}
		}
		return result, true
	default:
		return nil, false
	}
}

func artifactSemanticHasContent(value interface{}) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return typed != ""
	case []interface{}:
		return len(typed) > 0
	case map[string]interface{}:
		return len(typed) > 0
	default:
		return true
	}
}

func artifactPageContextLocation(value interface{}) map[string]interface{} {
	record, ok := value.(map[string]interface{})
	if !ok {
		return nil
	}
	next := make(map[string]interface{}, 2)
	if pathname := artifactPageContextString(record, "pathname", 1024); strings.HasPrefix(pathname, "/") {
		next["pathname"] = pathname
	}
	if hash := artifactPageContextString(record, "hash", 512); strings.HasPrefix(hash, "#") {
		next["hash"] = hash
	}
	if len(next) == 0 {
		return nil
	}
	return next
}

func artifactPageContextInteraction(value interface{}) map[string]interface{} {
	record, ok := value.(map[string]interface{})
	if !ok {
		return nil
	}
	next := make(map[string]interface{}, 4)
	if tag := strings.ToLower(artifactPageContextString(record, "tag", 32)); artifactPageContextTagPattern.MatchString(tag) {
		next["tag"] = tag
	}
	for key, maxRunes := range map[string]int{"role": 64, "name": 256, "text": 256} {
		if text := artifactPageContextString(record, key, maxRunes); text != "" {
			next[key] = text
		}
	}
	if len(next) == 0 {
		return nil
	}
	return next
}

func artifactPageContextControls(value interface{}) []interface{} {
	items, ok := value.([]interface{})
	if !ok {
		return nil
	}
	limit := len(items)
	if limit > artifactPageContextMaxControls {
		limit = artifactPageContextMaxControls
	}
	controls := make([]interface{}, 0, limit)
	for _, item := range items[:limit] {
		record, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		controlType := strings.ToLower(artifactPageContextString(record, "type", 32))
		if _, ok := artifactPageContextControlTypes[controlType]; !ok {
			continue
		}
		next := map[string]interface{}{"type": controlType}
		for key, maxRunes := range map[string]int{
			"name": 256, "aria_label": 256, "role": 64, "value": 512, "text": 256,
		} {
			if text := artifactPageContextString(record, key, maxRunes); text != "" {
				next[key] = text
			}
		}
		if checked, ok := record["checked"].(bool); ok && (controlType == "checkbox" || controlType == "radio") {
			next["checked"] = checked
		}
		if len(next) > 1 {
			controls = append(controls, next)
		}
	}
	return controls
}

func artifactPageContextString(record map[string]interface{}, key string, maxRunes int) string {
	value, ok := record[key].(string)
	if !ok {
		return ""
	}
	return truncateUTF8(strings.TrimSpace(value), maxRunes)
}

func validArtifactContextURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	return err == nil && parsed.User == nil && parsed.Host != "" &&
		(parsed.Scheme == "http" || parsed.Scheme == "https")
}

func logArtifactContextDrop(topicID string, actorUID, agentUID int64, artifactID, reason string) {
	if reason == "" {
		reason = "resolution failed"
	}
	log.Printf("[artifact_context] dropped topic=%s actor=%s agent=%s artifact=%s reason=%s", topicID, formatUID(actorUID), formatUID(agentUID), artifactID, reason)
}

// ResolveActiveArtifact implements ArtifactContextResolver using the same
// configured node registry and upstream validation as the management panel.
func (h *CloudArtifactHandler) ResolveActiveArtifact(ctx context.Context, agentUID int64, artifactID string) (ArtifactContextRecord, error) {
	if h == nil || agentUID <= 0 || !validArtifactID(artifactID) {
		return ArtifactContextRecord{}, errors.New("invalid artifact reference")
	}
	if cached, ok := h.cachedArtifactContext(agentUID, artifactID); ok {
		return cached, nil
	}
	cacheMutation := h.artifactContextCacheMutationSnapshot(agentUID, artifactID)
	node, err := h.resolveArtifactNode(agentUID)
	if err != nil {
		return ArtifactContextRecord{}, err
	}
	var record ArtifactContextRecord
	if node.managementURL != "" {
		record, err = h.resolveManagedActiveArtifact(ctx, node, agentUID, artifactID)
	} else {
		if node.publicBaseURL == "" {
			return ArtifactContextRecord{}, errors.New("artifact index is unavailable")
		}
		record, err = h.resolveIndexedActiveArtifact(ctx, node, agentUID, artifactID)
	}
	if err != nil {
		return ArtifactContextRecord{}, err
	}
	if !h.storeArtifactContext(agentUID, artifactID, record, cacheMutation) {
		return ArtifactContextRecord{}, errors.New("artifact changed during resolution")
	}
	return record, nil
}

func (h *CloudArtifactHandler) artifactContextCacheMutationSnapshot(agentUID int64, artifactID string) artifactContextCacheMutationToken {
	if h == nil {
		return artifactContextCacheMutationToken{}
	}
	key := artifactContextCacheKey{agentUID: agentUID, artifactID: artifactID}
	h.artifactContextCacheMu.Lock()
	defer h.artifactContextCacheMu.Unlock()
	return artifactContextCacheMutationToken{
		exact:    h.artifactContextExactMutationGeneration[key],
		wildcard: h.artifactContextIDMutationGeneration[artifactID],
	}
}

func (h *CloudArtifactHandler) cachedArtifactContext(agentUID int64, artifactID string) (ArtifactContextRecord, bool) {
	if h == nil {
		return ArtifactContextRecord{}, false
	}
	key := artifactContextCacheKey{agentUID: agentUID, artifactID: artifactID}
	now := time.Now()
	h.artifactContextCacheMu.Lock()
	defer h.artifactContextCacheMu.Unlock()
	entry, ok := h.artifactContextCache[key]
	if !ok {
		return ArtifactContextRecord{}, false
	}
	if !now.Before(entry.expiresAt) {
		delete(h.artifactContextCache, key)
		return ArtifactContextRecord{}, false
	}
	return entry.record, true
}

func (h *CloudArtifactHandler) storeArtifactContext(agentUID int64, artifactID string, record ArtifactContextRecord, expectedMutation artifactContextCacheMutationToken) bool {
	if h == nil {
		return false
	}
	ttl := h.artifactContextCacheTTL
	if ttl <= 0 {
		ttl = artifactContextCacheTTLDefault
	}
	key := artifactContextCacheKey{agentUID: agentUID, artifactID: artifactID}
	h.artifactContextCacheMu.Lock()
	defer h.artifactContextCacheMu.Unlock()
	currentMutation := artifactContextCacheMutationToken{
		exact:    h.artifactContextExactMutationGeneration[key],
		wildcard: h.artifactContextIDMutationGeneration[artifactID],
	}
	if currentMutation != expectedMutation {
		return false
	}
	if h.artifactContextCache == nil {
		h.artifactContextCache = make(map[artifactContextCacheKey]artifactContextCacheEntry)
	}
	h.artifactContextCache[key] = artifactContextCacheEntry{
		record:    record,
		expiresAt: time.Now().Add(ttl),
	}
	return true
}

func (h *CloudArtifactHandler) invalidateArtifactContextCache(agentUID int64, artifactID string) {
	if h == nil || artifactID == "" {
		return
	}
	h.artifactContextCacheMu.Lock()
	defer h.artifactContextCacheMu.Unlock()
	if agentUID > 0 {
		key := artifactContextCacheKey{agentUID: agentUID, artifactID: artifactID}
		if h.artifactContextExactMutationGeneration == nil {
			h.artifactContextExactMutationGeneration = make(map[artifactContextCacheKey]uint64)
		}
		h.artifactContextExactMutationGeneration[key]++
		delete(h.artifactContextCache, key)
		return
	}
	if h.artifactContextIDMutationGeneration == nil {
		h.artifactContextIDMutationGeneration = make(map[string]uint64)
	}
	h.artifactContextIDMutationGeneration[artifactID]++
	for key := range h.artifactContextCache {
		if key.artifactID == artifactID {
			delete(h.artifactContextCache, key)
		}
	}
}

func (h *CloudArtifactHandler) resolveManagedActiveArtifact(ctx context.Context, node artifactNode, agentUID int64, artifactID string) (ArtifactContextRecord, error) {
	collectionURL, err := agentManagementCollectionURL(node.managementURL, agentUID)
	if err != nil {
		return ArtifactContextRecord{}, err
	}
	body, err := h.requestArtifactContext(ctx, collectionURL+"?status=active", node.managementToken)
	if err != nil {
		return ArtifactContextRecord{}, err
	}
	var list cloudArtifactManagementList
	if err := json.Unmarshal(body, &list); err != nil || validateManagedArtifactList(list, "active") != nil {
		return ArtifactContextRecord{}, errors.New("artifact response is invalid")
	}
	if err := validateManagedArtifactAgentUIDs(list.Artifacts, agentUID); err != nil {
		return ArtifactContextRecord{}, err
	}
	if err := validateManagedArtifactNodeURLs(list.Artifacts, node.publicBaseURL, agentUID); err != nil {
		return ArtifactContextRecord{}, err
	}
	return exactArtifactContextRecord(list.Artifacts, artifactID)
}

func (h *CloudArtifactHandler) resolveIndexedActiveArtifact(ctx context.Context, node artifactNode, agentUID int64, artifactID string) (ArtifactContextRecord, error) {
	indexURL := strings.TrimRight(node.publicBaseURL, "/")
	expectedURLAgentUID := agentUID
	if node.rootPublicIndex {
		indexURL += "/artifacts-index.json"
		expectedURLAgentUID = 0
	} else {
		indexURL += "/by-agent/" + strconv.FormatInt(agentUID, 10) + "/artifacts-index.json"
	}
	body, err := h.requestArtifactContext(ctx, indexURL, "")
	if err != nil {
		return ArtifactContextRecord{}, err
	}
	var index cloudArtifactIndex
	if err := json.Unmarshal(body, &index); err != nil || validateCloudArtifactIndex(index) != nil {
		return ArtifactContextRecord{}, errors.New("artifact response is invalid")
	}
	if err := validateManagedArtifactNodeURLs(index.Artifacts, node.publicBaseURL, expectedURLAgentUID); err != nil {
		return ArtifactContextRecord{}, err
	}
	for _, artifact := range index.Artifacts {
		if artifact.AgentUID != "" && artifact.AgentUID != strconv.FormatInt(agentUID, 10) {
			return ArtifactContextRecord{}, errors.New("artifact agent UID mismatch")
		}
	}
	return exactArtifactContextRecord(index.Artifacts, artifactID)
}

func (h *CloudArtifactHandler) requestArtifactContext(ctx context.Context, target, managementToken string) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "catsco-artifact-context/1.0")
	if managementToken != "" {
		request.Header.Set("Authorization", "Bearer "+managementToken)
	}
	response, err := h.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	body, readErr := readArtifactResponse(response.Body)
	if readErr != nil {
		return nil, readErr
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("artifact upstream returned %d", response.StatusCode)
	}
	return body, nil
}

func exactArtifactContextRecord(artifacts []cloudArtifact, artifactID string) (ArtifactContextRecord, error) {
	for _, artifact := range artifacts {
		if artifact.ID != artifactID {
			continue
		}
		version := 0
		if artifact.PublishVersion != nil {
			version = *artifact.PublishVersion
		}
		return ArtifactContextRecord{
			ID:             artifact.ID,
			Title:          artifact.Title,
			Kind:           artifact.Kind,
			URL:            artifact.URL,
			PublishVersion: version,
		}, nil
	}
	return ArtifactContextRecord{}, errors.New("artifact not found")
}
