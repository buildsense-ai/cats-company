package postgres

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"

	"github.com/jackc/pgx/v5/pgconn"
)

// ensureMessageSearchIndex serializes builds in this schema and repairs an
// invalid index left by an interrupted concurrent build before retrying it.
func (a *Adapter) ensureMessageSearchIndex() error {
	ctx := context.Background()
	conn, err := a.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	return a.ensureMessageSearchIndexOnConn(ctx, conn)
}

func (a *Adapter) ensureMessageSearchIndexOnConn(ctx context.Context, conn *sql.Conn) error {
	a.messageSearchIndexReady.Store(false)
	a.messageSearchGramIndexReady.Store(false)
	var extensionInstalled bool
	if err := conn.QueryRowContext(ctx, `SELECT EXISTS (SELECT 1 FROM pg_extension WHERE extname = 'pg_trgm')`).Scan(&extensionInstalled); err != nil {
		return fmt.Errorf("check message search extension: %w", err)
	}
	if !extensionInstalled {
		if _, err := conn.ExecContext(ctx, createMessageSearchExtension); err != nil {
			var pgErr *pgconn.PgError
			if errors.As(err, &pgErr) && (pgErr.Code == "42501" || pgErr.Code == "0A000") {
				// Use the built-in GIN array operator class when managed databases
				// reserve extension installation for an administrator.
				log.Printf("pg_trgm installation unavailable: %v; using built-in message search gram index", err)
				err = ensureMessageSearchIndexDDL(ctx, conn, "idx_messages_search_grams_gin", createMessageSearchGramIndex)
				a.messageSearchGramIndexReady.Store(err == nil)
				return err
			}
			return fmt.Errorf("install message search extension: %w", err)
		}
	}
	err := ensureMessageSearchIndexDDL(ctx, conn, "idx_messages_search_document_trgm", createMessageSearchIndex)
	a.messageSearchIndexReady.Store(err == nil)
	return err
}

const createMessageSearchExtension = `CREATE EXTENSION IF NOT EXISTS pg_trgm WITH SCHEMA public;`

// Version the immutable document function: changing its output requires rebuilding
// the expression index. It is a prefilter; the search query still enforces block
// validity, individual field matching, sender visibility, and topic permissions.
const createMessageSearchDocument = `
CREATE OR REPLACE FUNCTION message_search_document_v1(kind text, body text, blocks jsonb)
RETURNS text LANGUAGE plpgsql IMMUTABLE PARALLEL SAFE AS $$
DECLARE
  fields text[] := ARRAY[]::text[];
  block jsonb;
  visible text;
  has_visible boolean := false;
  has_internal boolean := false;
  legacy jsonb;
BEGIN
  IF jsonb_typeof(blocks) = 'array' THEN
    FOR block IN SELECT value FROM jsonb_array_elements(blocks) LOOP
      IF block->>'type' IN ('text', 'assistant_text') AND kind = 'text' THEN
        visible := COALESCE(NULLIF(block->>'text', ''), block->>'content', '');
        IF visible <> '' THEN
          fields := array_append(fields, visible);
          has_visible := true;
        END IF;
      ELSIF block->>'type' IN ('thinking', 'tool_use', 'tool_result', 'runtime_plan', 'debug') THEN
        has_internal := true;
      ELSIF block->>'type' IN ('file', 'image', 'audio', 'video') THEN
        fields := fields || ARRAY[block->>'name', block->'payload'->>'name',
          block->'payload'->>'file_name', block->'payload'->>'filename', block->'payload'->>'title'];
      END IF;
    END LOOP;
  END IF;
  IF kind = 'text' AND NOT has_visible AND NOT has_internal THEN
    fields := array_append(fields, body);
  ELSIF kind = 'file' AND pg_input_is_valid(body, 'jsonb') THEN
    legacy := body::jsonb;
    IF jsonb_typeof(legacy) = 'string' AND pg_input_is_valid(legacy #>> '{}', 'jsonb') THEN
      legacy := (legacy #>> '{}')::jsonb;
    END IF;
    IF jsonb_typeof(legacy->'payload') = 'object' THEN
      legacy := legacy->'payload';
    END IF;
    fields := fields || ARRAY[legacy->>'name', legacy->>'file_name', legacy->>'filename', legacy->>'title'];
  END IF;
  RETURN lower(array_to_string(fields, E'\n'));
END;
$$;
`

const createMessageSearchIndex = `
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_messages_search_document_trgm
  ON messages USING gin (message_search_document_v1(msg_type, content, content_blocks) public.gin_trgm_ops);
`
