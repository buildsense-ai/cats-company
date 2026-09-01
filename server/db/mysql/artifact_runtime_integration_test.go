package mysql

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

func TestMySQLArtifactRuntimeEventSequenceFollowsCommitOrder(t *testing.T) {
	dsn := os.Getenv("CATS_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("set CATS_MYSQL_TEST_DSN to run MySQL integration tests")
	}

	db := &Adapter{}
	if err := db.Open(dsn); err != nil {
		t.Fatalf("open MySQL: %v", err)
	}
	defer db.Close()
	if err := db.CreateSchema(); err != nil {
		t.Fatalf("create Runtime sequence schema: %v", err)
	}
	suffix := time.Now().UnixNano()
	agentUID, err := db.CreateUser(&types.User{
		Username:    fmt.Sprintf("runtime-sequence-%d", suffix),
		Email:       fmt.Sprintf("runtime-sequence-%d@example.test", suffix),
		DisplayName: "Runtime Sequence Agent", AccountType: types.AccountBot,
		PassHash: []byte("test"),
	})
	if err != nil {
		t.Fatalf("create Runtime sequence Agent: %v", err)
	}
	defer db.db.Exec(`DELETE FROM users WHERE id = ?`, agentUID)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	firstTx, err := db.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin first Runtime sequence transaction: %v", err)
	}
	defer firstTx.Rollback()
	firstID, err := mysqlNextArtifactRuntimeEventID(ctx, firstTx, agentUID, "risk-register")
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
		id, allocationErr := mysqlNextArtifactRuntimeEventID(
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
