package server

import (
	"errors"
	"strings"
	"testing"
)

// A worker script that dies before printing anything used to log only
// "script failed: exit status 1; output=", which tells an operator nothing.
func TestScriptFailureErrorFlagsEmptyOutput(t *testing.T) {
	base := errors.New("exit status 1")
	cases := []struct {
		name    string
		out     string
		wantHit string
	}{
		{"empty", "", "script wrote no output"},
		{"whitespace only", "  \n\t", "script wrote no output"},
		{"with output", "boom: jump host unreachable\n", "script failed"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := scriptFailureError(tc.out, base)
			if !strings.Contains(err.Error(), tc.wantHit) {
				t.Fatalf("want %q in error, got %q", tc.wantHit, err.Error())
			}
			if !errors.Is(err, base) {
				t.Fatalf("error must still wrap the exit error, got %q", err.Error())
			}
		})
	}
}
