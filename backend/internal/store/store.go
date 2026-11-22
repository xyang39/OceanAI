package store

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

//go:embed bootstrap.sql
var bootstrapSQL string

//go:embed migration_v2.sql
var migrationV2 string

// Bootstrap applies the initial schema and demo data exactly once, atomically.
// The lock also makes simultaneous application startup safe.
func Bootstrap(ctx context.Context, pool *pgxpool.Pool) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(82411801)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now()
	)`); err != nil {
		return err
	}
	for i, sql := range []string{bootstrapSQL, migrationV2} {
		version := i + 1
		var exists bool
		if err = tx.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version = $1)", version).Scan(&exists); err != nil {
			return err
		}
		if exists {
			continue
		}
		if _, err = tx.Exec(ctx, sql); err != nil {
			return fmt.Errorf("migration %d: %w", version, err)
		}
		if _, err = tx.Exec(ctx, "INSERT INTO schema_migrations (version) VALUES ($1)", version); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}
