package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type ExecutionBudgetPolicy struct {
	Version          uint64
	MaxActive        int
	MaxPerCapability int
	MaxPerOrigin     int
	MaxPerCompany    int
	MaxPerProfile    int
	PermitTTL        time.Duration
}

func DefaultExecutionBudgetPolicy() ExecutionBudgetPolicy {
	return ExecutionBudgetPolicy{Version: 1, MaxActive: 1_000, MaxPerCapability: 1_000,
		MaxPerOrigin: 8, MaxPerCompany: 50, MaxPerProfile: 1, PermitTTL: 15 * time.Minute}
}

func (p ExecutionBudgetPolicy) validate() error {
	if p.Version == 0 || p.MaxActive < 1 || p.MaxActive > 100_000 || p.MaxPerCapability < 1 ||
		p.MaxPerCapability > p.MaxActive || p.MaxPerOrigin < 1 || p.MaxPerOrigin > p.MaxActive ||
		p.MaxPerCompany < 1 || p.MaxPerCompany > p.MaxActive || p.MaxPerProfile < 1 ||
		p.MaxPerProfile > p.MaxActive || p.PermitTTL < time.Second || p.PermitTTL > 24*time.Hour {
		return fmt.Errorf("execution budget policy has invalid version, limits, or permit TTL")
	}
	return nil
}

type budgetDimension struct {
	typeName string
	key      string
	limit    int
}

func budgetDimensions(permit model.BudgetPermit, policy ExecutionBudgetPolicy) []budgetDimension {
	dimensions := []budgetDimension{
		{"global", "all", policy.MaxActive},
		{"capability", permit.Capability, policy.MaxPerCapability},
		{"origin", permit.Origin, policy.MaxPerOrigin},
		{"company", permit.CompanyID, policy.MaxPerCompany},
	}
	if permit.ProfileID != "" {
		dimensions = append(dimensions, budgetDimension{"profile", permit.ProfileID, policy.MaxPerProfile})
	}
	sort.Slice(dimensions, func(i, j int) bool {
		if dimensions[i].typeName == dimensions[j].typeName {
			return dimensions[i].key < dimensions[j].key
		}
		return dimensions[i].typeName < dimensions[j].typeName
	})
	return dimensions
}

func acquireBudgetPermitTx(ctx context.Context, tx *sql.Tx, attemptID, origin, profileID, capability, companyID string,
	policy ExecutionBudgetPolicy, grantedAt time.Time) (model.BudgetPermit, time.Time, error) {
	if err := policy.validate(); err != nil {
		return model.BudgetPermit{}, time.Time{}, err
	}
	permitSum := sha256.Sum256([]byte("recruiting.execution.permit.v1\n" + attemptID))
	permitID := "permit-" + hex.EncodeToString(permitSum[:16])
	permit, err := model.NewBudgetPermit(permitID, attemptID, origin, profileID, capability, companyID, policy.Version)
	if err != nil {
		return model.BudgetPermit{}, time.Time{}, err
	}
	dimensions := budgetDimensions(permit, policy)
	for _, dimension := range dimensions {
		_, err := tx.ExecContext(ctx, `
INSERT INTO recruiting_budget_usage(dimension_type, dimension_key, active_count, version, updated_at)
VALUES (?, ?, 0, 1, ?) ON DUPLICATE KEY UPDATE dimension_key = VALUES(dimension_key)`,
			dimension.typeName, dimension.key, grantedAt.UTC())
		if err != nil {
			return model.BudgetPermit{}, time.Time{}, fmt.Errorf("ensure budget dimension: %w", err)
		}
	}
	for _, dimension := range dimensions {
		var active int
		if err := tx.QueryRowContext(ctx, `
SELECT active_count FROM recruiting_budget_usage
WHERE dimension_type = ? AND dimension_key = ? FOR UPDATE`, dimension.typeName, dimension.key).Scan(&active); err != nil {
			return model.BudgetPermit{}, time.Time{}, fmt.Errorf("lock budget dimension: %w", err)
		}
		if active >= dimension.limit {
			return model.BudgetPermit{}, time.Time{}, fmt.Errorf("%w: %s capacity exhausted", ErrBudgetBlocked, dimension.typeName)
		}
	}
	for _, dimension := range dimensions {
		if _, err := tx.ExecContext(ctx, `
UPDATE recruiting_budget_usage SET active_count = active_count + 1, version = version + 1, updated_at = ?
WHERE dimension_type = ? AND dimension_key = ?`, grantedAt.UTC(), dimension.typeName, dimension.key); err != nil {
			return model.BudgetPermit{}, time.Time{}, err
		}
	}
	expiresAt := grantedAt.UTC().Add(policy.PermitTTL)
	state, _ := json.Marshal(permit)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO recruiting_budget_permits(
  permit_id, attempt_id, origin, profile_id, capability, company_id,
  policy_version, permit_status, version, expires_at, state_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, permit.PermitID, permit.AttemptID, permit.Origin,
		nullableString(permit.ProfileID), permit.Capability, permit.CompanyID, permit.PolicyVersion,
		permit.Status, permit.Version, expiresAt, state); err != nil {
		return model.BudgetPermit{}, time.Time{}, fmt.Errorf("create execution budget permit: %w", err)
	}
	return permit, expiresAt, nil
}

func releaseBudgetPermitTx(ctx context.Context, tx *sql.Tx, attemptID string, terminal model.BudgetPermitStatus, at time.Time) error {
	var state []byte
	err := tx.QueryRowContext(ctx, `
SELECT state_json FROM recruiting_budget_permits WHERE attempt_id = ? FOR UPDATE`, attemptID).Scan(&state)
	if err == sql.ErrNoRows {
		return nil
	}
	if err != nil {
		return err
	}
	var permit model.BudgetPermit
	if err := json.Unmarshal(state, &permit); err != nil {
		return err
	}
	if permit.Status != model.PermitGranted {
		return nil
	}
	var closed model.BudgetPermit
	if terminal == model.PermitExpired {
		closed, err = permit.Expire(permit.Version)
	} else {
		closed, err = permit.Release(permit.Version)
	}
	if err != nil {
		return err
	}
	// Limits do not matter on release; lock the same dimension keys in the
	// same order as acquisition to avoid cross-dimensional deadlocks.
	dimensions := budgetDimensions(permit, ExecutionBudgetPolicy{
		MaxActive: 1, MaxPerCapability: 1, MaxPerOrigin: 1, MaxPerCompany: 1, MaxPerProfile: 1,
	})
	for _, dimension := range dimensions {
		var active int
		if err := tx.QueryRowContext(ctx, `SELECT active_count FROM recruiting_budget_usage
WHERE dimension_type = ? AND dimension_key = ? FOR UPDATE`, dimension.typeName, dimension.key).Scan(&active); err != nil {
			return err
		}
		if active < 1 {
			return fmt.Errorf("budget usage underflow for %s", dimension.typeName)
		}
	}
	for _, dimension := range dimensions {
		if _, err := tx.ExecContext(ctx, `UPDATE recruiting_budget_usage
SET active_count = active_count - 1, version = version + 1, updated_at = ?
WHERE dimension_type = ? AND dimension_key = ?`, at.UTC(), dimension.typeName, dimension.key); err != nil {
			return err
		}
	}
	closedState, _ := json.Marshal(closed)
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_budget_permits
SET permit_status = ?, version = ?, state_json = ? WHERE permit_id = ? AND version = ?`,
		closed.Status, closed.Version, closedState, closed.PermitID, permit.Version)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrAttemptConflict
	}
	return nil
}

func ensureBudgetPermitActiveTx(ctx context.Context, tx *sql.Tx, attemptID string, at time.Time) error {
	var status model.BudgetPermitStatus
	var expiresAt time.Time
	if err := tx.QueryRowContext(ctx, `SELECT permit_status, expires_at FROM recruiting_budget_permits
WHERE attempt_id = ? FOR UPDATE`, attemptID).Scan(&status, &expiresAt); err != nil {
		return fmt.Errorf("load execution budget permit: %w", err)
	}
	if status != model.PermitGranted || !at.UTC().Before(expiresAt.UTC()) {
		return fmt.Errorf("%w: permit is not granted or has expired", ErrBudgetBlocked)
	}
	return nil
}
