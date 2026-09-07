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

func (r *Repository) CreateProfile(ctx context.Context, profile model.BrowserProfile, businessAt time.Time) error {
	if profile.ProfileID == "" || profile.Version != 1 || profile.SecretRef == "" {
		return fmt.Errorf("new profile identity, opaque secret reference, and version 1 are required")
	}
	state, _ := json.Marshal(profile)
	_, err := r.db.ExecContext(ctx, `
INSERT INTO recruiting_profiles(
  profile_id, security_domain, device_id, secret_ref, auth_status, version, state_json, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		profile.ProfileID, profile.SecurityDomain, profile.DeviceID, profile.SecretRef,
		profile.AuthStatus, profile.Version, state, businessAt.UTC())
	if err != nil {
		return fmt.Errorf("create profile: %w", err)
	}
	return nil
}

func (r *Repository) GetProfile(ctx context.Context, profileID string) (model.BrowserProfile, error) {
	var state []byte
	err := r.db.QueryRowContext(ctx, "SELECT state_json FROM recruiting_profiles WHERE profile_id = ?", profileID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.BrowserProfile{}, ErrNotFound
	}
	if err != nil {
		return model.BrowserProfile{}, fmt.Errorf("get profile: %w", err)
	}
	var profile model.BrowserProfile
	if err := json.Unmarshal(state, &profile); err != nil {
		return model.BrowserProfile{}, fmt.Errorf("decode profile: %w", err)
	}
	return profile, nil
}

func (r *Repository) UpdateProfileCAS(ctx context.Context, expected uint64, profile model.BrowserProfile, businessAt time.Time) error {
	if profile.Version != expected+1 {
		return fmt.Errorf("profile update must advance exactly one expected version")
	}
	state, _ := json.Marshal(profile)
	result, err := r.db.ExecContext(ctx, `
UPDATE recruiting_profiles
SET security_domain = ?, device_id = ?, secret_ref = ?, auth_status = ?,
    version = ?, state_json = ?, updated_at = ?
WHERE profile_id = ? AND version = ?`,
		profile.SecurityDomain, profile.DeviceID, profile.SecretRef, profile.AuthStatus,
		profile.Version, state, businessAt.UTC(), profile.ProfileID, expected)
	if err != nil {
		return fmt.Errorf("update profile: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed == 1 {
		return nil
	}
	actual, readErr := r.GetProfile(ctx, profile.ProfileID)
	if errors.Is(readErr, ErrNotFound) {
		return ErrNotFound
	}
	if readErr != nil {
		return readErr
	}
	return &model.VersionConflictError{Expected: expected, Actual: actual.Version}
}

func (r *Repository) CreateBudgetPermit(ctx context.Context, permit model.BudgetPermit, expiresAt time.Time) error {
	if permit.PermitID == "" || permit.Version != 1 || expiresAt.IsZero() {
		return fmt.Errorf("new budget permit identity, version 1, and expiry are required")
	}
	state, _ := json.Marshal(permit)
	_, err := r.db.ExecContext(ctx, `
INSERT INTO recruiting_budget_permits(
  permit_id, attempt_id, origin, profile_id, capability, company_id,
  policy_version, permit_status, version, expires_at, state_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		permit.PermitID, permit.AttemptID, permit.Origin, nullableString(permit.ProfileID), permit.Capability,
		permit.CompanyID, permit.PolicyVersion, permit.Status, permit.Version, expiresAt.UTC(), state)
	if err == nil {
		return nil
	}
	var mysqlError *mysql.MySQLError
	if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
		return fmt.Errorf("%w: one permit per attempt", ErrBusinessKeyExists)
	}
	return fmt.Errorf("create budget permit: %w", err)
}

func (r *Repository) UpdateBudgetPermitCAS(ctx context.Context, expected uint64, permit model.BudgetPermit) error {
	if permit.Version != expected+1 {
		return fmt.Errorf("budget permit update must advance exactly one expected version")
	}
	state, _ := json.Marshal(permit)
	result, err := r.db.ExecContext(ctx, `
UPDATE recruiting_budget_permits
SET permit_status = ?, version = ?, state_json = ?
WHERE permit_id = ? AND version = ?`, permit.Status, permit.Version, state, permit.PermitID, expected)
	if err != nil {
		return fmt.Errorf("update budget permit: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		var actual uint64
		err := r.db.QueryRowContext(ctx, "SELECT version FROM recruiting_budget_permits WHERE permit_id = ?", permit.PermitID).Scan(&actual)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("read budget permit after failed CAS: %w", err)
		}
		return &model.VersionConflictError{Expected: expected, Actual: actual}
	}
	return nil
}

// OpenOrJoinRepair uses repair_key as the single-flight identity. A duplicate
// failure only links another affected Work; it does not create another repair.
func (r *Repository) OpenOrJoinRepair(ctx context.Context, incident model.RepairIncident, businessAt time.Time) (model.RepairIncident, bool, error) {
	if incident.IncidentID == "" || incident.RepairKey == "" || len(incident.AffectedWorkIDs) != 1 {
		return model.RepairIncident{}, false, fmt.Errorf("new repair incident requires identity, key, and one first work")
	}
	requestedWorkID := incident.AffectedWorkIDs[0]
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return model.RepairIncident{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	state, _ := json.Marshal(incident)
	_, err = tx.ExecContext(ctx, `
INSERT INTO recruiting_repair_incidents(
  incident_id, repair_key, failure_domain, domain_key, failure_signature,
  failing_version, repair_status, version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		incident.IncidentID, incident.RepairKey, incident.Domain, incident.DomainKey,
		incident.FailureSignature, incident.FailingVersion, incident.Status, incident.Version,
		state, businessAt.UTC(), businessAt.UTC())
	joined := false
	if err != nil {
		var mysqlError *mysql.MySQLError
		if !errors.As(err, &mysqlError) || mysqlError.Number != 1062 {
			return model.RepairIncident{}, false, fmt.Errorf("open repair incident: %w", err)
		}
		joined = true
		var existingState []byte
		if err := tx.QueryRowContext(ctx, `
SELECT state_json FROM recruiting_repair_incidents
WHERE repair_key = ? FOR UPDATE`, incident.RepairKey).Scan(&existingState); err != nil {
			return model.RepairIncident{}, false, fmt.Errorf("join repair incident: %w", err)
		}
		if err := json.Unmarshal(existingState, &incident); err != nil {
			return model.RepairIncident{}, false, err
		}
		previousVersion := incident.Version
		incident, err = incident.AddAffectedWork(incident.Version, requestedWorkID)
		if err != nil {
			return model.RepairIncident{}, false, err
		}
		if incident.Version != previousVersion {
			state, _ = json.Marshal(incident)
			result, updateErr := tx.ExecContext(ctx, `
UPDATE recruiting_repair_incidents
SET version = ?, state_json = ?, updated_at = ?
WHERE incident_id = ? AND version = ?`, incident.Version, state, businessAt.UTC(), incident.IncidentID, previousVersion)
			if updateErr != nil {
				return model.RepairIncident{}, false, updateErr
			}
			changed, _ := result.RowsAffected()
			if changed != 1 {
				return model.RepairIncident{}, false, ErrAttemptConflict
			}
		}
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO recruiting_repair_affected_works(incident_id, work_id, created_at)
VALUES (?, ?, ?)
ON DUPLICATE KEY UPDATE incident_id = VALUES(incident_id)`, incident.IncidentID, requestedWorkID, businessAt.UTC())
	if err != nil {
		return model.RepairIncident{}, false, fmt.Errorf("link repair affected work: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return model.RepairIncident{}, false, err
	}
	return incident, joined, nil
}
