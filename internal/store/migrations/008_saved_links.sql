CREATE TABLE IF NOT EXISTS saved_links (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    chat_id      INTEGER NOT NULL REFERENCES chats(chat_id) ON DELETE CASCADE,
    user_id      INTEGER NOT NULL,
    title        TEXT NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    canonical_url TEXT NOT NULL CHECK (length(canonical_url) BETWEEN 1 AND 2048),
    content      TEXT NOT NULL CHECK (length(content) BETWEEN 1 AND 8000),
    created_at   TEXT NOT NULL,
    updated_at   TEXT NOT NULL,
    UNIQUE (chat_id, canonical_url)
);

CREATE INDEX IF NOT EXISTS idx_saved_links_chat_updated
    ON saved_links(chat_id, updated_at DESC, id DESC);
