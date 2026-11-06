package store

import (
	"context"
	"errors"
	"time"

	"github.com/openchat/openchat/server/store/types"
)

var (
	ErrSessionForkNotFound  = errors.New("session_fork_not_found")
	ErrSessionForkConflict  = errors.New("session_fork_request_conflict")
	ErrSessionForkForbidden = errors.New("session_fork_forbidden")
	ErrSessionForkSource    = errors.New("session_fork_source_unavailable")
	// ErrSessionForkAnchor separates "this point is not a turn's end" from
	// "this conversation changed": the user can act on the first one by picking
	// a different spot, while the second needs a reload.
	ErrSessionForkAnchor = errors.New("session_fork_anchor_not_turn_end")
)

type SessionForkStore interface {
	CreateSessionFork(context.Context, types.SessionForkRequest) (*types.SessionFork, error)
	GetSessionFork(context.Context, int64, string, string) (*types.SessionFork, error)
	GetBotSessionFork(context.Context, int64, string) (*types.SessionFork, error)
	CompleteSessionFork(context.Context, int64, types.SessionForkCompletion) (*types.SessionFork, error)
	SessionForkWritable(context.Context, string) (bool, error)
	// CancelSessionFork closes a pending copy on the user's behalf. It is
	// idempotent and never rewrites a terminal state.
	CancelSessionFork(context.Context, int64, string) (*types.SessionFork, error)
	// ExpireSessionForks fails pending copies older than the cutoff, bounding how
	// long a silent runtime can keep a copy open. It returns how many rows it
	// actually transitioned, so a concurrent reaper or a late acknowledgement
	// cannot be double counted.
	ExpireSessionForks(context.Context, time.Time, int) (int, error)
}
