// Package server implements gateway artifact annotations: structured
// element/text/region comments a viewer attaches to a message about a gateway
// Artifact application. The annotations travel as message metadata under
// "gateway_annotations", are validated and authorized on the server before they
// are persisted, and are delivered to the owning Agent both as the same
// structured value and as a server-composed, human-readable context block.
//
// The trust boundary mirrors artifact_apps.go: only the platform holds the
// gateway control token, so app identity is resolved server-side against the
// gateway's own registry (Agent == owning account). Client-sent agent_uid and
// app_id claims are never trusted; agent_uid is cross-checked against the
// topic's server-resolved Agent and app_id against the gateway app list. No
// registry publish_version or legacy artifact registry record is involved.
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store/types"
)

const (
	// gatewayAnnotationsMetadataKey is the message metadata key that carries
	// the annotation attachment.
	gatewayAnnotationsMetadataKey = "gateway_annotations"
	// gatewayAnnotationsAgentContextKey is a per-recipient fanout-only
	// metadata key: a server-composed readable context for the Agent that is
	// annotated at. It is never persisted.
	gatewayAnnotationsAgentContextKey = "catsco_gateway_annotation_context"
	// GatewayAnnotationsContractV1 is the exact contract_version every
	// gateway_annotations value must declare.
	GatewayAnnotationsContractV1 = "catsco.gateway-annotations.v1"
	// gatewayAnnotationsAgentContextSchema marks the agent context envelope.
	gatewayAnnotationsAgentContextSchema = "catsco.gateway_annotations_context.v1"

	gatewayAnnotationsMaxAnnotations = 20
	// gatewayAnnotationsMaxBytes bounds the serialized UTF-8 size of one
	// gateway_annotations value.
	gatewayAnnotationsMaxBytes = 16 << 10
	// gatewayAnnotationsMaxBodyRunes bounds one comment body.
	gatewayAnnotationsMaxBodyRunes = 2000
	// gatewayAnnotationsMaxLabelRunes bounds one annotation label.
	gatewayAnnotationsMaxLabelRunes = 256
	// gatewayAnnotationsMaxTextRunes bounds selected text.
	gatewayAnnotationsMaxTextRunes = 2000
	// gatewayAnnotationsMaxAffixRunes bounds text prefix/suffix context.
	gatewayAnnotationsMaxAffixRunes = 256
	// gatewayAnnotationsMaxIDRunes bounds annotation ids and element ids.
	gatewayAnnotationsMaxIDRunes = 128
	// gatewayAnnotationsMaxSelectorRunes bounds one CSS selector.
	gatewayAnnotationsMaxSelectorRunes = 512
	// gatewayAnnotationsMaxPathRunes bounds one page pathname.
	gatewayAnnotationsMaxPathRunes = 1024
	// gatewayAnnotationsMaxRevisionRunes bounds one optional revision string.
	gatewayAnnotationsMaxRevisionRunes = 128
	// gatewayAnnotationsMaxSummaryRunes bounds the agent-readable summary.
	gatewayAnnotationsMaxSummaryRunes = 4000
	// gatewayAnnotationsMaxUID guards integer claims against float64 drift.
	gatewayAnnotationsMaxUID = int64(1) << 53
	// gatewayAnnotationsViewportMax bounds viewport/scroll evidence numbers.
	gatewayAnnotationsViewportMax = float64(1 << 20)

	gatewayAnnotationsAppLookupTimeout = 3 * time.Second
)

// GatewayAnnotationsAppResolver answers the server-canonical ownership of a
// registered gateway application. The gateway, not the caller, is the only
// place this can be answered from.
type GatewayAnnotationsAppResolver interface {
	// GatewayAppOwner returns the owning account id (as stored by the
	// gateway, e.g. "usr7") of one registered application.
	GatewayAppOwner(ctx context.Context, appID string) (owner string, found bool, err error)
}

// gatewayAnnotationRect is a viewport-normalized rectangle. All values are in
// 0..1 with x+width and y+height at most 1, so the shape carries no document
// pixel state that could silently re-anchor after a revision change.
type gatewayAnnotationRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// gatewayAnnotationViewport is the iframe viewport at capture time. It is
// evidence for interpreting a region rect, never a stable anchor.
type gatewayAnnotationViewport struct {
	Width   float64 `json:"width"`
	Height  float64 `json:"height"`
	ScrollX float64 `json:"scroll_x"`
	ScrollY float64 `json:"scroll_y"`
}

// gatewayAnnotationTarget is the bounded, kind-dependent target of one
// annotation.
type gatewayAnnotationTarget struct {
	ElementID       string                     `json:"element_id,omitempty"`
	Selector        string                     `json:"selector,omitempty"`
	Text            string                     `json:"text,omitempty"`
	Prefix          string                     `json:"prefix,omitempty"`
	Suffix          string                     `json:"suffix,omitempty"`
	Rect            *gatewayAnnotationRect     `json:"rect,omitempty"`
	CoordinateSpace string                     `json:"coordinate_space,omitempty"`
	Viewport        *gatewayAnnotationViewport `json:"viewport,omitempty"`
}

// gatewayAnnotation is one bounded user comment.
type gatewayAnnotation struct {
	ID     string                  `json:"id"`
	Kind   string                  `json:"kind"`
	Label  string                  `json:"label,omitempty"`
	Body   string                  `json:"body"`
	Target gatewayAnnotationTarget `json:"target"`
}

// gatewayAnnotationsPage identifies the app page the annotation was made on.
type gatewayAnnotationsPage struct {
	Path     string `json:"path"`
	Revision string `json:"revision,omitempty"`
}

// gatewayAnnotationsDocument is the validated, server-canonical shape of one
// gateway_annotations metadata value.
type gatewayAnnotationsDocument struct {
	ContractVersion string                 `json:"contract_version"`
	AgentUID        int64                  `json:"agent_uid"`
	AppID           string                 `json:"app_id"`
	Page            gatewayAnnotationsPage `json:"page"`
	Annotations     []gatewayAnnotation    `json:"annotations"`
}

// normalizeGatewayAnnotations re-encodes raw client JSON into the bounded
// canonical contract shape, so downstream persistence and delivery always see
// one exact form. Unknown fields are dropped. nil is returned only for an
// explicitly empty annotation list; every other violation is an error.
func normalizeGatewayAnnotations(value interface{}) (*gatewayAnnotationsDocument, error) {
	canonical, err := canonicalGatewayAnnotationsJSON(value)
	if err != nil {
		return nil, err
	}
	root, ok := canonical.(map[string]interface{})
	if !ok {
		return nil, errors.New("gateway_annotations must be an object")
	}

	version, _ := root["contract_version"].(string)
	if version != GatewayAnnotationsContractV1 {
		return nil, errors.New("gateway_annotations.contract_version is not supported")
	}
	rawAgentUID, ok := root["agent_uid"].(float64)
	if !ok || rawAgentUID < 1 || rawAgentUID != math.Trunc(rawAgentUID) || rawAgentUID > float64(gatewayAnnotationsMaxUID) {
		return nil, errors.New("gateway_annotations.agent_uid must be a positive integer")
	}
	appID, _ := root["app_id"].(string)
	if !validGatewayAnnotationAppID(appID) {
		return nil, errors.New("gateway_annotations.app_id is not a registered gateway application id")
	}
	page, err := normalizeGatewayAnnotationsPage(root["page"])
	if err != nil {
		return nil, err
	}
	rawAnnotations, ok := root["annotations"].([]interface{})
	if !ok {
		return nil, errors.New("gateway_annotations.annotations must be an array")
	}
	if len(rawAnnotations) == 0 {
		return nil, nil
	}
	if len(rawAnnotations) > gatewayAnnotationsMaxAnnotations {
		return nil, fmt.Errorf("gateway_annotations carries %d annotations, at most %d are allowed", len(rawAnnotations), gatewayAnnotationsMaxAnnotations)
	}
	annotations := make([]gatewayAnnotation, 0, len(rawAnnotations))
	seen := make(map[string]bool, len(rawAnnotations))
	for index, raw := range rawAnnotations {
		annotation, err := normalizeGatewayAnnotation(raw, index)
		if err != nil {
			return nil, err
		}
		if seen[annotation.ID] {
			return nil, fmt.Errorf("gateway_annotations[%d].id %q is not unique", index, annotation.ID)
		}
		seen[annotation.ID] = true
		annotations = append(annotations, *annotation)
	}

	document := &gatewayAnnotationsDocument{
		ContractVersion: GatewayAnnotationsContractV1,
		AgentUID:        int64(rawAgentUID),
		AppID:           appID,
		Page:            *page,
		Annotations:     annotations,
	}
	if err := document.checkSize(); err != nil {
		return nil, err
	}
	return document, nil
}

// canonicalGatewayAnnotationsJSON round-trips one raw value through JSON so
// downstream code sees only JSON-safe scalars (finite numbers, no NaN/Inf,
// no Go-native types). Input size is bounded before the round-trip.
func canonicalGatewayAnnotationsJSON(value interface{}) (interface{}, error) {
	if value == nil {
		return nil, nil
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, errors.New("gateway_annotations is not JSON-encodable")
	}
	if len(raw) > gatewayAnnotationsMaxBytes {
		return nil, fmt.Errorf("gateway_annotations is %d bytes, at most %d are allowed", len(raw), gatewayAnnotationsMaxBytes)
	}
	var out interface{}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, errors.New("gateway_annotations is not valid JSON")
	}
	return out, nil
}

func normalizeGatewayAnnotationsPage(value interface{}) (*gatewayAnnotationsPage, error) {
	record, ok := value.(map[string]interface{})
	if !ok {
		return nil, errors.New("gateway_annotations.page must be an object")
	}
	path, _ := record["path"].(string)
	if !validGatewayAnnotationPath(path) {
		return nil, errors.New("gateway_annotations.page.path must be a bounded pathname without query or fragment")
	}
	page := &gatewayAnnotationsPage{Path: path}
	if rawRevision, present := record["revision"]; present && rawRevision != nil {
		revision, _ := rawRevision.(string)
		if !validGatewayAnnotationRevision(revision) {
			return nil, errors.New("gateway_annotations.page.revision must be a short plain string")
		}
		page.Revision = revision
	}
	return page, nil
}

func normalizeGatewayAnnotation(value interface{}, index int) (*gatewayAnnotation, error) {
	record, ok := value.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("gateway_annotations[%d] must be an object", index)
	}
	prefix := fmt.Sprintf("gateway_annotations[%d]", index)
	annotationID, _ := record["id"].(string)
	if !validGatewayAnnotationID(annotationID) {
		return nil, fmt.Errorf("%s.id must be a plain string of up to %d characters", prefix, gatewayAnnotationsMaxIDRunes)
	}
	kind, _ := record["kind"].(string)
	if !validGatewayAnnotationKind(kind) {
		return nil, fmt.Errorf("%s.kind must be one of element, text, region", prefix)
	}
	label, _ := record["label"].(string)
	if utf8.RuneCountInString(label) > gatewayAnnotationsMaxLabelRunes {
		return nil, fmt.Errorf("%s.label must be at most %d characters", prefix, gatewayAnnotationsMaxLabelRunes)
	}
	body, _ := record["body"].(string)
	if strings.TrimSpace(body) == "" {
		return nil, fmt.Errorf("%s.body must carry the user's comment", prefix)
	}
	if utf8.RuneCountInString(body) > gatewayAnnotationsMaxBodyRunes {
		return nil, fmt.Errorf("%s.body must be at most %d characters", prefix, gatewayAnnotationsMaxBodyRunes)
	}
	target, err := normalizeGatewayAnnotationTarget(kind, record["target"])
	if err != nil {
		return nil, fmt.Errorf("%s.%v", prefix, err)
	}
	return &gatewayAnnotation{
		ID:     annotationID,
		Kind:   kind,
		Label:  label,
		Body:   body,
		Target: *target,
	}, nil
}

func normalizeGatewayAnnotationTarget(kind string, value interface{}) (*gatewayAnnotationTarget, error) {
	record, ok := value.(map[string]interface{})
	if !ok {
		return nil, errors.New("target must be an object")
	}
	target := &gatewayAnnotationTarget{}
	if value, present := optionalString(record, "element_id"); present {
		if !validGatewayAnnotationID(value) {
			return nil, errors.New("target.element_id must be a plain string of up to 128 characters")
		}
		target.ElementID = value
	}
	if value, present := optionalString(record, "selector"); present {
		if !validGatewayAnnotationSelector(value) {
			return nil, errors.New("target.selector must be a plain string of up to 512 characters")
		}
		target.Selector = value
	}
	if value, present := optionalString(record, "text"); present {
		if !validGatewayAnnotationRunes(value, gatewayAnnotationsMaxTextRunes, true) {
			return nil, errors.New("target.text must be at most 2000 characters")
		}
		target.Text = value
	}
	if value, present := optionalString(record, "prefix"); present {
		if !validGatewayAnnotationRunes(value, gatewayAnnotationsMaxAffixRunes, true) {
			return nil, errors.New("target.prefix must be at most 256 characters")
		}
		target.Prefix = value
	}
	if value, present := optionalString(record, "suffix"); present {
		if !validGatewayAnnotationRunes(value, gatewayAnnotationsMaxAffixRunes, true) {
			return nil, errors.New("target.suffix must be at most 256 characters")
		}
		target.Suffix = value
	}
	if rawRect, present := record["rect"]; present && rawRect != nil {
		rect, err := normalizeGatewayAnnotationRect(rawRect)
		if err != nil {
			return nil, err
		}
		target.Rect = rect
	}
	if rawSpace, present := record["coordinate_space"]; present && rawSpace != nil {
		space, _ := rawSpace.(string)
		if space != "viewport" {
			return nil, errors.New("target.coordinate_space must be viewport")
		}
		target.CoordinateSpace = space
	}
	if rawViewport, present := record["viewport"]; present && rawViewport != nil {
		viewport, err := normalizeGatewayAnnotationViewport(rawViewport)
		if err != nil {
			return nil, err
		}
		target.Viewport = viewport
	}

	switch kind {
	case "element":
		if target.ElementID == "" && target.Selector == "" {
			return nil, errors.New("target must name an element via element_id or selector")
		}
	case "text":
		if strings.TrimSpace(target.Text) == "" {
			return nil, errors.New("target.text must carry the selected text")
		}
	case "region":
		if target.Rect == nil || target.CoordinateSpace != "viewport" || target.Viewport == nil {
			return nil, errors.New("target must carry rect, coordinate_space and viewport for a region annotation")
		}
	}
	return target, nil
}

func normalizeGatewayAnnotationRect(value interface{}) (*gatewayAnnotationRect, error) {
	record, ok := value.(map[string]interface{})
	if !ok {
		return nil, errors.New("target.rect must be an object")
	}
	x, ok := unitFloat(record, "x")
	if !ok {
		return nil, errors.New("target.rect.x must be a number in 0..1")
	}
	y, ok := unitFloat(record, "y")
	if !ok {
		return nil, errors.New("target.rect.y must be a number in 0..1")
	}
	width, ok := unitFloat(record, "width")
	if !ok {
		return nil, errors.New("target.rect.width must be a number in 0..1")
	}
	height, ok := unitFloat(record, "height")
	if !ok {
		return nil, errors.New("target.rect.height must be a number in 0..1")
	}
	if width <= 0 || height <= 0 {
		return nil, errors.New("target.rect must be a non-empty area")
	}
	if x+width > 1+gatewayAnnotationsUnitEpsilon || y+height > 1+gatewayAnnotationsUnitEpsilon {
		return nil, errors.New("target.rect exceeds the viewport")
	}
	return &gatewayAnnotationRect{X: x, Y: y, Width: width, Height: height}, nil
}

func normalizeGatewayAnnotationViewport(value interface{}) (*gatewayAnnotationViewport, error) {
	record, ok := value.(map[string]interface{})
	if !ok {
		return nil, errors.New("target.viewport must be an object")
	}
	width, ok := boundedFloat(record, "width", 0, gatewayAnnotationsViewportMax)
	if !ok || width <= 0 {
		return nil, errors.New("target.viewport.width must be a positive number")
	}
	height, ok := boundedFloat(record, "height", 0, gatewayAnnotationsViewportMax)
	if !ok || height <= 0 {
		return nil, errors.New("target.viewport.height must be a positive number")
	}
	viewport := &gatewayAnnotationViewport{Width: width, Height: height}
	if value, ok := boundedFloat(record, "scroll_x", 0, gatewayAnnotationsViewportMax); ok {
		viewport.ScrollX = value
	} else if _, present := record["scroll_x"]; present && record["scroll_x"] != nil {
		return nil, errors.New("target.viewport.scroll_x must be a non-negative number")
	}
	if value, ok := boundedFloat(record, "scroll_y", 0, gatewayAnnotationsViewportMax); ok {
		viewport.ScrollY = value
	} else if _, present := record["scroll_y"]; present && record["scroll_y"] != nil {
		return nil, errors.New("target.viewport.scroll_y must be a non-negative number")
	}
	return viewport, nil
}

const gatewayAnnotationsUnitEpsilon = 1e-6

func validGatewayAnnotationAppID(value string) bool {
	return artifactLaunchAppPattern.MatchString(value)
}

func validGatewayAnnotationID(value string) bool {
	return value != "" && utf8.RuneCountInString(value) <= gatewayAnnotationsMaxIDRunes &&
		!containsControlCharacter(value)
}

func validGatewayAnnotationKind(value string) bool {
	switch value {
	case "element", "text", "region":
		return true
	default:
		return false
	}
}

func validGatewayAnnotationSelector(value string) bool {
	return value != "" && utf8.RuneCountInString(value) <= gatewayAnnotationsMaxSelectorRunes &&
		!containsControlCharacter(value)
}

// validGatewayAnnotationRunes bounds free-form strings. allowBreaks keeps
// newlines (comments and quoted text may wrap); identifiers reject them.
func validGatewayAnnotationRunes(value string, maxRunes int, allowBreaks bool) bool {
	if utf8.RuneCountInString(value) > maxRunes {
		return false
	}
	for _, r := range value {
		if r >= 0x20 && r != 0x7f {
			continue
		}
		if r == '\n' || r == '\r' || r == '\t' {
			if allowBreaks {
				continue
			}
			return false
		}
		return false
	}
	return true
}

func validGatewayAnnotationPath(value string) bool {
	if !strings.HasPrefix(value, "/") {
		return false
	}
	if strings.ContainsAny(value, "?#") {
		return false
	}
	return validGatewayAnnotationRunes(value, gatewayAnnotationsMaxPathRunes, false)
}

func validGatewayAnnotationRevision(value string) bool {
	return strings.TrimSpace(value) != "" && validGatewayAnnotationRunes(value, gatewayAnnotationsMaxRevisionRunes, false)
}

func optionalString(record map[string]interface{}, key string) (string, bool) {
	raw, present := record[key]
	if !present || raw == nil {
		return "", false
	}
	value, _ := raw.(string)
	return value, true
}

func unitFloat(record map[string]interface{}, key string) (float64, bool) {
	value, ok := boundedFloat(record, key, 0, 1)
	if !ok {
		return 0, false
	}
	return value, true
}

func boundedFloat(record map[string]interface{}, key string, min, max float64) (float64, bool) {
	raw, ok := record[key].(float64)
	if !ok {
		return 0, false
	}
	// NaN and ±Inf fail every comparison, so they never pass this guard.
	if !(raw >= min && raw <= max) {
		return 0, false
	}
	return raw, true
}

func containsControlCharacter(value string) bool {
	for _, r := range value {
		if r == '\n' || r == '\r' || r == '\t' {
			return true
		}
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// checkSize bounds the serialized canonical value. agent_uid is re-stamped by
// the server after validation, so the size is re-checked there too.
func (d *gatewayAnnotationsDocument) checkSize() error {
	d.ContractVersion = GatewayAnnotationsContractV1
	raw, err := json.Marshal(d)
	if err != nil {
		return errors.New("gateway_annotations is not JSON-encodable")
	}
	if len(raw) > gatewayAnnotationsMaxBytes {
		return fmt.Errorf("gateway_annotations is %d bytes, at most %d are allowed", len(raw), gatewayAnnotationsMaxBytes)
	}
	return nil
}

// restamped re-serializes the document after the server replaced the claimed
// identity, re-enforcing the total size bound on the exact persisted value.
func (d *gatewayAnnotationsDocument) restamped() (map[string]interface{}, error) {
	d.ContractVersion = GatewayAnnotationsContractV1
	raw, err := json.Marshal(d)
	if err != nil {
		return nil, errors.New("gateway_annotations is not JSON-encodable")
	}
	if len(raw) > gatewayAnnotationsMaxBytes {
		return nil, fmt.Errorf("gateway_annotations is %d bytes, at most %d are allowed", len(raw), gatewayAnnotationsMaxBytes)
	}
	var value map[string]interface{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, errors.New("gateway_annotations is not valid JSON")
	}
	return value, nil
}

// metadataWithoutGatewayAnnotations drops the gateway_annotations key. The
// artifact context scrub never touches this key, so this is the only place a
// rejected or unauthorized annotation value leaves the message.
func metadataWithoutGatewayAnnotations(metadata map[string]interface{}) map[string]interface{} {
	if metadata == nil {
		return nil
	}
	if _, present := metadata[gatewayAnnotationsMetadataKey]; !present {
		return metadata
	}
	next := make(map[string]interface{}, len(metadata))
	for key, value := range metadata {
		if key == gatewayAnnotationsMetadataKey {
			continue
		}
		next[key] = value
	}
	if len(next) == 0 {
		return nil
	}
	return next
}

// metadataWithoutGatewayAnnotationContext strips only the fanout-only context
// key, keeping a legitimate persisted gateway_annotations value. Read paths
// that serve a stored message (history, delivery replay) call this so a value
// that somehow reached the store can never masquerade as server output.
func metadataWithoutGatewayAnnotationContext(metadata map[string]interface{}) map[string]interface{} {
	if metadata == nil {
		return nil
	}
	if _, present := metadata[gatewayAnnotationsAgentContextKey]; !present {
		return metadata
	}
	next := make(map[string]interface{}, len(metadata))
	for key, value := range metadata {
		if key == gatewayAnnotationsAgentContextKey {
			continue
		}
		next[key] = value
	}
	if len(next) == 0 {
		return nil
	}
	return next
}

// hasGatewayAnnotationsMetadata reports whether the message carries the
// annotation attachment OR anything pretending to be server-generated context
// for it. Ingestion must see the forgery, not silently pass it through.
func hasGatewayAnnotationsMetadata(metadata map[string]interface{}) bool {
	if metadata == nil {
		return false
	}
	_, hasAnnotations := metadata[gatewayAnnotationsMetadataKey]
	_, hasContext := metadata[gatewayAnnotationsAgentContextKey]
	return hasAnnotations || hasContext
}

// validateGatewayAnnotationsMetadata is the shared ingestion point for HTTP
// and WebSocket publishing. Messages without the metadata key are untouched;
// a well-formed value is authorized against the topic's server-resolved Agent
// and the gateway's own app registry, identity-stamped, and returned in the
// canonical shape that will be persisted. Rejections fail the whole message so
// the composer can keep the draft for retry.
func (h *Hub) validateGatewayAnnotationsMetadata(actorUID int64, topicID string, metadata map[string]interface{}) (map[string]interface{}, error) {
	if metadata == nil {
		return nil, nil
	}
	_, hasAnnotations := metadata[gatewayAnnotationsMetadataKey]
	_, hasForgedContext := metadata[gatewayAnnotationsAgentContextKey]
	if !hasAnnotations && !hasForgedContext {
		return metadata, nil
	}
	// The context block is server-generated fanout-only state: any client copy
	// is stripped before anything else happens, whether or not annotations
	// then survive authorization.
	stripped := metadataWithoutGatewayAnnotationContext(metadata)
	if !hasAnnotations {
		// A lone forged context block carries nothing to authorize; drop it and
		// let the message continue as a normal message.
		return stripped, nil
	}
	if h == nil || h.db == nil {
		// No server state to authorize against: the claim cannot be trusted.
		return metadataWithoutGatewayAnnotations(stripped), nil
	}
	actor, err := h.db.GetUser(actorUID)
	if err != nil || actor == nil || actor.AccountType != types.AccountHuman {
		// Annotations are composed by a human viewer. A non-human account
		// carrying this key is a claim it may not make; drop it rather than
		// reject unrelated bot traffic.
		return metadataWithoutGatewayAnnotations(stripped), nil
	}
	document, err := normalizeGatewayAnnotations(stripped[gatewayAnnotationsMetadataKey])
	if err != nil {
		return nil, err
	}
	if document == nil {
		// An empty annotation list carries nothing to authorize; drop the
		// credential keys and let the message persist as a normal one.
		return metadataWithoutGatewayAnnotations(stripped), nil
	}

	agentUID, ok := h.artifactAgentForTopic(actorUID, topicID)
	if !ok || agentUID != document.AgentUID {
		return nil, errors.New("gateway_annotations.agent_uid does not match the topic's Agent")
	}
	if err := h.gatewayAnnotationsAppAuthorized(document.AgentUID, document.AppID); err != nil {
		return nil, err
	}
	value, err := document.restamped()
	if err != nil {
		return nil, err
	}
	next := metadataWithoutGatewayAnnotations(stripped)
	if next == nil {
		next = make(map[string]interface{}, 1)
	}
	next[gatewayAnnotationsMetadataKey] = value
	return next, nil
}

// gatewayAnnotationsAppAuthorized answers whether appID belongs to the agent
// or the acting account, per the gateway's own registry. The resolver is the
// same trusted platform path artifact_apps.go uses, so the answer never comes
// from the message itself.
func (h *Hub) gatewayAnnotationsAppAuthorized(agentUID int64, appID string) error {
	if h == nil || h.gatewayAnnotationAppResolver == nil {
		return errors.New("gateway_annotations: gateway app registry is not available")
	}
	ctx, cancel := context.WithTimeout(context.Background(), gatewayAnnotationsAppLookupTimeout)
	defer cancel()
	owner, found, err := h.gatewayAnnotationAppResolver.GatewayAppOwner(ctx, appID)
	if err != nil {
		return errors.New("gateway_annotations: gateway app ownership could not be verified")
	}
	if !found {
		return errors.New("gateway_annotations.app_id does not name a registered gateway application")
	}
	if owner == strconv.FormatInt(agentUID, 10) {
		return nil
	}
	return errors.New("gateway_annotations.app_id is not owned by this conversation's Agent")
}

// gatewayAnnotationAgentContext composes the fanout-only, agent-readable view
// of a validated annotation attachment. It is delivered only to the Agent the
// annotations were captured against, and it never replaces message content or
// system instructions: it is one additional metadata block.
func (h *Hub) gatewayAnnotationAgentContext(actorUID int64, recipientUID int64, topicID string, sourceMetadata map[string]interface{}) map[string]interface{} {
	if h == nil || h.db == nil || recipientUID <= 0 || sourceMetadata == nil {
		return nil
	}
	if !hasGatewayAnnotationsMetadata(sourceMetadata) {
		return nil
	}
	document, err := normalizeGatewayAnnotations(sourceMetadata[gatewayAnnotationsMetadataKey])
	if err != nil || document == nil {
		return nil
	}
	agentUID, ok := h.artifactAgentForTopic(actorUID, topicID)
	if !ok || agentUID != recipientUID || agentUID != document.AgentUID {
		return nil
	}

	annotations := make([]interface{}, 0, len(document.Annotations))
	for index := range document.Annotations {
		raw, err := json.Marshal(document.Annotations[index])
		if err != nil {
			return nil
		}
		var annotation map[string]interface{}
		if err := json.Unmarshal(raw, &annotation); err != nil {
			return nil
		}
		annotation["index"] = index + 1
		annotations = append(annotations, annotation)
	}
	page := map[string]interface{}{"path": document.Page.Path}
	if document.Page.Revision != "" {
		page["revision"] = document.Page.Revision
	}
	return map[string]interface{}{
		"schema":      gatewayAnnotationsAgentContextSchema,
		"agent_uid":   formatUID(document.AgentUID),
		"app_id":      document.AppID,
		"page":        page,
		"annotations": annotations,
		"summary":     gatewayAnnotationsAgentContextSummary(document),
	}
}

// gatewayAnnotationsAgentContextSummary renders the annotations as one bounded
// readable block so an Agent can consume the user's intent without parsing
// JSON. The structured fields above it remain the canonical source.
func gatewayAnnotationsAgentContextSummary(document *gatewayAnnotationsDocument) string {
	var builder strings.Builder
	builder.WriteString("用户在 gateway 应用 ")
	builder.WriteString(document.AppID)
	builder.WriteString(" 的页面 ")
	builder.WriteString(document.Page.Path)
	if document.Page.Revision != "" {
		builder.WriteString("（revision ")
		builder.WriteString(document.Page.Revision)
		builder.WriteString("）")
	}
	fmt.Fprintf(&builder, "上标注了 %d 处：", len(document.Annotations))
	for index, annotation := range document.Annotations {
		if index > 0 {
			builder.WriteString("；")
		}
		fmt.Fprintf(&builder, " [%d] %s", index+1, annotation.Kind)
		if annotation.Label != "" {
			builder.WriteString("「")
			builder.WriteString(annotation.Label)
			builder.WriteString("」")
		}
		builder.WriteString("，评论：")
		builder.WriteString(annotation.Body)
		if builder.Len() > gatewayAnnotationsMaxSummaryRunes {
			break
		}
	}
	return truncateGatewayAnnotationRunes(builder.String(), gatewayAnnotationsMaxSummaryRunes)
}

func truncateGatewayAnnotationRunes(value string, maxRunes int) string {
	if utf8.RuneCountInString(value) <= maxRunes {
		return value
	}
	runes := []rune(value)
	return string(runes[:maxRunes-1]) + "…"
}

// withGatewayAnnotationAgentContext attaches the composed context block for
// exactly one recipient. It never mutates the input map.
func withGatewayAnnotationAgentContext(metadata map[string]interface{}, context map[string]interface{}, recipientUID int64) map[string]interface{} {
	if context == nil || recipientUID <= 0 {
		return metadata
	}
	next := make(map[string]interface{}, len(metadata)+1)
	for key, value := range metadata {
		next[key] = value
	}
	next[gatewayAnnotationsAgentContextKey] = context
	return next
}

// SetGatewayAnnotationsAppResolver installs the trusted gateway app ownership
// source, mirroring the existing resolver-injection pattern so tests and
// deployments without a gateway fail closed for annotation-bearing messages.
func (h *Hub) SetGatewayAnnotationsAppResolver(resolver GatewayAnnotationsAppResolver) {
	if h == nil {
		return
	}
	h.mu.Lock()
	h.gatewayAnnotationAppResolver = resolver
	h.mu.Unlock()
}

// gatewayAnnotationModelText composes the readable annotation context for
// exactly one recipient. The text is derived solely from the canonical,
// server-validated gateway_annotations value; a client-generated context block
// is never consulted. Empty means the recipient is not the annotations'
// target Agent (or there is nothing validated).
func (h *Hub) gatewayAnnotationModelText(actorUID int64, recipientUID int64, topicID string, sourceMetadata map[string]interface{}) string {
	if h == nil || h.db == nil || recipientUID <= 0 || sourceMetadata == nil {
		return ""
	}
	raw, hasAnnotations := sourceMetadata[gatewayAnnotationsMetadataKey]
	if !hasAnnotations {
		return ""
	}
	document, err := normalizeGatewayAnnotations(raw)
	if err != nil || document == nil {
		return ""
	}
	agentUID, ok := h.artifactAgentForTopic(actorUID, topicID)
	if !ok || agentUID != recipientUID || agentUID != document.AgentUID {
		return ""
	}
	return gatewayAnnotationsModelText(document)
}

// gatewayAnnotationModelTextForPayload is the payload-shaped wrapper used by
// p2p fanout, guarding against accidental mutation of shared blocks.
func (h *Hub) gatewayAnnotationModelTextForPayload(actorUID int64, recipientUID int64, topicID string, payload *normalizedMessagePayload) string {
	if h == nil || payload == nil {
		return ""
	}
	return h.gatewayAnnotationModelText(actorUID, recipientUID, topicID, payload.Metadata)
}

// withGatewayAnnotationAgentDelivery builds the annotated Agent's fanout or
// history read copy as one unified, copy-on-write envelope that satisfies both
// real XiaoBa consumers of the same message:
//
//   - live parseMessage (WS live fanout AND WS history replay data messages):
//     mergedText = exact-lowercase "text" blocks joined with blank lines ||
//     top-level string content (blocks win);
//   - cloud restore cloudMessageText (HTTP history / session rebuild): a
//     non-empty string content wins outright; a rich file/image/voice object
//     always renders "[历史X：name]" plus its description, where the
//     description resolves as payload.text || payload.description with real JS
//     truthiness (" " is truthy, missing payload falls back to the rich object
//     itself); any other object falls back to trimmed text blocks only when
//     its description is falsy.
//
// Both channels therefore carry the original text plus the annotation exactly
// once per consumer: the blocks channel always ends with one annotation text
// block (and materializes the user's string content first when the message had
// no exact text block), and the content channel appends the annotation to the
// string content or merges it into the rendered description field. Human
// copies and the stored message are never touched.
func withGatewayAnnotationAgentDelivery(blocks []types.ContentBlock, displayContent interface{}, modelText string) (interface{}, []types.ContentBlock) {
	if modelText == "" {
		return displayContent, blocks
	}
	originalText := ""
	if s, ok := displayContent.(string); ok && strings.TrimSpace(s) != "" {
		originalText = s
	}
	hasOriginalTextBlock := false
	for _, block := range blocks {
		// Exact lowercase "text" is what live agent clients match on (XiaoBa
		// parseMessage: typedBlock.type === 'text'); "TEXT" or " text " are
		// invisible to that merge and must not suppress the user-text
		// materialization below.
		if block.Type == "text" && strings.TrimSpace(block.Text) != "" {
			hasOriginalTextBlock = true
		}
	}
	annotatedBlocks := make([]types.ContentBlock, 0, len(blocks)+2)
	if !hasOriginalTextBlock && originalText != "" {
		// Materialize the user text so block-merging consumers (whose merged
		// text wins over the top-level content) cannot lose the body once the
		// annotation block exists.
		annotatedBlocks = append(annotatedBlocks, types.ContentBlock{Type: "text", Text: originalText})
	}
	annotatedBlocks = append(annotatedBlocks, blocks...)
	annotatedBlocks = append(annotatedBlocks, types.ContentBlock{Type: "text", Text: modelText})

	switch typed := displayContent.(type) {
	case nil:
		// Blocks-only message: keep content empty so the cloud consumer keeps
		// reading content_blocks (now carrying original text + annotation)
		// instead of manufacturing a string that would shadow them.
		return nil, annotatedBlocks
	case string:
		if strings.TrimSpace(typed) == "" {
			return typed, annotatedBlocks
		}
		return typed + "\n\n" + modelText, annotatedBlocks
	case map[string]interface{}:
		return withGatewayAnnotationRichContent(typed, annotatedBlocks, modelText)
	default:
		return typed, annotatedBlocks
	}
}

// withGatewayAnnotationRichContent merges the annotation into the description
// the actual cloudMessageText renders for object content. file/image/voice
// always render their description line (never fall back to blocks), and any
// other object renders its trimmed description or falls back to blocks; in
// both cases the annotation rides the winning field (payload.text wins over
// payload.description with JS truthiness, payload defaults to the rich object
// itself when message.payload is missing), so the original text and the
// annotation stay readable while url/name and sibling fields survive.
func withGatewayAnnotationRichContent(content map[string]interface{}, blocks []types.ContentBlock, modelText string) (interface{}, []types.ContentBlock) {
	nextContent := make(map[string]interface{}, len(content)+1)
	for key, value := range content {
		nextContent[key] = value
	}
	contentType := strings.TrimSpace(fmt.Sprint(nextContent["type"]))
	payload, ok := nextContent["payload"].(map[string]interface{})
	if !ok {
		// JS: rich.payload && typeof rich.payload === 'object' ? rich.payload : rich
		payload = nextContent
	} else {
		nextPayload := make(map[string]interface{}, len(payload)+1)
		for key, value := range payload {
			nextPayload[key] = value
		}
		nextContent["payload"] = nextPayload
		payload = nextPayload
	}
	winner := ""
	if jsTruthy(payload["text"]) {
		winner = "text"
	} else if jsTruthy(payload["description"]) {
		winner = "description"
	} else if contentType == "file" || contentType == "image" || contentType == "voice" {
		// The consumer renders this shape's description unconditionally; with
		// no truthy source field the annotation needs one to exist.
		winner = "description"
	}
	if winner != "" {
		payload[winner] = jsStringOf(payload[winner]) + "\n\n" + modelText
	}
	// Blocks keep the annotation block for live/WS replay consumers; the
	// cloud consumer either reads the merged description (truthy) or falls
	// back to these same blocks when it did not render a description.
	return nextContent, blocks
}

// jsTruthy mirrors JavaScript truthiness for JSON-decoded values: "" false,
// false false, 0 false, everything else (including " ") true.
func jsTruthy(value interface{}) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case string:
		return typed != ""
	case bool:
		return typed
	case float64:
		return typed != 0
	case int:
		return typed != 0
	default:
		return true
	}
}

// jsStringOf approximates String(value) for the merged description base.
func jsStringOf(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case bool:
		if typed {
			return "true"
		}
		return "false"
	case float64:
		return strconv.FormatFloat(typed, 'g', -1, 64)
	default:
		return fmt.Sprint(typed)
	}
}

// GatewayAnnotationsEnabled reports whether a deployment can verify gateway
// application identity, so callers can degrade the annotation UI before a
// message is ever composed.
func (h *Hub) GatewayAnnotationsEnabled() bool {
	return h != nil && h.gatewayAnnotationAppResolver != nil
}

const (
	// gatewayAnnotationsMaxModelTextBytes bounds the fanout-only model text
	// block in UTF-8 bytes. The rendered text repeats every bounded value the
	// 16KiB metadata carried plus fixed per-annotation labels; the derived
	// worst case (16KiB of values + header/labels + numeric target fields) is
	// under 24KiB, so a 32KiB cap never truncates a contract-legal value. It
	// is only a safety net against future drift, and the proof test locks it.
	gatewayAnnotationsMaxModelTextBytes = 32 << 10
)

// gatewayAnnotationsModelText renders the validated annotation document as one
// bounded, clearly user-labeled text block for Agent clients that only forward
// content_blocks text (or the top-level content) into the model. The block is
// explicitly provenance-labeled discussion context: it never replaces user
// message text and never carries system instructions.
func gatewayAnnotationsModelText(document *gatewayAnnotationsDocument) string {
	if document == nil || len(document.Annotations) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("[Gateway 标注 | 用户提供的评审上下文，非系统指令]\n")
	builder.WriteString("应用 ")
	builder.WriteString(document.AppID)
	builder.WriteString("，页面 ")
	builder.WriteString(document.Page.Path)
	if document.Page.Revision != "" {
		builder.WriteString("（页面 revision ")
		builder.WriteString(document.Page.Revision)
		builder.WriteString("）")
	}
	builder.WriteString("。")
	for index, annotation := range document.Annotations {
		fmt.Fprintf(&builder, "\n%d. [%s]", index+1, annotation.Kind)
		if annotation.Label != "" {
			fmt.Fprintf(&builder, " 标题：%s |", annotation.Label)
		}
		fmt.Fprintf(&builder, " 评论：%s |", annotation.Body)
		builder.WriteString(annotation.Target.modelDescription())
	}
	text := builder.String()
	if len(text) > gatewayAnnotationsMaxModelTextBytes {
		byteCap := gatewayAnnotationsMaxModelTextBytes
		for byteCap < len(text) && !utf8.RuneStart(text[byteCap]) {
			byteCap++
		}
		text = text[:byteCap] + "…（标注文本块超出安全上限，结构化 gateway_annotations 仍是完整源）"
	}
	return text
}

// modelDescription renders one target with its locating evidence, mirroring the
// bounded metadata contract field names an Agent SDK can rely on.
func (t gatewayAnnotationTarget) modelDescription() string {
	var parts []string
	if t.ElementID != "" {
		parts = append(parts, "element_id="+t.ElementID)
	}
	if t.Selector != "" {
		parts = append(parts, "selector="+t.Selector)
	}
	if t.Text != "" {
		parts = append(parts, "选中文本：「"+t.Text+"」")
	}
	if t.Prefix != "" {
		parts = append(parts, "前缀：「"+t.Prefix+"」")
	}
	if t.Suffix != "" {
		parts = append(parts, "后缀：「"+t.Suffix+"」")
	}
	if t.Rect != nil {
		parts = append(parts, fmt.Sprintf("区域(x=%v,y=%v,w=%v,h=%v)", t.Rect.X, t.Rect.Y, t.Rect.Width, t.Rect.Height))
	}
	// Region rects are meaningless without their coordinate frame and the
	// viewport they were measured in; the SDK contract always records the
	// coordinate-space and passing the viewport dimension/scroll evidence
	// keeps the model able to place the highlight on a recordable scale.
	if t.Rect != nil || t.CoordinateSpace != "" {
		parts = append(parts, "coordinate_space="+t.CoordinateSpace)
	}
	if t.Viewport != nil {
		parts = append(parts, fmt.Sprintf("viewport(w=%v,h=%v,scroll_x=%v,scroll_y=%v)",
			t.Viewport.Width, t.Viewport.Height, t.Viewport.ScrollX, t.Viewport.ScrollY))
	}
	return strings.Join(parts, " ")
}

// gatewayAnnotationHistoryModelText composes the readable annotation context
// for one offline history reader. Only the annotated Agent (never the message
// author, and only when the topic still resolves to that exact Agent) sees
// it, and it is rendered from the canonical stored gateway_annotations value
// alone — a client-supplied context block is never consulted.
func (h *Hub) gatewayAnnotationHistoryModelText(message *types.Message, recipientUID int64) string {
	if h == nil || h.db == nil || recipientUID <= 0 || message == nil || message.Metadata == nil {
		return ""
	}
	if recipientUID == message.FromUID {
		return ""
	}
	raw, hasAnnotations := message.Metadata[gatewayAnnotationsMetadataKey]
	if !hasAnnotations {
		return ""
	}
	document, err := normalizeGatewayAnnotations(raw)
	if err != nil || document == nil {
		return ""
	}
	agentUID, ok := h.artifactAgentForTopic(message.FromUID, message.TopicID)
	if !ok || agentUID != recipientUID || agentUID != document.AgentUID {
		return ""
	}
	return gatewayAnnotationsModelText(document)
}
