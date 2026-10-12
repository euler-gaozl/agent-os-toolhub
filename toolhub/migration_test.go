package toolhub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"os"
	"strings"
	"testing"
	"time"
)

// Only TEST_DATABASE_URL is read. Tests create and drop an isolated fixture
// database and NOLOGIN role, never migrate the database named in that URL.
func fixtureDatabase(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not configured; PostgreSQL integration test not executed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal("fixture admin connection invalid")
	}
	t.Cleanup(admin.Close)
	var suffix [10]byte
	if _, err = rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	name := "p2_toolhub_" + hex.EncodeToString(suffix[:])
	role := name + "_app"
	if _, err = admin.Exec(ctx, "CREATE ROLE "+pgx.Identifier{role}.Sanitize()+" NOLOGIN"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, e := admin.Exec(c, "DROP ROLE "+pgx.Identifier{role}.Sanitize()); e != nil {
			t.Error(e)
		}
	})
	if _, err = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{name}.Sanitize()+" TEMPLATE template0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if _, e := admin.Exec(c, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); e != nil {
			t.Error(e)
		}
	})
	config := admin.Config().Copy()
	config.ConnConfig.Database = name
	config.MaxConns = 3
	config.ConnConfig.RuntimeParams["search_path"] = "toolhub,public"
	db, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db, role
}

func TestOwnedMigrationReadinessAndPermissions(t *testing.T) {
	db, role := fixtureDatabase(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if Readiness(ctx, db) == nil {
		t.Fatal("empty database was ready")
	}
	if _, err := db.Exec(ctx, "CREATE TABLE foreign_domain_sentinel (value text); INSERT INTO foreign_domain_sentinel VALUES('preserve')"); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db, "missing_p2_fixture_role"); err == nil {
		t.Fatal("unprovisioned role accepted")
	}
	for i := 0; i < 2; i++ {
		if err := Migrate(ctx, db, role); err != nil {
			t.Fatal(err)
		}
	}
	var value string
	if err := db.QueryRow(ctx, "SELECT value FROM foreign_domain_sentinel").Scan(&value); err != nil || value != "preserve" {
		t.Fatal("foreign data modified", err)
	}
	rows, err := db.Query(ctx, "SELECT tablename FROM pg_tables WHERE schemaname='toolhub' ORDER BY tablename")
	if err != nil {
		t.Fatal(err)
	}
	owned := map[string]bool{}
	for _, name := range []string{"assets", "asset_state", "asset_files", "asset_audit"} {
		owned[name] = true
	}
	count := 0
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		if !owned[name] {
			t.Fatalf("unexpected table %s", name)
		}
		count++
	}
	rows.Close()
	if err = rows.Err(); err != nil || count != len(owned) {
		t.Fatal("incomplete schema", err, count)
	}
	conn, err := db.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err = conn.Exec(ctx, "SET ROLE "+pgx.Identifier{role}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer conn.Exec(context.Background(), "RESET ROLE")
	for _, name := range []string{"assets", "asset_state", "asset_files", "asset_audit"} {
		for _, privilege := range []string{"SELECT", "INSERT", "UPDATE", "DELETE"} {
			var allowed bool
			if err = conn.QueryRow(ctx, "SELECT has_table_privilege(current_user,$1,$2)", name, privilege).Scan(&allowed); err != nil || !allowed {
				t.Fatal("owned permission missing", name, privilege, err)
			}
		}
	}
	if _, err = conn.Exec(ctx, "SELECT * FROM foreign_domain_sentinel"); err == nil || !strings.Contains(err.Error(), "42501") {
		t.Fatal("foreign table not denied", err)
	}
	if _, err = conn.Exec(ctx, "CREATE TABLE must_not_create(id int)"); err == nil {
		t.Fatal("runtime role has DDL")
	}
	if _, err = conn.Exec(ctx, readinessQuery); err != nil {
		t.Fatal("application identity readiness failed", err)
	}
	if _, err = conn.Exec(ctx, "RESET ROLE"); err != nil {
		t.Fatal(err)
	}
	if err = Readiness(ctx, db); err != nil {
		t.Fatal(err)
	}
}

func TestFailedDDLDoesNotLeavePartialServiceSchema(t *testing.T) {
	db, role := fixtureDatabase(t)
	saved := migrationSQL
	migrationSQL += "\nSELECT 1/0;"
	defer func() { migrationSQL = saved }()
	if err := Migrate(context.Background(), db, role); err == nil {
		t.Fatal("injected DDL failure ignored")
	}
	var count int
	if err := db.QueryRow(context.Background(), "SELECT count(*) FROM pg_tables WHERE schemaname='toolhub'").Scan(&count); err != nil || count != 0 {
		t.Fatal("failed migration left partial tables", err, count)
	}
}
