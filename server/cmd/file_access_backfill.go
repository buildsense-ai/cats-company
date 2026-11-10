package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/openchat/openchat/server"
)

// runFileAccessBackfill is a bounded offline operation: no web server, Redis,
// notification workers or external services are started.
func runFileAccessBackfill(args []string) error {
	flags := flag.NewFlagSet("file-access-backfill", flag.ContinueOnError)
	configPath := flags.String("config", "tinode.conf", "server configuration path")
	afterID := flags.Int64("after-id", 0, "resume after this message ID")
	limit := flags.Int("limit", 500, "maximum messages in this batch (1..1000)")
	apply := flags.Bool("apply", false, "persist references; default is dry-run")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments")
	}
	if *afterID < 0 || *limit < 1 || *limit > 1000 {
		return fmt.Errorf("after-id must be nonnegative and limit must be 1..1000")
	}
	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	db, _, err := openStore(cfg.Database)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	result, err := server.NewFileAccessService(db, nil).Backfill(ctx, *afterID, *limit, !*apply)
	if encodeErr := json.NewEncoder(os.Stdout).Encode(result); encodeErr != nil {
		return encodeErr
	}
	return err
}
