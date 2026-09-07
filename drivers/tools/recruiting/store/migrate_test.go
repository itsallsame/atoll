package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"
)

func TestMigrationsAreVersionedAndStable(t *testing.T) {
	migrations, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) == 0 {
		t.Fatal("no recruiting migrations embedded")
	}
	for index, migration := range migrations {
		if migration.Version == 0 || len(migration.Checksum) != 64 || strings.TrimSpace(migration.SQL) == "" {
			t.Fatalf("invalid migration: %+v", migration)
		}
		if index > 0 && migrations[index-1].Version >= migration.Version {
			t.Fatal("migrations are not strictly ordered")
		}
	}
}

func TestOpenRejectsRootAndMissingDatabase(t *testing.T) {
	for _, dsn := range []string{
		"root:secret@tcp(127.0.0.1:3306)/recruiting",
		"staircase:secret@tcp(127.0.0.1:3306)/",
	} {
		if db, err := Open(dsn); err == nil {
			_ = db.Close()
			t.Fatalf("unsafe DSN accepted: %s", redactDSNForTest(dsn))
		}
	}
}

func TestMigrationIntegration(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_MIGRATION_TEST_DSN")
	if dsn == "" {
		dsn = os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	}
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		t.Fatal(err)
	}
	var currentUser string
	if err := db.QueryRowContext(ctx, "SELECT CURRENT_USER()").Scan(&currentUser); err != nil {
		t.Fatal(err)
	}
	if strings.HasPrefix(strings.ToLower(currentUser), "root@") {
		t.Fatalf("integration test connected as root: %s", currentUser)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err != nil {
		t.Fatalf("migration replay is not idempotent: %v", err)
	}
	var applied int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_schema_migrations WHERE migration_status = 'applied'").Scan(&applied); err != nil {
		t.Fatal(err)
	}
	migrations, _ := Migrations()
	if applied != len(migrations) {
		t.Fatalf("applied migrations=%d, embedded=%d", applied, len(migrations))
	}
	var tableCount int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM information_schema.tables WHERE table_schema = DATABASE()").Scan(&tableCount); err != nil {
		t.Fatal(err)
	}
	if tableCount < 25 {
		t.Fatalf("migration created only %d tables", tableCount)
	}
	assertRuntimeIndexes(t, ctx, db)

	migrations, _ = Migrations()
	if _, err := db.ExecContext(ctx, "UPDATE recruiting_schema_migrations SET checksum = REPEAT('0', 64) WHERE version = ?", migrations[0].Version); err != nil {
		t.Fatal(err)
	}
	if err := Migrate(ctx, db); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("changed migration checksum was not rejected: %v", err)
	}
	if _, err := db.ExecContext(ctx, "UPDATE recruiting_schema_migrations SET checksum = ? WHERE version = ?", migrations[0].Checksum, migrations[0].Version); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeIdentityHasDMLButNoDDL(t *testing.T) {
	if os.Getenv("RECRUITING_MYSQL_MIGRATION_TEST_DSN") == "" {
		t.Skip("split migration/runtime identities are not configured")
	}
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	migrateTestDatabase(t, ctx, db)
	var currentUser string
	if err := db.QueryRowContext(ctx, "SELECT CURRENT_USER()").Scan(&currentUser); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(currentUser, "staircase_runtime@") {
		t.Fatalf("repository tests are not using runtime identity: %s", currentUser)
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE recruiting_runtime_must_not_create(id INT)"); err == nil {
		t.Fatal("runtime identity unexpectedly has DDL permission")
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO recruiting_command_receipts(command_id, word_name, request_hash, response_bytes, committed_at)
VALUES ('runtime-permission-fixture', 'fixture', 'hash', '{}', UTC_TIMESTAMP(6))`); err != nil {
		t.Fatalf("runtime identity lacks required DML permission: %v", err)
	}
}

func assertRuntimeIndexes(t *testing.T, ctx context.Context, db *sql.DB) {
	t.Helper()
	for _, index := range []string{"ix_recruiting_work_runnable", "ix_recruiting_occurrence_due", "ix_recruiting_outbox_pending"} {
		var count int
		if err := db.QueryRowContext(ctx, `
SELECT COUNT(*) FROM information_schema.statistics
WHERE table_schema = DATABASE() AND index_name = ?`, index).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count == 0 {
			t.Fatalf("required runtime index %q is absent", index)
		}
	}
}

func redactDSNForTest(dsn string) string {
	if before, after, ok := strings.Cut(dsn, ":"); ok {
		if _, suffix, ok := strings.Cut(after, "@"); ok {
			return before + ":***@" + suffix
		}
	}
	return "***"
}
