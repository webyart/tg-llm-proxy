-- 021_task_status.sql — live task progress message (schema 21).
--
-- One nullable id of the Telegram message that shows the live progress of a task. The scheduler rewrites
-- that single message on step transitions instead of sending a new line for every step. NULL means "no
-- status message yet": rows written by an older release keep working unchanged, and a task created from
-- the Mini App has no Telegram message at all until its first step is claimed.
--
-- The column deliberately has no NOT NULL and no default: an old row cannot invent a message id, and 0 is
-- not a valid Telegram message, so NULL is the only honest "absent" value. Reads coalesce it to 0.
ALTER TABLE tasks ADD COLUMN status_message_id INTEGER;
