-- 027_task_planning_lease.sql — one planning lease shared by every instance (schema 27).
--
-- Planning used to be deduplicated by an in-process map only: two bot instances on the same database could
-- plan the same row, and a row left by a dead process was recovered by age alone. The two additive,
-- nullable columns below make the claim a fact of the database instead:
--
--   * planning_owner       — the opaque id of the instance that is producing the plan;
--   * planning_lease_until — the moment that claim expires. A NULL owner, a NULL deadline or a deadline in
--                            the past means "free": any instance may claim the row.
--
-- Nothing is rebuilt and no row is copied: this is a nullable ADD COLUMN (unlike 025), and every row written
-- by an older release gets NULL in both columns, which reads as "unleased" and is claimed by the next
-- scheduler pass. The ALTERs are guarded by migrationStatementGuards[27], so a database where the columns
-- already exist still reaches the index statement.
ALTER TABLE tasks ADD COLUMN planning_owner TEXT;

ALTER TABLE tasks ADD COLUMN planning_lease_until TEXT;

-- The claim is one UPDATE ... RETURNING over "status = 'planning' and the lease is free"; this index keeps
-- that scan bounded by the few planning rows of the database, not by the whole task history.
CREATE INDEX IF NOT EXISTS idx_tasks_planning_lease
    ON tasks(status, planning_lease_until, due_at, id);
