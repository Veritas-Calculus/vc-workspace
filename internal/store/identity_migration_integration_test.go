package store

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestIdentityMigrationPreservesMultipleExistingUserAssignments(t *testing.T) {
	databaseURL := os.Getenv("VC_WORKSPACE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("VC_WORKSPACE_TEST_DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	basePool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer basePool.Close()
	schema := fmt.Sprintf("vcw_identity_migration_%d", time.Now().UnixNano())
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := basePool.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_, _ = basePool.Exec(cleanup, "DROP SCHEMA "+identifier+" CASCADE")
	}()

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	legacyStore := &Store{pool: pool}
	defer pool.Close()
	if _, err := pool.Exec(ctx, `CREATE TABLE schema_migrations (name text PRIMARY KEY, applied_at timestamptz NOT NULL DEFAULT now())`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrations.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") && entry.Name() < "014_" {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		sql, readErr := migrations.ReadFile("migrations/" + name)
		if readErr != nil {
			t.Fatal(readErr)
		}
		tx, beginErr := pool.Begin(ctx)
		if beginErr != nil {
			t.Fatal(beginErr)
		}
		if _, execErr := tx.Exec(ctx, string(sql)); execErr != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("apply legacy migration %s: %v", name, execErr)
		}
		if _, execErr := tx.Exec(ctx, `INSERT INTO schema_migrations(name) VALUES($1)`, name); execErr != nil {
			_ = tx.Rollback(ctx)
			t.Fatal(execErr)
		}
		if commitErr := tx.Commit(ctx); commitErr != nil {
			t.Fatal(commitErr)
		}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO users(id,username,username_normalized,display_name,password_hash,role)
		VALUES ('migration-user-a','migration-a','migration-a','A','hash','user'),
		       ('migration-user-b','migration-b','migration-b','B','hash','user');
		INSERT INTO managed_desktops(vmid,display_name,node) VALUES (899999991,'Existing shared desktop','node-test');
		INSERT INTO user_desktop_assignments(user_id,desktop_vmid)
		VALUES ('migration-user-a',899999991),('migration-user-b',899999991)`); err != nil {
		t.Fatal(err)
	}
	if err := legacyStore.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var accessMode, owner, osFamily string
	if err := pool.QueryRow(ctx, `SELECT access_mode,COALESCE(owner_user_id,''),os_family FROM managed_desktops WHERE vmid=899999991`).Scan(&accessMode, &owner, &osFamily); err != nil {
		t.Fatal(err)
	}
	var assignmentCount int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM user_desktop_assignments WHERE desktop_vmid=899999991`).Scan(&assignmentCount); err != nil {
		t.Fatal(err)
	}
	if accessMode != "shared" || owner != "" || assignmentCount != 2 || osFamily != "unknown" {
		t.Fatalf("migration changed existing access: mode=%q owner=%q assignments=%d os_family=%q", accessMode, owner, assignmentCount, osFamily)
	}
}
