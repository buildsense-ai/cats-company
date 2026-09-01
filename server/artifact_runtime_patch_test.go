package server

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestApplyArtifactRuntimePatch(t *testing.T) {
	current := json.RawMessage(`{
		"title":"Risk register",
		"items":[{"id":"r1","status":"open"}],
		"meta":{"owner":"Ada","a/b":"old"}
	}`)
	patch := json.RawMessage(`[
		{"op":"replace","path":"/items/0/status","value":"closed"},
		{"op":"add","path":"/items/-","value":{"id":"r2","status":"open"}},
		{"op":"remove","path":"/meta/owner"},
		{"op":"replace","path":"/meta/a~1b","value":"new"}
	]`)
	got, err := applyArtifactRuntimePatch(current, patch)
	if err != nil {
		t.Fatalf("apply patch: %v", err)
	}
	var actual interface{}
	if err := json.Unmarshal(got, &actual); err != nil {
		t.Fatalf("decode result: %v", err)
	}
	var expected interface{}
	if err := json.Unmarshal([]byte(`{
		"title":"Risk register",
		"items":[{"id":"r1","status":"closed"},{"id":"r2","status":"open"}],
		"meta":{"a/b":"new"}
	}`), &expected); err != nil {
		t.Fatalf("decode expected: %v", err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("patched document = %#v, want %#v", actual, expected)
	}
}

func TestApplyArtifactRuntimePatchRejectsInvalidMutation(t *testing.T) {
	tests := []struct {
		name  string
		patch string
	}{
		{name: "missing target", patch: `[ {"op":"replace","path":"/missing","value":1} ]`},
		{name: "remove root", patch: `[ {"op":"remove","path":""} ]`},
		{name: "invalid escape", patch: `[ {"op":"add","path":"/bad~2key","value":1} ]`},
		{name: "unsupported op", patch: `[ {"op":"move","path":"/a","value":1} ]`},
		{name: "extra field", patch: `[ {"op":"add","path":"/a","value":1,"from":"/b"} ]`},
		{name: "duplicate operation field", patch: `[ {"op":"add","op":"remove","path":"/a","value":1} ]`},
		{name: "duplicate nested value field", patch: `[ {"op":"add","path":"/b","value":{"id":1,"id":2}} ]`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := applyArtifactRuntimePatch(json.RawMessage(`{"a":1}`), json.RawMessage(test.patch)); err == nil {
				t.Fatal("expected patch to be rejected")
			}
		})
	}
}

func TestValidateArtifactRuntimeJSONTokensRejectsDuplicateStateField(t *testing.T) {
	if err := validateArtifactRuntimeJSONTokens([]byte(`{"items":[{"id":"r1","status":"open","status":"closed"}]}`)); err == nil {
		t.Fatal("expected duplicate State field to be rejected")
	}
}

func TestApplyArtifactRuntimePatchCanReplaceRoot(t *testing.T) {
	got, err := applyArtifactRuntimePatch(
		json.RawMessage(`{"old":true}`),
		json.RawMessage(`[{"op":"replace","path":"","value":{"new":true}}]`),
	)
	if err != nil {
		t.Fatalf("replace root: %v", err)
	}
	if string(got) != `{"new":true}` {
		t.Fatalf("result = %s", got)
	}
}
