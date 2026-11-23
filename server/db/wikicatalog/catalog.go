package wikicatalog

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/openchat/openchat/server/store"
)

const Columns = `id varchar(128) primary key,
	bot_uid bigint not null, topic_id varchar(128) not null, title varchar(240) not null,
	created_at char(27) not null, revision varchar(128) not null`

const columns = "id, bot_uid, topic_id, title, created_at, revision"

type Catalog struct {
	db       *sql.DB
	postgres bool
}

func New(db *sql.DB, postgres bool) *Catalog {
	return &Catalog{db: db, postgres: postgres}
}

func (c *Catalog) bind(query string) string {
	if !c.postgres {
		return query
	}
	parts := strings.Split(query, "?")
	var bound strings.Builder
	bound.WriteString(parts[0])
	for i, part := range parts[1:] {
		bound.WriteByte('$')
		bound.WriteString(strconv.Itoa(i + 1))
		bound.WriteString(part)
	}
	return bound.String()
}

func (c *Catalog) ReplaceWikiCatalog(ctx context.Context, entries []store.WikiCatalogEntry) error {
	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if c.postgres {
		if _, err := tx.ExecContext(ctx, "LOCK TABLE wiki_catalog IN SHARE ROW EXCLUSIVE MODE"); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM wiki_catalog"); err != nil {
		return err
	}
	for offset := 0; offset < len(entries); offset += 200 {
		batch := entries[offset:min(len(entries), offset+200)]
		values := make([]string, 0, len(batch))
		args := make([]any, 0, len(batch)*6)
		for _, entry := range batch {
			values = append(values, "(?, ?, ?, ?, ?, ?)")
			uid, err := strconv.ParseInt(entry.BotUID, 10, 64)
			if err != nil {
				return err
			}
			args = append(args, entry.ID, uid, entry.TopicID, entry.Title, entry.CreatedAt, entry.Revision)
		}
		query := "INSERT INTO wiki_catalog (" + columns + ") VALUES " + strings.Join(values, ", ")
		if _, err := tx.ExecContext(ctx, c.bind(query), args...); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (c *Catalog) ListWikiCatalog(ctx context.Context, botUID int64, topics []string, after store.WikiCatalogPosition, limit int) ([]store.WikiCatalogEntry, error) {
	entries := make([]store.WikiCatalogEntry, 0)
	if topics != nil && len(topics) == 0 {
		return entries, nil
	}
	args := []any{botUID}
	query := "SELECT " + columns + " FROM wiki_catalog WHERE bot_uid = ?"
	if topics != nil {
		placeholders := make([]string, 0, len(topics))
		for _, topic := range topics {
			args = append(args, topic)
			placeholders = append(placeholders, "?")
		}
		query += " AND topic_id IN (" + strings.Join(placeholders, ", ") + ")"
	}
	if after.ID != "" {
		query += " AND (created_at < ? OR (created_at = ? AND id < ?))"
		args = append(args, after.CreatedAt, after.CreatedAt, after.ID)
	}
	query += " ORDER BY created_at DESC, id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := c.db.QueryContext(ctx, c.bind(query), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var entry store.WikiCatalogEntry
		if err := rows.Scan(&entry.ID, &entry.BotUID, &entry.TopicID, &entry.Title, &entry.CreatedAt, &entry.Revision); err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (c *Catalog) GetWikiCatalogEntry(ctx context.Context, id string) (store.WikiCatalogEntry, bool, error) {
	var entry store.WikiCatalogEntry
	row := c.db.QueryRowContext(ctx, c.bind("SELECT "+columns+" FROM wiki_catalog WHERE id = ?"), id)
	err := row.Scan(&entry.ID, &entry.BotUID, &entry.TopicID, &entry.Title, &entry.CreatedAt, &entry.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return entry, false, nil
	}
	return entry, err == nil, err
}
