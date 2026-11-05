CREATE INDEX IF NOT EXISTS idx_events_user_kind ON events(user_id, kind);
CREATE INDEX IF NOT EXISTS idx_events_kind_detail ON events(kind, detail);
CREATE INDEX IF NOT EXISTS idx_events_created_at ON events(created_at);
