package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

const defaultDispatchMaxDeliveryAttempts = 8

var ErrDispatchConflict = errors.New("recruiting execution dispatch conflict")

type ExecutionDispatchIntent struct {
	DispatchID    string    `json:"dispatch_id"`
	TargetActorID string    `json:"target_actor_id"`
	Capability    string    `json:"capability"`
	Origin        string    `json:"origin,omitempty"`
	ProfileID     string    `json:"profile_id,omitempty"`
	CauseKind     string    `json:"cause_kind"`
	CauseID       string    `json:"cause_id"`
	NextAttemptAt time.Time `json:"next_attempt_at"`
}

type ExecutionDispatchTarget struct {
	ActorID    string
	Capability string
}

func NewExecutionDispatchIntent(dispatchID, targetActorID, capability, origin, profileID, causeKind, causeID string,
	nextAttemptAt time.Time) (ExecutionDispatchIntent, error) {
	intent := ExecutionDispatchIntent{DispatchID: strings.TrimSpace(dispatchID), TargetActorID: strings.TrimSpace(targetActorID),
		Capability: strings.TrimSpace(capability), Origin: strings.TrimSpace(origin), ProfileID: strings.TrimSpace(profileID),
		CauseKind: strings.TrimSpace(causeKind), CauseID: strings.TrimSpace(causeID), NextAttemptAt: nextAttemptAt.UTC()}
	if intent.DispatchID == "" || len(intent.DispatchID) > 191 || intent.TargetActorID == "" || len(intent.TargetActorID) > 191 ||
		intent.Capability == "" || len(intent.Capability) > 128 || strings.ContainsAny(intent.Capability, "\r\n\t ") ||
		len(intent.Origin) > 512 || len(intent.ProfileID) > 191 || intent.CauseKind == "" || len(intent.CauseKind) > 64 ||
		intent.CauseID == "" || len(intent.CauseID) > 191 || nextAttemptAt.IsZero() {
		return ExecutionDispatchIntent{}, fmt.Errorf("execution dispatch requires bounded target, capability, cause, and due time")
	}
	return intent, nil
}

type PendingExecutionDispatch struct {
	Intent      ExecutionDispatchIntent
	Attempts    uint64
	MaxAttempts uint64
}

func (r *Repository) EnqueueExecutionDispatch(ctx context.Context, intent ExecutionDispatchIntent, createdAt time.Time) error {
	if createdAt.IsZero() {
		return fmt.Errorf("execution dispatch creation time is required")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := appendExecutionDispatch(ctx, tx, intent, createdAt); err != nil {
		return err
	}
	return tx.Commit()
}

func appendExecutionDispatch(ctx context.Context, tx *sql.Tx, intent ExecutionDispatchIntent, createdAt time.Time) error {
	if intent.ProfileID != "" {
		var state []byte
		if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_profiles WHERE profile_id = ?`, intent.ProfileID).Scan(&state); err != nil {
			return fmt.Errorf("resolve Profile execution dispatch: %w", err)
		}
		var profile model.BrowserProfile
		if err := json.Unmarshal(state, &profile); err != nil || !executioncontract.ValidToolTarget(profile.DeviceID) {
			return fmt.Errorf("Profile execution dispatch has no authorized Tool Actor")
		}
		intent.TargetActorID = profile.DeviceID
	}
	validated, err := NewExecutionDispatchIntent(intent.DispatchID, intent.TargetActorID, intent.Capability, intent.Origin,
		intent.ProfileID, intent.CauseKind, intent.CauseID, intent.NextAttemptAt)
	if err != nil || validated != intent || createdAt.IsZero() {
		return fmt.Errorf("invalid execution dispatch intent")
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO recruiting_execution_dispatch_outbox(
  dispatch_id, target_actor_id, capability, origin, profile_id, cause_kind, cause_id,
  delivery_status, delivery_attempts, max_delivery_attempts, next_attempt_at, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, 'pending', 0, ?, ?, ?)`, intent.DispatchID, intent.TargetActorID, intent.Capability,
		nullableString(intent.Origin), nullableString(intent.ProfileID), intent.CauseKind, intent.CauseID,
		defaultDispatchMaxDeliveryAttempts, intent.NextAttemptAt.UTC(), createdAt.UTC())
	if err == nil {
		return nil
	}
	var duplicate *mysql.MySQLError
	if !errors.As(err, &duplicate) || duplicate.Number != 1062 {
		return fmt.Errorf("append execution dispatch: %w", err)
	}
	existing, found, readErr := getExecutionDispatch(ctx, tx, intent.DispatchID)
	if readErr != nil || !found {
		return fmt.Errorf("read conflicting execution dispatch: %w", readErr)
	}
	if existing.Intent == intent {
		return nil
	}
	return ErrDispatchConflict
}

func (r *Repository) ListPendingExecutionDispatches(ctx context.Context, dueAt time.Time, limit int) ([]PendingExecutionDispatch, error) {
	if dueAt.IsZero() || limit < 1 || limit > 500 {
		return nil, fmt.Errorf("dispatch query requires due time and limit in [1,500]")
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT dispatch_id, target_actor_id, capability, COALESCE(origin, ''), COALESCE(profile_id, ''),
       cause_kind, cause_id, next_attempt_at, delivery_attempts, max_delivery_attempts
FROM recruiting_execution_dispatch_outbox
WHERE delivery_status = 'pending' AND next_attempt_at <= ?
ORDER BY next_attempt_at, dispatch_id LIMIT ?`, dueAt.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list pending execution dispatches: %w", err)
	}
	defer rows.Close()
	result := make([]PendingExecutionDispatch, 0, limit)
	for rows.Next() {
		var pending PendingExecutionDispatch
		if err := rows.Scan(&pending.Intent.DispatchID, &pending.Intent.TargetActorID, &pending.Intent.Capability,
			&pending.Intent.Origin, &pending.Intent.ProfileID, &pending.Intent.CauseKind, &pending.Intent.CauseID,
			&pending.Intent.NextAttemptAt, &pending.Attempts, &pending.MaxAttempts); err != nil {
			return nil, err
		}
		pending.Intent.NextAttemptAt = pending.Intent.NextAttemptAt.UTC()
		result = append(result, pending)
	}
	return result, rows.Err()
}

func (r *Repository) CompleteExecutionDispatch(ctx context.Context, dispatchID, targetActorID string, deliveredAt time.Time) error {
	if strings.TrimSpace(dispatchID) == "" || strings.TrimSpace(targetActorID) == "" || deliveredAt.IsZero() {
		return fmt.Errorf("dispatch identity, target, and completion time are required")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var storedTarget, status string
	err = tx.QueryRowContext(ctx, "SELECT target_actor_id, delivery_status FROM recruiting_execution_dispatch_outbox WHERE dispatch_id = ? FOR UPDATE", dispatchID).
		Scan(&storedTarget, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	if !executioncontract.TargetMatchesAuthenticatedActor(storedTarget, targetActorID) {
		return ErrDispatchConflict
	}
	if status == "delivered" {
		return tx.Commit()
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE recruiting_execution_dispatch_outbox SET delivery_status = 'delivered', delivered_at = ?
WHERE dispatch_id = ?`, deliveredAt.UTC(), dispatchID); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) RecordExecutionDispatchFailureCAS(ctx context.Context, dispatchID string, expectedAttempts uint64,
	nextAttemptAt time.Time, errorClass string) (EventDeliveryUpdate, error) {
	errorClass = strings.TrimSpace(errorClass)
	if strings.TrimSpace(dispatchID) == "" || expectedAttempts >= math.MaxUint32 || nextAttemptAt.IsZero() || errorClass == "" || len(errorClass) > 128 {
		return EventDeliveryUpdate{}, fmt.Errorf("dispatch failure requires identity, next time, and error class")
	}
	result, err := r.db.ExecContext(ctx, `
UPDATE recruiting_execution_dispatch_outbox
SET delivery_status = CASE WHEN delivery_attempts + 1 >= max_delivery_attempts THEN 'exhausted' ELSE 'pending' END,
    delivery_attempts = delivery_attempts + 1, next_attempt_at = ?, last_error_class = ?
WHERE dispatch_id = ? AND delivery_status = 'pending' AND delivery_attempts = ?`,
		nextAttemptAt.UTC(), errorClass, dispatchID, expectedAttempts)
	if err != nil {
		return EventDeliveryUpdate{}, err
	}
	var update EventDeliveryUpdate
	err = r.db.QueryRowContext(ctx, `
SELECT delivery_status, delivery_attempts, max_delivery_attempts, next_attempt_at, COALESCE(last_error_class, '')
FROM recruiting_execution_dispatch_outbox WHERE dispatch_id = ?`, dispatchID).
		Scan(&update.Status, &update.Attempts, &update.MaxAttempts, &update.NextAttemptAt, &update.LastErrorClass)
	if errors.Is(err, sql.ErrNoRows) {
		return EventDeliveryUpdate{}, ErrNotFound
	}
	if err != nil {
		return EventDeliveryUpdate{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return update, ErrDispatchConflict
	}
	return update, nil
}

func getExecutionDispatch(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, dispatchID string) (PendingExecutionDispatch, bool, error) {
	var pending PendingExecutionDispatch
	var status string
	err := query.QueryRowContext(ctx, `
SELECT target_actor_id, capability, COALESCE(origin, ''), COALESCE(profile_id, ''), cause_kind, cause_id,
       next_attempt_at, delivery_attempts, max_delivery_attempts, delivery_status
FROM recruiting_execution_dispatch_outbox WHERE dispatch_id = ?`, dispatchID).
		Scan(&pending.Intent.TargetActorID, &pending.Intent.Capability, &pending.Intent.Origin, &pending.Intent.ProfileID,
			&pending.Intent.CauseKind, &pending.Intent.CauseID, &pending.Intent.NextAttemptAt, &pending.Attempts, &pending.MaxAttempts, &status)
	if errors.Is(err, sql.ErrNoRows) {
		return PendingExecutionDispatch{}, false, nil
	}
	if err != nil {
		return PendingExecutionDispatch{}, false, err
	}
	pending.Intent.DispatchID = dispatchID
	pending.Intent.NextAttemptAt = pending.Intent.NextAttemptAt.UTC()
	return pending, true, nil
}

func appendCapabilityDispatches(ctx context.Context, tx *sql.Tx, targets []ExecutionDispatchTarget,
	queuedByCapability map[string]int, causeID string, at time.Time) (int, error) {
	byCapability := make(map[string][]string)
	seen := make(map[string]struct{}, len(targets))
	for _, target := range targets {
		target.ActorID, target.Capability = strings.TrimSpace(target.ActorID), strings.TrimSpace(target.Capability)
		if !executioncontract.ValidToolTarget(target.ActorID) || target.Capability == "" || len(target.Capability) > 128 ||
			strings.ContainsAny(target.Capability, "\r\n\t ") {
			return 0, fmt.Errorf("invalid execution dispatch target")
		}
		if _, duplicate := seen[target.ActorID]; duplicate {
			return 0, fmt.Errorf("duplicate execution dispatch target")
		}
		seen[target.ActorID] = struct{}{}
		byCapability[target.Capability] = append(byCapability[target.Capability], target.ActorID)
	}
	capabilities := make([]string, 0, len(queuedByCapability))
	for capability := range queuedByCapability {
		capabilities = append(capabilities, capability)
	}
	sort.Strings(capabilities)
	created := 0
	for _, capability := range capabilities {
		actors := byCapability[capability]
		sort.Strings(actors)
		count := queuedByCapability[capability]
		if count > len(actors) {
			count = len(actors)
		}
		if count <= 0 {
			continue
		}
		rotation := int(dispatchDigest(causeID + "\n" + capability)[0]) % len(actors)
		for index := 0; index < count; index++ {
			dispatchID := "dispatch-" + hex.EncodeToString(dispatchDigest(fmt.Sprintf("%s\n%s\n%d", causeID, capability, index))[:16])
			intent, err := NewExecutionDispatchIntent(dispatchID, actors[(rotation+index)%len(actors)], capability, "", "",
				"work_materialized", causeID, at)
			if err != nil {
				return created, err
			}
			if err := appendExecutionDispatch(ctx, tx, intent, at); err != nil {
				return created, err
			}
			created++
		}
	}
	return created, nil
}

type profileDispatchDemand struct {
	Capability string
	ProfileID  string
	Count      int
}

// appendProfileDispatches routes Profile-bound work directly to the Tool
// Actor recorded by the ready BrowserProfile. One wake per Profile is enough;
// subsequent capacity wakes remain tied to the accepted Attempt's executor.
func appendProfileDispatches(ctx context.Context, tx *sql.Tx, demands []profileDispatchDemand,
	causeID string, at time.Time) (int, error) {
	sort.Slice(demands, func(i, j int) bool {
		if demands[i].ProfileID != demands[j].ProfileID {
			return demands[i].ProfileID < demands[j].ProfileID
		}
		return demands[i].Capability < demands[j].Capability
	})
	created := 0
	for _, demand := range demands {
		if demand.Count <= 0 || strings.TrimSpace(demand.ProfileID) == "" || strings.TrimSpace(demand.Capability) == "" {
			return created, fmt.Errorf("invalid Profile dispatch demand")
		}
		var state []byte
		if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_profiles WHERE profile_id = ? FOR SHARE`,
			demand.ProfileID).Scan(&state); err != nil {
			return created, fmt.Errorf("load Profile dispatch target: %w", err)
		}
		var profile model.BrowserProfile
		if err := json.Unmarshal(state, &profile); err != nil {
			return created, err
		}
		if profile.AuthStatus != model.ProfileReady || !executioncontract.ValidToolTarget(profile.DeviceID) {
			return created, fmt.Errorf("Profile dispatch requires a ready Profile on an authorized Tool Actor")
		}
		dispatchID := "dispatch-" + hex.EncodeToString(dispatchDigest("profile\n" + causeID + "\n" + demand.Capability + "\n" + demand.ProfileID)[:16])
		intent, err := NewExecutionDispatchIntent(dispatchID, profile.DeviceID, demand.Capability, "", demand.ProfileID,
			"profile_work_materialized", causeID, at)
		if err != nil {
			return created, err
		}
		if err := appendExecutionDispatch(ctx, tx, intent, at); err != nil {
			return created, err
		}
		created++
	}
	return created, nil
}

func dispatchDigest(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func appendAttemptDispatch(ctx context.Context, tx *sql.Tx, attemptID, targetActorID, capability, profileID,
	causeKind, causeID string, dueAt, createdAt time.Time) error {
	dispatchID := "dispatch-" + hex.EncodeToString(dispatchDigest("attempt\n" + attemptID + "\n" + causeKind)[:16])
	intent, err := NewExecutionDispatchIntent(dispatchID, targetActorID, capability, "", profileID, causeKind, causeID, dueAt)
	if err != nil {
		return err
	}
	return appendExecutionDispatch(ctx, tx, intent, createdAt)
}
