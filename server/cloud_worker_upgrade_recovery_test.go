package server

import (
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

// Keep one stateful adapter throughout the test: the recovery path must observe
// the first attempt's effects without a test replacing the executable.
func TestMachineUpgradeAutomaticMigrationRecovery(t *testing.T) {
	for _, scenario := range []string{"lost-receipt", "settlement-failure"} {
		t.Run(scenario, func(t *testing.T) {
			dir := t.TempDir()
			calls := filepath.Join(dir, "calls")
			script := filepath.Join(dir, "upgrade.sh")
			body := fmt.Sprintf(`#!/bin/sh
set -eu
printf '%%s\n' "$*" >> '%s'
case "$*" in
  '--name bot-worker --plan pro --allow-migrate') ;;
  *) exit 2 ;;
esac
if [ '%s' = lost-receipt ] && [ ! -f '%s/applied' ]; then
  touch '%s/applied'
  printf '%%s\n' '{"status":"failed","pending":true}'
  exit 1
fi
printf '%%s\n' '{"plan":"pro","status":"noop","pending":false}'
`, calls, scenario, dir, dir)
			if runtime.GOOS == "windows" {
				script = filepath.Join(dir, "upgrade.cmd")
				body = fmt.Sprintf("@echo off\r\necho %%*>>\"%s\"\r\nif not \"%%*\"==\"--name bot-worker --plan pro --allow-migrate\" exit /b 2\r\n", calls)
				if scenario == "lost-receipt" {
					body += fmt.Sprintf("if not exist \"%s\\applied\" (\r\n type nul > \"%s\\applied\"\r\n echo {\"status\":\"failed\",\"pending\":true}\r\n exit /b 1\r\n)\r\n", dir, dir)
				}
				body += "echo {\"plan\":\"pro\",\"status\":\"noop\",\"pending\":false}\r\n"
			}
			if err := os.WriteFile(script, []byte(body), 0755); err != nil {
				t.Fatal(err)
			}
			h, db := machineUpgradeTestHandler(t, script)
			if scenario == "settlement-failure" {
				db.completeErr = errors.New("injected local commit failure")
			}
			quote := machineUpgradeQuoteForTest(t, h, 77)
			before := db.balance
			requestBody := `{"name":"bot-worker","plan":"pro","quote_id":"` + quote.QuoteID + `","client_request_id":"automatic-migration-1"}`
			rec := httptest.NewRecorder()
			h.HandleMachineUpgrade(rec, machineUpgradeRequest("POST", "/api/cloud-workers/upgrade", requestBody, 77))
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"status":"reconciling"`) {
				t.Fatalf("first attempt: %d %s", rec.Code, rec.Body.String())
			}
			var op *types.MachineUpgradeOperation
			for _, value := range db.operations {
				op = value
			}
			if op == nil || !op.Held || op.Settled || db.balance != before-quote.CostUnits || len(db.terms) != 0 {
				t.Fatalf("unknown outcome lost hold: %+v balance=%d", op, db.balance)
			}
			// An immediate sweep and a replay must neither execute nor reserve again.
			h.ReconcileMachineUpgrades()
			replay := httptest.NewRecorder()
			h.HandleMachineUpgrade(replay, machineUpgradeRequest("POST", "/api/cloud-workers/upgrade", requestBody, 77))
			if replay.Code != 200 || op.Attempts != 1 {
				t.Fatalf("replay re-executed: %d %+v", replay.Code, op)
			}
			op.UpdatedAt = time.Now().Add(-4 * time.Minute)
			db.completeErr = nil
			h.ReconcileMachineUpgrades()
			term := db.terms["bot-worker"]
			lifecycle, _ := h.lifecycleByName(77, "bot-worker")
			if op.Status != types.MachineUpgradeStatusCompleted || op.Held || !op.Settled || op.Attempts != 2 || db.balance != before-quote.CostUnits || term == nil || lifecycle == nil || !term.ExpiresAt.Equal(op.MachineExpiresAt) || !term.ExpiresAt.Equal(lifecycle.PackageExpiresAt) {
				t.Fatalf("recovery: %+v term=%+v balance=%d", op, term, db.balance)
			}
			h.ReconcileMachineUpgrades()
			replay = httptest.NewRecorder()
			h.HandleMachineUpgrade(replay, machineUpgradeRequest("POST", "/api/cloud-workers/upgrade", requestBody, 77))
			data, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			if replay.Code != 200 || len(strings.FieldsFunc(strings.TrimSpace(string(data)), func(r rune) bool { return r == '\n' })) != 2 || db.balance != before-quote.CostUnits || op.Attempts != 2 {
				t.Fatalf("terminal replay changed operation: %d %+v calls=%s", replay.Code, op, data)
			}
		})
	}
}
