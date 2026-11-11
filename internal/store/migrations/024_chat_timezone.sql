-- 024_chat_timezone.sql — per-chat display timezone (schema 24).
--
-- The zone is an IANA name (for example Asia/Yekaterinburg). It changes ONLY how a timestamp is rendered:
-- created_at/due_at/expires_at stay UTC in every table, so switching the zone never rewrites history and a
-- reminder created before the switch fires at the same absolute instant.
--
-- A missing row means "use DEFAULT_TIMEZONE". updated_at is the SQLite UTC datetime of the last write; it
-- is nullable so a row inserted by an older/hand-written migration does not invent a time.
CREATE TABLE IF NOT EXISTS chat_timezone (
    chat_id    INTEGER PRIMARY KEY REFERENCES chats(chat_id) ON DELETE CASCADE,
    timezone   TEXT NOT NULL,
    updated_at TEXT
);
