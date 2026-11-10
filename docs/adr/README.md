# Architecture Decision Records

Short records of the decisions that shape this repository: the context, the choice, and what it costs. They
exist because the reasoning used to live in `PLAN.md` — a 185 KB local file excluded from git — and a decision
nobody can find is a decision that gets re-litigated.

The format is deliberately small. Each record answers: what was decided, why, and what would make us revisit it.

| # | Decision |
|---|---|
| [0001](0001-sqlite-single-writer.md) | SQLite + WAL as the only store, with a single writer process |
| [0002](0002-telegram-long-polling.md) | Long polling as the default transport, webhook as an opt-in |
| [0003](0003-fail-fast-configuration.md) | Fail fast on any configuration problem, deny by default |
| [0004](0004-search-indexes.md) | FTS5 external-content indexes built at open time |
| [0005](0005-miniapp-private-chats.md) | The Mini App is a private-chat companion |
| [0006](0006-restart-policy.md) | The container restarts `unless-stopped`; crash loops are the monitor's job |
| [0007](0007-store-repo-postgres.md) | Keep SQLite by default; introduce store.Repo and Postgres only when a recorded trigger fires |
