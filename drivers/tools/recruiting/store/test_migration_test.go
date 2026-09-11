package store

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"
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
	// The MySQL contract package intentionally reuses one migrated schema for
	// speed. Every top-level contract must nevertheless see an isolated data
	// set: runnable Work, active permits, and ready Sources are global queues
	// by design and can otherwise make a later test select another test's
	// business facts. Cleanup runs even after t.Fatal and uses the migrator
	// identity only; production runtime code never receives DDL privileges.
	t.Cleanup(func() {
		resetRecruitingTestData(t, dsn)
	})
}

func resetRecruitingTestData(t *testing.T, migrationDSN string) {
	t.Helper()
	cleanupDB, err := Open(migrationDSN)
	if err != nil {
		t.Errorf("open migration database for test cleanup: %v", err)
		return
	}
	defer cleanupDB.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	conn, err := cleanupDB.Conn(ctx)
	if err != nil {
		t.Errorf("reserve migration connection for test cleanup: %v", err)
		return
	}
	defer conn.Close()
	rows, err := conn.QueryContext(ctx, `SELECT table_name FROM information_schema.tables
WHERE table_schema = DATABASE() AND table_name LIKE 'recruiting\\_%' ESCAPE '\\'
  AND table_name <> 'recruiting_schema_migrations' ORDER BY table_name`)
	if err != nil {
		t.Errorf("list recruiting tables for test cleanup: %v", err)
		return
	}
	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			_ = rows.Close()
			t.Errorf("read recruiting table for test cleanup: %v", err)
			return
		}
		tables = append(tables, table)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		t.Errorf("iterate recruiting tables for test cleanup: %v", err)
		return
	}
	if err := rows.Close(); err != nil {
		t.Errorf("close recruiting table list for test cleanup: %v", err)
		return
	}
	if _, err := conn.ExecContext(ctx, "SET FOREIGN_KEY_CHECKS = 0"); err != nil {
		t.Errorf("disable foreign key checks for test cleanup: %v", err)
		return
	}
	defer func() {
		restoreCtx, restoreCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer restoreCancel()
		if _, err := conn.ExecContext(restoreCtx, "SET FOREIGN_KEY_CHECKS = 1"); err != nil {
			t.Errorf("restore foreign key checks after test cleanup: %v", err)
		}
	}()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		t.Errorf("begin test cleanup: %v", err)
		return
	}
	for _, table := range tables {
		quoted := "`" + strings.ReplaceAll(table, "`", "``") + "`"
		if _, err := tx.ExecContext(ctx, "DELETE FROM "+quoted); err != nil {
			_ = tx.Rollback()
			t.Errorf("clear %s after contract: %v", table, err)
			return
		}
	}
	if err := tx.Commit(); err != nil {
		t.Errorf("commit test cleanup: %v", err)
		return
	}
	if len(tables) == 0 {
		t.Error("test cleanup found no recruiting tables")
	}
}
