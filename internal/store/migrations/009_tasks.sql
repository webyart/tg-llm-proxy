CREATE TABLE IF NOT EXISTS tasks (
    id                   INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id              INTEGER NOT NULL REFERENCES chats(chat_id) ON DELETE CASCADE,
    user_id              INTEGER NOT NULL,
    goal                 TEXT NOT NULL CHECK (length(goal) BETWEEN 1 AND 2000),
    model                TEXT NOT NULL CHECK (length(model) BETWEEN 1 AND 200),
    plan_json            TEXT NOT NULL CHECK (length(plan_json) BETWEEN 1 AND 8192),
    results_json         TEXT NOT NULL DEFAULT '[]' CHECK (length(results_json) <= 65536),
    next_step            INTEGER NOT NULL DEFAULT 0 CHECK (next_step BETWEEN 0 AND 3),
    status               TEXT NOT NULL DEFAULT 'ready' CHECK (status IN ('ready', 'running', 'awaiting_approval', 'completed', 'cancelled', 'failed')),
    attempts             INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
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
    finished_at          TEXT
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_tasks_chat_active
    ON tasks(chat_id) WHERE status IN ('ready', 'running', 'awaiting_approval');
CREATE UNIQUE INDEX IF NOT EXISTS uq_tasks_approval_token
    ON tasks(approval_token) WHERE approval_token IS NOT NULL;
CREATE INDEX IF NOT EXISTS idx_tasks_due
    ON tasks(status, due_at, id);
CREATE INDEX IF NOT EXISTS idx_tasks_lease
    ON tasks(status, lease_until, due_at, id);
CREATE INDEX IF NOT EXISTS idx_tasks_chat
    ON tasks(chat_id, id DESC);
