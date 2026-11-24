package server

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"
)

func TestRetryCloudWorkerRenewSpawn(t *testing.T) {
	spawnErr := &os.PathError{Op: "fork/exec", Path: "renew.sh", Err: syscall.EAGAIN}
	for _, tt := range []struct {
		name      string
		err       error
		cancelled bool
		retry     bool
		retryErr  error
	}{
		{name: "temporary spawn failure", err: spawnErr, retry: true},
		{name: "retry also fails", err: spawnErr, retry: true, retryErr: spawnErr},
		{name: "script started", err: &exec.ExitError{}},
		{name: "permanent spawn failure", err: &os.PathError{Op: "fork/exec", Err: os.ErrPermission}},
		{name: "operation timed out", err: context.DeadlineExceeded},
		{name: "context already cancelled", err: spawnErr, cancelled: true},
		{name: "success"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancelled {
				cancel()
			}
			calls := 0
			out, err := retryCloudWorkerRenewSpawn(ctx, []byte("first"), tt.err, func() ([]byte, error) {
				calls++
				return []byte("second"), tt.retryErr
			})
			wantCalls, wantOut, wantErr := 0, "first", tt.err
			if tt.retry {
				wantCalls, wantOut, wantErr = 1, "second", tt.retryErr
			}
			if calls != wantCalls || string(out) != wantOut || !errors.Is(err, wantErr) {
				t.Fatalf("retry calls=%d output=%q error type=%T, want calls=%d output=%q error type=%T", calls, out, err, wantCalls, wantOut, wantErr)
			}
		})
	}
}
