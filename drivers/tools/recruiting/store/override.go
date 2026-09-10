package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type OverrideHead struct {
	TargetID        string `json:"target_id"`
	Field           string `json:"field"`
	OverrideID      string `json:"override_id"`
	OverrideVersion uint64 `json:"override_version"`
	Active          bool   `json:"active"`
}

// ApplyOverrideCAS appends the immutable override version and moves the
// target-field head in one transaction. expected=nil means that no head may
// exist yet; replacing an existing head requires its exact identity/version.
func (r *Repository) ApplyOverrideCAS(ctx context.Context, expected *OverrideHead, override model.CuratedOverride, businessAt time.Time) error {
	if strings.TrimSpace(override.OverrideID) == "" || strings.TrimSpace(override.TargetID) == "" || strings.TrimSpace(override.Field) == "" ||
		strings.TrimSpace(override.ActorID) == "" || strings.TrimSpace(override.Reason) == "" || override.Version == 0 ||
		!json.Valid(override.Value) || bytes.Equal(bytes.TrimSpace(override.Value), []byte("null")) {
		return fmt.Errorf("complete override and valid JSON value are required")
	}
	if expected != nil && (expected.TargetID != override.TargetID || expected.Field != override.Field) {
		return fmt.Errorf("expected override head must address the same target field")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := applyOverrideTx(ctx, tx, expected, override, businessAt); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit override: %w", err)
	}
	return nil
}

func applyOverrideTx(ctx context.Context, tx *sql.Tx, expected *OverrideHead,
	override model.CuratedOverride, businessAt time.Time) error {
	current, exists, err := getOverrideHeadTx(ctx, tx, override.TargetID, override.Field)
	if err != nil {
		return err
	}
	if expected == nil && exists {
		return ErrOverrideConflict
	}
	if expected != nil && (!exists || current.OverrideID != expected.OverrideID || current.OverrideVersion != expected.OverrideVersion || current.Active != expected.Active) {
		return ErrOverrideConflict
	}
	if exists && current.OverrideID == override.OverrideID && override.Version != current.OverrideVersion+1 {
		return fmt.Errorf("same override must append exactly one version")
	}
	if exists && current.OverrideID == override.OverrideID && (!current.Active || override.Active) {
		return fmt.Errorf("existing override can only append its inactive clear version")
	}
	if exists && current.OverrideID != override.OverrideID && override.Version != 1 {
		return fmt.Errorf("replacement override must begin at version 1")
	}
	if (!exists || current.OverrideID != override.OverrideID) && (override.Version != 1 || !override.Active) {
		return fmt.Errorf("new override head must begin active at version 1")
	}

	_, err = tx.ExecContext(ctx, `
INSERT INTO recruiting_override_versions(
  override_id, override_version, target_id, field_name, actor_id,
  reason_text, active, value_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, override.OverrideID, override.Version,
		override.TargetID, override.Field, override.ActorID, override.Reason, override.Active, []byte(override.Value), businessAt.UTC())
	if err != nil {
		var mysqlError *mysql.MySQLError
		if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
			return ErrOverrideConflict
		}
		return fmt.Errorf("append override version: %w", err)
	}
	if !exists {
		_, err = tx.ExecContext(ctx, `
INSERT INTO recruiting_override_heads(target_id, field_name, override_id, override_version, active)
VALUES (?, ?, ?, ?, ?)`, override.TargetID, override.Field, override.OverrideID, override.Version, override.Active)
	} else {
		_, err = tx.ExecContext(ctx, `
UPDATE recruiting_override_heads
SET override_id = ?, override_version = ?, active = ?
WHERE target_id = ? AND field_name = ? AND override_id = ? AND override_version = ?`,
			override.OverrideID, override.Version, override.Active, override.TargetID, override.Field,
			current.OverrideID, current.OverrideVersion)
	}
	if err != nil {
		var mysqlError *mysql.MySQLError
		if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
			return ErrOverrideConflict
		}
		return fmt.Errorf("move override head: %w", err)
	}
	return nil
}

func (r *Repository) GetOverrideHead(ctx context.Context, targetID, field string) (OverrideHead, model.CuratedOverride, error) {
	row := r.db.QueryRowContext(ctx, `
SELECT h.override_id, h.override_version, h.active,
       v.actor_id, v.reason_text, v.value_json
FROM recruiting_override_heads h
JOIN recruiting_override_versions v
  ON v.override_id = h.override_id AND v.override_version = h.override_version
WHERE h.target_id = ? AND h.field_name = ?`, targetID, field)
	return scanOverrideHead(row, targetID, field)
}

func (r *Repository) ListOverrideHistory(ctx context.Context, targetID, field string, limit int) ([]model.CuratedOverride, error) {
	if targetID == "" || field == "" || limit <= 0 || limit > 500 {
		return nil, fmt.Errorf("override history requires target, field, and limit in [1,500]")
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT override_id, override_version, actor_id, reason_text, active, value_json
FROM recruiting_override_versions
WHERE target_id = ? AND field_name = ?
ORDER BY created_at, override_id, override_version
LIMIT ?`, targetID, field, limit)
	if err != nil {
		return nil, fmt.Errorf("list override history: %w", err)
	}
	defer rows.Close()
	var history []model.CuratedOverride
	for rows.Next() {
		var override model.CuratedOverride
		var value []byte
		if err := rows.Scan(&override.OverrideID, &override.Version, &override.ActorID, &override.Reason, &override.Active, &value); err != nil {
			return nil, err
		}
		override.TargetID, override.Field = targetID, field
		override.Value = append(json.RawMessage(nil), value...)
		history = append(history, override)
	}
	return history, rows.Err()
}

func getOverrideHeadTx(ctx context.Context, tx *sql.Tx, targetID, field string) (OverrideHead, bool, error) {
	var head OverrideHead
	head.TargetID, head.Field = targetID, field
	err := tx.QueryRowContext(ctx, `
SELECT override_id, override_version, active
FROM recruiting_override_heads
WHERE target_id = ? AND field_name = ? FOR UPDATE`, targetID, field).
		Scan(&head.OverrideID, &head.OverrideVersion, &head.Active)
	if errors.Is(err, sql.ErrNoRows) {
		return OverrideHead{}, false, nil
	}
	if err != nil {
		return OverrideHead{}, false, fmt.Errorf("get override head: %w", err)
	}
	return head, true, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanOverrideHead(row rowScanner, targetID, field string) (OverrideHead, model.CuratedOverride, error) {
	var head OverrideHead
	var override model.CuratedOverride
	var value []byte
	head.TargetID, head.Field = targetID, field
	err := row.Scan(&head.OverrideID, &head.OverrideVersion, &head.Active,
		&override.ActorID, &override.Reason, &value)
	if errors.Is(err, sql.ErrNoRows) {
		return OverrideHead{}, model.CuratedOverride{}, ErrNotFound
	}
	if err != nil {
		return OverrideHead{}, model.CuratedOverride{}, fmt.Errorf("get override head: %w", err)
	}
	override.OverrideID, override.Version = head.OverrideID, head.OverrideVersion
	override.TargetID, override.Field, override.Active = targetID, field, head.Active
	override.Value = append(json.RawMessage(nil), value...)
	return head, override, nil
}
