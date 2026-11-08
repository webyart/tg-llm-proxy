// the container the file of the migration can be absent, the text of the schema
// lives in the binary). The text is split into statements by store.Statements:
// separator in query"), so the schema cannot be Execed whole.
package store

// The blank import is what makes //go:embed legal: without it the build fails with
// «go:embed requires import "embed"» (verified on this toolchain).
import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
)

//go:embed migrations/001_init.sql
var InitSQL string

//go:embed migrations/002_chat_settings.sql
var ChatSettingsSQL string

//go:embed migrations/003_chat_streaming.sql
var ChatStreamingSQL string

//go:embed migrations/004_chat_personas.sql
var ChatPersonasSQL string

//go:embed migrations/005_rag_documents.sql
var RAGDocumentsSQL string

//go:embed migrations/006_event_indexes.sql
var EventIndexesSQL string

//go:embed migrations/007_reminders.sql
var RemindersSQL string

//go:embed migrations/008_saved_links.sql
var SavedLinksSQL string

//go:embed migrations/009_tasks.sql
var TasksSQL string

//go:embed migrations/010_daily_quota_reservations.sql
var DailyQuotaReservationsSQL string

//go:embed migrations/011_message_user_attribution.sql
var MessageUserAttributionSQL string

//go:embed migrations/012_hot_path_indexes.sql
var HotPathIndexesSQL string

//go:embed migrations/013_message_search_index.sql
var MessageSearchIndexSQL string

//go:embed migrations/014_rag_search_index.sql
var RAGSearchIndexSQL string

//go:embed migrations/015_message_feedback.sql
var MessageFeedbackSQL string

//go:embed migrations/016_rag_chunk_embeddings.sql
var RAGChunkEmbeddingsSQL string

// MigrationSQL is the ordered, append-only migration history. The slice index plus one is the permanent schema
// version recorded in schema_migrations; existing entries must never be edited after deployment.
var MigrationSQL = []string{InitSQL, ChatSettingsSQL, ChatStreamingSQL, ChatPersonasSQL, RAGDocumentsSQL, EventIndexesSQL, RemindersSQL, SavedLinksSQL, TasksSQL, DailyQuotaReservationsSQL, MessageUserAttributionSQL, HotPathIndexesSQL, MessageSearchIndexSQL, RAGSearchIndexSQL, MessageFeedbackSQL, RAGChunkEmbeddingsSQL}

// SchemaVersion returns the newest applied migration, or zero for a pre-versioning backup.
func SchemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var present int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'schema_migrations'`).Scan(&present); err != nil {
		return 0, err
	}
	if present == 0 {
		return 0, nil
	}
	var version int
	if err := db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, err
	}
	return version, nil
}

// ErrSchemaDowngrade — the database carries a schema version this binary does not know.
var ErrSchemaDowngrade = errors.New("the database schema is newer than this binary")

// SupportedSchemaVersion — the newest migration compiled into this binary.
func SupportedSchemaVersion() int { return len(MigrationSQL) }

// GuardSchemaVersion refuses a database that a newer release already migrated.
//
// Without this check a rollback of the image silently ran against a schema it does not know: the extra
// columns are invisible to the older code, and the first INSERT that omits a NOT NULL column of the new
// schema fails at request time instead of at start time. The failure belongs to the start, where an
// operator can act on it — with the version numbers, in one line.
func GuardSchemaVersion(ctx context.Context, db *sql.DB) error {
	version, err := SchemaVersion(ctx, db)
	if err != nil {
		return fmt.Errorf("store: the schema version could not be read: %w", err)
	}
	if supported := len(MigrationSQL); version > supported {
		return fmt.Errorf("%w: the database is at version %d, this binary supports %d — a newer release migrated it; deploy that release or restore a backup it wrote", ErrSchemaDowngrade, version, supported)
	}
	return nil
}
