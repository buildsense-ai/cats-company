package mysql

import (
	"strings"
	"testing"
)

func TestPushSubscriptionEndpointUsesCaseSensitiveCollation(t *testing.T) {
	const endpointColumn = "endpoint VARCHAR(512) COLLATE utf8mb4_bin NOT NULL"
	if !strings.Contains(createPushSubscriptionsTable, endpointColumn) {
		t.Fatalf("push subscription endpoint must be case-sensitive; schema=%s", createPushSubscriptionsTable)
	}
}
