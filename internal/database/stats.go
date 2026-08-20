package database

import (
	"context"
	"fmt"
	"strings"
)

// TableStat is one table's row count and on-disk size (including its
// indexes).
type TableStat struct {
	Name      string `json:"name"`
	RowCount  int64  `json:"row_count"`
	SizeBytes int64  `json:"size_bytes"`
}

// TableStats reports row count and size for every user table in the
// database, ordered by name. Size is read from the sqlite dbstat virtual
// table and attributed to the owning table (an index's pages count toward
// the table they index, via sqlite_master.tbl_name).
func (db *DB) TableStats(ctx context.Context) ([]TableStat, error) {
	sizeRows, err := db.sql.QueryContext(ctx, `
		SELECT COALESCE(m.tbl_name, d.name), SUM(d.pgsize)
		FROM dbstat d
		LEFT JOIN sqlite_master m ON m.name = d.name
		WHERE d.name NOT LIKE 'sqlite_%'
		GROUP BY 1`)
	if err != nil {
		return nil, fmt.Errorf("query dbstat: %w", err)
	}
	defer sizeRows.Close()

	sizes := make(map[string]int64)
	for sizeRows.Next() {
		var name string
		var size int64
		if err := sizeRows.Scan(&name, &size); err != nil {
			return nil, fmt.Errorf("scan dbstat row: %w", err)
		}
		sizes[name] = size
	}
	if err := sizeRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dbstat rows: %w", err)
	}

	nameRows, err := db.sql.QueryContext(ctx, `
		SELECT name FROM sqlite_master
		WHERE type = 'table' AND name NOT LIKE 'sqlite_%'
		ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("query sqlite_master: %w", err)
	}
	defer nameRows.Close()

	var names []string
	for nameRows.Next() {
		var name string
		if err := nameRows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan table name: %w", err)
		}
		names = append(names, name)
	}
	if err := nameRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate table names: %w", err)
	}

	stats := make([]TableStat, 0, len(names))
	for _, name := range names {
		quoted := `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
		var count int64
		if err := db.sql.QueryRowContext(ctx, "SELECT COUNT(*) FROM "+quoted).Scan(&count); err != nil {
			return nil, fmt.Errorf("count %s: %w", name, err)
		}
		stats = append(stats, TableStat{Name: name, RowCount: count, SizeBytes: sizes[name]})
	}

	return stats, nil
}
