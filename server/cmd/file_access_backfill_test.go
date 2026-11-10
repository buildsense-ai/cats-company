package main

import (
	"strings"
	"testing"
)

func TestFileAccessBackfillRejectsInvalidBatchBeforeOpeningDatabase(t *testing.T) {
	for _, args := range [][]string{{"--after-id=-1"}, {"--limit=0"}, {"--limit=1001"}, {"unexpected"}} {
		err := runFileAccessBackfill(append(args, "--config=missing-file-privacy-config"))
		if err == nil || strings.Contains(err.Error(), "missing-file-privacy-config") {
			t.Fatalf("invalid batch reached configuration/database: %v", err)
		}
	}
}
