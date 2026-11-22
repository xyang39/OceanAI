package store

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// GenerateLarge creates an additive, repeatable fixture; normal startup never calls it.
func GenerateLarge(ctx context.Context, pool *pgxpool.Pool, tenant string, count int) error {
	if count < 1 || count > 1000000 {
		return fmt.Errorf("count must be between 1 and 1000000")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(context.Background())
	var actor string
	if err = tx.QueryRow(ctx, "SELECT email FROM admins WHERE tenant_id=$1 ORDER BY email LIMIT 1", tenant).Scan(&actor); err != nil {
		return fmt.Errorf("tenant admin: %w", err)
	}
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock(82411802)"); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO courses (id, tenant_id, title) VALUES ($1 || '-load', $1, 'Export fixture') ON CONFLICT DO NOTHING`, tenant); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO learners (id, tenant_id, name)
        SELECT $1 || '-load-' || lpad(n::text, 7, '0'), $1, 'Load Learner ' || lpad((n % 100)::text, 3, '0')
        FROM generate_series(1, $2::integer) n ON CONFLICT DO NOTHING`, tenant, count); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO enrollments (tenant_id, course_id, learner_id, completed_at)
        SELECT $1, $1 || '-load', $1 || '-load-' || lpad(n::text, 7, '0'), '2026-01-09T00:00:00Z'::timestamptz
        FROM generate_series(1, $2::integer) n ON CONFLICT DO NOTHING`, tenant, count); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `WITH inserted AS (
        INSERT INTO certificates (id, tenant_id, course_id, learner_id, serial_number, issued_at)
        SELECT $1 || '-load-cert-' || lpad(n::text, 7, '0'), $1, $1 || '-load',
            $1 || '-load-' || lpad(n::text, 7, '0'), upper($1) || '-LOAD-' || lpad(n::text, 7, '0'),
            '2026-01-10T13:30:00Z'::timestamptz + (n % 10) * interval '1 day'
        FROM generate_series(1, $2::integer) n ON CONFLICT DO NOTHING RETURNING id, tenant_id, issued_at
        ) INSERT INTO audit_events (tenant_id, certificate_id, actor_email, action, occurred_at)
        SELECT tenant_id, id, $3, 'ISSUED', issued_at FROM inserted`, tenant, count, actor); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
