package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type CommandResult struct {
	Response json.RawMessage
	Replayed bool
}

const defaultOutboxMaxDeliveryAttempts uint64 = 8

// ApplyCompanyCommand atomically persists the aggregate CAS, stable command
// response, and an outbox event intent. Publishing that intent to the Atoll
// ledger happens after commit and is independently retryable.
func (r *Repository) ApplyCompanyCommand(ctx context.Context, expectedVersion uint64, company model.Company, receipt model.CommandReceipt, event model.EventIntent, businessAt time.Time) (CommandResult, error) {
	if company.CompanyID == "" || company.Version != expectedVersion+1 || receipt.CommandID == "" ||
		event.AggregateType != "company" || event.AggregateID != company.CompanyID || event.AggregateVersion != company.Version ||
		event.CauseCommandID != receipt.CommandID {
		return CommandResult{}, fmt.Errorf("company command aggregate, receipt, and event are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil {
		return CommandResult{}, fmt.Errorf("event business time: %w", err)
	}
	state, err := json.Marshal(company)
	if err != nil {
		return CommandResult{}, fmt.Errorf("encode company: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin company command: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}

	_, err = tx.ExecContext(ctx, `
INSERT INTO recruiting_command_receipts(command_id, word_name, request_hash, response_bytes, committed_at)
VALUES (?, ?, ?, ?, ?)`, receipt.CommandID, receipt.Word, receipt.RequestHash, []byte(receipt.Response), businessAt.UTC())
	if err != nil {
		var mysqlError *mysql.MySQLError
		if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, fmt.Errorf("reserve command receipt: %w", err)
	}

	result, err := tx.ExecContext(ctx, `
UPDATE recruiting_companies
SET normalized_website = ?, name = ?, onboarding_status = ?, control_status = ?,
    version = ?, state_json = ?, updated_at = ?
WHERE company_id = ? AND version = ?`,
		nullableString(company.Website), company.Name, company.OnboardingStatus, company.ControlStatus,
		company.Version, state, businessAt.UTC(), company.CompanyID, expectedVersion)
	if err != nil {
		return CommandResult{}, fmt.Errorf("update company in command: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return CommandResult{}, fmt.Errorf("inspect company command update: %w", err)
	}
	if changed != 1 {
		var actual uint64
		readErr := tx.QueryRowContext(ctx, "SELECT version FROM recruiting_companies WHERE company_id = ?", company.CompanyID).Scan(&actual)
		if errors.Is(readErr, sql.ErrNoRows) {
			return CommandResult{}, ErrNotFound
		}
		if readErr != nil {
			return CommandResult{}, fmt.Errorf("read company version after failed CAS: %w", readErr)
		}
		return CommandResult{}, &model.VersionConflictError{Expected: expectedVersion, Actual: actual}
	}

	_, err = tx.ExecContext(ctx, `
INSERT INTO recruiting_event_outbox(
  event_id, event_kind, aggregate_type, aggregate_id, aggregate_version,
  cause_command_id, business_at, payload_json, delivery_status,
  delivery_attempts, max_delivery_attempts, next_attempt_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'pending', 0, ?, ?)`,
		event.EventID, event.Kind, event.AggregateType, event.AggregateID, event.AggregateVersion,
		event.CauseCommandID, eventAt.UTC(), []byte(event.Payload), defaultOutboxMaxDeliveryAttempts, businessAt.UTC())
	if err != nil {
		return CommandResult{}, fmt.Errorf("append company event intent: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit company command: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func readCommandReceipt(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, commandID, requestHash string) (json.RawMessage, bool, error) {
	var storedHash string
	var response []byte
	err := query.QueryRowContext(ctx, "SELECT request_hash, response_bytes FROM recruiting_command_receipts WHERE command_id = ?", commandID).Scan(&storedHash, &response)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("read command receipt: %w", err)
	}
	if storedHash != requestHash {
		return nil, false, ErrCommandConflict
	}
	if !json.Valid(response) {
		return nil, false, fmt.Errorf("stored command response is invalid")
	}
	return append(json.RawMessage(nil), response...), true, nil
}

func (r *Repository) replayCommittedCommand(ctx context.Context, commandID, requestHash string) (CommandResult, error) {
	response, found, err := readCommandReceipt(ctx, r.db, commandID, requestHash)
	if err != nil {
		return CommandResult{}, err
	}
	if !found {
		return CommandResult{}, fmt.Errorf("concurrent command receipt disappeared")
	}
	return CommandResult{Response: response, Replayed: true}, nil
}

type PendingEvent struct {
	Intent      model.EventIntent
	Attempts    uint64
	MaxAttempts uint64
}

func (r *Repository) ListPendingEvents(ctx context.Context, dueAt time.Time, limit int) ([]PendingEvent, error) {
	if dueAt.IsZero() || limit <= 0 || limit > 500 {
		return nil, fmt.Errorf("outbox due time and limit in [1,500] are required")
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT event_id, event_kind, aggregate_type, aggregate_id, aggregate_version,
       cause_command_id, business_at, payload_json, delivery_attempts, max_delivery_attempts
FROM recruiting_event_outbox
WHERE delivery_status = 'pending' AND next_attempt_at <= ?
ORDER BY next_attempt_at, event_id
LIMIT ?`, dueAt.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list pending recruiting events: %w", err)
	}
	defer rows.Close()
	var events []PendingEvent
	for rows.Next() {
		var event PendingEvent
		var businessAt time.Time
		if err := rows.Scan(&event.Intent.EventID, &event.Intent.Kind, &event.Intent.AggregateType, &event.Intent.AggregateID,
			&event.Intent.AggregateVersion, &event.Intent.CauseCommandID, &businessAt, &event.Intent.Payload, &event.Attempts, &event.MaxAttempts); err != nil {
			return nil, fmt.Errorf("scan pending recruiting event: %w", err)
		}
		event.Intent.BusinessAt = businessAt.UTC().Format(time.RFC3339Nano)
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read pending recruiting events: %w", err)
	}
	return events, nil
}

func (r *Repository) MarkEventDelivered(ctx context.Context, eventID string, deliveredAt time.Time) error {
	if strings.TrimSpace(eventID) == "" || deliveredAt.IsZero() {
		return fmt.Errorf("event identity and delivery time are required")
	}
	result, err := r.db.ExecContext(ctx, `
UPDATE recruiting_event_outbox
SET delivery_status = 'delivered', delivered_at = ?
WHERE event_id = ? AND delivery_status <> 'delivered'`, deliveredAt.UTC(), eventID)
	if err != nil {
		return fmt.Errorf("mark recruiting event delivered: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect recruiting event delivery: %w", err)
	}
	if changed == 1 {
		return nil
	}
	var status string
	err = r.db.QueryRowContext(ctx, "SELECT delivery_status FROM recruiting_event_outbox WHERE event_id = ?", eventID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("read recruiting event delivery: %w", err)
	}
	if status == "delivered" {
		return nil
	}
	return ErrOutboxConflict
}

type EventDeliveryUpdate struct {
	Status         string
	Attempts       uint64
	MaxAttempts    uint64
	NextAttemptAt  time.Time
	LastErrorClass string
}

// RecordEventFailureCAS advances the persisted retry count exactly once. The
// event's maximum is frozen when its intent is created; the final failure
// moves it out of the runnable pending set instead of retrying forever.
func (r *Repository) RecordEventFailureCAS(ctx context.Context, eventID string, expectedAttempts uint64, nextAttemptAt time.Time, errorClass string) (EventDeliveryUpdate, error) {
	errorClass = strings.TrimSpace(errorClass)
	if strings.TrimSpace(eventID) == "" || expectedAttempts >= math.MaxUint32 || nextAttemptAt.IsZero() || errorClass == "" || len(errorClass) > 128 {
		return EventDeliveryUpdate{}, fmt.Errorf("event failure requires identity, bounded attempt, next time, and error class")
	}
	result, err := r.db.ExecContext(ctx, `
UPDATE recruiting_event_outbox
SET delivery_status = CASE WHEN delivery_attempts + 1 >= max_delivery_attempts THEN 'exhausted' ELSE 'pending' END,
    delivery_attempts = delivery_attempts + 1,
    next_attempt_at = ?, last_error_class = ?
WHERE event_id = ? AND delivery_status = 'pending' AND delivery_attempts = ?`,
		nextAttemptAt.UTC(), errorClass, eventID, expectedAttempts)
	if err != nil {
		return EventDeliveryUpdate{}, fmt.Errorf("record recruiting event failure: %w", err)
	}
	changed, _ := result.RowsAffected()
	update, readErr := r.getEventDeliveryUpdate(ctx, eventID)
	if readErr != nil {
		return EventDeliveryUpdate{}, readErr
	}
	if changed != 1 {
		return update, ErrOutboxConflict
	}
	return update, nil
}

func (r *Repository) getEventDeliveryUpdate(ctx context.Context, eventID string) (EventDeliveryUpdate, error) {
	var update EventDeliveryUpdate
	err := r.db.QueryRowContext(ctx, `
SELECT delivery_status, delivery_attempts, max_delivery_attempts, next_attempt_at, COALESCE(last_error_class, '')
FROM recruiting_event_outbox WHERE event_id = ?`, eventID).
		Scan(&update.Status, &update.Attempts, &update.MaxAttempts, &update.NextAttemptAt, &update.LastErrorClass)
	if errors.Is(err, sql.ErrNoRows) {
		return EventDeliveryUpdate{}, ErrNotFound
	}
	if err != nil {
		return EventDeliveryUpdate{}, fmt.Errorf("read recruiting event retry state: %w", err)
	}
	return update, nil
}
