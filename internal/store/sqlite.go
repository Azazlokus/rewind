package store

import (
	"context"
	"fmt"

	_ "modernc.org/sqlite" // pure-Go драйвер SQLite (без cgo), имя "sqlite"
)

// OpenSQLite открывает SQLite-хранилище (dsn — путь к файлу или ":memory:"),
// применяет миграции и возвращает готовый Store. Для dev/CI/тестов.
func OpenSQLite(ctx context.Context, dsn string) (Store, error) {
	db, err := openTraced("sqlite", dsn, "sqlite")
	if err != nil {
		return nil, fmt.Errorf("store: open sqlite: %w", err)
	}
	// Один коннект: у ":memory:" каждое соединение — своя БД, а файловый WAL не любит
	// конкурентную запись через пул. Запись у нас редкая — сериализуем на одном.
	db.SetMaxOpenConns(1)
	if _, err := db.ExecContext(ctx, `PRAGMA foreign_keys = ON`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: sqlite pragma: %w", err)
	}
	migs, err := loadMigrations(sqliteMigrations, "migrations/sqlite")
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := applyMigrations(ctx, db, migs); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &sqlStore{db: db, d: dialect{name: "sqlite"}}, nil
}
