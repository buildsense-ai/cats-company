package server

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/openchat/openchat/server/store"
)

const wikiCatalogSyncInterval = time.Hour
const wikiTimeLayout = "2006-01-02T15:04:05.000000Z"

type wikiCatalogRequest struct {
	Cursor string `json:"cursor,omitempty"`
	Limit  int    `json:"limit"`
}

type wikiCatalogPage struct {
	Items      []store.WikiCatalogEntry `json:"items"`
	NextCursor *string                  `json:"next_cursor"`
}

type wikiBodyRequest struct {
	ID string `json:"id"`
}

func validWikiTime(value string) bool {
	parsed, err := time.Parse(wikiTimeLayout, value)
	return err == nil && parsed.Format(wikiTimeLayout) == value
}

func (h *WikiMemoryHandler) RunCatalogSync(ctx context.Context) {
	if !h.configured() {
		return
	}
	ticker := time.NewTicker(wikiCatalogSyncInterval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		attempt, cancel := context.WithTimeout(ctx, 5*time.Minute)
		err := h.syncWikiCatalog(attempt)
		cancel()
		if err != nil && ctx.Err() == nil {
			log.Printf("Wiki catalog sync failed: %v", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (h *WikiMemoryHandler) syncWikiCatalog(ctx context.Context) error {
	const limit = 200
	var entries []store.WikiCatalogEntry
	ids := make(map[string]bool)
	cursors := make(map[string]bool)
	cursor := ""
	for {
		var page wikiCatalogPage
		_, status := h.query(ctx, "catalog", wikiCatalogRequest{Cursor: cursor, Limit: limit}, "", &page)
		if status != http.StatusOK {
			return fmt.Errorf("catalog request returned HTTP %d", status)
		}
		if page.Items == nil || len(page.Items) > limit {
			return errors.New("invalid Wiki catalog page")
		}
		for _, item := range page.Items {
			botUID, err := strconv.ParseInt(item.BotUID, 10, 64)
			if err != nil || botUID <= 0 || strconv.FormatInt(botUID, 10) != item.BotUID ||
				!wikiMemoryID.MatchString(item.ID) || ids[item.ID] ||
				!wikiMemoryID.MatchString(item.Revision) || !validWikiTime(item.CreatedAt) ||
				strings.TrimSpace(item.Title) == "" || utf8.RuneCountInString(item.Title) > 240 ||
				(item.TopicID != "" && !wikiMemoryBotTopic(item.TopicID, botUID)) {
				return errors.New("invalid Wiki catalog entry")
			}
			ids[item.ID] = true
			entries = append(entries, item)
		}
		if page.NextCursor == nil {
			return h.catalog.ReplaceWikiCatalog(ctx, entries)
		}
		cursor = *page.NextCursor
		if len(cursor) == 0 || len(cursor) > 512 || cursors[cursor] {
			return errors.New("invalid Wiki catalog cursor")
		}
		cursors[cursor] = true
	}
}
