CREATE TABLE IF NOT EXISTS chat_miniapp_fallback (
    chat_id  INTEGER PRIMARY KEY REFERENCES chats(chat_id) ON DELETE CASCADE,
    enabled  INTEGER NOT NULL CHECK (enabled IN (0, 1))
);
