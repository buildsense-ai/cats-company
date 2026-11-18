// Command invite-monthly-repair books one audited, previously unissued term.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/openchat/openchat/server/db/postgres"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	var in postgres.InviteMonthlyRepair
	flag.Int64Var(&in.EntitlementID, "entitlement", 0, "reviewed invite entitlement ID")
	flag.Int64Var(&in.PlanID, "plan", 0, "reviewed plan ID")
	flag.Int64Var(&in.Units, "units", 0, "reviewed monthly micro-credit units")
	flag.Int64Var(&in.GiftUnits, "gift-units", 0, "reviewed already-issued gift micro-credit units")
	start := flag.String("starts-at", "", "original term start (RFC3339, including microseconds)")
	end := flag.String("ends-at", "", "original term end (RFC3339, including microseconds)")
	flag.StringVar(&in.AuditRef, "audit-ref", "", "non-sensitive operator audit reference")
	flag.StringVar(&in.Database, "database", "", "expected database name")
	flag.StringVar(&in.Schema, "schema", "", "expected current schema")
	flag.BoolVar(&in.Commit, "commit", false, "commit; otherwise roll back the preview")
	flag.Parse()
	var err error
	in.StartsAt, err = time.Parse(time.RFC3339Nano, *start)
	if err != nil {
		return fmt.Errorf("invalid starts-at")
	}
	in.TermEndsAt, err = time.Parse(time.RFC3339Nano, *end)
	if err != nil {
		return fmt.Errorf("invalid ends-at")
	}
	dsn := os.Getenv("OC_DB_DSN")
	if dsn == "" {
		return fmt.Errorf("OC_DB_DSN is required")
	}
	var db postgres.Adapter
	defer db.Close()
	if err := db.Open(dsn); err != nil {
		return fmt.Errorf("database connection failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	changed, err := db.RepairInviteMonthly(ctx, in)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Changed   bool `json:"changed"`
		Committed bool `json:"committed"`
	}{Changed: changed, Committed: in.Commit && changed})
}
