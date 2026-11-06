-- 000048_session_fork_turn_count (down, MySQL)
ALTER TABLE session_forks DROP COLUMN source_total_turns;
ALTER TABLE session_forks DROP COLUMN source_turn_count;
