-- 020_chat_limits.sql — per-chat/group limits and restrictions (schema 20).
--
-- One row per chat that has an explicit limit. The absence of a row means "no limits": every number is
-- 0 (unlimited) and no tool is disabled, which keeps a fresh database byte-identical in behaviour to
-- v1.63.0. A private chat and a group live in the same table and are told apart by the sign of chat_id
-- (Telegram gives groups negative ids); chat_id is NEVER a user id.
--
-- The 0 default is the "unlimited" marker of the whole feature: daily_model_limit, daily_network_limit
-- and per_minute_limit are checked with `> 0` before they are enforced.
CREATE TABLE IF NOT EXISTS chat_limits (
    chat_id             INTEGER PRIMARY KEY REFERENCES chats(chat_id) ON DELETE CASCADE,
    daily_model_limit   INTEGER NOT NULL DEFAULT 0 CHECK (daily_model_limit >= 0),
    daily_network_limit INTEGER NOT NULL DEFAULT 0 CHECK (daily_network_limit >= 0),
    per_minute_limit    INTEGER NOT NULL DEFAULT 0 CHECK (per_minute_limit >= 0),
    disabled_tools      TEXT NOT NULL DEFAULT ''   -- comma-separated allowlist of read_url,web_search,calculator
);

-- Per-chat usage is counted from events, which until now carried only user_id. The column is nullable
-- and every existing insert keeps working unchanged: historical rows stay unattributed (NULL), so the
-- per-chat counters begin at zero on the day of the upgrade. New model_call/usage rows written by the
-- dispatcher carry the chat id, which is what makes a per-chat daily limit possible at all.
ALTER TABLE events ADD COLUMN chat_id INTEGER;

-- Today's per-chat counters: events of one chat by kind/detail over a UTC day.
CREATE INDEX IF NOT EXISTS idx_events_chat_kind_detail_created_at
    ON events(chat_id, kind, detail, created_at);

-- The per-minute limit counts stored user messages of one chat in a trailing minute, and the admin
-- chat list shows MAX(messages.created_at) as the last activity; both read (chat_id, created_at).
CREATE INDEX IF NOT EXISTS idx_messages_chat_created_at
    ON messages(chat_id, created_at);
