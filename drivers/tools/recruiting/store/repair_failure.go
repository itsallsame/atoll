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
	repairWork, err := model.NewWork("repair-work-"+suffix, "repair_incident", "repair-incident-"+suffix, "repair", "automatic")
	if err == nil {
		repairWork, err = repairWork.WithCausality("system:recruiting-repair", "", "")
	}
	if err != nil {
		return repairFailure{}, err
	}
	incident, err := model.NewRepairIncident("repair-incident-"+suffix, domain, domainKey,
		report.StableSignature(), failingVersion, work.WorkID)
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

func openOrJoinRepairFailureTx(ctx context.Context, tx *sql.Tx, failure repairFailure,
	affectedWorkID, causeID string, at time.Time) (model.RepairIncident, bool, error) {
	failure.Placement.NotBefore = at.UTC()
	state, _ := json.Marshal(failure.Incident)
	result, err := tx.ExecContext(ctx, `INSERT IGNORE INTO recruiting_repair_incidents(
  incident_id, repair_key, failure_domain, domain_key, failure_signature,
  failing_version, repair_work_id, repair_status, version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, failure.Incident.IncidentID, failure.Incident.RepairKey,
		failure.Incident.Domain, failure.Incident.DomainKey, failure.Incident.FailureSignature,
		failure.Incident.FailingVersion, failure.Incident.RepairWorkID, failure.Incident.Status,
		failure.Incident.Version, state, at.UTC(), at.UTC())
	if err != nil {
		return model.RepairIncident{}, false, fmt.Errorf("open repair incident: %w", err)
	}
	createdRows, _ := result.RowsAffected()
	created := createdRows == 1
	var existingState []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_repair_incidents
WHERE repair_key = ?`, failure.Incident.RepairKey).Scan(&existingState); err != nil {
		return model.RepairIncident{}, false, fmt.Errorf("read repair incident: %w", err)
	}
	var incident model.RepairIncident
	if err := json.Unmarshal(existingState, &incident); err != nil {
		return model.RepairIncident{}, false, err
	}
	if incident.RepairKey != failure.Incident.RepairKey || incident.RepairWorkID != failure.Work.WorkID {
		return model.RepairIncident{}, false, fmt.Errorf("repair incident identity collision")
	}
	// Locking the smaller single-flight row before touching the shared Work key
	// gives every failure transaction the same order. The reverse Incident→Work
	// foreign key is intentionally absent; the exact Work is verified here,
	// while affected Work rows retain their database-enforced self-reference.
	if err := insertWork(ctx, tx, failure.Work, failure.Placement, at); err != nil {
		if !errors.Is(err, ErrBusinessKeyExists) {
			return model.RepairIncident{}, false, err
		}
		existing, readErr := getWorkWith(ctx, tx, failure.Work.WorkID, true)
		if readErr != nil {
			return model.RepairIncident{}, false, fmt.Errorf("read competing repair Work: %w", readErr)
		}
		if existing != failure.Work {
			return model.RepairIncident{}, false, fmt.Errorf("repair Work identity collision: stored=%+v expected=%+v",
				existing, failure.Work)
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
