-- The two indexes the hot read and retention paths were missing.
--
-- messages(created_at): every prune and every retention preview filters by created_at, and the only
-- indexes on the table are (chat_id, id DESC), (chat_id, telegram_message_id) and
-- (user_id, role, created_at) — the first two cannot answer "older than X" for all chats at once.
--
-- chats(last_active_at): the admin and inspect listings order every chat by last activity.
CREATE INDEX IF NOT EXISTS idx_messages_created_at ON messages(created_at);

CREATE INDEX IF NOT EXISTS idx_chats_last_active ON chats(last_active_at DESC, chat_id DESC);
