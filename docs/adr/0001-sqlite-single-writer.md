# 0001 — SQLite + WAL as the only store, one writer process

**Status:** accepted (in force since the first day of the project)

## Context

The bot keeps conversation history, per-chat settings, RAG documents and chunks, reminders, tasks, saved links,
usage counters and a daily quota. The deployment target is a single home machine or a small VPS running one
container; the database is one file inside a named Docker volume. Nothing else is allowed to write it.

The alternative — Postgres — buys concurrent writers, real migrations tooling and remote access, at the cost of
a second service to run, back up and secure for a workload measured in single-digit chats.

## Decision

One SQLite file, WAL journal, four pooled connections, `_txlock=immediate`, `busy_timeout=5000`,
`synchronous=NORMAL`. The DSN is a closed whitelist: every pragma is written explicitly and read back at open,
so a typo cannot silently change it (`internal/store/dsn.go`, `store.go`). Migrations are embedded SQL, applied
one by one with the version recorded in `schema_migrations`; a database whose version is newer than the binary
is refused outright (`store.GuardSchemaVersion`).

## Consequences

- Backups are `VACUUM INTO` on a read-only handle: a consistent snapshot without stopping the writer.
- A long read must not run inside a transaction of the runtime pool: with `_txlock=immediate` any transaction
  takes the write lock. History export therefore reads an explicit id range without `BeginTx`.
- Only one process may write. `cmd/dbprune -apply`, backup rotation and the maintenance scripts all respect
  that; running two replicas of the bot against one file is not supported.
- Revisit when a second long-lived writer appears (a second replica, or a web client with its own write path).
  Then the store interface gets a Postgres implementation behind it, and the FTS5 indexes are replaced by
  Postgres full-text search.
