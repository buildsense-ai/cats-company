package store

import "context"

type WikiCatalogEntry struct {
	ID        string `json:"id"`
	BotUID    string `json:"bot_uid"`
	TopicID   string `json:"topic_id"`
	Title     string `json:"title"`
	CreatedAt string `json:"created_at"`
	Revision  string `json:"revision"`
}

type WikiCatalogPosition struct {
	CreatedAt string
	ID        string
}

type WikiCatalogStore interface {
	ReplaceWikiCatalog(context.Context, []WikiCatalogEntry) error
	ListWikiCatalog(context.Context, int64, []string, WikiCatalogPosition, int) ([]WikiCatalogEntry, error)
	GetWikiCatalogEntry(context.Context, string) (WikiCatalogEntry, bool, error)
}

type WikiCatalogProvider interface {
	WikiCatalog() WikiCatalogStore
}
