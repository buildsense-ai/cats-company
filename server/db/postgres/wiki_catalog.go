package postgres

import (
	"github.com/openchat/openchat/server/db/wikicatalog"
	"github.com/openchat/openchat/server/store"
)

const createWikiCatalog = `
CREATE TABLE IF NOT EXISTS wiki_catalog (` + wikicatalog.Columns + `);
CREATE INDEX IF NOT EXISTS idx_wiki_catalog_bot_time ON wiki_catalog(bot_uid, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_wiki_catalog_bot_topic ON wiki_catalog(bot_uid, topic_id, created_at DESC, id DESC);
`

func (a *Adapter) WikiCatalog() store.WikiCatalogStore {
	return wikicatalog.New(a.db, true)
}
