package postgres

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateCommercialErrorPreservesUTF8(t *testing.T) {
	value := strings.Repeat("付", 200)
	got := truncateCommercialError(value)
	if len(got) > 500 || !utf8.ValidString(got) {
		t.Fatalf("invalid truncated error: bytes=%d valid=%v", len(got), utf8.ValidString(got))
	}
}
