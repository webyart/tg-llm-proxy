// migrations.go — the schema of §5 «Model of data (SQLite)», compiled into the
// binary through go:embed (§13 risk «Migration does not get into the image»: in
// the container the file of the migration can be absent, the text of the schema
// lives in the binary). The text is split into statements by store.Statements:
// the sqlite driver rejects ';' inside one query (§5: "invalid semicolon
// separator in query"), so the schema cannot be Execed whole.
package store

// The blank import is what makes //go:embed legal: without it the build fails with
// «go:embed requires import "embed"» (verified on this toolchain).
import _ "embed"

//go:embed migrations/001_init.sql
var InitSQL string

// MigrationSQL — the migration set of §4 in the order of the file names. v1 has
// exactly one migration; versioned migrations are in the §15 backlog.
var MigrationSQL = []string{InitSQL}
