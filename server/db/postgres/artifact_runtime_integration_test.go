package postgres

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestPostgresArtifactRuntimeEventSequenceFollowsCommitOrder(t *testing.T) {
	rawDSN := os.Getenv("CATS_PG_TEST_DSN")
	if rawDSN == "" {
		t.Skip("set CATS_PG_TEST_DSN to run PostgreSQL integration tests")
	}

	schemaName := fmt.Sprintf("cats_runtime_sequence_%d", time.Now().UnixNano())
	base := &Adapter{}
	if err := base.Open(rawDSN); err != nil {
		t.Fatalf("open base postgres connection: %v", err)
	}
	defer base.Close()
	if _, err := base.db.Exec(`CREATE SCHEMA ` + quoteIdent(schemaName)); err != nil {
		t.Fatalf("create Runtime sequence schema: %v", err)
	}
	defer base.db.Exec(`DROP SCHEMA ` + quoteIdent(schemaName) + ` CASCADE`)

	db := &Adapter{}
	if err := db.Open(dsnWithSearchPath(t, rawDSN, schemaName)); err != nil {
		t.Fatalf("open Runtime sequence database: %v", err)
	}
	defer db.Close()
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create Runtime sequence schema: %v", err)
	}
	agentUID, err := db.CreateUser(&types.User{
		Username: "runtime-sequence-agent", DisplayName: "Runtime Sequence Agent",
		AccountType: types.AccountBot, PassHash: []byte("test"),
	})
	if err != nil {
		t.Fatalf("create Runtime sequence Agent: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstTx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin first Runtime sequence transaction: %v", err)
	}
	defer firstTx.Rollback()
	firstID, err := postgresNextArtifactRuntimeEventID(ctx, firstTx, agentUID, "risk-register")
	if err != nil || firstID != 1 {
		t.Fatalf("first Runtime event ID=%d err=%v, want 1", firstID, err)
	}

	type allocationResult struct {
		id  int64
		err error
	}
	started := make(chan struct{})
	allocated := make(chan allocationResult, 1)
	go func() {
		secondTx, beginErr := db.db.BeginTx(ctx, nil)
		if beginErr != nil {
			close(started)
			allocated <- allocationResult{err: beginErr}
			return
		}
		defer secondTx.Rollback()
		close(started)
		id, allocationErr := postgresNextArtifactRuntimeEventID(
			ctx, secondTx, agentUID, "risk-register",
		)
		if allocationErr == nil {
			allocationErr = secondTx.Commit()
		}
		allocated <- allocationResult{id: id, err: allocationErr}
	}()
	<-started

	select {
	case result := <-allocated:
		t.Fatalf("second allocation escaped the uncommitted sequence lock: %+v", result)
	case <-time.After(100 * time.Millisecond):
	}
	if err := firstTx.Commit(); err != nil {
		t.Fatalf("commit first Runtime sequence transaction: %v", err)
	}

	select {
	case result := <-allocated:
		if result.err != nil || result.id != 2 {
			t.Fatalf("second Runtime event ID=%d err=%v, want 2", result.id, result.err)
		}
	case <-ctx.Done():
		t.Fatalf("second Runtime sequence allocation stayed blocked: %v", ctx.Err())
	}
}
