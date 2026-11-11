-- 023_chat_cost.sql — per-chat daily cost budget (schema 23).
--
-- The budget is stored in millionths of a dollar (micro-USD), the same unit the usage API reports with six
-- decimals, so no floating-point value is ever written to the database. 0 means "unlimited", exactly like the
-- three limits of 020: a chat without a row and a chat with a zero budget behave the same.
--
-- The column is added, never rewritten: existing rows keep their limits and start with no cost budget, which
-- is the pre-023 behaviour. Prices themselves do not live in the database: they are the MODEL_PRICES env row,
-- so a price change needs no migration.
ALTER TABLE chat_limits ADD COLUMN daily_cost_limit_micros INTEGER NOT NULL DEFAULT 0
    CHECK (daily_cost_limit_micros >= 0);
