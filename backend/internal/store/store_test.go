package store

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrationPreservesV1Data(t *testing.T) {
	dsn := os.Getenv("ACCEPTANCE_DATABASE_URL")
	if dsn == "" {
		t.Skip("requires PostgreSQL acceptance database")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("upgrade_%d", time.Now().UnixNano())
	if _, err = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer admin.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	for _, sql := range []string{bootstrapSQL, `CREATE TABLE schema_migrations (version integer PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now()); INSERT INTO schema_migrations(version) VALUES (1); UPDATE learners SET name='Preserved name' WHERE id='east-alex'`} {
		if _, err = pool.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		if err = Bootstrap(ctx, pool); err != nil {
			t.Fatal(err)
		}
	}
	var name string
	if err = pool.QueryRow(ctx, "SELECT name FROM learners WHERE id='east-alex'").Scan(&name); err != nil || name != "Preserved name" {
		t.Fatalf("migration overwrote data: %s %v", name, err)
	}
	var migrations, certificates, audits int
	if err = pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM schema_migrations),(SELECT count(*) FROM certificates),(SELECT count(*) FROM audit_events WHERE tenant_id IS NOT NULL)`).Scan(&migrations, &certificates, &audits); err != nil {
		t.Fatal(err)
	}
	if migrations != 2 || certificates != 2 || audits != 2 {
		t.Fatalf("unexpected migration state %d %d %d", migrations, certificates, audits)
	}
	if _, err = pool.Exec(ctx, `INSERT INTO audit_events(tenant_id,certificate_id,actor_email,action,occurred_at) VALUES ('east','east-demo-certificate','bob@west.test','REVOKED',now())`); err == nil {
		t.Fatal("cross-tenant audit actor was accepted")
	}
}
