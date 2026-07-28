package store

import (
	"context"
	"fmt"
)

// Open выбирает реализацию Store по имени драйвера ("sqlite" или "postgres") и dsn.
// Единая точка входа для wiring в cmd/server.
func Open(ctx context.Context, driver, dsn string) (Store, error) {
	switch driver {
	case "sqlite", "sqlite3":
		return OpenSQLite(ctx, dsn)
	case "postgres", "postgresql", "pgx":
		return OpenPostgres(ctx, dsn)
	default:
		return nil, fmt.Errorf("store: unknown driver %q (want sqlite or postgres)", driver)
	}
}
