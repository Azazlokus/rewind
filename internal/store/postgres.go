package store

import (
	"context"
	"database/sql"
	"fmt"

	_ "github.com/jackc/pgx/v5/stdlib" // драйвер PostgreSQL через database/sql, имя "pgx"
)

// OpenPostgres открывает PostgreSQL-хранилище (dsn — строка подключения pgx/libpq),
// проверяет связь, применяет миграции и возвращает Store. Для prod.
func OpenPostgres(ctx context.Context, dsn string) (Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open postgres: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: ping postgres: %w", err)
	}
	migs, err := loadMigrations(postgresMigrations, "migrations/postgres")
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := applyMigrations(ctx, db, migs); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &sqlStore{db: db, d: dialect{name: "postgres"}}, nil
}
