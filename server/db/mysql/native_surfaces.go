package mysql

import (
	"context"
	"github.com/openchat/openchat/server/db/nativesurface"
	"github.com/openchat/openchat/server/store"
	"github.com/openchat/openchat/server/store/types"
)

func (a *Adapter) NativeSurfaceReader() store.NativeSurfaceReader {
	return nativesurface.Reader{DB: a.db, Postgres: false}
}

func (a *Adapter) ReplacePushSubscription(ctx context.Context, row, previous *types.PushSubscription, limit int) (bool, error) {
	return (nativesurface.Reader{DB: a.db, Postgres: false}).ReplaceSubscription(ctx, row, previous, limit)
}
