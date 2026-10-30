-- 001_init.sql — the v1 schema of §5 ("Model of data (SQLite)"), one idempotent
-- migration (every clause has IF NOT EXISTS), shipped inside the binary through
-- go:embed (§13 risk "the migration does not get into the image": in the container
-- the file of the migration can be absent, the text of the schema is in the binary).
--
-- PRAGMA is deliberately absent here:
--   foreign_keys — works on the level of the CONNECTION, one Exec of the migration
--     does not propagate it to the pool;
--   journal_mode — is stored in the database FILE itself, is applied once in store.Open.
-- Both are given through the DSN (see §5 "Pragmas"), the whitelist in store.BuildDSN.

CREATE TABLE IF NOT EXISTS chats (
    chat_id          INTEGER PRIMARY KEY,
    user_id          INTEGER NOT NULL,
    title            TEXT,
    created_at       TEXT NOT NULL DEFAULT (datetime('now')),
    last_active_at   TEXT NOT NULL DEFAULT (datetime('now')),
    context_reset_at TEXT           -- messages earlier than this date do not enter the context
);

CREATE TABLE IF NOT EXISTS messages (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id     INTEGER NOT NULL REFERENCES chats(chat_id) ON DELETE CASCADE,
    role        TEXT NOT NULL CHECK (role IN ('user','assistant','system')),
    content     TEXT NOT NULL,
    tokens_in   INTEGER,
    tokens_out  INTEGER,
    model       TEXT,
    latency_ms  INTEGER,
    telegram_message_id INTEGER,   -- the Telegram message id; NULL for model answers
    created_at  TEXT NOT NULL DEFAULT (datetime('now'))
);

CREATE INDEX IF NOT EXISTS idx_messages_chat_time ON messages(chat_id, id DESC);

-- the guard against repeated answers after a container restart (§2 point 7, §13)
CREATE UNIQUE INDEX IF NOT EXISTS uq_messages_chat_tg_id
    ON messages(chat_id, telegram_message_id)
    WHERE telegram_message_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS bot_state (          -- the service state of the process
    key   TEXT PRIMARY KEY,                     -- 'last_update_id'
    value TEXT NOT NULL                         -- the offset of getUpdates, survives a restart
);

CREATE TABLE IF NOT EXISTS events (        -- service: commands, errors, limits
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    user_id    INTEGER,
    kind       TEXT NOT NULL,
    detail     TEXT,
    created_at TEXT NOT NULL DEFAULT (datetime('now'))
);
