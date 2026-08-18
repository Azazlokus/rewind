package store

import (
	"database/sql"

	"github.com/XSAM/otelsql"
	"go.opentelemetry.io/otel/attribute"
)

// openTraced открывает БД через otelsql (итер. 34): каждый SQL-запрос становится
// спаном, дочерним от контекста запроса (HTTP/join), — так в трейсе видна латентность
// обращений к БД. Драйвер должен быть зарегистрирован (blank-import в sqlite.go/
// postgres.go). При выключенной трассировке (глобальный no-op провайдер) спаны no-op,
// накладные пренебрежимо малы, а БД — не горячий игровой путь. system — значение
// атрибута db.system ("sqlite"/"postgresql").
func openTraced(driver, dsn, system string) (*sql.DB, error) {
	return otelsql.Open(driver, dsn, otelsql.WithAttributes(attribute.String("db.system", system)))
}
