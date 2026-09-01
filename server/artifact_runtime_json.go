package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

func decodeArtifactRuntimeJSON(raw json.RawMessage) (interface{}, error) {
	if err := validateArtifactRuntimeJSONTokens(raw); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value interface{}
	if err := decoder.Decode(&value); err != nil || ensureJSONEOF(decoder) != nil {
		return nil, errors.New("invalid JSON")
	}
	return value, nil
}

// Durable Runtime data must not depend on which duplicate field a JSON
// implementation keeps when it normalizes an object into a map.
func validateArtifactRuntimeJSONTokens(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := scanArtifactRuntimeJSONValue(decoder, 0); err != nil {
		return err
	}
	if err := ensureJSONEOF(decoder); err != nil {
		return errors.New("invalid JSON")
	}
	return nil
}

func scanArtifactRuntimeJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 64 {
		return errors.New("JSON exceeds depth limits")
	}
	token, err := decoder.Token()
	if err != nil {
		return errors.New("invalid JSON")
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return errors.New("invalid JSON")
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("invalid JSON object key")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("JSON contains duplicate field %q", key)
			}
			seen[key] = struct{}{}
			if err := scanArtifactRuntimeJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("invalid JSON object")
		}
	case '[':
		for decoder.More() {
			if err := scanArtifactRuntimeJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("invalid JSON array")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}
