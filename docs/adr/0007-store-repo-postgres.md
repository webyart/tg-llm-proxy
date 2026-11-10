# 0007 — Keep SQLite as the store; introduce `store.Repo` and Postgres only when a trigger fires

**Status:** proposed (2026-11-10, HEAD `209064a`, tag `v1.60.0`, schema 19).
This record does not supersede [ADR-0001](adr/0001-sqlite-single-writer.md); it makes its
"revisit when a second long-lived writer appears" clause concrete and adds a rollout plan.
It also affirms [ADR-0004](adr/0004-search-indexes.md) (FTS5) until a trigger fires.
The decision itself is §2; §3–§6 are the boundary, the plan, the risks and the cost that the decision buys.

---

## 1. Context

### 1.1 The store in numbers

| Fact | Value | Where it is verifiable |
|---|---|---|
| Driver | `modernc.org/sqlite` (pure Go, no C toolchain) | `internal/store/dsn.go:26`, `internal/store/store.go:19` |
| Schema | **19** embedded migrations (`001_init` … `019_chat_language`) | `internal/store/migrations.go:75`, `internal/store/migrations/` |
| Tables | 22, of which 2 are FTS5 virtual tables (`messages_fts`, `rag_chunks_fts`) and 1 is the version table (`schema_migrations`) | migrations `001`–`019` |
| Journal / locking | WAL, `busy_timeout=5000`, `synchronous=NORMAL`, `_txlock=immediate`, `journal_size_limit=16 MiB`, `wal_autocheckpoint=1000` | `internal/store/dsn.go:43-52`, `:63-78` |
| Pool | `MaxOpenConns=4`, `MaxIdleConns=2` | `internal/store/store.go:24-25`, `:66-67` |
| Store code | 27 non-test `.go` files, **6,402** lines; 42 test files, **8,708** lines | counted on this checkout |
| Exported API | 145 exported free functions | counted on this checkout |
| Call sites outside the package | 266 `store.Func(…)` calls in 33 non-test files; 169 `deps.DB` references | counted on this checkout |
| DB size | **2,101,248 B = 2.0 MiB** (`data/backups/pre-v1.60-20261110-230059.db`, schema 19) | `data/backups/`, `README.md:595` ("файл ~1–2 МБ") |
| Content at the last measured snapshot (2026-11-08, schema 14) | `messages 238`, `rag_chunks 3`, FTS rows `238`/`3`, 6 triggers, integrity ok | `docs/CODE_REVIEW_2026-11-08.md:362` |

The roadmap and the review quote "25 store files / ~5.3k LOC". On this checkout the count is 27 non-test
files / 6,402 lines; the difference does not change any conclusion below. Treat **schema 19** and **2.0 MiB**
as the numbers this record is written against.

### 1.2 The deployment reality at v1.60.0

- **One replica.** `docker-compose.yml:8` defines a single `bot` service, no `deploy.replicas`, one SQLite
  file in the `bot-data` volume (`DB_PATH=/data/bot.db`). A second replica would additionally have to solve
  Telegram `getUpdates` leader election — only one poller may consume an update.
- **One owner, single-digit chats.** ADR-0001 was accepted for "a single home machine or a small VPS"; the
  measured content above (238 messages, 3 RAG chunks) is a personal assistant, not a multi-tenant service.
- **The web client already shares the writer.** The Mini App is not a second write path: `main.go:317` calls
  `webapp.New(cfg, db, client, logger)` with the same `*sql.DB`, and `main.go:357` wires
  `SetChatBackend(dispatcher)` — the same `Dispatcher`, the same `ratelimit.Locks`, the same daily quota
  (confirmed by the Sprint 5 status in `docs/ROADMAP.md:44-53`). The "web client with its own write path"
  trigger is therefore **not** met by the current Mini App.
- **The per-chat lock is in-process, not in the store.** `internal/ratelimit/ratelimit.go:93-108` is a
  `map[int64]*chatLock` guarded by a `sync.Mutex`. It serialises work inside one binary and does nothing for a
  second process. Any plan for a second writer must replace it with a cross-process mechanism; Postgres alone
  does not do that (see §3.3 and R9).
- **The single-writer assumption is already hardened, not buggy.** The review found and the code fixed the
  race classes that a second process exposes: the version-check → migrate race
  (`internal/store/store.go:165-203`, re-check inside `BEGIN IMMEDIATE` at `:250-293`), the FTS rebuild/marker
  race (`internal/store/fts.go:86-127`), history export under the write lock
  (`docs/CODE_REVIEW_2026-11-08.md:94`), and the non-rollback quota reservation
  (`internal/store/quota.go:40-68`). What remains is an architectural constraint, not a defect.

### 1.3 Triggers — what must become true to justify the work

At least one of the following, with a measurement or a written requirement, plus an owner and a date.
"Postgres is nicer" and "we might need HA" are not triggers.

| # | Trigger | Why it breaks SQLite | How it is measured |
|---|---|---|---|
| **T1** | **A second long-lived writer process**: a second `bot` replica (with leader election for long polling) or a separate web/API process that opens its own `*sql.DB` and writes | WAL allows one writer; the migration guards and the maintenance/backup rotation assume a single writer (§1.2). Two writers against one file are explicitly unsupported | A process diagram naming the second writer and its write paths; the per-chat lock has a cross-process design |
| **T2** | **An availability requirement that one container cannot meet**: two live instances behind a load balancer, or an RPO/RTO that a 2 MiB file + nightly `VACUUM INTO` cannot satisfy (current RPO ≈ 24 h, RTO minutes — `README.md:595`) | A file copy is a cold-standby strategy; it cannot do continuous availability or point-in-time recovery | An SLA/RPO number written down by the owner; a failed or impossible restore drill |
| **T3** | **Measured write contention**: sustained `SQLITE_BUSY` / `busy_timeout` exhaustion, or write p95 above an agreed threshold over a real window | `_txlock=immediate` turns every transaction into a write-lock request; a slow reader or a long prune queues all writes | Counters in `internal/store` / `internal/health` over ≥ 7 days; not a code smell |
| **T4** | **Data size or startup cost** that no longer fits the model: a database far beyond RAM, or a one-time FTS rebuild at `store.Open` (`internal/store/fts.go:62-84`) exceeding the deploy budget | The rebuild is part of the startup path; WAL checkpoints and `VACUUM` are whole-file operations | Measured startup time and DB size; the current 2.0 MiB is nowhere near it |
| **T5** | **A consumer that needs direct or remote data access** (analytics, an external service, several read-heavy clients) that cannot go through the bot's HTTP API | SQLite has no network protocol and no concurrent-writer server | A named consumer and its query pattern |

**T1 and T2 are the only triggers that pay for Postgres.** T3 is first a reason to fix access patterns (the
history export under the write lock, `docs/CODE_REVIEW_2026-11-08.md:94`, and the prune batching of §3.3), not
to add a server. T4 and T5 are cheaper to solve with an export/read model than with a store migration.

### 1.4 What the review already says

`docs/CODE_REVIEW_2026-11-08.md:236` (recommendation 2) says: introduce a `store.Repo` interface and a second
implementation "when a second replica or a web client with load appears". This record accepts that
recommendation and adds the acceptance criteria, the boundary, and the cost — so that the work is not started
on a hunch.

---

## 2. Decision

### 2.1 The decision

1. **SQLite stays the production store and the default.** `modernc.org/sqlite`, one file, WAL,
   `_txlock=immediate`, the existing 19-migration history and the existing `VACUUM INTO` backup path remain as
   they are at v1.60.0. No schema change, no DSN change, no compose change follows from this record.
2. **`store.Repo` is not introduced now.** With one writer it adds indirection and test surface with zero
   behavioural payoff. It is introduced **only when a trigger in §1.3 fires**, as Phase 1 of §4.
3. **When it is introduced, Postgres is a second implementation, not a rewrite.** The domain logic — the Go
   RAG scorer (`internal/store/rag.go:716-736`), chunking, tokenisation, stopwords, validation, error semantics,
   the Telegram and Mini App flows — does not move and is not forked. Only persistence moves behind the
   interface.
4. **The interface is extracted from the call sites, not designed up front.** It is the union of the
   operations the bot, the Mini App and the `cmd/*` tools actually perform, grouped by use case, with one
   method = one transaction.
5. **SQLite remains the reference implementation** for tests, development, `cmd/*` tools and offline restore
   even after a Postgres cutover. It is not deleted by this work.

### 2.2 What is explicitly NOT decided

| Open question | Why it is not decided here | When it must be decided |
|---|---|---|
| **Schema-per-tenant vs one shared schema with `chat_id`** | The current model is one schema, `chat_id` is the tenant key and every chat-scoped table has a FK to `chats` (migration `001`). Schema-per-tenant would change every query, every migration and every connection-pool decision; it is a separate ADR | Before Phase 2, together with the multitenancy row of Sprint 7 |
| **Managed Postgres vs self-hosted container** | Managed (Neon/Supabase/RDS) buys PITR and off-host backups but adds a vendor, a network dependency and a bill; self-hosted keeps the "one box" story but puts the backup/RPO burden back on the owner | Before Phase 2 (it determines the backup path of Phase 4) |
| **Hosting** (same home host, a VPS, a cloud region) | It determines latency, disk, backup target and whether `pg_hba`/TLS is needed; none of that is knowable today | Before Phase 2 |
| **Whether pgvector is used at all** | First port is `bytea` + exact scan, which is numerically identical to the current BLOB + Go cosine; ANN (HNSW/IVFFlat) is a separate quality decision | Phase 3, only if the exact scan misses a latency budget |
| **Whether SQLite stays a supported production driver after cutover** | Recommend: yes for dev/tests/offline, no for production; not decided | Before Phase 5 |
| **Per-chat cutover vs stop-the-world** | §4.1 recommends stop-the-world; per-chat routing doubles the failure modes and is not chosen | Phase 5 planning |
| **Connection pooling / PgBouncer** | Only matters if the writer count grows beyond one process; depends on the hosting decision | Phase 2 |
| **Who operates the second service** | A named owner for upgrades, credentials, monitoring and backups is a precondition, not a detail | Before Phase 2 |

### 2.3 Consequences of the decision

- No engineering time is spent on the migration until a trigger fires; the 2–3 week roadmap row stays open
  but blocked, with this record as its definition of ready.
- The `store` package keeps its current shape; no package move, no `*sql.DB` removal from `main.go`,
  `internal/bot` and `internal/webapp`.
- The trigger is measurable: `SQLITE_BUSY`/`busy_timeout` and DB size already have a natural home in
  `internal/health` and the existing Prometheus registry (`internal/store/metrics.go`). The first thing a
  trigger investigation needs is a baseline, and the current code can produce one without the migration.
- ADR-0001's revisit clause is now satisfied procedurally: a second writer is not a surprise, it is T1.

---

## 3. Repository boundary

### 3.1 Principles

1. **One method = one use case = one transaction.** The interface must not expose `BeginTx`, `*sql.Tx`,
   `sql.Rows`, or a way to compose two calls into one transaction. `PutRAGDocument` is one method because the
   document upsert, the chunk delete and the batched insert are one unit of work
   (`internal/store/rag.go:109-186`); `insertRAGChunks` must not leak.
2. **Domain types cross the boundary, driver types do not.** `store.Message`, `store.RAGChunk`,
   `store.Reminder`, `store.Task`, `store.PruneReport` stay where they are; `[]byte`, `sql.NullString` and
   `driver.Value` do not appear in the interface.
3. **Sentinels, not driver errors.** `sql.ErrNoRows` and `pgx.ErrNoRows` are translated by the implementation
   to `store.ErrNotFound` (plus the existing `ErrFeedback*`, `ErrRAGDocumentLimit`, `ErrReminderLimit`,
   `ErrSchemaDowngrade`). Callers must not branch on a driver string.
4. **No SQL, no dialect names, no table names in the interface.** Table names appear only in a diagnostics
   sub-interface (§3.3).
5. **The interface lives in `internal/store`** (types and sentinels stay there). Implementations live in
   `internal/store` (SQLite, delegating to the existing functions) and `internal/store/postgres` (new). This
   avoids moving 6.4k lines and keeps the SQLite tests and `cmd/*` tools working unchanged.
6. **Config is per implementation.** `store.Config{Driver, DSN, MaxOpenConns}` at the call site; the SQLite
   branch keeps `DB_PATH` and the DSN whitelist, the Postgres branch reads a new secret (`DATABASE_URL`) and
   goes through fail-fast config validation (ADR-0003). This is a new config row, a new secret and a new
   `.env.example` entry — a Phase 1/2 cost, not a free abstraction.

### 3.2 Interface sketch (illustrative, not to be committed as-is)

```go
// internal/store/repo.go — introduced in Phase 1, only after a trigger.
type Repo interface {
    // lifecycle
    Ping(ctx context.Context) error
    Migrate(ctx context.Context) error
    SchemaVersion(ctx context.Context) (int, error)
    Close() error

    // chats, context and service state
    EnsureChat(ctx context.Context, chatID, userID int64, title string) error
    ClearChat(ctx context.Context, chatID int64) (int64, error)
    State(ctx context.Context, key string) (string, bool, error)
    SetState(ctx context.Context, key, value string) error
    LastUpdateID(ctx context.Context) (int64, bool, error)
    SetLastUpdateID(ctx context.Context, id int64) error

    // messages and events
    Append(ctx context.Context, m Message) (int64, error)
    ReplacePendingUserContent(ctx context.Context, chatID int64, content string) (bool, error)
    IsDuplicate(ctx context.Context, chatID int64, telegramMessageID int64) (bool, error)
    History(ctx context.Context, chatID int64, limit int) ([]Message, error)
    RecentByChat(ctx context.Context, limit int) ([]Message, error)
    LastVisibleMessage(ctx context.Context, chatID int64) (Message, error)
    UnansweredQuestions(ctx context.Context, limit int) ([]Message, error)
    DeleteMessage(ctx context.Context, chatID, id int64) (bool, error)
    DeleteChatHistory(ctx context.Context, chatID int64) (int64, error)
    LogEvent(ctx context.Context, userID int64, kind, detail string) error
    CountEvents(ctx context.Context, userID int64, kind string, since time.Time) (int64, error)

    // search and RAG
    SearchMessages(ctx context.Context, chatID int64, query string, limit int) ([]Message, error)
    PutRAGDocument(ctx context.Context, chatID, userID int64, name, content string, maxDocs int) (int64, int, error)
    ListRAGDocuments(ctx context.Context, chatID int64) ([]RAGDocument, error)
    DeleteRAGDocument(ctx context.Context, chatID, id int64) (bool, error)
    ClearRAGDocuments(ctx context.Context, chatID int64) (int64, error)
    RAGCandidates(ctx context.Context, chatID int64, terms []string, limit int) ([]RAGCandidate, error)
    ChunkVectors(ctx context.Context, chatID int64, model string, ids []int64) (map[int64][]float32, error)

    // quota, usage, tasks, reminders, links, settings, feedback, resume …
    // (~90 methods in total; the full list is the union of §3.3.)
}
```

The RAG split is deliberate: `RAGCandidates` is the dialect-specific candidate selection (FTS5 `MATCH` +
`bm25()` today, `tsvector`/`ts_rank_cd` or an exact scan tomorrow), while the scoring stays in a
dialect-neutral function. The public entry point `SearchRAGChunksWithOptions`
(`internal/store/rag.go:589-712`) becomes a service that calls `RAGCandidates` + `ChunkVectors` and then the
existing scorer. **The scorer is not reimplemented per dialect.**

### 3.3 Mapping: current `store` API → `Repo` method

Legend: **✅** abstractable as-is · **⚠️** abstractable but the behaviour or type changes · **❌** stays
dialect-specific and must not be in `Repo`.

| Current API (file) | `Repo` method / fate | Verdict |
|---|---|---|
| `Open`, `OpenMemory`, `BuildDSN`, `MemoryDSN`, `EscapePath`, `Pragma`, `EnableStrictPragmas`, `StrictPragmasEnabled` (`store.go`, `dsn.go`) | Replaced by `Open(ctx, Config)` per implementation; DSN whitelist, pragma read-back and memory form stay SQLite-only. Postgres: `pgxpool.ParseConfig` + `SHOW server_version_num`, `SHOW transaction_isolation`, `standard_conforming_strings` checks in `postgres.Open` | ❌ |
| `ApplySchema`, `Statements`, `MigrationSQL`, `SchemaVersion`, `SupportedSchemaVersion`, `GuardSchemaVersion`, `addColumnIfMissingGuard` (`store.go`, `migrations.go`) | `Repo.Migrate(ctx)` + `Repo.SchemaVersion(ctx)`. Each dialect owns its embedded history and its `schema_migrations` table; **do not share version numbers across dialects** — a fake 1:1 mapping hides a divergence. `ErrSchemaDowngrade` behaviour is part of the contract | ⚠️ |
| `ensureSearchIndexes`, `ensureSearchIndexMarker`, `searchIndexIntegrity`, `messageSearchExpression`, `ragSearchExpression`, `quoteFTSTerm`, `searchTerms` (`fts.go`) | FTS5 only: triggers, `search_index_meta`, `MATCH` expression grammar, `integrity-check`. Postgres: generated `tsvector` column + GIN, `websearch_to_tsquery`/`plainto_tsquery` with a `russian`/`english` config, `ts_rank_cd`. The expression builder moves inside `postgres` | ❌ |
| `SearchMessages` (`history.go:146-180`) | `Repo.SearchMessages`. Contract: newest-first internally, caller reverses for display (`history.go:175-178`); expression building is implementation-owned. Postgres `ts_rank_cd` ordering will NOT be identical — see R2 | ⚠️ |
| `SearchRAGChunks`, `SearchRAGChunksDetailed`, `SearchRAGChunksWithOptions`, `readRAGSearchCorpus`, `readRAGFTSCandidates`, `readRAGNameCandidates`, `readRAGVectorCandidates`, `ragChunkRowsByID`, `ragDocumentsMatchingTerms` (`rag.go`) | Split: `RAGCandidates` + `ChunkVectors` in the Repo; `lexicalScore`, `ragTopChunks`, `ragTruncateChunks`, `ragCosine`, chunking, stopwords and limits stay dialect-neutral. Contract: `ragCandidateLimit=300`, `ragVectorScanLimit=5000`, `ragVectorCandidateLimit=64`, `ragHybridMinimumScore=0.15`, `DefaultEmbeddingWeight=0.4` are domain constants | ⚠️ |
| `PutRAGDocument`, `PutRAGDocumentWithLimit` (`rag.go:109-163`) | `Repo.PutRAGDocument` — one transaction: upsert by `(chat_id, name)`, limit check, delete chunks, batched insert of 100 | ✅ |
| `ListRAGDocuments`, `DeleteRAGDocument`, `ClearRAGDocuments`, `ExportRAGDocuments` | `Repo.ListRAGDocuments`, `DeleteRAGDocument`, `ClearRAGDocuments`, `ExportRAGDocuments` | ✅ |
| `PutChunkEmbedding`, `PutChunkEmbeddingWithDimension`, `resolveEmbeddingDimension`, `encodeVector`, `decodeVector` (`embeddings.go`) | `Repo.PutChunkEmbedding(ctx, chatID, chunkID, model, dimension, vector)`. Vector encoding is implementation-owned (LE BLOB vs `bytea`/`vector`); `[]float32` crosses the boundary. The dimension registry (`rag_embedding_model_dimensions`) is a read-modify-write that needs `ON CONFLICT`/`RETURNING` or `FOR UPDATE` in PG | ⚠️ |
| `ChunkEmbeddings`, `ChunksMissingEmbedding`, `DocumentEmbeddingTargets`, `EmbeddingStats`, `RAGChatIDs` | Same names on `Repo`; batches of 400 stay a caller concern (`ragEmbeddingChatBatch`) | ✅ |
| `ReserveDailyQuota`, `ReleaseDailyQuota`, `DailyQuotaUsed` (`quota.go`) | `Repo.ReserveDailyQuota`, `ReleaseDailyQuota`, `DailyQuotaUsed`. Contract: reserve-before-call, release-on-failure, never double-reserve. SQLite uses `INSERT … ON CONFLICT … WHERE used < ? RETURNING`; PG needs the same row-level serialisation (the statement ports, but the lock waits differently) | ⚠️ |
| `UserDailyUsage`, `DailyModelCalls`, `DailyNetworkToolCalls`, `utcDayStart` (`usage_daily.go`) | `Repo.UserDailyUsage`; day boundaries stay in Go | ✅ |
| `CreateReminder`, `ListReminders`, `ClaimNextReminder` (`reminders.go:286`), `CompleteReminder`, `CancelReminder`, `CancelClaimedReminder`, `RetryReminder`, `RetryFailedReminder`, `ListFailedReminders`, `DeleteFailedReminder` | Same on `Repo`. The claim is a lease (`claim_token`, `lease_until`, conditional `UPDATE … RETURNING`); PG should use `SELECT … FOR UPDATE SKIP LOCKED` or the same single-statement `UPDATE … WHERE id = (SELECT … LIMIT 1) RETURNING`. Contract: exactly one claimant per row per lease | ⚠️ |
| `CreateTask`, `CurrentTask`, `ActiveTaskStatus`, `ListAwaitingTasks`, `ClaimNextTask` (`tasks.go:211`), `CompleteTaskStep`, `CompleteTask`, `FailTask`, `CancelTask`, `ResumeTask`, `SaveTaskApproval`, `ConsumeTaskApproval`, `TaskByApprovalToken`, `ExpireTasks`, `ClearTasks`, `DeleteTask` | Same on `Repo`. `CompleteTask` writes the assistant message in the same transaction (`tasks.go:334-390`) — the port must keep that atomicity | ⚠️ |
| `PutSavedLink`, `GetSavedLink`, `ListSavedLinks`, `SearchSavedLinks`, `DeleteSavedLink`, `ClearSavedLinks` | Same on `Repo` | ✅ |
| `EnsureChat`, `GetState`, `SetState`, `LastUpdateID`, `SetLastUpdateID`, `ClearChat`, `HistoryOfChat`, `RecentByChat`, `LastVisibleMessage`, `UnansweredQuestions`, `Stats`, `Append`, `ReplacePendingUserContent`, `IsDuplicate`, `RowIDByTelegramMessageID`, `DeleteMessage`, `DeleteChatHistory`, `LogEvent`, `CountEvents`, `RecentEvents` | Same on `Repo`. `IsDuplicate` relies on the partial unique index `uq_messages_chat_tg_id` — recreate it with the same name and predicate in PG | ✅ |
| `ChatModel`/`SetChatModel`/`ClearChatModel`, `ChatStreaming`/`SetChatStreaming`/`ClearChatStreaming`, `ChatPersona`/`SetChatPersona`/`ClearChatPersona`, `ChatMiniAppFallback`/`Set`/`Reset`, `ChatLanguage`/`Set`/`Reset` | Five typed groups on `Repo`. **Do not** collapse them into a generic key/value settings table in PG: that changes the schema and loses the PK/CHECK guarantees | ✅ |
| `SetMessageFeedback`, `MessageFeedbackFor`, `MessageFeedbackCounts`, `LastAssistantMessageID` (`feedback.go`) | Same on `Repo` | ✅ |
| `ResumeSkip`/`SetResumeSkip`/`ClearResumeSkip`, `ResumeRecovery`/`SetResumeRecovery`/`ClearResumeRecovery` | Same on `Repo` (they are `bot_state` keys) | ✅ |
| `AdminStats` (`admin.go`) | `Repo.AdminStats`; `COUNT(*) FILTER (WHERE …)` is portable | ✅ |
| `Prune`, `PrunePreview` (`prune.go:67-180`) | `Repo.Prune(ctx, opts, apply bool)`. **Behaviour changes:** SQLite deletes all seven classes in one immediate transaction; PG must batch (e.g. 5–10k rows per `DELETE … WHERE id IN (SELECT … LIMIT n)`) to bound WAL, bloat and lock time. Counts-before-delete stay part of the report | ⚠️ |
| `Vacuum`, `CheckpointWAL` (`prune.go:184-203`) | SQLite file maintenance. PG: autovacuum; `Repo.Maintain(ctx)` is a no-op or `ANALYZE` in the PG implementation. No `VACUUM INTO`, no `wal_checkpoint(TRUNCATE)` | ❌ |
| `Backup`, `VerifyBackup`, `verifyForeignKeyCheck`, `verifyRequiredSchemaObjects`, `verifySearchIndexes` (`backup.go`) | SQLite file operations (`VACUUM INTO`, `sqlite_master`, `PRAGMA integrity_check`/`foreign_key_check`, FTS5 `integrity-check`). PG backup is an operational path (`pg_dump -Fc`, `pg_basebackup`, PITR) outside the Go process. `cmd/dbbackup` stays SQLite-only; a new ops path appears in Phase 2/4 | ❌ |
| `OpenReadOnly`, `ReadOnlyDSN`, `OpenReadOnlySnapshot`, `ReadOnlyImmutableDSN`, `WALSideFile`, `WALSideFilePresent`, `TableNames`, `IntegrityCheck` (`inspect.go`) | SQLite file/read-only semantics. PG: a read-only role or `SET TRANSACTION READ ONLY`, `pg_amcheck` for integrity | ❌ |
| `TableCounts`, `EventCounts`, `StateEntries`, `ListChats`, `RecentMessages` (`inspect.go`) | A `Diagnostics` sub-interface on `Repo`, used by `cmd/dbinspect` and `/admin`. `TableCounts` returns domain rows, not `sqlite_master` names | ✅ |
| `ContentFingerprint` + `fingerprint*` helpers (`fingerprint.go`) | Portable over `messages`, `events`, `rag_chunks`, `rag_chunk_embeddings`, but `tablePresence` reads `sqlite_master`. Keep SQLite-only; define a PG equivalent with the same 4-table contract for migration verification | ⚠️ |
| `RegisterMetrics` (`metrics.go`) | Not data access (reads `DB_PATH` and `schema_version` for Prometheus). Untouched by the Repo | ❌ |
| **Per-chat lock** (`internal/ratelimit/ratelimit.go:93-190`) — not in `store` | **Cannot be abstracted by `Repo`.** It is an in-process map. A second writer needs `pg_advisory_xact_lock(hashtext(concat('chat:', $1)))` or a `chat_locks` row, and that must be designed with T1, not with the store | ❌ |

### 3.4 What stays dialect-specific — and why

| Concern | SQLite today | Postgres tomorrow | Why it cannot be hidden |
|---|---|---|---|
| Full-text search | FTS5 external-content tables (`messages_fts`, `rag_chunks_fts`), `unicode61`, prefix queries, `bm25()` for RAG candidates | `tsvector` generated column + GIN, `russian`/`english` config, `ts_rank_cd`; **core Postgres has no BM25** | The grammar, the tokenizer and the ranking are different; the Repo can only expose the use case |
| Embeddings | `BLOB` of float32 LE, full scan of the newest 5000 per chat+model, cosine in Go | `bytea` + exact `ORDER BY vector <-> $1` (identical results), or `vector` + HNSW/IVFFlat (approximate, different results) | Encoding, scan plan and approximation are implementation choices |
| Transactions | `_txlock=immediate`: every transaction takes the write lock | Read Committed by default; `SELECT … FOR UPDATE`, `SKIP LOCKED`, `SERIALIZABLE` per use case | The isolation strategy is the implementation's, never the interface's |
| Migrations | Embedded SQL, lexical splitter, `BEGIN IMMEDIATE`, `schema_migrations`, `pragma_table_info` guard for 011 | Own embedded history, advisory lock around `Migrate`, own version table | The SQL is not portable and the histories must not be forced to match |
| File lifecycle | `VACUUM INTO`, `wal_checkpoint(TRUNCATE)`, `-wal`/`-shm`, `journal_size_limit`, `wal_autocheckpoint` | `pg_dump`/PITR, autovacuum, no side files | File-level vs server-level operations |
| Timestamps | `TEXT` in two formats: `YYYY-MM-DD HH:MM:SS` (`pruneTimestamp`) and RFC3339Nano (`formatReminderTime`) | `timestamptz` | Type change + a data conversion; string comparison currently works only because the format is fixed |
| SQL surface | `?` placeholders, `INSERT OR IGNORE`, `AUTOINCREMENT`, `rowid`, `sqlite_master`, `datetime('now')`, `PRAGMA` | `$1`, `ON CONFLICT DO NOTHING`, `GENERATED … AS IDENTITY`, `now()`, `information_schema` | Mechanical but pervasive; a hard rule in Phase 1/2: no SQL outside the implementation |
| Error strings as control flow | `ragCandidateExpressionRejected` matches "no such table: rag_chunks_fts", "fts5", "unterminated string" (`rag.go:545-554`) | Different driver, different strings | Must become typed errors (`ErrIndexUnavailable`, `ErrBadQuery`) before Phase 2 |
| Pool sizing | `MaxOpenConns=4` because SQLite serialises writers | `pgxpool` / PgBouncer, sized by concurrency | Different failure modes (queue wait vs connection exhaustion) |

### 3.5 The contract both implementations must satisfy

These are the invariants the parity harness (§4, Phase 3) checks; they are not negotiable per dialect:

- **Tenancy:** `chat_id` is the tenant key in every chat-scoped row; a query never returns another chat's row
  (the RAG code repeats the filter even for ids that came from a chat-scoped table — `rag.go:900-914`).
- **Ordering:** `messages.id` is monotonic per database; history is ascending by `id`, search returns
  newest-first internally and the caller reverses it (`history.go:175-178`).
- **Idempotency:** partial unique index `uq_messages_chat_tg_id` on
  `(chat_id, telegram_message_id) WHERE telegram_message_id IS NOT NULL`.
- **Claims:** token + lease, exactly one claimant; a crashed claimant is reclaimed after `lease_until`.
- **Quota:** reserve before the model call, release on failure, never double-reserve; the daily row is
  `(user_id, day, class)` and `used > 0`.
- **Embedding identity:** unique `(chunk_id, model)`; the first write fixes the dimension; a vector of another
  model or another length is never scored.
- **Schema:** monotonic version; a database newer than the binary is refused at start (`ErrSchemaDowngrade`).
- **Time:** everything is UTC; the wire formats above round-trip.
- **Limits:** `RAGDocumentLimit=20`, `RAGChunkRunes=1000`, `RAGChunkOverlap=100`, `ragCandidateLimit=300`,
  `ragVectorScanLimit=5000`, `ragVectorCandidateLimit=64`, `ragHybridMinimumScore=0.15`,
  `DefaultEmbeddingWeight=0.4`, `messageSearchMaxTerms=8`, `RAGQueryMaxTerms=32`.

---

## 4. Migration and rollout plan

The phases below are the plan **after** a trigger fires. Phase 0 is the only thing this record authorises
today. Effort is one engineer, in working days, and is an estimate, not a measurement.

| Phase | Work | Acceptance criteria | Estimate |
|---|---|---|---|
| **0. Trigger record** | Write down which trigger fired, with the measurement, the owner and the decision date. Collect a baseline: `SQLITE_BUSY`/`busy_timeout` counts, write p95, DB size, startup time, backup age | A one-page record attached to the roadmap row; the owner signs off; no code changes | 1–2 d |
| **1. `store.Repo`, SQLite only** | Define `Repo` from the call sites; add `sqliteRepo` delegating to the existing functions; thread it through `main.go`, `internal/bot` (`Deps.DB`), `internal/webapp`, `cmd/*`. No SQL, schema, DSN or behaviour change | `go build ./...`, `go vet ./...`, full `go test -race -shuffle=on ./...` green; no test deleted or skipped; `schema_migrations` head stays 19; `scripts/db_inspect.ps1 -Counts` on the 2.0 MiB backup unchanged; `git diff` contains no SQL text | 5–7 d |
| **2. Postgres implementation** | Translate 19 migrations into a PG history (22 tables, partial unique indexes, `timestamptz`, `tsvector` + GIN, `bytea` embeddings, dimension registry); `postgres.Open` with `pgxpool`; advisory-lock `Migrate`; batch `Prune`; advisory locks for claims | Store suite runs against PG (`STORE_DRIVER=postgres`); migrations idempotent from an empty database; `EXPLAIN (ANALYZE, BUFFERS)` shows index use for history, search, RAG candidates and prune; 8 concurrent workers × 100 reminders claim each exactly once; no `?`, `datetime('now')` or `sqlite_master` left in `internal/store/postgres` | 5–7 d |
| **3. Parity harness** | Golden dataset from a real 2.0 MiB backup (anonymised); contract tests against both drivers; RAG quality gate; timestamp round-trip; embedding top-k; FTS recall; dual **shadow reads** (read-only, no dual writes) | Per-table counts and fingerprint equal; `rag_quality_test.go` gates hold on PG (recall@3 ≥ 0.95, MRR@5 ≥ 0.85); top-5 overlap ≥ 4/5 per query on the golden set; no-answer queries still empty; exact kNN matches the Go float32 path within 1e-6; UTC round-trip preserves order | 3–4 d |
| **4. Offline migration + rehearsal** | `cmd/dbmigrate` (read SQLite read-only, write PG in batches, `-dry-run`, idempotent `-apply`), verification report, PG backup/restore drill, rollback rehearsal | Dry-run matches per-table counts; `-apply` re-run inserts nothing; 0 orphan FKs; `pg_dump -Fc` → restore into scratch → counts match; rollback restores the SQLite file and the bot starts, measured < 30 min | 3–4 d |
| **5. Cutover + observe** | Stop bot → final delta → start on PG → keep the SQLite file read-only for 7 days; PG backup schedule and alerts | 7 days with no error-rate increase, write p95 ≤ baseline + 20 %, no lost or duplicated message (reconciliation query), daily backup verified, PG-down alert fires | 2–3 d + 7 d observation |

**Total: 19–27 working days (≈ 4–6 weeks) for a production cutover.** The roadmap's "2–3 weeks" is realistic
only for Phases 1–2, i.e. "the suite is green on Postgres"; it does not include parity, migration, rehearsal
and observation. Write that distinction into the roadmap row when the trigger fires.

### 4.1 Dual-write?

**No, not as the default.** Dual-write creates two sources of truth exactly when the reason for the migration
is "two writers are hard", and it needs an outbox, idempotent replay and a reconciliation job — more work than
the migration itself. The plan uses:

- **shadow reads** in Phase 3 (read-only comparison against a snapshot restored into PG, no writes);
- **offline batch migration + a short stop-the-world delta** in Phases 4–5 (the bot is a personal assistant;
  a few minutes of downtime is acceptable; the current `stop_grace_period: 60s` and measured shutdown ≈ 11 s
  already set the order of magnitude — `README.md:576-589`).

A per-chat cutover (both stores live, a router in front) is explicitly not chosen: it doubles the failure
modes and gives no benefit for a single-owner bot. Revisit only if a multi-tenant migration needs a long
dual-run.

### 4.2 Backup and restore path

- **Today:** `VACUUM INTO` on a read-only handle (`backup.go:15-51`), verification via
  `integrity_check`/`foreign_key_check`/FTS5 `integrity-check` (`backup.go:54-111`), nightly copy + pre-prune,
  RPO ≈ 24 h, RTO minutes, 2.0 MiB file (`README.md:576-607`).
- **Postgres:** the RPO/RTO story changes to WAL archiving + PITR (managed) or `pg_dump -Fc` on a schedule
  (self-hosted). This must be decided with §2.2 before Phase 2. The existing `restore-drill` job
  (`.gitlab-ci.yml`, `scripts/restore-drill.sh`) is the template for a PG drill: restore into a scratch
  database, compare `table_*` counts, `schema_version` and a content fingerprint, then check read-write.
- **Do not cut over without a passing PG restore drill.** A backup that was never restored is not a backup,
  and the 2.0 MiB file is currently the only copy that has been drilled.

### 4.3 Test matrix

| Level | What it proves | Where |
|---|---|---|
| Contract | Both implementations satisfy §3.5 identically | new `internal/store/contract` suite, run against `sqlite` and `postgres` |
| Unit | Existing 42 store test files still pass on SQLite; the domain functions (scorer, chunking, tokens) are untouched | `internal/store/*_test.go` |
| PG integration | Every store function against a real PG, not a mock | `internal/store/postgres` tests + `STORE_DRIVER=postgres` |
| Golden data | Search/RAG parity on a real snapshot | Phase 3 harness, `rag_quality_test.go` gates |
| Concurrency | Exactly-once claims, quota, migration lock | new property tests (8–32 workers) |
| Migration/rollback | Counts + fingerprint, restore < 30 min | `cmd/dbmigrate` + `restore-drill` |
| End-to-end | Telegram fake + Mini App against PG | `internal/bot`, `internal/webapp` integration tests |

CI: a PG service (compose or testcontainer) becomes a new job; keep it out of the default fast
`scripts/verify.ps1` path until Phase 3, run it nightly and as a release gate. The existing `docker-race` job
already pays the container cost; measure the added wall-clock and set a budget before merging (R6).

### 4.4 Rollback

1. **Before cutover:** the SQLite file is the source of truth; PG is a verified copy. Rollback is "delete the
   PG copy".
2. **At cutover:** keep the pre-cutover file (`data/backups/pre-v1.60-…` pattern) and stop the bot before the
   final delta. The 2.0 MiB file restores in minutes (RTO — `README.md:595`); the measured manual restore path is
   in `README.md:638-658`.
3. **After writes on PG:** a rollback loses them unless a reverse sync exists. Decide explicitly: either
   accept the loss during a short window (recommended: 24 h, with the owner's sign-off, since the bot can
   re-ask), or do not cut over until a reverse path is testable. Do not pretend dual-write makes this free.
4. **Acceptance:** the rollback is rehearsed in Phase 4 and measured; if it cannot be done in < 30 min, the
   cutover does not happen.

---

## 5. Risks and mitigations

| # | Risk | Why it is real here | Mitigation |
|---|---|---|---|
| **R1** | **Transaction/locking semantics change** | SQLite's `_txlock=immediate` makes every transaction a write-lock request, which accidentally serialises read-modify-write. PG Read Committed does not. The quota reserve (`quota.go:16-38`), the embedding dimension registry (`embeddings.go:103-163`), the reminder/task claim (`reminders.go:286`, `tasks.go:211`) and `CompleteTask`'s message insert (`tasks.go:334-390`) all depend on that serialisation | Port each read-modify-write to an explicit mechanism (`ON CONFLICT … RETURNING`, `SELECT … FOR UPDATE`, `SKIP LOCKED`, advisory lock); keep the claim-token + lease pattern; property tests with 8–32 workers; review every `BeginTx` site (11 non-test call sites in `internal/store`) one by one |
| **R2** | **FTS ranking parity** | FTS5 `bm25()` + `unicode61` + prefix queries vs `tsvector`/`ts_rank_cd`; core PG has no BM25. `ragCandidateExpressionRejected` matches driver error strings; `ragSearchExpression` relies on FTS5 prefix semantics | Define parity by outcome, not by score: recall@3 ≥ 0.95, MRR@5 ≥ 0.85 on `rag_quality_test.go`, top-5 overlap ≥ 4/5 on a golden set. Keep the Go scorer. Do not add `paradedb`/`pg_search` without a separate ADR and a measured win |
| **R3** | **Embedding search parity** | BLOB + full scan + Go cosine is exact; `pgvector` HNSW is approximate and changes top-k. Dimension/model filtering is currently done in Go after reading rows | Phase 3 uses `bytea` + exact `ORDER BY vector <-> $1` (numerically identical). Introduce `vector`/ANN only behind a new ADR with a recall measurement; never mix models in one score space |
| **R4** | **Backup / RPO regression** | `VACUUM INTO` on a 2.0 MiB file is simple and drilled; a self-hosted PG needs WAL archiving, a restore drill and a second service to monitor. RPO could silently get worse | Decide managed vs self-hosted first; require a passing PG restore drill before cutover; keep the SQLite snapshot during the first week; alert on backup age (the existing backup-age alert pattern) |
| **R5** | **Operational complexity** | A second service on a home host: credentials, upgrades, `pg_hba`, disk, connection limits, monitoring. The bot currently has one file and one container | Pin the image, add a healthcheck and the same alerting; name an owner (§2.2); prefer managed if the owner does not want to run a database |
| **R6** | **Test time and matrix** | 42 store test files / 8,708 lines currently run against in-memory SQLite; a PG instance per package multiplies runtime and adds a CI dependency. The `-race` job is already the slow one | Build-tagged `postgres` tests, one shared PG per package with a per-run schema, skip in `-short`; keep PG out of the PR fast path until Phase 3; measure wall-clock before/after and set a budget |
| **R7** | **Dual-write consistency** (if chosen despite §4.1) | Partial failure leaves one store behind; ordering and idempotency are hard | Do not dual-write. If forced: outbox + monotonic ids + a reconciliation query, and a named owner for the reconciliation |
| **R8** | **Type and timezone drift** | Two TEXT timestamp formats today; `LIKE`/collation and Unicode differ; `NULL` and `COALESCE` semantics are close but not identical | Convert to `timestamptz` with an explicit parser per format; round-trip tests; keep UTC everywhere; a conversion table in the migration tool |
| **R9** | **The per-chat lock is not in the store** | A second replica would still have no cross-process mutual exclusion, so "we moved to Postgres" would not deliver the promised concurrency | Make the distributed lock a T1 deliverable (advisory lock / lease row), designed and tested with the second writer, not after it |
| **R10** | **Scope creep into a rewrite** | 145 exported functions and 266 call sites invite "while we're here" changes to the scorer, settings schema or prune semantics | Freeze behaviour in Phase 1 (no SQL change); the only allowed semantic change is the prune batching (⚠️) and it has its own acceptance test |

---

## 6. Cost/benefit and recommendation

### 6.1 Cost

- **Engineering:** 19–27 working days for a production cutover (§4), against a roadmap estimate of 2–3 weeks
  that covers only the interface + a green PG suite.
- **Refactor surface:** 145 exported functions, 266 `store.Func(` call sites in 33 non-test files outside the
  package, 169 `deps.DB` references, 42 test files / 8,708 lines.
- **New operational surface:** a database service, credentials/secrets, a backup/PITR path, monitoring,
  upgrades, connection limits — on a single home host that currently runs one container and one file.
- **New failure modes:** two stores during migration, a distributed lock, FTS ranking divergence, timestamp
  conversion, a second CI dependency.
- **Opportunity cost:** the same 4–6 weeks are the Sprint 7 budget for provider routing, multitenancy or
  group-mode polish — all of which have a user-visible payoff; the store migration has none at the current
  scale.

### 6.2 Benefit

- **At the current scale: zero measurable benefit.** One owner, one replica, 238 messages at the last content
  measurement, 2.0 MiB, one writer. Postgres buys concurrent writers, PITR and remote access — none of which
  is used or required today.
- **Under a trigger:** T1 (a real second writer) cannot be served by SQLite at all; T2 (HA/RPO) cannot be
  served by a file copy. That is the entire value, and it is conditional.
- **Secondary:** a cleaner `Repo` boundary would make the store testable against a fake and would isolate the
  dialect. That is a real but small benefit, and it can be captured later at the same cost — the extraction
  does not get cheaper or more expensive by waiting, while the target dialect may change (pgvector,
  paradedb, managed vs self-hosted).

### 6.3 Recommendation

**Do not start. Keep SQLite. Keep this ADR as the definition of ready.** The current store is hardened for
the one-writer model (the review's race classes are fixed), the data is 2.0 MiB, and every benefit of Postgres
is conditional on a trigger that has not fired. The cheapest useful action is Phase 0 only: write the trigger
down and make it measurable, so the next discussion is about a number, not about taste.

### 6.4 "Do not start until …"

1. **A trigger from §1.3 has fired and is written down** with a measurement (T1: a named second writer and
   its write paths; T2: an RPO/RTO number; T3: `SQLITE_BUSY`/write-p95 data over ≥ 7 days; T5: a named
   consumer), an owner and a date.
2. **The per-chat lock has a cross-process design.** `internal/ratelimit.Locks` is an in-process map; the plan
   must name the mechanism (PG advisory lock or a lease row), its failure mode and its test, before the store
   work starts.
3. **A second writer is actually funded**, including Telegram long-polling leader election. Postgres does not
   make `getUpdates` safe for two pollers.
4. **The managed-vs-self-hosted and hosting decisions are made** (§2.2), and someone owns the second service:
   upgrades, credentials, monitoring, backups.
5. **The backup/RPO target is written down and drilled for Postgres** — a restore into a scratch database with
   counts, schema version and fingerprint compared, in < 30 min.
6. **The FTS/embedding parity gate is agreed**: `rag_quality_test.go` thresholds (recall@3 ≥ 0.95,
   MRR@5 ≥ 0.85) plus a golden set from a real backup, and everyone accepts that core Postgres has no BM25.
7. **The migration window and rollback are agreed**: bot stopped, 2.0 MiB SQLite file restorable in < 30 min,
   a named owner, and an explicit decision about writes lost if the rollback happens after the cutover.
8. **The test budget is agreed**: a PG service in CI, the wall-clock delta measured, and the `-race` job
   staying within its cap.
9. **The scope is frozen**: Phase 1 changes no SQL, no schema and no DSN. The only allowed semantic change is
   prune batching (⚠️ in §3.3), with its own test.

Until all of the above for the specific trigger that fired: **no `store.Repo`, no Postgres, no schema change.**

---

## 7. References

- [ADR-0001 — SQLite + WAL as the only store, one writer process](adr/0001-sqlite-single-writer.md) (the
  decision this record extends; its "revisit when" clause is §1.3/T1).
- [ADR-0003 — Fail fast on any configuration problem](adr/0003-fail-fast-configuration.md) (a Postgres DSN is a
  new config row and a new secret).
- [ADR-0004 — FTS5 external-content indexes, built at open time](adr/0004-search-indexes.md) (affirmed until a
  trigger fires).
- `docs/CODE_REVIEW_2026-11-08.md:94` (migration race), `:95` (quota), `:236` (the `store.Repo` recommendation),
  `:362` (measured snapshot).
- `docs/ROADMAP.md:112-120` (Sprint 7, the row this record defines).
- `README.md:532-607` (SQLite maintenance, backup, restore, RPO/RTO), `README.md:638-658` (manual restore).
- `internal/store/store.go`, `migrations.go`, `fts.go`, `rag.go`, `embeddings.go`, `backup.go`, `prune.go`,
  `dsn.go`, `quota.go`, `reminders.go`, `tasks.go`.
