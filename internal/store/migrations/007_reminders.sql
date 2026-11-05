CREATE TABLE IF NOT EXISTS reminders (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id      INTEGER NOT NULL REFERENCES chats(chat_id) ON DELETE CASCADE,
    user_id      INTEGER NOT NULL,
    text         TEXT NOT NULL CHECK (length(text) BETWEEN 1 AND 500),
    cadence      TEXT NOT NULL CHECK (cadence IN ('once', 'daily')),
    daily_time   TEXT CHECK (
        (cadence = 'once' AND daily_time IS NULL) OR
        (cadence = 'daily' AND daily_time GLOB '[0-9][0-9]:[0-9][0-9]' AND length(daily_time) = 5
            AND CAST(substr(daily_time, 1, 2) AS INTEGER) BETWEEN 0 AND 23
            AND CAST(substr(daily_time, 4, 2) AS INTEGER) BETWEEN 0 AND 59)
    ),
    next_run_at  TEXT NOT NULL,
    status       TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'sending', 'sent', 'cancelled', 'failed')),
    attempts     INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    claim_token  TEXT,
    lease_until  TEXT,
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    last_sent_at TEXT,
    finished_at  TEXT
);

CREATE INDEX IF NOT EXISTS idx_reminders_due
    ON reminders(status, next_run_at, id);
CREATE INDEX IF NOT EXISTS idx_reminders_lease
    ON reminders(status, lease_until, next_run_at, id);
CREATE INDEX IF NOT EXISTS idx_reminders_chat_active
    ON reminders(chat_id, status, next_run_at, id);
