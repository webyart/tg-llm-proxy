-- 025_task_planning.sql — asynchronous task planning (schema 25).
--
-- A task used to be inserted only after the STRATA planner answered, so POST /api/tasks and Telegram
-- /task blocked for the whole model call. The row now exists from the first moment with status
-- 'planning' and an empty plan_json; a background worker fills the plan and moves the task to 'ready'.
--
-- This migration therefore:
--   * adds 'planning' to the status CHECK of tasks (SQLite cannot alter a CHECK in place, so the table
--     is rebuilt and every existing row is copied unchanged);
--   * allows plan_json to be empty while a plan is being generated (it stays non-NULL);
--   * adds planning_attempts, the bounded counter of planning attempts used by crash recovery;
--   * includes 'planning' in the per-chat one-active-task unique index, so a chat cannot have a second
--     task while its first plan is still being produced.
--
-- status_message_id (021) is carried over: the "план готов" edit reuses the creation message.
CREATE TABLE tasks_migration_025 (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id              INTEGER NOT NULL REFERENCES chats(chat_id) ON DELETE CASCADE,
    user_id              INTEGER NOT NULL,
    goal                 TEXT NOT NULL CHECK (length(goal) BETWEEN 1 AND 2000),
    model                TEXT NOT NULL CHECK (length(model) BETWEEN 1 AND 200),
    plan_json            TEXT NOT NULL CHECK (length(plan_json) BETWEEN 0 AND 8192),
    results_json         TEXT NOT NULL DEFAULT '[]' CHECK (length(results_json) <= 65536),
    next_step            INTEGER NOT NULL DEFAULT 0 CHECK (next_step BETWEEN 0 AND 3),
    status               TEXT NOT NULL DEFAULT 'ready' CHECK (status IN ('planning', 'ready', 'running', 'awaiting_approval', 'completed', 'cancelled', 'failed')),
    attempts             INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    planning_attempts    INTEGER NOT NULL DEFAULT 0 CHECK (planning_attempts >= 0),
    due_at               TEXT NOT NULL,
    claim_token          TEXT,
    lease_until          TEXT,
    continuation_json    TEXT CHECK (continuation_json IS NULL OR length(continuation_json) <= 131072),
    approval_token       TEXT,
    approval_message_id  INTEGER,
    approval_expires_at  TEXT,
    expires_at           TEXT NOT NULL,
    error                TEXT CHECK (error IS NULL OR length(error) <= 500),
    created_at           TEXT NOT NULL,
    updated_at           TEXT NOT NULL,
    finished_at          TEXT,
    status_message_id    INTEGER
);

INSERT INTO tasks_migration_025
    (id, chat_id, user_id, goal, model, plan_json, results_json, next_step, status, attempts,
     planning_attempts, due_at, claim_token, lease_until, continuation_json, approval_token,
     approval_message_id, approval_expires_at, expires_at, error, created_at, updated_at,
     finished_at, status_message_id)
SELECT id, chat_id, user_id, goal, model, plan_json, results_json, next_step, status, attempts,
       0, due_at, claim_token, lease_until, continuation_json, approval_token,
       approval_message_id, approval_expires_at, expires_at, error, created_at, updated_at,
       finished_at, status_message_id
FROM tasks;

DROP TABLE tasks;

ALTER TABLE tasks_migration_025 RENAME TO tasks;

CREATE UNIQUE INDEX IF NOT EXISTS uq_tasks_chat_active
    ON tasks(chat_id) WHERE status IN ('planning', 'ready', 'running', 'awaiting_approval');
CREATE UNIQUE INDEX IF NOT EXISTS uq_tasks_approval_token
    ON tasks(approval_token) WHERE approval_token IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tasks_due
    ON tasks(status, due_at, id);
CREATE INDEX IF NOT EXISTS idx_tasks_lease
    ON tasks(status, lease_until, due_at, id);
CREATE INDEX IF NOT EXISTS idx_tasks_chat
    ON tasks(chat_id, id DESC);
