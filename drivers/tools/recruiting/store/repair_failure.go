package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type repairFailure struct {
	Incident  model.RepairIncident
	Work      model.Work
	Placement WorkPlacement
}

func (r *Repository) lockRepairFailureCommand(ctx context.Context, command ExecutionTransitionCommand,
	at time.Time) (func(), error) {
	if command.Action != "fail" || command.Failure == nil {
		return func() {}, nil
	}
	attempt, err := r.GetAttempt(ctx, command.AttemptID)
	if err != nil {
		return nil, err
	}
	record, err := r.GetWorkRecord(ctx, attempt.WorkID)
	if err != nil {
		return nil, err
	}
	decision, err := command.FailurePolicy.Decide(record.Work, *command.Failure, at)
	if err != nil {
		return nil, err
	}
	if decision.Route != model.FailureHuman {
		return func() {}, nil
	}
	failure, err := newRepairFailure(record.Work, attempt, record.Placement, *command.Failure,
		command.FailurePolicy.Version, at)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256([]byte(failure.Incident.RepairKey))
	stripe := &r.repairStripes[(uint16(digest[0])<<8|uint16(digest[1]))%uint16(len(r.repairStripes))]
	stripe.Lock()
	return stripe.Unlock, nil
}

func newRepairFailure(work model.Work, attempt model.Attempt, placement WorkPlacement,
	report executioncontract.FailureReport, policyVersion uint64, at time.Time) (repairFailure, error) {
	domain := failureDomainFor(work, attempt, placement, report.Class)
	domainKey, failingVersion, err := failureDomainIdentity(domain, work, attempt, placement, policyVersion)
	if err != nil {
		return repairFailure{}, err
	}
	repairKey, err := model.RepairKey(domain, domainKey, report.StableSignature(), failingVersion)
	if err != nil {
		return repairFailure{}, err
	}
	digest := sha256.Sum256([]byte(repairKey))
	suffix := hex.EncodeToString(digest[:16])
	return repairFailureWithSuffix(work, domain, domainKey, report.StableSignature(), failingVersion, repairKey, suffix, at)
}

func repairFailureWithSuffix(affected model.Work, domain model.FailureDomain, domainKey, signature, failingVersion,
	repairKey, suffix string, at time.Time) (repairFailure, error) {
	repairWork, err := model.NewWork("repair-work-"+suffix, "repair_incident", "repair-incident-"+suffix, "repair", "automatic")
	if err == nil {
		repairWork, err = repairWork.WithCausality("system:recruiting-repair", "", "")
	}
	if err != nil {
		return repairFailure{}, err
	}
	incident, err := model.NewRepairIncident("repair-incident-"+suffix, domain, domainKey,
		signature, failingVersion, affected.WorkID)
	if err == nil {
		incident, err = incident.WithRepairWork(repairWork.WorkID)
	}
	if err != nil {
		return repairFailure{}, err
	}
	return repairFailure{Incident: incident, Work: repairWork, Placement: WorkPlacement{
		BusinessKey: "repair|" + suffix, Priority: 500, NotBefore: at.UTC(),
	}}, nil
}

func rekeyResolvedRepairFailure(failure repairFailure, affectedWorkID, causeID string, at time.Time) (repairFailure, error) {
	digest := sha256.Sum256([]byte(failure.Incident.RepairKey + "\nregression\n" + affectedWorkID + "\n" + causeID))
	suffix := hex.EncodeToString(digest[:16])
	affected := model.Work{WorkID: affectedWorkID}
	return repairFailureWithSuffix(affected, failure.Incident.Domain, failure.Incident.DomainKey,
		failure.Incident.FailureSignature, failure.Incident.FailingVersion, failure.Incident.RepairKey, suffix, at)
}

func failureDomainFor(work model.Work, attempt model.Attempt, placement WorkPlacement, failureClass string) model.FailureDomain {
	switch failureClass {
	case "auth_expired", "captcha":
		if attempt.ProfileID != "" {
			return model.FailureProfile
		}
	case "parse_error", "contract_violated":
		if attempt.RecipeID != "" {
			return model.FailureRecipeVersion
		}
	case "quality_rejected":
		if work.Purpose == "detail_sync" {
			return model.FailureSingleTarget
		}
		if attempt.RecipeID != "" {
			return model.FailureRecipeVersion
		}
	case "budget_revoked":
		if attempt.ProfileID != "" {
			return model.FailureProfile
		}
		return model.FailureSingleTarget
	}
	if placement.Origin != "" {
		return model.FailureOrigin
	}
	return model.FailureSingleTarget
}

func failureDomainIdentity(domain model.FailureDomain, work model.Work, attempt model.Attempt,
	placement WorkPlacement, policyVersion uint64) (string, string, error) {
	switch domain {
	case model.FailureOrigin:
		if placement.Origin == "" || policyVersion == 0 {
			return "", "", fmt.Errorf("origin repair requires frozen origin and failure policy version")
		}
		return placement.Origin, "policy:" + strconv.FormatUint(policyVersion, 10), nil
	case model.FailureRecipeVersion:
		if attempt.RecipeID == "" || attempt.RecipeVersion == 0 {
			return "", "", fmt.Errorf("Recipe repair requires frozen Recipe identity")
		}
		return attempt.RecipeID, "recipe:" + strconv.FormatUint(attempt.RecipeVersion, 10), nil
	case model.FailureProfile:
		if attempt.ProfileID == "" || attempt.ProfileVersion == 0 {
			return "", "", fmt.Errorf("Profile repair requires frozen Profile identity")
		}
		return attempt.ProfileID, "profile:" + strconv.FormatUint(attempt.ProfileVersion, 10), nil
	case model.FailureSingleTarget:
		if strings.TrimSpace(work.TargetType) == "" || strings.TrimSpace(work.TargetID) == "" {
			return "", "", fmt.Errorf("single-target repair requires Work target identity")
		}
		version := "acceptance:" + strconv.FormatUint(work.AcceptanceVersion, 10)
		if attempt.RefreshGeneration != 0 {
			version = "refresh:" + strconv.FormatUint(attempt.RefreshGeneration, 10)
		}
		return work.TargetType + ":" + work.TargetID, version, nil
	default:
		return "", "", fmt.Errorf("unknown failure domain %q", domain)
	}
}

func beginProfileRepairForFailureTx(ctx context.Context, tx *sql.Tx, attempt model.Attempt,
	incident model.RepairIncident, causeCommandID string, at time.Time) error {
	if attempt.ProfileID == "" || attempt.ProfileVersion == 0 || incident.Domain != model.FailureProfile ||
		incident.DomainKey != attempt.ProfileID || strings.TrimSpace(causeCommandID) == "" || at.IsZero() {
		return fmt.Errorf("Profile repair failure facts are inconsistent")
	}
	var state []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_profiles
WHERE profile_id = ? FOR UPDATE`, attempt.ProfileID).Scan(&state); err != nil {
		return fmt.Errorf("lock failed Profile: %w", err)
	}
	var current model.BrowserProfile
	if err := json.Unmarshal(state, &current); err != nil {
		return err
	}
	if current.ProfileID != attempt.ProfileID {
		return fmt.Errorf("failed Profile identity is inconsistent")
	}
	// Failure evidence is still accepted after a concurrent failure has fenced
	// this Profile. Only the Attempt bound to the current ready version may
	// advance it; older in-flight Attempts join the same incident without
	// fencing a repaired or disabled Profile again.
	if current.Version != attempt.ProfileVersion || current.AuthStatus != model.ProfileReady {
		return nil
	}
	next, err := current.BeginRepair(current.Version)
	if err != nil {
		return err
	}
	nextState, _ := json.Marshal(next)
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_profiles
SET auth_status = ?, version = ?, state_json = ?, updated_at = ?
WHERE profile_id = ? AND version = ? AND auth_status = ?`, next.AuthStatus, next.Version, nextState,
		at.UTC(), current.ProfileID, current.Version, current.AuthStatus)
	if err != nil {
		return fmt.Errorf("fence failed Profile: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return fmt.Errorf("%w: Profile changed during authentication failure", ErrResultFenced)
	}
	payload, _ := json.Marshal(map[string]any{"profile_id": current.ProfileID,
		"failed_profile_version": current.Version, "repair_incident_id": incident.IncidentID,
		"repair_work_id": incident.RepairWorkID})
	event, err := model.NewEventIntent("profile-repair-required-"+attempt.AttemptID,
		"profile.repair_required", "profile", current.ProfileID, next.Version,
		at.UTC().Format(time.RFC3339Nano), causeCommandID, payload)
	if err != nil {
		return err
	}
	return appendEventIntent(ctx, tx, event, at, at)
}

func openOrJoinRepairFailureTx(ctx context.Context, tx *sql.Tx, failure repairFailure,
	affectedWorkID, causeID string, at time.Time) (model.RepairIncident, bool, error) {
	failure.Placement.NotBefore = at.UTC()
	state, _ := json.Marshal(failure.Incident)
	result, err := tx.ExecContext(ctx, `INSERT IGNORE INTO recruiting_repair_incidents(
  incident_id, repair_key, active_repair_key, failure_domain, domain_key, failure_signature,
  failing_version, repair_work_id, repair_status, version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, failure.Incident.IncidentID, failure.Incident.RepairKey, failure.Incident.RepairKey,
		failure.Incident.Domain, failure.Incident.DomainKey, failure.Incident.FailureSignature,
		failure.Incident.FailingVersion, failure.Incident.RepairWorkID, failure.Incident.Status,
		failure.Incident.Version, state, at.UTC(), at.UTC())
	if err != nil {
		return model.RepairIncident{}, false, fmt.Errorf("open repair incident: %w", err)
	}
	createdRows, _ := result.RowsAffected()
	created := createdRows == 1
	if !created {
		var activeID string
		activeErr := tx.QueryRowContext(ctx, `SELECT incident_id FROM recruiting_repair_incidents
WHERE active_repair_key = ?`, failure.Incident.RepairKey).Scan(&activeID)
		if errors.Is(activeErr, sql.ErrNoRows) {
			failure, err = rekeyResolvedRepairFailure(failure, affectedWorkID, causeID, at)
			if err != nil {
				return model.RepairIncident{}, false, err
			}
			state, _ = json.Marshal(failure.Incident)
			result, err = tx.ExecContext(ctx, `INSERT IGNORE INTO recruiting_repair_incidents(
  incident_id, repair_key, active_repair_key, failure_domain, domain_key, failure_signature,
  failing_version, repair_work_id, repair_status, version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, failure.Incident.IncidentID, failure.Incident.RepairKey, failure.Incident.RepairKey,
				failure.Incident.Domain, failure.Incident.DomainKey, failure.Incident.FailureSignature,
				failure.Incident.FailingVersion, failure.Incident.RepairWorkID, failure.Incident.Status,
				failure.Incident.Version, state, at.UTC(), at.UTC())
			if err != nil {
				return model.RepairIncident{}, false, fmt.Errorf("open regressed repair incident: %w", err)
			}
			createdRows, _ = result.RowsAffected()
			created = createdRows == 1
		} else if activeErr != nil {
			return model.RepairIncident{}, false, activeErr
		}
	}
	var existingState []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_repair_incidents
WHERE active_repair_key = ?`, failure.Incident.RepairKey).Scan(&existingState); err != nil {
		return model.RepairIncident{}, false, fmt.Errorf("read repair incident: %w", err)
	}
	var incident model.RepairIncident
	if err := json.Unmarshal(existingState, &incident); err != nil {
		return model.RepairIncident{}, false, err
	}
	if incident.RepairKey != failure.Incident.RepairKey {
		return model.RepairIncident{}, false, fmt.Errorf("repair incident identity collision")
	}
	// Locking the smaller single-flight row before touching the shared Work key
	// gives every failure transaction the same order. The reverse Incident→Work
	// foreign key is intentionally absent; the exact Work is verified here,
	// while affected Work rows retain their database-enforced self-reference.
	if created {
		if err := insertWork(ctx, tx, failure.Work, failure.Placement, at); err != nil {
			return model.RepairIncident{}, false, err
		}
	} else {
		existing, readErr := getWorkWith(ctx, tx, incident.RepairWorkID, true)
		if readErr != nil {
			return model.RepairIncident{}, false, fmt.Errorf("read competing repair Work: %w", readErr)
		}
		if existing.WorkID != incident.RepairWorkID || existing.TargetID != incident.IncidentID || existing.Purpose != "repair" {
			return model.RepairIncident{}, false, fmt.Errorf("repair Work identity collision: stored=%+v incident=%+v",
				existing, incident)
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO recruiting_repair_affected_works(incident_id, work_id, created_at)
VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE incident_id = VALUES(incident_id)`,
		incident.IncidentID, affectedWorkID, at.UTC()); err != nil {
		return model.RepairIncident{}, false, fmt.Errorf("link affected Work to repair: %w", err)
	}
	if created {
		payload, _ := json.Marshal(map[string]any{"repair_work_id": incident.RepairWorkID,
			"failure_domain": incident.Domain, "failure_signature": incident.FailureSignature,
			"failing_version": incident.FailingVersion, "first_affected_work_id": affectedWorkID})
		event, eventErr := model.NewEventIntent("repair-opened-"+incident.IncidentID, "repair.opened",
			"repair_incident", incident.IncidentID, incident.Version, at.UTC().Format(time.RFC3339Nano), causeID, payload)
		if eventErr != nil {
			return model.RepairIncident{}, false, eventErr
		}
		if eventErr = appendEventIntent(ctx, tx, event, at, at); eventErr != nil {
			return model.RepairIncident{}, false, eventErr
		}
	}
	return incident, created, nil
}
