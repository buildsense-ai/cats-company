package mysql

import "github.com/openchat/openchat/server/db/wikiinjection"

func (a *Adapter) WikiInjections() *wikiinjection.Storage { return wikiinjection.New(a.db, false) }
