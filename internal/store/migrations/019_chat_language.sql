CREATE TABLE IF NOT EXISTS chat_language (
    chat_id   INTEGER PRIMARY KEY REFERENCES chats(chat_id) ON DELETE CASCADE,
    language  TEXT NOT NULL CHECK (language IN ('ru', 'en'))
);
