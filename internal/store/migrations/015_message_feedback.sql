-- 👍/👎 feedback on model answers.
--
-- One row per (message, user): a repeated click replaces the value through the primary key, and the
-- cascade removes the rating together with the message it judges. chat_id is denormalized so the
-- aggregates of /stats and of the Mini App answer from this table alone.
CREATE TABLE IF NOT EXISTS message_feedback (
    message_id INTEGER NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    chat_id    INTEGER NOT NULL,
    user_id    INTEGER NOT NULL,
    value      INTEGER NOT NULL CHECK (value IN (-1, 1)),
    created_at TEXT NOT NULL,
    PRIMARY KEY (message_id, user_id)
);

-- Aggregates and date filters are per chat; the primary key already answers "rating of this message".
CREATE INDEX IF NOT EXISTS idx_message_feedback_chat_created ON message_feedback(chat_id, created_at);
