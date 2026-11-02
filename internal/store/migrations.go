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
)

//go:embed migrations/001_init.sql
var InitSQL string

//go:embed migrations/002_chat_settings.sql
var ChatSettingsSQL string

//go:embed migrations/003_chat_streaming.sql
var ChatStreamingSQL string

//go:embed migrations/004_chat_personas.sql
var ChatPersonasSQL string

// MigrationSQL is the ordered, append-only migration history. The slice index plus one is the permanent schema
// version recorded in schema_migrations; existing entries must never be edited after deployment.
var MigrationSQL = []string{InitSQL, ChatSettingsSQL, ChatStreamingSQL, ChatPersonasSQL}

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
