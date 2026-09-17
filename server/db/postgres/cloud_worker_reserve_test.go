package postgres

import (
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// The create path sizes the provider's prepaid cycle count from the reserved
// credit, so the reservation result must carry the credit's own expiry. This
// test keeps the SELECT/RETURNING projection honest for both bounded and
// perpetual grants.
func TestReserveCloudWorkerConfiguredCreditCarriesCreditExpiry(t *testing.T) {
	expiresAt := time.Date(2026, time.November, 13, 5, 12, 22, 0, time.UTC)
	cases := []struct {
		name      string
		value     interface{}
		wantNil   bool
		expiresAt time.Time
	}{
		{name: "bounded package credit", value: expiresAt, expiresAt: expiresAt},
		{name: "perpetual manual grant", value: nil, wantNil: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sqlDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
			if err != nil {
				t.Fatalf("create mock database: %v", err)
			}
			defer sqlDB.Close()
			adapter := &Adapter{db: sqlDB}
			mock.ExpectBegin()
			mock.ExpectExec(`UPDATE cloud_worker_credits c\s+SET state = CASE`).
				WithArgs(int64(38)).
				WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectQuery(`SELECT id, deployment_profile, billing_mode, expires_at FROM cloud_worker_credits`).
				WithArgs(int64(38), "", "").
				WillReturnRows(sqlmock.NewRows([]string{"id", "deployment_profile", "billing_mode", "expires_at"}).
					AddRow(int64(7), "private_nat", "month", tc.value))
			mock.ExpectExec(`UPDATE cloud_worker_credits\s+SET state = 'reserved', reservation_ref = \$2`).
				WithArgs(int64(7), "create-38-1").
				WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit()

			selection, reserved, err := adapter.ReserveCloudWorkerConfiguredCredit(38, "create-38-1", "", "")
			if err != nil || !reserved {
				t.Fatalf("reserve: reserved=%v err=%v", reserved, err)
			}
			if tc.wantNil {
				if selection.ExpiresAt != nil {
					t.Fatalf("expiry=%v want nil", selection.ExpiresAt)
				}
			} else if selection.ExpiresAt == nil || !selection.ExpiresAt.Equal(tc.expiresAt) {
				t.Fatalf("expiry=%v want %s", selection.ExpiresAt, tc.expiresAt)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatalf("unmet database expectations: %v", err)
			}
		})
	}
}
