ALTER TABLE compute_tasks
    DROP CONSTRAINT IF EXISTS compute_tasks_admitted_nodes_nonnegative;
ALTER TABLE compute_tasks
    DROP COLUMN IF EXISTS admitted_nodes;
