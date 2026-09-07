package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

const migrationLockName = "atoll_recruiting_schema_migration_v1"

type Migration struct {
	Version  uint64
	Name     string
	Checksum string
	SQL      string
}

func Migrations() ([]Migration, error) {
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded recruiting migrations: %w", err)
	}
	result := make([]Migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || path.Ext(entry.Name()) != ".sql" {
			continue
		}
		versionText, _, found := strings.Cut(entry.Name(), "_")
		if !found {
			return nil, fmt.Errorf("migration %q has no version prefix", entry.Name())
		}
		version, err := strconv.ParseUint(versionText, 10, 64)
		if err != nil || version == 0 {
			return nil, fmt.Errorf("migration %q has invalid version", entry.Name())
		}
		content, err := migrationFiles.ReadFile(path.Join("migrations", entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		sum := sha256.Sum256(content)
		result = append(result, Migration{Version: version, Name: entry.Name(), Checksum: hex.EncodeToString(sum[:]), SQL: string(content)})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Version < result[j].Version })
	for index := 1; index < len(result); index++ {
		if result[index-1].Version == result[index].Version {
			return nil, fmt.Errorf("duplicate migration version %d", result[index].Version)
		}
	}
	return result, nil
}

func Migrate(ctx context.Context, db *sql.DB) error {
	if db == nil {
		return fmt.Errorf("recruiting MySQL database is required")
	}
	if _, err := db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS recruiting_schema_migrations (
  version BIGINT UNSIGNED NOT NULL,
  name VARCHAR(255) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  checksum CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  migration_status VARCHAR(16) CHARACTER SET ascii NOT NULL,
  started_at DATETIME(6) NOT NULL,
  completed_at DATETIME(6) NULL,
  PRIMARY KEY (version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci`); err != nil {
		return fmt.Errorf("create recruiting migration ledger: %w", err)
	}

	var locked int
	if err := db.QueryRowContext(ctx, "SELECT GET_LOCK(?, 30)", migrationLockName).Scan(&locked); err != nil || locked != 1 {
		return fmt.Errorf("acquire recruiting migration lock: result=%d: %w", locked, err)
	}
	defer func() {
		_, _ = db.ExecContext(context.WithoutCancel(ctx), "SELECT RELEASE_LOCK(?)", migrationLockName)
	}()

	migrations, err := Migrations()
	if err != nil {
		return err
	}
	for _, migration := range migrations {
		var checksum, status string
		err := db.QueryRowContext(ctx, "SELECT checksum, migration_status FROM recruiting_schema_migrations WHERE version = ?", migration.Version).Scan(&checksum, &status)
		switch {
		case err == nil:
			if checksum != migration.Checksum {
				return fmt.Errorf("recruiting migration %d checksum mismatch", migration.Version)
			}
			if status != "applied" {
				return fmt.Errorf("recruiting migration %d is %q; restore the pre-migration backup before retrying", migration.Version, status)
			}
			continue
		case err != sql.ErrNoRows:
			return fmt.Errorf("read recruiting migration %d: %w", migration.Version, err)
		}

		if _, err := db.ExecContext(ctx, `
INSERT INTO recruiting_schema_migrations(version, name, checksum, migration_status, started_at)
VALUES (?, ?, ?, 'applying', UTC_TIMESTAMP(6))`, migration.Version, migration.Name, migration.Checksum); err != nil {
			return fmt.Errorf("begin recruiting migration %d: %w", migration.Version, err)
		}
		for statementIndex, statement := range splitSQLStatements(migration.SQL) {
			if _, err := db.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply recruiting migration %d statement %d: %w", migration.Version, statementIndex+1, err)
			}
		}
		result, err := db.ExecContext(ctx, `
UPDATE recruiting_schema_migrations
SET migration_status = 'applied', completed_at = UTC_TIMESTAMP(6)
WHERE version = ? AND checksum = ? AND migration_status = 'applying'`, migration.Version, migration.Checksum)
		if err != nil {
			return fmt.Errorf("finish recruiting migration %d: %w", migration.Version, err)
		}
		changed, err := result.RowsAffected()
		if err != nil || changed != 1 {
			return fmt.Errorf("finish recruiting migration %d affected %d rows: %w", migration.Version, changed, err)
		}
	}
	return nil
}

func splitSQLStatements(source string) []string {
	parts := strings.Split(strings.ReplaceAll(source, "\r\n", "\n"), ";\n")
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		statement := strings.TrimSpace(part)
		if statement != "" {
			result = append(result, statement)
		}
	}
	return result
}
