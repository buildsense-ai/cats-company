package server

import (
	"context"
	"testing"
	"time"
)

type blockingCreditExpiryLedger struct {
	started chan struct{}
	stopped chan struct{}
}

func (l *blockingCreditExpiryLedger) ReleaseExpiredModelCredits(ctx context.Context, _ time.Time, _ int) (int, error) {
	close(l.started)
	<-ctx.Done()
	close(l.stopped)
	return 0, ctx.Err()
}

func TestModelCreditExpiryReaperStartsAndCancelsInFlightRecovery(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ledger := &blockingCreditExpiryLedger{started: make(chan struct{}), stopped: make(chan struct{})}
	StartModelCreditExpiryReaper(ctx, ledger)
	select {
	case <-ledger.started:
	case <-time.After(3 * time.Second):
		t.Fatal("confirmed-zero recovery did not start")
	}
	cancel()
	select {
	case <-ledger.stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown did not cancel the in-flight ledger operation")
	}
}
