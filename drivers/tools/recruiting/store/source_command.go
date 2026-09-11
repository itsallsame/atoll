package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

// ApplyCreateSourceCommand atomically persists a new Source, its stable
// command response, and the event intent later delivered to the Atoll ledger.
func (r *Repository) ApplyCreateSourceCommand(ctx context.Context, source model.RecruitmentSource, receipt model.CommandReceipt, event model.EventIntent, businessAt time.Time) (CommandResult, error) {
	if source.SourceID == "" || source.CompanyID == "" || source.Version != 1 || receipt.CommandID == "" ||
		event.AggregateType != "source" || event.AggregateID != source.SourceID || event.AggregateVersion != source.Version ||
		event.CauseCommandID != receipt.CommandID {
		return CommandResult{}, fmt.Errorf("source create command, receipt, and event are inconsistent")
	}
	endpoint, origin, err := sourceStorageIdentity(source)
	if err != nil {
		return CommandResult{}, err
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil {
		return CommandResult{}, fmt.Errorf("event business time: %w", err)
	}
	state, err := json.Marshal(source)
	if err != nil {
		return CommandResult{}, fmt.Errorf("encode source: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin source create command: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	var companyControl model.ControlStatus
	if err := tx.QueryRowContext(ctx, `
SELECT control_status FROM recruiting_companies
WHERE company_id = ? FOR SHARE`, source.CompanyID).Scan(&companyControl); errors.Is(err, sql.ErrNoRows) {
		return CommandResult{}, ErrNotFound
	} else if err != nil {
		return CommandResult{}, fmt.Errorf("lock source company: %w", err)
	}
	if companyControl == model.ControlArchived {
		return CommandResult{}, &model.InvalidTransitionError{Entity: "company", From: string(companyControl), Action: "add source"}
	}
	var canonicalCompanyID string
	err = tx.QueryRowContext(ctx, `
SELECT canonical_company_id FROM recruiting_company_aliases
WHERE alias_company_id = ? AND active_alias_key = ?`, source.CompanyID, source.CompanyID).Scan(&canonicalCompanyID)
	if err == nil {
		return CommandResult{}, fmt.Errorf("%w: company %s is an alias of %s; add the Source to the canonical company",
			ErrBusinessKeyExists, source.CompanyID, canonicalCompanyID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return CommandResult{}, fmt.Errorf("inspect source company alias mapping: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO recruiting_command_receipts(command_id, word_name, request_hash, response_bytes, committed_at)
VALUES (?, ?, ?, ?, ?)`, receipt.CommandID, receipt.Word, receipt.RequestHash, []byte(receipt.Response), businessAt.UTC()); err != nil {
		var mysqlError *mysql.MySQLError
		if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, fmt.Errorf("reserve source create receipt: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO recruiting_sources(
  source_id, company_id, canonical_source_key, origin, readiness_status,
  control_status, health_status, discovery_generation, version, state_json,
  created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		source.SourceID, source.CompanyID, endpoint.CanonicalKey, origin, source.ReadinessStatus,
		source.ControlStatus, source.HealthStatus, source.DiscoveryGeneration, source.Version, state,
		businessAt.UTC(), businessAt.UTC()); err != nil {
		var mysqlError *mysql.MySQLError
		if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
			return CommandResult{}, ErrBusinessKeyExists
		}
		return CommandResult{}, fmt.Errorf("create source in command: %w", err)
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit source create command: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

// ApplySourceCommand is the Source equivalent of ApplyCompanyCommand. The CAS,
// receipt, and outbox event share one transaction, so an acknowledged command
// can never exist without its domain state and recoverable event intent.
func (r *Repository) ApplySourceCommand(ctx context.Context, expectedVersion uint64, source model.RecruitmentSource, receipt model.CommandReceipt, event model.EventIntent, businessAt time.Time) (CommandResult, error) {
	if expectedVersion == 0 || source.SourceID == "" || source.Version != expectedVersion+1 || receipt.CommandID == "" ||
		event.AggregateType != "source" || event.AggregateID != source.SourceID || event.AggregateVersion != source.Version ||
		event.CauseCommandID != receipt.CommandID {
		return CommandResult{}, fmt.Errorf("source command aggregate, receipt, and event are inconsistent")
	}
	endpoint, origin, err := sourceStorageIdentity(source)
	if err != nil {
		return CommandResult{}, err
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil {
		return CommandResult{}, fmt.Errorf("event business time: %w", err)
	}
	state, err := json.Marshal(source)
	if err != nil {
		return CommandResult{}, fmt.Errorf("encode source: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin source command: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO recruiting_command_receipts(command_id, word_name, request_hash, response_bytes, committed_at)
VALUES (?, ?, ?, ?, ?)`, receipt.CommandID, receipt.Word, receipt.RequestHash, []byte(receipt.Response), businessAt.UTC()); err != nil {
		var mysqlError *mysql.MySQLError
		if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, fmt.Errorf("reserve source command receipt: %w", err)
	}
	result, err := tx.ExecContext(ctx, `
UPDATE recruiting_sources
SET canonical_source_key = ?, origin = ?, readiness_status = ?, control_status = ?,
    health_status = ?, discovery_generation = ?, version = ?, state_json = ?, updated_at = ?
WHERE source_id = ? AND version = ?`,
		endpoint.CanonicalKey, origin, source.ReadinessStatus, source.ControlStatus, source.HealthStatus,
		source.DiscoveryGeneration, source.Version, state, businessAt.UTC(), source.SourceID, expectedVersion)
	if err != nil {
		var mysqlError *mysql.MySQLError
		if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
			return CommandResult{}, ErrBusinessKeyExists
		}
		return CommandResult{}, fmt.Errorf("update source in command: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return CommandResult{}, fmt.Errorf("inspect source command update: %w", err)
	}
	if changed != 1 {
		var actual uint64
		readErr := tx.QueryRowContext(ctx, "SELECT version FROM recruiting_sources WHERE source_id = ?", source.SourceID).Scan(&actual)
		if errors.Is(readErr, sql.ErrNoRows) {
			return CommandResult{}, ErrNotFound
		}
		if readErr != nil {
			return CommandResult{}, fmt.Errorf("read source version after failed CAS: %w", readErr)
		}
		return CommandResult{}, &model.VersionConflictError{Expected: expectedVersion, Actual: actual}
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit source command: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}
