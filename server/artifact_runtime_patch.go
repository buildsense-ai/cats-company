package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

const (
	artifactRuntimePatchMaxOperations = 64
	artifactRuntimePatchMaxPathBytes  = 1024
)

func applyArtifactRuntimePatch(current, rawOperations json.RawMessage) (json.RawMessage, error) {
	document, err := decodeArtifactRuntimeJSON(current)
	if err != nil {
		return nil, errors.New("Artifact Runtime State is invalid")
	}
	operationsValue, err := decodeArtifactRuntimeJSON(rawOperations)
	if err != nil {
		return nil, errors.New("Artifact Runtime patch is invalid JSON")
	}
	operations, ok := operationsValue.([]interface{})
	if !ok || len(operations) == 0 || len(operations) > artifactRuntimePatchMaxOperations {
		return nil, fmt.Errorf("Artifact Runtime patch must contain 1-%d operations", artifactRuntimePatchMaxOperations)
	}
	for index, raw := range operations {
		operation, ok := raw.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("Artifact Runtime patch[%d] must be an object", index)
		}
		next, err := applyArtifactRuntimePatchOperation(document, operation)
		if err != nil {
			return nil, fmt.Errorf("Artifact Runtime patch[%d]: %w", index, err)
		}
		document = next
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		return nil, errors.New("Artifact Runtime patch result is invalid")
	}
	return normalizeBoundedArtifactJSON(encoded, artifactRuntimeStateMaxBytes, artifactRuntimeStateMaxNodes)
}

func applyArtifactRuntimePatchOperation(document interface{}, operation map[string]interface{}) (interface{}, error) {
	for key := range operation {
		if artifactTaskUnsafeKey(key) || (key != "op" && key != "path" && key != "value") {
			return nil, fmt.Errorf("contains unsupported field %q", key)
		}
	}
	op, opOK := operation["op"].(string)
	path, pathOK := operation["path"].(string)
	if !opOK || !pathOK || (op != "add" && op != "replace" && op != "remove") {
		return nil, errors.New("op or path is invalid")
	}
	value, hasValue := operation["value"]
	if (op == "add" || op == "replace") != hasValue {
		return nil, errors.New("value presence does not match op")
	}
	segments, err := artifactRuntimeJSONPointer(path)
	if err != nil {
		return nil, err
	}
	if len(segments) == 0 {
		if op == "remove" {
			return nil, errors.New("removing the document root is not supported")
		}
		return value, nil
	}
	parent, err := artifactRuntimePatchParent(document, segments[:len(segments)-1])
	if err != nil {
		return nil, err
	}
	leaf := segments[len(segments)-1]
	switch typed := parent.(type) {
	case map[string]interface{}:
		_, exists := typed[leaf]
		switch op {
		case "add":
			typed[leaf] = value
		case "replace":
			if !exists {
				return nil, errors.New("replace target does not exist")
			}
			typed[leaf] = value
		case "remove":
			if !exists {
				return nil, errors.New("remove target does not exist")
			}
			delete(typed, leaf)
		}
		return document, nil
	case []interface{}:
		if op == "add" && leaf == "-" {
			typed = append(typed, value)
			return replaceArtifactRuntimePatchParent(document, segments[:len(segments)-1], typed)
		}
		position, err := artifactRuntimeArrayIndex(leaf, len(typed), op == "add")
		if err != nil {
			return nil, err
		}
		switch op {
		case "add":
			typed = append(typed, nil)
			copy(typed[position+1:], typed[position:])
			typed[position] = value
		case "replace":
			typed[position] = value
		case "remove":
			typed = append(typed[:position], typed[position+1:]...)
		}
		return replaceArtifactRuntimePatchParent(document, segments[:len(segments)-1], typed)
	default:
		return nil, errors.New("patch parent is not an object or array")
	}
}

func artifactRuntimePatchParent(document interface{}, segments []string) (interface{}, error) {
	current := document
	for _, segment := range segments {
		switch typed := current.(type) {
		case map[string]interface{}:
			next, ok := typed[segment]
			if !ok {
				return nil, errors.New("patch parent path does not exist")
			}
			current = next
		case []interface{}:
			position, err := artifactRuntimeArrayIndex(segment, len(typed), false)
			if err != nil {
				return nil, err
			}
			current = typed[position]
		default:
			return nil, errors.New("patch parent path is not traversable")
		}
	}
	return current, nil
}

func replaceArtifactRuntimePatchParent(document interface{}, segments []string, replacement interface{}) (interface{}, error) {
	if len(segments) == 0 {
		return replacement, nil
	}
	parent, err := artifactRuntimePatchParent(document, segments[:len(segments)-1])
	if err != nil {
		return nil, err
	}
	leaf := segments[len(segments)-1]
	switch typed := parent.(type) {
	case map[string]interface{}:
		if _, ok := typed[leaf]; !ok {
			return nil, errors.New("patch parent path changed unexpectedly")
		}
		typed[leaf] = replacement
	case []interface{}:
		position, err := artifactRuntimeArrayIndex(leaf, len(typed), false)
		if err != nil {
			return nil, err
		}
		typed[position] = replacement
	default:
		return nil, errors.New("patch parent path is not traversable")
	}
	return document, nil
}

func artifactRuntimeArrayIndex(value string, length int, allowEnd bool) (int, error) {
	if value == "" || (len(value) > 1 && value[0] == '0') {
		return 0, errors.New("array index is invalid")
	}
	position, err := strconv.Atoi(value)
	if err != nil || position < 0 || position > length || (!allowEnd && position == length) {
		return 0, errors.New("array index is out of range")
	}
	return position, nil
}

func artifactRuntimeJSONPointer(value string) ([]string, error) {
	if len(value) > artifactRuntimePatchMaxPathBytes {
		return nil, errors.New("path exceeds size limits")
	}
	if value == "" {
		return nil, nil
	}
	if !strings.HasPrefix(value, "/") {
		return nil, errors.New("path must be a JSON Pointer")
	}
	parts := strings.Split(value[1:], "/")
	if len(parts) > 64 {
		return nil, errors.New("path exceeds depth limits")
	}
	for index, part := range parts {
		var decoded strings.Builder
		for offset := 0; offset < len(part); offset++ {
			if part[offset] != '~' {
				decoded.WriteByte(part[offset])
				continue
			}
			if offset+1 >= len(part) || (part[offset+1] != '0' && part[offset+1] != '1') {
				return nil, errors.New("path contains an invalid escape")
			}
			offset++
			if part[offset] == '0' {
				decoded.WriteByte('~')
			} else {
				decoded.WriteByte('/')
			}
		}
		parts[index] = decoded.String()
	}
	return parts, nil
}
