// Package migrations holds the goose SQL migrations of the Postgres schema,
// embedded into the binary, and builds the goose provider that runs them.
// Files are timestamp-numbered; never edit a migration once it is merged.
//
// Import boundaries: goose and the standard library. Only cmd/migrate and
// tests use it.
package migrations

import (
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
)

//go:embed *.sql
var files embed.FS

// NewProvider returns a goose provider that applies the embedded migrations to
// db, a Postgres database.
func NewProvider(db *sql.DB) (*goose.Provider, error) {
	p, err := goose.NewProvider(goose.DialectPostgres, db, files)
	if err != nil {
		return nil, fmt.Errorf("new goose provider: %w", err)
	}
	return p, nil
}
