package postgres

import (
	"context"
	"database/sql"
	"fmt"
)

// Every literal substring with at least three UTF-8 bytes contains its query's
// byte triples. Packing each triple into an integer is lossless; the array
// containment check can only admit extra candidates. Exact visible-field and
// permission predicates remain authoritative. Sorted unique keys bound both
// index work and PostgreSQL's array-element statistics work for repeated text.
const createMessageSearchGrams = `
CREATE OR REPLACE FUNCTION message_search_grams_v1(document text)
RETURNS integer[] LANGUAGE sql IMMUTABLE PARALLEL SAFE STRICT AS $$
  WITH document_bytes AS MATERIALIZED (SELECT convert_to(document, 'UTF8') AS value)
  SELECT ARRAY(
    SELECT DISTINCT (get_byte(value, i) << 16) | (get_byte(value, i+1) << 8) | get_byte(value, i+2)
    FROM document_bytes, LATERAL generate_series(0, octet_length(value)-3) AS i
    ORDER BY 1
  );
$$;
`

const createMessageSearchGramIndex = `
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_messages_search_grams_gin
  ON messages USING gin (message_search_grams_v1(message_search_document_v1(msg_type, content, content_blocks)));
`

// indexName and indexDDL are internal constants, never request input.
func ensureMessageSearchIndexDDL(ctx context.Context, conn *sql.Conn, indexName, indexDDL string) error {
	const lock = `SELECT pg_advisory_lock(hashtextextended(current_schema() || '.message-search-index', 0))`
	const unlock = `SELECT pg_advisory_unlock(hashtextextended(current_schema() || '.message-search-index', 0))`
	if _, err := conn.ExecContext(ctx, lock); err != nil {
		return err
	}
	defer conn.ExecContext(ctx, unlock)
	var exists, valid bool
	if err := conn.QueryRowContext(ctx, `SELECT count(*) > 0, COALESCE(bool_and(indisvalid), false)
		FROM pg_index WHERE indexrelid = to_regclass($1)
	`, indexName).Scan(&exists, &valid); err != nil {
		return err
	}
	if valid {
		return nil
	}
	if exists {
		if _, err := conn.ExecContext(ctx, `DROP INDEX CONCURRENTLY IF EXISTS `+indexName); err != nil {
			return fmt.Errorf("drop interrupted search index: %w", err)
		}
	}
	if _, err := conn.ExecContext(ctx, indexDDL); err != nil {
		return err
	}
	// Statistics let custom plans choose ordered history reads for common
	// terms and the GIN index for selective ones after the initial build.
	_, err := conn.ExecContext(ctx, `ANALYZE messages`)
	return err
}
