package testsupport

import (
	"database/sql"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// OpenDatabase opens a test-only SQL bridge from an explicit disposable
// database URL. Production stores do not expose their connection pool.
func OpenDatabase(url string) (*sql.DB, error) {
	return sql.Open("pgx", url)
}
