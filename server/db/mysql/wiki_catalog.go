package mysql

import (
	"github.com/openchat/openchat/server/db/wikicatalog"
	"github.com/openchat/openchat/server/store"
)

const createWikiCatalog = `
CREATE TABLE IF NOT EXISTS wiki_catalog (` + wikicatalog.Columns + `,
	INDEX idx_wiki_catalog_bot_time (bot_uid, created_at DESC, id DESC),
	INDEX idx_wiki_catalog_bot_topic (bot_uid, topic_id, created_at DESC, id DESC)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
`

func (a *Adapter) WikiCatalog() store.WikiCatalogStore {
	return wikicatalog.New(a.db, false)
}
