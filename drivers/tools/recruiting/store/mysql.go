package store

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
)

const DriverName = "mysql"

// Open validates security-sensitive DSN properties before creating the pool.
// It never returns or logs the DSN.
func Open(dsn string) (*sql.DB, error) {
	config, err := mysql.ParseDSN(strings.TrimSpace(dsn))
	if err != nil {
		return nil, fmt.Errorf("parse recruiting MySQL configuration: %w", err)
	}
	if strings.EqualFold(config.User, "root") || strings.TrimSpace(config.User) == "" {
		return nil, fmt.Errorf("recruiting MySQL requires an explicit non-root account")
	}
	if strings.TrimSpace(config.DBName) == "" {
		return nil, fmt.Errorf("recruiting MySQL requires an explicit database")
	}
	config.ParseTime = true
	config.Loc = time.UTC
	config.MultiStatements = false
	config.Params = cloneParams(config.Params)
	config.Params["time_zone"] = "'+00:00'"

	db, err := sql.Open(DriverName, config.FormatDSN())
	if err != nil {
		return nil, fmt.Errorf("open recruiting MySQL: %w", err)
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	return db, nil
}

func cloneParams(source map[string]string) map[string]string {
	result := make(map[string]string, len(source)+1)
	for key, value := range source {
		result[key] = value
	}
	return result
}
