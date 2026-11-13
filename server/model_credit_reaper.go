package server

import (
	"context"
	"log"
	"time"
)

// ModelCreditExpiryReleaser returns only expired holds with confirmed zero usage.
// Unknown upstream usage is deliberately excluded by the ledger implementation.
type ModelCreditExpiryReleaser interface {
	ReleaseExpiredModelCredits(context.Context, time.Time, int) (int, error)
}

func StartModelCreditExpiryReaper(ctx context.Context, ledger ModelCreditExpiryReleaser) {
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			if ctx.Err() != nil {
				return
			}
			batchCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
			count, err := ledger.ReleaseExpiredModelCredits(batchCtx, time.Now().UTC(), 100)
			cancel()
			if err != nil && ctx.Err() == nil {
				log.Printf("model credit confirmed-zero expiry release failed: %v", err)
			} else if count > 0 {
				log.Printf("model credit confirmed-zero expiry released: count=%d", count)
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}
