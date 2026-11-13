// Package schema embeds and applies the Platform PostgreSQL migrations.
package schema

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
)

//go:embed *.sql
var migrations embed.FS

// Migrate serializes migration runs using a PostgreSQL advisory lock.
func Migrate(ctx context.Context, db *sql.DB) error {
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return fmt.Errorf("migration lock: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectPostgres, db, migrations,
		goose.WithSessionLocker(locker), goose.WithDisableGlobalRegistry(true),
		// Creating the platform schema can change PostgreSQL's default search
		// path when the database role is also named platform. Pin the history.
		goose.WithTableName("public.goose_db_version"))
	if err != nil {
		return fmt.Errorf("migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migrate PostgreSQL: %w", err)
	}
	return nil
}
