package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store/types"
)

const (
	// FileAnnotationsContractV1 is the exact contract_version every file
	// annotations value must declare.
	FileAnnotationsContractV1 = "catsco.file-annotations.v1"
	// FileOpenBindingContractV1 marks file-open binding references.
	FileOpenBindingContractV1 = "catsco.file-open-binding.v1"
	// fileAnnotationsMetadataKey is the message metadata key carrying the
	// canonical file_annotations document.
	fileAnnotationsMetadataKey = "file_annotations"
	// fileAnnotationsSummarySchema marks the agent-readable summary envelope.
	fileAnnotationsSummarySchema = "catsco.file_annotations_summary.v1"

	fileAnnotationsMaxAnnotations = 20
	fileAnnotationsMaxDocBytes    = 16 << 10
	fileAnnotationsMaxBodyRunes   = 2000
	fileAnnotationsMaxLabelRunes  = 256
	fileAnnotationsMaxIDRunes     = 128
	fileAnnotationsMaxQuoteRunes  = 4096
	fileAnnotationsMaxSheetRunes  = 128
	fileAnnotationsMaxSummary     = 4000
	fileAnnotationsMaxTimeSeconds = float64(36000)
	fileAnnotationsMaxPage        = int64(2000)
	fileAnnotationsMaxRow         = int64(200)
	fileAnnotationsMaxColumn      = int64(50)
	fileAnnotationsMaxOffset      = int64(1) << 20
	// Own-upload text verification is attempted only for small readable files;
	// anything larger or non-text is validated structurally.
	fileAnnotationsMaxVerifyBytes = 64 << 10
)

type fileAnnotationRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

// fileAnnotationTarget is the bounded, kind-dependent target of one file
// annotation. Only the fields for its kind are populated.
type fileAnnotationTarget struct {
	Rect        *fileAnnotationRect `json:"rect,omitempty"`
	TimeSeconds float64             `json:"time_seconds,omitempty"`
	Page        int64               `json:"page,omitempty"`
	Start       int64               `json:"start,omitempty"`
	End         int64               `json:"end,omitempty"`
	Quote       string              `json:"quote,omitempty"`
	LineStart   int64               `json:"line_start,omitempty"`
	LineEnd     int64               `json:"line_end,omitempty"`
	Sheet       string              `json:"sheet,omitempty"`
	RowStart    int64               `json:"row_start,omitempty"`
	RowEnd      int64               `json:"row_end,omitempty"`
	ColumnStart int64               `json:"column_start,omitempty"`
	ColumnEnd   int64               `json:"column_end,omitempty"`
}

type fileAnnotation struct {
	ID     string               `json:"id"`
	Kind   string               `json:"kind"`
	Label  string               `json:"label,omitempty"`
	Body   string               `json:"body"`
	Target fileAnnotationTarget `json:"target"`
}

// MarshalJSON preserves required zero-valued offsets and video time without
// adding unrelated target fields for other annotation kinds.
func (a fileAnnotation) MarshalJSON() ([]byte, error) {
	target := map[string]interface{}{}
	switch a.Kind {
	case "image":
		target["rect"] = a.Target.Rect
	case "video":
		target["rect"], target["time_seconds"] = a.Target.Rect, a.Target.TimeSeconds
	case "pdf":
		target["rect"], target["page"] = a.Target.Rect, a.Target.Page
	case "text", "code":
		target["start"], target["end"], target["quote"] = a.Target.Start, a.Target.End, a.Target.Quote
		target["line_start"], target["line_end"] = a.Target.LineStart, a.Target.LineEnd
	case "cells":
		target["sheet"], target["quote"] = a.Target.Sheet, a.Target.Quote
		target["row_start"], target["row_end"] = a.Target.RowStart, a.Target.RowEnd
		target["column_start"], target["column_end"] = a.Target.ColumnStart, a.Target.ColumnEnd
	}
	return json.Marshal(struct {
		ID     string                 `json:"id"`
		Kind   string                 `json:"kind"`
		Label  string                 `json:"label,omitempty"`
		Body   string                 `json:"body"`
		Target map[string]interface{} `json:"target"`
	}{a.ID, a.Kind, a.Label, a.Body, target})
}

// fileAnnotationSource is the server-derived canonical attachment descriptor.
// Version is the SHA256 of this descriptor: a stability signal for the
// persisted attachment reference, never a file-content digest and never
// derived from an arbitrary URL.
type fileAnnotationSource struct {
	TopicID         string `json:"topic_id"`
	MessageID       int64  `json:"message_id"`
	AttachmentIndex int64  `json:"attachment_index"`
	Name            string `json:"name"`
	URL             string `json:"url"`
	FileKey         string `json:"file_key,omitempty"`
	MimeType        string `json:"mime_type,omitempty"`
	Type            string `json:"type"`
	Size            int64  `json:"size"`
	Width           int64  `json:"width,omitempty"`
	Height          int64  `json:"height,omitempty"`
	Version         string `json:"version"`
}

type fileAnnotationsDocument struct {
	ContractVersion string               `json:"contract_version"`
	Source          fileAnnotationSource `json:"source"`
	Annotations     []fileAnnotation     `json:"annotations"`
	CreatedAt       time.Time            `json:"created_at,omitempty"`
}

func validFileAnnotationKind(value string) bool {
	switch value {
	case "image", "video", "pdf", "text", "code", "cells":
		return true
	}
	return false
}

// normalizeFileAnnotations re-encodes raw client JSON into the bounded
// canonical contract shape. Unknown fields are dropped. nil is returned only
// for an explicitly empty annotation list; every other violation is an error.
func normalizeFileAnnotations(value interface{}) (*fileAnnotationsDocument, error) {
	canonical, err := canonicalGatewayAnnotationsJSON(value)
	if err != nil {
		return nil, err
	}
	root, ok := canonical.(map[string]interface{})
	if !ok {
		return nil, errors.New("file_annotations must be an object")
	}
	version, _ := root["contract_version"].(string)
	if version != FileAnnotationsContractV1 {
		return nil, errors.New("file_annotations.contract_version is not supported")
	}
	source, err := normalizeFileAnnotationSource(root["source"])
	if err != nil {
		return nil, err
	}
	rawAnnotations, ok := root["annotations"].([]interface{})
	if !ok {
		return nil, errors.New("file_annotations.annotations must be an array")
	}
	if len(rawAnnotations) == 0 {
		return nil, nil
	}
	if len(rawAnnotations) > fileAnnotationsMaxAnnotations {
		return nil, fmt.Errorf("file_annotations carries %d annotations, at most %d are allowed", len(rawAnnotations), fileAnnotationsMaxAnnotations)
	}
	annotations := make([]fileAnnotation, 0, len(rawAnnotations))
	seen := make(map[string]bool, len(rawAnnotations))
	for index, raw := range rawAnnotations {
		annotation, err := normalizeFileAnnotation(raw, index)
		if err != nil {
			return nil, err
		}
		if seen[annotation.ID] {
			return nil, fmt.Errorf("file_annotations[%d].id %q is not unique", index, annotation.ID)
		}
		seen[annotation.ID] = true
		annotations = append(annotations, *annotation)
	}

	document := &fileAnnotationsDocument{
		ContractVersion: FileAnnotationsContractV1,
		Source:          *source,
		Annotations:     annotations,
	}
	if rawCreated, present := root["created_at"]; present && rawCreated != nil {
		created, _ := rawCreated.(string)
		stamped, err := time.Parse(time.RFC3339Nano, created)
		if err != nil {
			return nil, errors.New("file_annotations.created_at must be an RFC3339 timestamp")
		}
		if stamped.After(time.Now().UTC().Add(time.Minute)) {
			return nil, errors.New("file_annotations.created_at must not be in the future")
		}
		document.CreatedAt = stamped.UTC()
	}
	if err := document.checkSize(); err != nil {
		return nil, err
	}
	return document, nil
}

func normalizeFileAnnotationSource(value interface{}) (*fileAnnotationSource, error) {
	record, ok := value.(map[string]interface{})
	if !ok {
		return nil, errors.New("file_annotations.source must be an object")
	}
	source := &fileAnnotationSource{}
	var err error
	if source.TopicID, err = requiredFileString(record, "topic_id"); err != nil {
		return nil, err
	}
	if source.MessageID, err = requiredFileInt(record, "message_id"); err != nil {
		return nil, err
	}
	if source.AttachmentIndex, err = requiredFileInt(record, "attachment_index"); err != nil {
		return nil, err
	}
	if source.Name, err = optionalFileString(record, "name"); err != nil {
		return nil, err
	}
	if source.URL, err = optionalFileString(record, "url"); err != nil {
		return nil, err
	}
	if source.MimeType, err = optionalFileString(record, "mime_type"); err != nil {
		return nil, err
	}
	if source.Type, err = requiredFileString(record, "type"); err != nil {
		return nil, err
	}
	if source.Size, err = requiredFileInt(record, "size"); err != nil {
		return nil, err
	}
	if source.Version, err = requiredFileString(record, "version"); err != nil {
		return nil, err
	}
	if !isGroupTopic(source.TopicID) && !strings.HasPrefix(source.TopicID, "p2p_") {
		return nil, errors.New("file_annotations.source.topic_id is invalid")
	}
	if source.MessageID <= 0 || source.AttachmentIndex < 0 || source.Size < 0 {
		return nil, errors.New("file_annotations.source identity is invalid")
	}
	if source.Type != "image" && source.Type != "file" {
		return nil, errors.New("file_annotations.source.type must be image or file")
	}
	if !isLowerHex64(source.Version) {
		return nil, errors.New("file_annotations.source.version must be a 64-character hex descriptor digest")
	}
	if source.FileKey, err = optionalFileString(record, "file_key"); err != nil {
		return nil, err
	}
	if value, present := fileOptionalInt(record, "width"); present {
		if value <= 0 || value > int64(gatewayAnnotationsViewportMax) {
			return nil, errors.New("file_annotations.source.width is invalid")
		}
		source.Width = value
	}
	if value, present := fileOptionalInt(record, "height"); present {
		if value <= 0 || value > int64(gatewayAnnotationsViewportMax) {
			return nil, errors.New("file_annotations.source.height is invalid")
		}
		source.Height = value
	}
	return source, nil
}

func isLowerHex64(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, char := range value {
		if !('0' <= char && char <= '9' || 'a' <= char && char <= 'f') {
			return false
		}
	}
	return true
}

func fileOptionalInt(record map[string]interface{}, key string) (int64, bool) {
	raw, present := record[key]
	if !present || raw == nil {
		return 0, false
	}
	value, ok := imageEditInteger(raw)
	return int64(value), ok && value >= 0
}

func requiredFileString(record map[string]interface{}, key string) (string, error) {
	value, err := optionalFileString(record, key)
	if err != nil || strings.TrimSpace(value) == "" || containsControlCharacter(value) {
		return "", fmt.Errorf("file_annotations.source.%s is required", key)
	}
	if utf8.RuneCountInString(value) > 2048 {
		return "", fmt.Errorf("file_annotations.source.%s is too long", key)
	}
	return value, nil
}

func requiredFileInt(record map[string]interface{}, key string) (int64, error) {
	raw, present := record[key]
	if !present || raw == nil {
		return 0, fmt.Errorf("file_annotations.source.%s is required", key)
	}
	value, ok := imageEditInteger(raw)
	if !ok || value < 0 {
		return 0, fmt.Errorf("file_annotations.source.%s must be a non-negative integer", key)
	}
	return int64(value), nil
}

func optionalFileString(record map[string]interface{}, key string) (string, error) {
	raw, present := record[key]
	if !present || raw == nil {
		return "", nil
	}
	value, valid := raw.(string)
	if !valid {
		return "", fmt.Errorf("file_annotations.source.%s must be a string", key)
	}
	if containsControlCharacter(value) {
		return "", fmt.Errorf("file_annotations.source.%s contains control characters", key)
	}
	if utf8.RuneCountInString(value) > 2048 {
		return "", fmt.Errorf("file_annotations.source.%s is too long", key)
	}
	return value, nil
}

func normalizeFileAnnotation(value interface{}, index int) (*fileAnnotation, error) {
	record, ok := value.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("file_annotations[%d] must be an object", index)
	}
	prefix := fmt.Sprintf("file_annotations[%d]", index)
	annotationID, _ := record["id"].(string)
	if !strings.HasPrefix(annotationID, "fa_") || utf8.RuneCountInString(annotationID) > fileAnnotationsMaxIDRunes || containsControlCharacter(annotationID) {
		return nil, fmt.Errorf("%s.id must be a unique fa_ prefixed string of up to %d characters", prefix, fileAnnotationsMaxIDRunes)
	}
	kind, _ := record["kind"].(string)
	if !validFileAnnotationKind(kind) {
		return nil, fmt.Errorf("%s.kind must be one of image, video, pdf, text, code, cells", prefix)
	}
	label, _ := record["label"].(string)
	if utf8.RuneCountInString(label) > fileAnnotationsMaxLabelRunes {
		return nil, fmt.Errorf("%s.label must be at most %d characters", prefix, fileAnnotationsMaxLabelRunes)
	}
	body, _ := record["body"].(string)
	if strings.TrimSpace(body) == "" {
		return nil, fmt.Errorf("%s.body must carry the comment", prefix)
	}
	if utf8.RuneCountInString(body) > fileAnnotationsMaxBodyRunes {
		return nil, fmt.Errorf("%s.body must be at most %d characters", prefix, fileAnnotationsMaxBodyRunes)
	}
	target, err := normalizeFileAnnotationTarget(kind, record["target"])
	if err != nil {
		return nil, fmt.Errorf("%s.%v", prefix, err)
	}
	return &fileAnnotation{
		ID:     annotationID,
		Kind:   kind,
		Label:  label,
		Body:   body,
		Target: *target,
	}, nil
}

func normalizeFileAnnotationTarget(kind string, value interface{}) (*fileAnnotationTarget, error) {
	record, ok := value.(map[string]interface{})
	if !ok {
		return nil, errors.New("target must be an object")
	}
	target := &fileAnnotationTarget{}
	if kind == "image" || kind == "video" || kind == "pdf" {
		rawRect, present := record["rect"]
		if !present || rawRect == nil {
			return nil, errors.New("target.rect is required")
		}
		rect, err := normalizeFileAnnotationRect(rawRect)
		if err != nil {
			return nil, err
		}
		target.Rect = rect
	}
	switch kind {
	case "video":
		rawTime, present := record["time_seconds"]
		if !present || rawTime == nil {
			return nil, errors.New("target.time_seconds is required")
		}
		timeSeconds, ok := boundedFloat(record, "time_seconds", 0, fileAnnotationsMaxTimeSeconds)
		if !ok {
			return nil, errors.New("target.time_seconds must be a finite number in 0..36000")
		}
		target.TimeSeconds = timeSeconds
	case "pdf":
		page, ok := imageEditInteger(record["page"])
		if !ok || page < 1 || int64(page) > fileAnnotationsMaxPage {
			return nil, errors.New("target.page must be a positive integer up to 2000")
		}
		target.Page = int64(page)
	case "text", "code":
		for _, key := range []string{"start", "end"} {
			raw, present := record[key]
			if !present || raw == nil {
				return nil, fmt.Errorf("target.%s is required", key)
			}
			value, ok := imageEditInteger(raw)
			if !ok || value < 0 || int64(value) > fileAnnotationsMaxOffset {
				return nil, fmt.Errorf("target.%s must be an integer in 0..%d", key, fileAnnotationsMaxOffset)
			}
			if key == "start" {
				target.Start = int64(value)
			} else {
				target.End = int64(value)
			}
		}
		if target.Start >= target.End {
			return nil, errors.New("target.start must be smaller than target.end")
		}
		quote, _ := record["quote"].(string)
		if strings.TrimSpace(quote) == "" {
			return nil, errors.New("target.quote must carry the selected text")
		}
		if utf8.RuneCountInString(quote) > fileAnnotationsMaxQuoteRunes {
			return nil, errors.New("target.quote must be at most 4096 characters")
		}
		target.Quote = quote
		if value, present := fileOptionalInt(record, "line_start"); present {
			if value < 1 {
				return nil, errors.New("target.line_start must be positive")
			}
			target.LineStart = value
		}
		if value, present := fileOptionalInt(record, "line_end"); present {
			if value < 1 {
				return nil, errors.New("target.line_end must be positive")
			}
			target.LineEnd = value
		}
		if target.LineStart < 1 || target.LineEnd < target.LineStart {
			return nil, errors.New("target line range must be positive and ordered")
		}
	case "cells":
		sheet, _ := record["sheet"].(string)
		if strings.TrimSpace(sheet) == "" || utf8.RuneCountInString(sheet) > fileAnnotationsMaxSheetRunes || containsControlCharacter(sheet) {
			return nil, errors.New("target.sheet must be a bounded sheet name")
		}
		target.Sheet = sheet
		bounds := map[string]int64{}
		for _, key := range []string{"row_start", "row_end", "column_start", "column_end"} {
			raw, present := record[key]
			if !present || raw == nil {
				return nil, fmt.Errorf("target.%s is required", key)
			}
			value, ok := imageEditInteger(raw)
			if !ok || value < 1 {
				return nil, fmt.Errorf("target.%s must be a positive integer", key)
			}
			bounds[key] = int64(value)
		}
		if bounds["row_start"] > fileAnnotationsMaxRow || bounds["row_end"] > fileAnnotationsMaxRow {
			return nil, errors.New("target rows must be within 1..200")
		}
		if bounds["column_start"] > fileAnnotationsMaxColumn || bounds["column_end"] > fileAnnotationsMaxColumn {
			return nil, errors.New("target columns must be within 1..50")
		}
		if bounds["row_start"] > bounds["row_end"] || bounds["column_start"] > bounds["column_end"] {
			return nil, errors.New("target ranges must not be reversed")
		}
		target.RowStart, target.RowEnd = bounds["row_start"], bounds["row_end"]
		target.ColumnStart, target.ColumnEnd = bounds["column_start"], bounds["column_end"]
		quote, _ := record["quote"].(string)
		if strings.TrimSpace(quote) != "" {
			if utf8.RuneCountInString(quote) > fileAnnotationsMaxQuoteRunes {
				return nil, errors.New("target.quote must be at most 4096 characters")
			}
			target.Quote = quote
		}
	}
	return target, nil
}

func normalizeFileAnnotationRect(value interface{}) (*fileAnnotationRect, error) {
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
		return nil, errors.New("target.rect exceeds the surface")
	}
	return &fileAnnotationRect{X: x, Y: y, Width: width, Height: height}, nil
}

func (d *fileAnnotationsDocument) checkSize() error {
	raw, err := compactGatewayAnnotationsJSON(d)
	if err != nil {
		return errors.New("file_annotations is not JSON-encodable")
	}
	if len(raw) > fileAnnotationsMaxDocBytes {
		return fmt.Errorf("file_annotations is %d bytes, at most %d are allowed", len(raw), fileAnnotationsMaxDocBytes)
	}
	return nil
}

// restamped re-serializes the canonical document, re-enforcing the total size
// bound on the exact value that will be persisted.
func (d *fileAnnotationsDocument) restamped() (map[string]interface{}, error) {
	raw, err := compactGatewayAnnotationsJSON(d)
	if err != nil {
		return nil, errors.New("file_annotations is not JSON-encodable")
	}
	if len(raw) > fileAnnotationsMaxDocBytes {
		return nil, fmt.Errorf("file_annotations is %d bytes, at most %d are allowed", len(raw), fileAnnotationsMaxDocBytes)
	}
	var value map[string]interface{}
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, errors.New("file_annotations is not valid JSON")
	}
	return value, nil
}

// hasFileAnnotationsMetadata distinguishes an absent document from an invalid one.
func hasFileAnnotationsMetadata(metadata map[string]interface{}) bool {
	if metadata == nil {
		return false
	}
	_, ok := metadata[fileAnnotationsMetadataKey]
	return ok
}

// validateFileAnnotationsMetadata is the shared ingestion point for HTTP and
// WebSocket publishing. Only the private request field authorizes ingestion.
// Trust comes only from the unexported request field set by the dedicated
// file annotation handler (or this package's WS hook): a client-supplied
// public metadata key can never authorize the document. Messages without
// the key are untouched.
func (h *Hub) validateFileAnnotationsMetadata(actorUID int64, topicID string, metadata map[string]interface{}, trusted bool) (map[string]interface{}, *fileAnnotationsDocument, error) {
	metadata = metadataWithoutFileAnnotationContext(metadata)
	if metadata == nil {
		return nil, nil, nil
	}
	if !hasFileAnnotationsMetadata(metadata) {
		return metadata, nil, nil
	}
	if !trusted {
		// Ordinary clients cannot forge source authority.
		return nil, nil, errors.New("file_annotations require the dedicated annotation endpoint")
	}
	if h == nil || h.db == nil {
		return metadataWithoutFileAnnotations(metadata), nil, nil
	}
	document, err := normalizeFileAnnotations(metadata[fileAnnotationsMetadataKey])
	if err != nil {
		return nil, nil, err
	}
	if document == nil {
		return metadataWithoutFileAnnotations(metadata), nil, nil
	}
	// The document is already fully validated and canonical; re-stamp the
	// exact value that will be persisted.
	value, err := document.restamped()
	if err != nil {
		return nil, nil, err
	}
	next := metadataWithoutFileAnnotations(metadata)
	if next == nil {
		next = make(map[string]interface{}, 1)
	}
	next[fileAnnotationsMetadataKey] = value
	return next, document, nil
}

func metadataWithoutFileAnnotationContext(metadata map[string]interface{}) map[string]interface{} {
	if metadata == nil {
		return nil
	}
	_, hasContext := metadata[fileAnnotationsMetadataKey+"_context"]
	_, hasMarker := metadata["catsco_file_annotations_trusted"]
	if !hasContext && !hasMarker {
		return metadata
	}
	next := make(map[string]interface{}, len(metadata))
	for key, value := range metadata {
		if key != fileAnnotationsMetadataKey+"_context" && key != "catsco_file_annotations_trusted" {
			next[key] = value
		}
	}
	return next
}

// metadataWithoutFileAnnotations removes client contexts and private markers as well.
func metadataWithoutFileAnnotations(metadata map[string]interface{}) map[string]interface{} {
	if metadata == nil {
		return nil
	}
	next := make(map[string]interface{}, len(metadata))
	for key, value := range metadata {
		if key != fileAnnotationsMetadataKey && key != fileAnnotationsMetadataKey+"_context" && key != "catsco_file_annotations_trusted" {
			next[key] = value
		}
	}
	return next
}

// fileAnnotationsHumanContent renders the user-visible message text for an
// annotation submit. Server-side rendering only; clients never supply content.
func fileAnnotationsHumanContent(document *fileAnnotationsDocument) string {
	if document == nil {
		return ""
	}
	var builder strings.Builder
	fmt.Fprintf(&builder, "对文件「%s」做了 %d 处批注：", document.Source.Name, len(document.Annotations))
	for index, annotation := range document.Annotations {
		if index > 0 {
			builder.WriteString("；")
		}
		fmt.Fprintf(&builder, "[%d] %s", index+1, annotation.Body)
		if builder.Len() > fileAnnotationsMaxSummary {
			break
		}
	}
	return truncateGatewayAnnotationRunes(builder.String(), fileAnnotationsMaxSummary)
}

// fileAnnotationsSummary renders one bounded readable block for the topic's
// resolved agent. It is informational context, never a system instruction.
func fileAnnotationsSummary(document *fileAnnotationsDocument) string {
	if document == nil || len(document.Annotations) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("用户在会话 ")
	builder.WriteString(document.Source.TopicID)
	fmt.Fprintf(&builder, " 的消息 %d 附件「%s」（%s）上批注了 %d 处：", document.Source.MessageID, document.Source.Name, document.Source.MimeType, len(document.Annotations))
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
		builder.WriteString(" ")
		builder.WriteString(annotation.Target.modelDescription())
		if annotation.Kind == "video" && annotation.Target.TimeSeconds == 0 {
			builder.WriteString(" time_seconds=0")
		}
		if builder.Len() > fileAnnotationsMaxSummary {
			break
		}
	}
	return truncateGatewayAnnotationRunes(builder.String(), fileAnnotationsMaxSummary)
}

func (t fileAnnotationTarget) modelDescription() string {
	var parts []string
	if t.Rect != nil {
		parts = append(parts, fmt.Sprintf("区域(x=%v,y=%v,w=%v,h=%v)", t.Rect.X, t.Rect.Y, t.Rect.Width, t.Rect.Height))
	}
	if t.TimeSeconds != 0 {
		parts = append(parts, fmt.Sprintf("time_seconds=%v", t.TimeSeconds))
	}
	if t.Page != 0 {
		parts = append(parts, fmt.Sprintf("page=%d", t.Page))
	}
	if t.Start != 0 || t.End != 0 {
		parts = append(parts, fmt.Sprintf("文本区间(%d,%d)", t.Start, t.End))
	}
	if t.Quote != "" {
		parts = append(parts, "引用：「"+truncateGatewayAnnotationRunes(t.Quote, 512)+"」")
	}
	if t.LineStart != 0 || t.LineEnd != 0 {
		parts = append(parts, fmt.Sprintf("行(%d,%d)", t.LineStart, t.LineEnd))
	}
	if t.Sheet != "" {
		parts = append(parts, fmt.Sprintf("工作表%s 单元格(行%d-%d,列%d-%d)", t.Sheet, t.RowStart, t.RowEnd, t.ColumnStart, t.ColumnEnd))
	}
	return strings.Join(parts, " ")
}

// fileAnnotationAgentContext composes a fanout-only readable view for each
// authorized bot recipient. Human copies and durable metadata keep the canonical
// document; group comments retain ordinary mention and delivery behavior.
func (h *Hub) fileAnnotationAgentContext(actorUID int64, recipientUID int64, topicID string, sourceMetadata map[string]interface{}) map[string]interface{} {
	if h == nil || h.db == nil || recipientUID <= 0 || sourceMetadata == nil {
		return nil
	}
	if !hasFileAnnotationsMetadata(sourceMetadata) {
		return nil
	}
	document, err := normalizeFileAnnotations(sourceMetadata[fileAnnotationsMetadataKey])
	if err != nil || document == nil || document.Source.TopicID != topicID {
		return nil
	}
	actor, actorErr := h.db.GetUser(actorUID)
	if actorErr != nil || actor == nil || actor.AccountType != types.AccountHuman {
		return nil
	}
	agent, userErr := h.db.GetUser(recipientUID)
	if userErr != nil || agent == nil || agent.State != 0 || agent.AccountType != types.AccountBot {
		return nil
	}
	if status, _ := h.validateTopicReadAccess(recipientUID, types.AccountBot, topicID); status != 0 {
		return nil
	}
	original, readErr := h.db.GetMessagesSince(topicID, document.Source.MessageID-1, 1)
	if readErr != nil || len(original) != 1 || original[0] == nil || original[0].ID != document.Source.MessageID || original[0].TopicID != topicID {
		return nil
	}
	actual, present := canonicalFileAnnotationSource(original[0], int(document.Source.AttachmentIndex))
	if !present || actual.Version != document.Source.Version || !fileAnnotationSourceDeclaredMatch(document.Source, actual) {
		return nil
	}
	document.Source = actual
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
	source := map[string]interface{}{
		"topic_id":         document.Source.TopicID,
		"message_id":       document.Source.MessageID,
		"attachment_index": document.Source.AttachmentIndex,
		"name":             document.Source.Name,
		"url":              document.Source.URL,
		"mime_type":        document.Source.MimeType,
		"type":             document.Source.Type,
		"size":             document.Source.Size,
		"version":          document.Source.Version,
	}
	if document.Source.Width > 0 {
		source["width"] = document.Source.Width
	}
	if document.Source.Height > 0 {
		source["height"] = document.Source.Height
	}
	if document.Source.FileKey != "" {
		source["file_key"] = document.Source.FileKey
	}
	return map[string]interface{}{
		"schema":      fileAnnotationsSummarySchema,
		"source":      source,
		"annotations": annotations,
		"summary":     fileAnnotationsSummary(document),
	}
}

// fileAnnotationModelText is consumed by both live and history readers, which
// may ignore metadata. It only augments eligible bot recipients' read copies.
func (h *Hub) fileAnnotationModelText(actorUID, recipientUID int64, topicID string, metadata map[string]interface{}) string {
	context := h.fileAnnotationAgentContext(actorUID, recipientUID, topicID, metadata)
	if context == nil {
		return ""
	}
	summary, _ := context["summary"].(string)
	document, _ := normalizeFileAnnotations(metadata[fileAnnotationsMetadataKey])
	if document == nil {
		return ""
	}
	return "[文件批注上下文]\n文件引用：" + document.Source.URL + "\n" + summary
}

// fileAnnotationsMetadataForRecipient reconstructs verified bot context on
// live and replay copies, withholding unresolvable provenance from bot readers.
func (h *Hub) fileAnnotationsMetadataForRecipient(actorUID, recipientUID int64, topicID string, metadata map[string]interface{}) map[string]interface{} {
	clean := metadataWithoutFileAnnotationContext(metadata)
	context := h.fileAnnotationAgentContext(actorUID, recipientUID, topicID, clean)
	if context == nil && hasFileAnnotationsMetadata(clean) && h.isBotUser(recipientUID) {
		return metadataWithoutFileAnnotations(clean)
	}
	return withFileAnnotationAgentContext(clean, context, recipientUID)
}

// withFileAnnotationAgentContext attaches the composed context for one eligible
// recipient. It never mutates the input map.
func withFileAnnotationAgentContext(metadata map[string]interface{}, context map[string]interface{}, recipientUID int64) map[string]interface{} {
	if context == nil || recipientUID <= 0 {
		return metadata
	}
	next := make(map[string]interface{}, len(metadata)+1)
	for key, value := range metadata {
		next[key] = value
	}
	next[fileAnnotationsMetadataKey+"_context"] = context
	return next
}

// fileAnnotationTextExt lists the previewable text extensions for which quote
// verification against the own upload is attempted.
var fileAnnotationTextExt = map[string]bool{
	".txt": true, ".md": true, ".markdown": true, ".json": true, ".csv": true,
	".html": true, ".htm": true, ".xml": true, ".js": true, ".jsx": true, ".ts": true, ".tsx": true, ".rs": true,
	".go": true, ".py": true, ".java": true, ".c": true, ".cpp": true,
	".h": true, ".css": true, ".yaml": true, ".yml": true, ".log": true,
	".sql": true, ".sh": true,
}

// verifyFileTextQuote checks a text/code quote against the actual bytes of a
// small own-upload file. Verification is attempted only when the descriptor
// names a readable text upload within budget; anything else is validated
// structurally and the limitation is documented.
func (h *UploadHandler) verifyFileTextQuote(source fileAnnotationSource, target fileAnnotationTarget) error {
	if h == nil || source.FileKey == "" || !uploadFileNamePattern.MatchString(source.FileKey) {
		return nil
	}
	ext := strings.ToLower(fileExtFromFileKey(source.FileKey))
	if !fileAnnotationTextExt[ext] {
		return nil
	}
	path := filepath.Join(h.baseDir, "files", source.FileKey)
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > fileAnnotationsMaxVerifyBytes {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, fileAnnotationsMaxVerifyBytes+1))
	if err != nil || len(data) > fileAnnotationsMaxVerifyBytes {
		return nil
	}
	if !utf8.Valid(data) {
		return nil
	}
	text := strings.TrimPrefix(string(data), "\ufeff") // Match Response.text()'s UTF-8 BOM handling.
	units := utf16.Encode([]rune(text))
	if target.Start < 0 || target.End <= target.Start || target.End > int64(len(units)) {
		return errors.New("file_annotations text selection exceeds source text")
	}
	if string(utf16.Decode(units[target.Start:target.End])) != target.Quote {
		return errors.New("file_annotations text quote does not match the source text")
	}
	if target.LineStart > 0 || target.LineEnd > 0 {
		firstLine := int64(strings.Count(string(utf16.Decode(units[:target.Start])), "\n") + 1)
		lastLine := int64(strings.Count(string(utf16.Decode(units[:target.End])), "\n") + 1)
		if target.LineStart != firstLine || target.LineEnd != lastLine {
			return errors.New("file_annotations line range does not match the quote")
		}
	}
	return nil
}

func fileExtFromFileKey(key string) string {
	if index := strings.LastIndex(key, "."); index >= 0 {
		return strings.ToLower(key[index:])
	}
	return ""
}
