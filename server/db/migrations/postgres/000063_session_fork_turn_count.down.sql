-- 000061_session_fork_turn_count (down, PostgreSQL)
ALTER TABLE session_forks DROP COLUMN IF EXISTS source_total_turns;
ALTER TABLE session_forks DROP COLUMN IF EXISTS source_turn_count;
