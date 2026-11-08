package store

import (
	"os"

	"bot/internal/metrics"
)

// RegisterMetrics exposes the database size and the applied schema version. db_size_bytes is stat-ed at
// scrape time because SQLite grows under the process; a value measured once would describe the start of
// the run. A memory database, or a path that cannot be stat-ed, has no size to report and is skipped.
func RegisterMetrics(reg *metrics.Registry, path string, schemaVersion int) {
	if reg == nil {
		return
	}
	reg.Gauge("schema_version", "Highest applied migration version.").Set(nil, float64(schemaVersion))
	if IsMemory(path) {
		return
	}
	if _, err := os.Stat(path); err != nil {
		return
	}
	reg.GaugeFunc("db_size_bytes", "Size of the SQLite database file in bytes.", func() float64 {
		var info, statErr = os.Stat(path)
		if statErr != nil {
			return 0
		}
		return float64(info.Size())
	})
}
