package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	artifactTaskManifestContract   = "catsco.artifact-manifest.v3"
	artifactTaskManifestContractV4 = "catsco.artifact-manifest.v4"
	artifactTaskManifestFilename   = "artifact-manifest.json"
	artifactTaskManifestMaxBytes   = 64 * 1024
	artifactTaskIntentMaxItems     = 16
	artifactTaskSchemaMaxDepth     = 8
	artifactTaskSchemaMaxNodes     = 256
	artifactTaskSchemaMaxProps     = 64
	artifactTaskSchemaMaxEnum      = 64
	artifactTaskPayloadMaxBytes    = 64 * 1024
)

var artifactTaskManifestKeys = map[string]bool{
	"contract_version":         true,
	"purpose":                  true,
	"views":                    true,
	"entities":                 true,
	"entrypoints":              true,
	"observation_capabilities": true,
	"result_sinks":             true,
	"task_intents":             true,
	"runtime":                  true,
}

// ArtifactTaskIntent is application-authored data from one immutable Artifact
// version. Its identity is server-validated, but its prose remains untrusted.
type ArtifactTaskIntent struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	ResultSink  string          `json:"result_sink"`
}

// ArtifactTaskIntentResolver loads one declared intent from the exact
// displayed Artifact version. Implementations must not read mutable latest
// content when a version was supplied.
type ArtifactTaskIntentResolver interface {
	ResolveArtifactTaskIntent(ctx context.Context, record ArtifactContextRecord, displayedVersion int64, intentID string) (ArtifactTaskIntent, error)
}

func (h *CloudArtifactHandler) ResolveArtifactTaskIntent(
	ctx context.Context,
	record ArtifactContextRecord,
	displayedVersion int64,
	intentID string,
) (ArtifactTaskIntent, error) {
	if h == nil || !validArtifactContextRecord(record, record.ID) || displayedVersion <= 0 ||
		!artifactResultSinkIDPattern.MatchString(intentID) {
		return ArtifactTaskIntent{}, errors.New("invalid Artifact task intent request")
	}
	manifestURL, err := artifactTaskVersionManifestURL(record, displayedVersion)
	if err != nil {
		return ArtifactTaskIntent{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, manifestURL, nil)
	if err != nil {
		return ArtifactTaskIntent{}, err
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("User-Agent", "catsco-artifact-task/1.0")
	client := h.httpClient
	if client == nil {
		client = http.DefaultClient
	}
	response, err := client.Do(request)
	if err != nil {
		return ArtifactTaskIntent{}, fmt.Errorf("Artifact task manifest unavailable: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return ArtifactTaskIntent{}, fmt.Errorf("Artifact task manifest returned %d", response.StatusCode)
	}
	if response.Request == nil || response.Request.URL == nil || response.Request.URL.String() != manifestURL {
		return ArtifactTaskIntent{}, errors.New("Artifact task manifest redirect is not allowed")
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, artifactTaskManifestMaxBytes+1))
	if err != nil || len(body) < 2 || len(body) > artifactTaskManifestMaxBytes {
		return ArtifactTaskIntent{}, errors.New("Artifact task manifest exceeds size limits")
	}
	return parseArtifactTaskIntentManifest(body, intentID)
}

func artifactTaskVersionManifestURL(record ArtifactContextRecord, displayedVersion int64) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(record.URL))
	if err != nil || parsed.User != nil || parsed.Host == "" ||
		(parsed.Scheme != "http" && parsed.Scheme != "https") || displayedVersion <= 0 {
		return "", errors.New("Artifact task manifest URL is invalid")
	}
	segments := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	artifactIndex := -1
	for index := 0; index+2 < len(segments); index++ {
		if segments[index] == "artifacts" && segments[index+1] == record.ID {
			artifactIndex = index
			break
		}
	}
	if artifactIndex < 0 {
		return "", errors.New("Artifact task manifest URL does not use a supported version route")
	}
	segments = append(segments[:artifactIndex+2], "v"+strconv.FormatInt(displayedVersion, 10), artifactTaskManifestFilename)
	parsed.Path = "/" + strings.Join(segments, "/")
	parsed.RawPath = ""
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func parseArtifactTaskIntentManifest(body []byte, intentID string) (ArtifactTaskIntent, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var manifest map[string]interface{}
	if err := decoder.Decode(&manifest); err != nil || ensureJSONEOF(decoder) != nil || manifest == nil {
		return ArtifactTaskIntent{}, errors.New("Artifact task manifest is invalid JSON")
	}
	for key := range manifest {
		if !artifactTaskManifestKeys[key] || artifactTaskUnsafeKey(key) {
			return ArtifactTaskIntent{}, fmt.Errorf("Artifact task manifest contains unsupported field %q", key)
		}
	}
	contractVersion := manifest["contract_version"]
	if contractVersion != artifactTaskManifestContract && contractVersion != artifactTaskManifestContractV4 {
		return ArtifactTaskIntent{}, fmt.Errorf(
			"Artifact task manifest must use %s or %s",
			artifactTaskManifestContract,
			artifactTaskManifestContractV4,
		)
	}
	_, hasRuntime := manifest["runtime"]
	if contractVersion != artifactTaskManifestContractV4 && hasRuntime {
		return ArtifactTaskIntent{}, errors.New("Only Artifact manifest v4 supports runtime")
	}
	sinkIDs, err := artifactTaskManifestSinkIDs(manifest["result_sinks"])
	if err != nil {
		return ArtifactTaskIntent{}, err
	}
	intents, ok := manifest["task_intents"].([]interface{})
	if !ok || len(intents) == 0 || len(intents) > artifactTaskIntentMaxItems {
		return ArtifactTaskIntent{}, errors.New("Artifact task manifest task_intents is invalid")
	}
	seen := make(map[string]bool, len(intents))
	var matched ArtifactTaskIntent
	for index, raw := range intents {
		value, ok := raw.(map[string]interface{})
		if !ok {
			return ArtifactTaskIntent{}, fmt.Errorf("Artifact task_intents[%d] must be an object", index)
		}
		for key := range value {
			if artifactTaskUnsafeKey(key) || (key != "id" && key != "title" && key != "description" && key != "input_schema" && key != "result_sink") {
				return ArtifactTaskIntent{}, fmt.Errorf("Artifact task_intents[%d] contains unsupported field %q", index, key)
			}
		}
		id, idOK := artifactTaskManifestText(value["id"], 128)
		title, titleOK := artifactTaskManifestText(value["title"], 256)
		description, descriptionOK := artifactTaskManifestText(value["description"], 500)
		resultSink, sinkOK := artifactTaskManifestText(value["result_sink"], 128)
		if !idOK || !artifactResultSinkIDPattern.MatchString(id) || seen[id] || !titleOK || !descriptionOK ||
			!sinkOK || !artifactResultSinkIDPattern.MatchString(resultSink) || !sinkIDs[resultSink] {
			return ArtifactTaskIntent{}, fmt.Errorf("Artifact task_intents[%d] is invalid", index)
		}
		seen[id] = true
		schema, err := normalizeArtifactTaskSchema(value["input_schema"])
		if err != nil {
			return ArtifactTaskIntent{}, fmt.Errorf("Artifact task_intents[%d].input_schema: %w", index, err)
		}
		if id == intentID {
			matched = ArtifactTaskIntent{
				ID:          id,
				Title:       title,
				Description: description,
				InputSchema: schema,
				ResultSink:  resultSink,
			}
		}
	}
	if matched.ID == "" {
		return ArtifactTaskIntent{}, errors.New("Artifact task intent is not declared by this version")
	}
	return matched, nil
}

func artifactTaskManifestSinkIDs(value interface{}) (map[string]bool, error) {
	items, ok := value.([]interface{})
	if !ok || len(items) == 0 || len(items) > 16 {
		return nil, errors.New("Artifact task manifest requires result_sinks")
	}
	result := make(map[string]bool, len(items))
	for index, raw := range items {
		entry, ok := raw.(map[string]interface{})
		id, idOK := artifactTaskManifestText(entry["id"], 128)
		if !ok || !idOK || !artifactResultSinkIDPattern.MatchString(id) || result[id] {
			return nil, fmt.Errorf("Artifact result_sinks[%d] is invalid", index)
		}
		result[id] = true
	}
	return result, nil
}

func artifactTaskManifestText(value interface{}, maxRunes int) (string, bool) {
	text, ok := value.(string)
	return text, ok && text != "" && text == strings.TrimSpace(text) && utf8.ValidString(text) &&
		utf8.RuneCountInString(text) <= maxRunes && !strings.ContainsAny(text, "\x00\r\n")
}

func artifactTaskUnsafeKey(value string) bool {
	return value == "__proto__" || value == "prototype" || value == "constructor"
}

func normalizeArtifactTaskSchema(value interface{}) (json.RawMessage, error) {
	budget := artifactTaskSchemaBudget{nodes: artifactTaskSchemaMaxNodes}
	if err := validateArtifactTaskSchemaNode(value, 0, &budget); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) == 0 || len(raw) > 24*1024 {
		return nil, errors.New("schema exceeds size limits")
	}
	return raw, nil
}

type artifactTaskSchemaBudget struct {
	nodes int
}

func validateArtifactTaskSchemaNode(value interface{}, depth int, budget *artifactTaskSchemaBudget) error {
	if depth > artifactTaskSchemaMaxDepth || budget == nil || budget.nodes <= 0 {
		return errors.New("schema exceeds complexity limits")
	}
	budget.nodes--
	schema, ok := value.(map[string]interface{})
	if !ok {
		return errors.New("schema must be an object")
	}
	typeName, ok := artifactTaskManifestText(schema["type"], 32)
	if !ok || !artifactTaskSchemaType(typeName) {
		return errors.New("schema type is invalid")
	}
	allowed := map[string]bool{"type": true, "title": true, "description": true, "enum": true}
	switch typeName {
	case "object":
		allowed["properties"], allowed["required"], allowed["additionalProperties"] = true, true, true
	case "array":
		allowed["items"], allowed["minItems"], allowed["maxItems"] = true, true, true
	case "string":
		allowed["minLength"], allowed["maxLength"] = true, true
	case "number", "integer":
		allowed["minimum"], allowed["maximum"] = true, true
	}
	for key := range schema {
		if !allowed[key] || artifactTaskUnsafeKey(key) {
			return fmt.Errorf("schema contains unsupported field %q", key)
		}
	}
	if title, present := schema["title"]; present {
		if _, ok := artifactTaskManifestText(title, 256); !ok {
			return errors.New("schema title is invalid")
		}
	}
	if description, present := schema["description"]; present {
		if _, ok := artifactTaskManifestText(description, 1000); !ok {
			return errors.New("schema description is invalid")
		}
	}
	if enum, present := schema["enum"]; present {
		items, ok := enum.([]interface{})
		if !ok || len(items) == 0 || len(items) > artifactTaskSchemaMaxEnum {
			return errors.New("schema enum is invalid")
		}
		seen := make(map[string]bool, len(items))
		for _, item := range items {
			if !artifactTaskValueMatchesType(item, typeName) {
				return errors.New("schema enum value has the wrong type")
			}
			encoded, _ := json.Marshal(item)
			if seen[string(encoded)] {
				return errors.New("schema enum contains duplicates")
			}
			seen[string(encoded)] = true
		}
	}
	switch typeName {
	case "object":
		properties := map[string]interface{}{}
		if raw, present := schema["properties"]; present {
			var ok bool
			properties, ok = raw.(map[string]interface{})
			if !ok || len(properties) > artifactTaskSchemaMaxProps {
				return errors.New("schema properties is invalid")
			}
		}
		for key, child := range properties {
			if key == "" || artifactTaskUnsafeKey(key) || utf8.RuneCountInString(key) > 128 {
				return errors.New("schema property name is invalid")
			}
			if err := validateArtifactTaskSchemaNode(child, depth+1, budget); err != nil {
				return err
			}
		}
		if raw, present := schema["required"]; present {
			items, ok := raw.([]interface{})
			if !ok || len(items) > artifactTaskSchemaMaxProps {
				return errors.New("schema required is invalid")
			}
			seen := map[string]bool{}
			for _, item := range items {
				name, ok := artifactTaskManifestText(item, 128)
				if !ok || seen[name] || properties[name] == nil {
					return errors.New("schema required references an unknown property")
				}
				seen[name] = true
			}
		}
		if additional, present := schema["additionalProperties"]; present {
			if _, ok := additional.(bool); !ok {
				return errors.New("schema additionalProperties must be boolean")
			}
		}
	case "array":
		items, present := schema["items"]
		if !present {
			return errors.New("array schema requires items")
		}
		if err := validateArtifactTaskSchemaNode(items, depth+1, budget); err != nil {
			return err
		}
		if err := validateArtifactTaskIntegerBounds(schema, "minItems", "maxItems", 0, 1000); err != nil {
			return err
		}
	case "string":
		if err := validateArtifactTaskIntegerBounds(schema, "minLength", "maxLength", 0, 16384); err != nil {
			return err
		}
	case "number", "integer":
		minimum, hasMinimum, err := artifactTaskSchemaNumber(schema["minimum"])
		if err != nil {
			return errors.New("schema minimum is invalid")
		}
		maximum, hasMaximum, err := artifactTaskSchemaNumber(schema["maximum"])
		if err != nil {
			return errors.New("schema maximum is invalid")
		}
		if hasMinimum && hasMaximum && minimum > maximum {
			return errors.New("schema minimum exceeds maximum")
		}
	}
	return nil
}

func artifactTaskSchemaType(value string) bool {
	switch value {
	case "object", "array", "string", "number", "integer", "boolean", "null":
		return true
	default:
		return false
	}
}

func validateArtifactTaskIntegerBounds(schema map[string]interface{}, minKey, maxKey string, floor, ceiling int64) error {
	minimum, hasMinimum, err := artifactTaskSchemaInteger(schema[minKey])
	if err != nil || (hasMinimum && (minimum < floor || minimum > ceiling)) {
		return fmt.Errorf("schema %s is invalid", minKey)
	}
	maximum, hasMaximum, err := artifactTaskSchemaInteger(schema[maxKey])
	if err != nil || (hasMaximum && (maximum < floor || maximum > ceiling)) {
		return fmt.Errorf("schema %s is invalid", maxKey)
	}
	if hasMinimum && hasMaximum && minimum > maximum {
		return fmt.Errorf("schema %s exceeds %s", minKey, maxKey)
	}
	return nil
}

func artifactTaskSchemaInteger(value interface{}) (int64, bool, error) {
	if value == nil {
		return 0, false, nil
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, false, errors.New("not an integer")
	}
	parsed, err := number.Int64()
	return parsed, true, err
}

func artifactTaskSchemaNumber(value interface{}) (float64, bool, error) {
	if value == nil {
		return 0, false, nil
	}
	number, ok := value.(json.Number)
	if !ok {
		return 0, false, errors.New("not a number")
	}
	parsed, err := number.Float64()
	return parsed, true, err
}

func validateArtifactTaskPayload(schemaRaw, payloadRaw json.RawMessage) (json.RawMessage, error) {
	payload, err := normalizeBoundedArtifactJSON(payloadRaw, artifactTaskPayloadMaxBytes, 16_384)
	if err != nil {
		return nil, err
	}
	schemaValue, err := decodeArtifactTaskJSON(schemaRaw)
	if err != nil {
		return nil, errors.New("Artifact task input schema is invalid")
	}
	payloadValue, err := decodeArtifactTaskJSON(payload)
	if err != nil {
		return nil, errors.New("Artifact task payload is invalid")
	}
	if err := validateArtifactTaskValue(schemaValue, payloadValue, "payload"); err != nil {
		return nil, err
	}
	return payload, nil
}

func decodeArtifactTaskJSON(raw json.RawMessage) (interface{}, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value interface{}
	if err := decoder.Decode(&value); err != nil || ensureJSONEOF(decoder) != nil {
		return nil, errors.New("invalid JSON")
	}
	return value, nil
}

func validateArtifactTaskValue(schemaValue, value interface{}, path string) error {
	schema, ok := schemaValue.(map[string]interface{})
	if !ok {
		return errors.New("Artifact task schema is invalid")
	}
	typeName, _ := schema["type"].(string)
	if !artifactTaskValueMatchesType(value, typeName) {
		return fmt.Errorf("%s does not match schema type %s", path, typeName)
	}
	if rawEnum, present := schema["enum"]; present {
		items, _ := rawEnum.([]interface{})
		encoded, _ := json.Marshal(value)
		matched := false
		for _, item := range items {
			candidate, _ := json.Marshal(item)
			if bytes.Equal(candidate, encoded) {
				matched = true
				break
			}
		}
		if !matched {
			return fmt.Errorf("%s is not an allowed enum value", path)
		}
	}
	switch typeName {
	case "object":
		object := value.(map[string]interface{})
		properties, _ := schema["properties"].(map[string]interface{})
		if required, ok := schema["required"].([]interface{}); ok {
			for _, raw := range required {
				name, _ := raw.(string)
				if _, present := object[name]; !present {
					return fmt.Errorf("%s.%s is required", path, name)
				}
			}
		}
		allowAdditional := true
		if configured, ok := schema["additionalProperties"].(bool); ok {
			allowAdditional = configured
		}
		for key, child := range object {
			childSchema, declared := properties[key]
			if !declared {
				if !allowAdditional {
					return fmt.Errorf("%s.%s is not allowed", path, key)
				}
				continue
			}
			if err := validateArtifactTaskValue(childSchema, child, path+"."+key); err != nil {
				return err
			}
		}
	case "array":
		array := value.([]interface{})
		if minimum, present, _ := artifactTaskSchemaInteger(schema["minItems"]); present && int64(len(array)) < minimum {
			return fmt.Errorf("%s has too few items", path)
		}
		if maximum, present, _ := artifactTaskSchemaInteger(schema["maxItems"]); present && int64(len(array)) > maximum {
			return fmt.Errorf("%s has too many items", path)
		}
		for index, item := range array {
			if err := validateArtifactTaskValue(schema["items"], item, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	case "string":
		length := int64(utf8.RuneCountInString(value.(string)))
		if minimum, present, _ := artifactTaskSchemaInteger(schema["minLength"]); present && length < minimum {
			return fmt.Errorf("%s is too short", path)
		}
		if maximum, present, _ := artifactTaskSchemaInteger(schema["maxLength"]); present && length > maximum {
			return fmt.Errorf("%s is too long", path)
		}
	case "number", "integer":
		number, _ := value.(json.Number)
		parsed, _ := number.Float64()
		if minimum, present, _ := artifactTaskSchemaNumber(schema["minimum"]); present && parsed < minimum {
			return fmt.Errorf("%s is below minimum", path)
		}
		if maximum, present, _ := artifactTaskSchemaNumber(schema["maximum"]); present && parsed > maximum {
			return fmt.Errorf("%s exceeds maximum", path)
		}
	}
	return nil
}

func artifactTaskValueMatchesType(value interface{}, typeName string) bool {
	switch typeName {
	case "object":
		_, ok := value.(map[string]interface{})
		return ok
	case "array":
		_, ok := value.([]interface{})
		return ok
	case "string":
		_, ok := value.(string)
		return ok
	case "number":
		_, ok := value.(json.Number)
		return ok
	case "integer":
		number, ok := value.(json.Number)
		if !ok {
			return false
		}
		_, err := number.Int64()
		return err == nil
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	default:
		return false
	}
}
