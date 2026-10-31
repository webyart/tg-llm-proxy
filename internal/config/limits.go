package config

// limits — the limits of §7

// The bounds of the §7 table (CONTEXT_MESSAGES, MAX_CONTEXT_CHARS, MAX_INPUT_CHARS,
// TG_REPLY_DEADLINE_SECONDS, MAX_MESSAGE_CHUNK, HEALTH_PORT) live next to the loader,
// in internal/config/config.go, because the loader of §10 День 1 DoD ("ENV → Config,
// validation, понятные сообщения об ошибках") validates every row in the same place
// where it reads it and names the row and its bounds in the problem message.
// The module of the time budgets of the glued history and of the TTLs of bot_state
// (§7, §15 backlog) arrives with the context builder of Дня 4, not in v1.1.
