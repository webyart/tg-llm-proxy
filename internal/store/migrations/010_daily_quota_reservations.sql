CREATE TABLE IF NOT EXISTS daily_quota_reservations (
    user_id INTEGER NOT NULL CHECK (user_id > 0),
    day     TEXT NOT NULL,
    class   TEXT NOT NULL CHECK (class IN ('model', 'network_tools')),
    used    INTEGER NOT NULL CHECK (used > 0),
    PRIMARY KEY (user_id, day, class)
);

CREATE INDEX IF NOT EXISTS idx_daily_quota_day_class ON daily_quota_reservations(day, class);
CREATE INDEX IF NOT EXISTS idx_events_user_kind_created_at ON events(user_id, kind, created_at);
CREATE INDEX IF NOT EXISTS idx_events_user_kind_detail_created_at ON events(user_id, kind, detail, created_at);
