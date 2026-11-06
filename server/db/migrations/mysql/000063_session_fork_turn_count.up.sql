ALTER TABLE session_forks ADD COLUMN source_turn_count BIGINT NOT NULL DEFAULT 0;
ALTER TABLE session_forks ADD COLUMN source_total_turns BIGINT NOT NULL DEFAULT 0;
