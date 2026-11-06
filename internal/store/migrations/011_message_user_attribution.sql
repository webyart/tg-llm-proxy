ALTER TABLE messages ADD COLUMN user_id INTEGER NOT NULL DEFAULT 0 CHECK (user_id >= 0);

CREATE INDEX IF NOT EXISTS idx_messages_user_role_created_at ON messages(user_id, role, created_at);
