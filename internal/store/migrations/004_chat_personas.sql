CREATE TABLE IF NOT EXISTS chat_personas (
    chat_id  INTEGER PRIMARY KEY REFERENCES chats(chat_id) ON DELETE CASCADE,
    prompt   TEXT NOT NULL CHECK (length(prompt) > 0)
);
