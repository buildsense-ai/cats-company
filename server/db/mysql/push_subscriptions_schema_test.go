package mysql

import (
	"regexp"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPushSubscriptionEndpointUsesCaseSensitiveCollation(t *testing.T) {
	const endpointColumn = "endpoint VARCHAR(512) COLLATE utf8mb4_bin NOT NULL"
	if !strings.Contains(createPushSubscriptionsTable, endpointColumn) {
		t.Fatalf("push subscription endpoint must be case-sensitive; schema=%s", createPushSubscriptionsTable)
	}
}

func TestPushSubscriptionRegistrationIDUsesCaseSensitiveCollation(t *testing.T) {
	const registrationIDColumn = "registration_id VARCHAR(64) COLLATE utf8mb4_bin NOT NULL"
	if !strings.Contains(createPushSubscriptionsTable, registrationIDColumn) {
		t.Fatalf("push subscription registration id must be case-sensitive; schema=%s", createPushSubscriptionsTable)
	}
	if !strings.Contains(migratePushSubscriptionsRegistrationIDBinary, "COLLATE utf8mb4_bin") {
		t.Fatalf("existing registration ids must be migrated to a case-sensitive collation; migration=%s", migratePushSubscriptionsRegistrationIDBinary)
	}
}

func TestPushSubscriptionsAreDeletedWithTheirUser(t *testing.T) {
	const foreignKey = "FOREIGN KEY (uid) REFERENCES users(id) ON DELETE CASCADE"
	if !strings.Contains(createPushSubscriptionsTable, foreignKey) {
		t.Fatalf("push subscriptions must be deleted with their user; schema=%s", createPushSubscriptionsTable)
	}
	if !strings.Contains(migratePushSubscriptionsAddUserForeignKey, foreignKey) {
		t.Fatalf("existing push subscriptions must gain the user foreign key; migration=%s", migratePushSubscriptionsAddUserForeignKey)
	}
}

func TestPushSubscriptionForeignKeyUpgradePreservesOrphans(t *testing.T) {
	sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("create mock database: %v", err)
	}
	defer sqlDB.Close()

	mock.ExpectQuery(regexp.QuoteMeta("SELECT COUNT(*)")).
		WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(1))

	adapter := &Adapter{db: sqlDB}
	err = adapter.ensurePushSubscriptionUserForeignKey()
	if err == nil || !strings.Contains(err.Error(), "manual repair") {
		t.Fatalf("orphan push subscriptions must require an explicit repair, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unexpected schema mutation while checking orphan subscriptions: %v", err)
	}
}
