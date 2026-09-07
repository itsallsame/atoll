package store

import (
	"context"
	"database/sql"
	"os"
	"testing"
)

func migrateTestDatabase(t *testing.T, ctx context.Context, runtimeDB *sql.DB) {
	t.Helper()
	dsn := os.Getenv("RECRUITING_MYSQL_MIGRATION_TEST_DSN")
	if dsn == "" {
		if err := Migrate(ctx, runtimeDB); err != nil {
			t.Fatal(err)
		}
		return
	}
	migrationDB, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer migrationDB.Close()
	if err := Migrate(ctx, migrationDB); err != nil {
		t.Fatal(err)
	}
}
