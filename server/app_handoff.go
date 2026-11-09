package server

import (
	"encoding/json"
	"errors"
	"math"
	"regexp"
	"strings"
	"time"
)

const AppOpenContractV2 = "catsco.app-open.v2"

var handoffRequestID = regexp.MustCompile(`^[a-zA-Z0-9_-]{8,80}$`)
var handoffParamKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,47}$`)
var handoffSensitiveKey = regexp.MustCompile(`(?i)password|passwd|token|secret|credential|private.?key|authorization|cookie|command|script`)
var handoffUnsafeValue = regexp.MustCompile("(?i)-----BEGIN|(?:https?|ssh|sftp)://|bearer\\s|[\\r\\n\\x00-\\x1f]")

// This is only a declarative handoff. No job, version, credential or URL is
// accepted. Per-application schema validation happens before iframe delivery.
func normalizeAppHandoff(value interface{}) (map[string]interface{}, error) {
	fail := func() (map[string]interface{}, error) { return nil, errors.New("invalid app handoff") }
	object, ok := value.(map[string]interface{})
	if !ok {
		return fail()
	}
	id, _ := object["request_id"].(string)
	topic, _ := object["topic_id"].(string)
	action, _ := object["action"].(string)
	if !handoffRequestID.MatchString(id) || topic == "" || len(topic) > 128 || (action != "initialize" && action != "run") {
		return fail()
	}
	userRun, _ := object["user_requested_run"].(bool)
	if action == "run" && !userRun {
		return fail()
	}
	expires, err := appOpenUID(object["expires_at"])
	if err != nil || expires <= time.Now().UnixMilli() || expires > time.Now().Add(5*time.Minute).UnixMilli() {
		return fail()
	}
	params, ok := object["parameters"].(map[string]interface{})
	if !ok || len(params) > 16 {
		return fail()
	}
	encoded, err := json.Marshal(params)
	if err != nil || len(encoded) > 4096 {
		return fail()
	}
	clean := make(map[string]interface{}, len(params))
	for key, value := range params {
		if !handoffParamKey.MatchString(key) || handoffSensitiveKey.MatchString(key) {
			return fail()
		}
		switch v := value.(type) {
		case string:
			if len([]rune(v)) > 512 || handoffUnsafeValue.MatchString(v) {
				return fail()
			}
		case float64:
			if math.IsNaN(v) || math.IsInf(v, 0) {
				return fail()
			}
		case int, int64, bool:
		default:
			return fail()
		}
		clean[key] = value
	}
	return map[string]interface{}{"request_id": id, "topic_id": strings.TrimSpace(topic), "action": action, "parameters": clean, "expires_at": expires, "user_requested_run": action == "run"}, nil
}
