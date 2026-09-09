package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func (r *Repository) ApplyRepairValidationCommand(ctx context.Context, expectedVersion uint64,
	next model.RepairIncident, repairWork model.Work, receipt model.CommandReceipt, repairEvent, workEvent model.EventIntent,
	businessAt time.Time) (CommandResult, error) {
	if expectedVersion == 0 || next.Version != expectedVersion+1 || next.Status != model.RepairValidating ||
		next.ValidationWorkID == "" || repairWork.WorkID != next.RepairWorkID || repairWork.Status != model.WorkRunning ||
		receipt.CommandID == "" || repairEvent.AggregateType != "repair_incident" || repairEvent.AggregateID != next.IncidentID ||
		repairEvent.AggregateVersion != next.Version || repairEvent.CauseCommandID != receipt.CommandID ||
		workEvent.AggregateType != "work" || workEvent.AggregateID != repairWork.WorkID ||
		workEvent.AggregateVersion != repairWork.Version || workEvent.CauseCommandID != receipt.CommandID {
		return CommandResult{}, fmt.Errorf("repair validation command facts are inconsistent")
	}
	repairEventAt, err := time.Parse(time.RFC3339, repairEvent.BusinessAt)
	if err != nil {
		return CommandResult{}, err
	}
	workEventAt, err := time.Parse(time.RFC3339, workEvent.BusinessAt)
	if err != nil || !repairEventAt.Equal(businessAt) || !workEventAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("repair validation business time is inconsistent")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	current, err := getRepairIncidentForUpdate(ctx, tx, next.IncidentID)
	if err != nil {
		return CommandResult{}, err
	}
	derived, err := current.BeginValidationWithWork(expectedVersion, next.ValidationWorkID)
	if err != nil || !reflect.DeepEqual(derived, next) {
		if current.Version != expectedVersion {
			return CommandResult{}, &model.VersionConflictError{Expected: expectedVersion, Actual: current.Version}
		}
		if err != nil {
			return CommandResult{}, err
		}
		return CommandResult{}, fmt.Errorf("repair validation does not match locked state")
	}
	if err := verifyRepairValidationWork(ctx, tx, current, next.ValidationWorkID); err != nil {
		return CommandResult{}, err
	}
	currentRepairWork, err := getWorkWith(ctx, tx, current.RepairWorkID, true)
	if err != nil {
		return CommandResult{}, err
	}
	derivedRepairWork, err := currentRepairWork.Start(currentRepairWork.Version)
	if err != nil || !reflect.DeepEqual(derivedRepairWork, repairWork) {
		if err != nil {
			return CommandResult{}, err
		}
		return CommandResult{}, fmt.Errorf("repair validation Work transition does not match locked state")
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	if err := updateRepairIncidentTx(ctx, tx, expectedVersion, next, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := updateRecoveredWorkTx(ctx, tx, currentRepairWork.Version, repairWork, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, repairEvent, repairEventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, workEvent, workEventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, err
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func (r *Repository) ApplyRepairResolveCommand(ctx context.Context, expectedVersion uint64, next model.RepairIncident,
	repairWork model.Work, receipt model.CommandReceipt, repairEvent, workEvent model.EventIntent,
	businessAt time.Time) (CommandResult, error) {
	if expectedVersion == 0 || next.Version != expectedVersion+1 || next.Status != model.RepairResolved ||
		next.ValidationWorkID == "" || repairWork.WorkID != next.RepairWorkID || repairWork.Status != model.WorkCompleted ||
		repairWork.Resolution != model.ResolutionSucceeded || receipt.CommandID == "" ||
		repairEvent.AggregateType != "repair_incident" || repairEvent.AggregateID != next.IncidentID ||
		repairEvent.AggregateVersion != next.Version || workEvent.AggregateType != "work" ||
		workEvent.AggregateID != repairWork.WorkID || workEvent.AggregateVersion != repairWork.Version ||
		repairEvent.CauseCommandID != receipt.CommandID || workEvent.CauseCommandID != receipt.CommandID {
		return CommandResult{}, fmt.Errorf("repair resolve command facts are inconsistent")
	}
	repairEventAt, err := time.Parse(time.RFC3339, repairEvent.BusinessAt)
	if err != nil {
		return CommandResult{}, err
	}
	workEventAt, err := time.Parse(time.RFC3339, workEvent.BusinessAt)
	if err != nil || !repairEventAt.Equal(businessAt) || !workEventAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("repair resolve business times are inconsistent")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	current, err := getRepairIncidentForUpdate(ctx, tx, next.IncidentID)
	if err != nil {
		return CommandResult{}, err
	}
	derived, err := current.Resolve(expectedVersion, next.Resolution)
	if err != nil || !reflect.DeepEqual(derived, next) {
		if current.Version != expectedVersion {
			return CommandResult{}, &model.VersionConflictError{Expected: expectedVersion, Actual: current.Version}
		}
		if err != nil {
			return CommandResult{}, err
		}
		return CommandResult{}, fmt.Errorf("repair resolution does not match locked state")
	}
	if err := verifyRepairValidationWork(ctx, tx, current, current.ValidationWorkID); err != nil {
		return CommandResult{}, err
	}
	currentRepairWork, err := getWorkWith(ctx, tx, current.RepairWorkID, true)
	if err != nil {
		return CommandResult{}, err
	}
	completed, err := currentRepairWork.Complete(currentRepairWork.Version, model.ResolutionSucceeded, "", "")
	if err == nil && !reflect.DeepEqual(completed, repairWork) {
		err = fmt.Errorf("derived Repair Work completion differs")
	}
	if err != nil {
		return CommandResult{}, err
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	if err := updateRepairIncidentTx(ctx, tx, expectedVersion, next, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := updateRecoveredWorkTx(ctx, tx, currentRepairWork.Version, repairWork, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, repairEvent, repairEventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, workEvent, workEventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, err
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

type RepairRecoveryPreparation struct {
	Incident        model.RepairIncident
	Works           []WorkRecord
	RemainingBefore int
}

func (r *Repository) PrepareRepairRecovery(ctx context.Context, incidentID string, expectedVersion uint64, limit int) (RepairRecoveryPreparation, error) {
	if expectedVersion == 0 || limit < 1 || limit > 100 {
		return RepairRecoveryPreparation{}, fmt.Errorf("repair recovery requires expected version and limit in [1,100]")
	}
	overview, err := r.GetRepairIncident(ctx, incidentID)
	if err != nil {
		return RepairRecoveryPreparation{}, err
	}
	if overview.Incident.Version != expectedVersion {
		return RepairRecoveryPreparation{}, &model.VersionConflictError{Expected: expectedVersion, Actual: overview.Incident.Version}
	}
	if overview.Incident.Status != model.RepairResolved {
		return RepairRecoveryPreparation{}, &model.InvalidTransitionError{Entity: "repair incident", From: string(overview.Incident.Status), Action: "recover affected work"}
	}
	repairWork, err := r.GetWork(ctx, overview.Incident.RepairWorkID)
	if err != nil {
		return RepairRecoveryPreparation{}, err
	}
	if repairWork.Status != model.WorkCompleted || repairWork.Resolution != model.ResolutionSucceeded {
		return RepairRecoveryPreparation{}, fmt.Errorf("%w: Repair Work is not successfully completed", ErrRepairEvidenceRejected)
	}
	aggregate, err := r.GetRepairIncidentAggregate(ctx, incidentID)
	if err != nil {
		return RepairRecoveryPreparation{}, err
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT w.work_id FROM recruiting_repair_affected_works a
JOIN recruiting_works w ON w.work_id = a.work_id
WHERE a.incident_id = ? AND w.status = 'waiting_human' AND w.blocked_by_repair_work_id = ?
ORDER BY a.work_id LIMIT ?`, incidentID, overview.Incident.RepairWorkID, limit)
	if err != nil {
		return RepairRecoveryPreparation{}, err
	}
	defer rows.Close()
	ids := make([]string, 0, limit)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return RepairRecoveryPreparation{}, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return RepairRecoveryPreparation{}, err
	}
	preparation := RepairRecoveryPreparation{Incident: aggregate, RemainingBefore: overview.WaitingHumanCount,
		Works: make([]WorkRecord, 0, len(ids))}
	for _, id := range ids {
		record, err := r.GetWorkRecord(ctx, id)
		if err != nil {
			return RepairRecoveryPreparation{}, err
		}
		preparation.Works = append(preparation.Works, record)
	}
	return preparation, nil
}

func (r *Repository) ApplyRepairRecoveryCommand(ctx context.Context, expectedVersion uint64, next model.RepairIncident,
	recovered []model.Work, receipt model.CommandReceipt, event model.EventIntent, dispatches []ExecutionDispatchIntent,
	businessAt time.Time) (CommandResult, error) {
	if expectedVersion == 0 || len(recovered) == 0 || len(recovered) > 100 || next.Version != expectedVersion+1 ||
		next.Status != model.RepairResolved || next.RecoveredWorks < uint64(len(recovered)) || receipt.CommandID == "" ||
		event.AggregateType != "repair_incident" || event.AggregateID != next.IncidentID ||
		event.AggregateVersion != next.Version || event.CauseCommandID != receipt.CommandID {
		return CommandResult{}, fmt.Errorf("repair recovery command facts are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil || !eventAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("repair recovery business time is inconsistent")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	current, err := getRepairIncidentForUpdate(ctx, tx, next.IncidentID)
	if err != nil {
		return CommandResult{}, err
	}
	derivedIncident, err := current.RecordRecoveryBatch(expectedVersion, uint64(len(recovered)))
	if err != nil || !reflect.DeepEqual(derivedIncident, next) {
		if current.Version != expectedVersion {
			return CommandResult{}, &model.VersionConflictError{Expected: expectedVersion, Actual: current.Version}
		}
		if err != nil {
			return CommandResult{}, err
		}
		return CommandResult{}, fmt.Errorf("repair recovery does not match locked state")
	}
	repairWork, err := getWorkWith(ctx, tx, current.RepairWorkID, true)
	if err != nil {
		return CommandResult{}, err
	}
	if repairWork.Status != model.WorkCompleted || repairWork.Resolution != model.ResolutionSucceeded {
		return CommandResult{}, fmt.Errorf("%w: Repair Work is not successfully completed", ErrRepairEvidenceRejected)
	}
	rows, err := tx.QueryContext(ctx, `
SELECT w.state_json, COALESCE(w.capability, '') FROM recruiting_repair_affected_works a
JOIN recruiting_works w ON w.work_id = a.work_id
WHERE a.incident_id = ? AND w.status = 'waiting_human' AND w.blocked_by_repair_work_id = ?
ORDER BY a.work_id LIMIT ? FOR UPDATE`, current.IncidentID, current.RepairWorkID, len(recovered))
	if err != nil {
		return CommandResult{}, err
	}
	locked := make([]model.Work, 0, len(recovered))
	allowedCapabilities := make(map[string]struct{})
	for rows.Next() {
		var state []byte
		var capability string
		var work model.Work
		if err := rows.Scan(&state, &capability); err != nil {
			_ = rows.Close()
			return CommandResult{}, err
		}
		if err := json.Unmarshal(state, &work); err != nil {
			_ = rows.Close()
			return CommandResult{}, err
		}
		locked = append(locked, work)
		allowedCapabilities[capability] = struct{}{}
	}
	if err := rows.Close(); err != nil {
		return CommandResult{}, err
	}
	if len(locked) != len(recovered) {
		return CommandResult{}, ErrProgressConflict
	}
	for index, work := range locked {
		derived, err := work.RecoverFromRepair(work.Version, current.RepairWorkID)
		if err != nil || !reflect.DeepEqual(derived, recovered[index]) {
			return CommandResult{}, ErrProgressConflict
		}
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	if err := updateRepairIncidentTx(ctx, tx, expectedVersion, next, businessAt); err != nil {
		return CommandResult{}, err
	}
	for index, work := range recovered {
		if err := updateRecoveredWorkTx(ctx, tx, locked[index].Version, work, businessAt); err != nil {
			return CommandResult{}, err
		}
	}
	seenDispatchCapabilities := make(map[string]struct{}, len(dispatches))
	for _, dispatch := range dispatches {
		if dispatch.CauseKind != "repair_recovery" || dispatch.CauseID != receipt.CommandID ||
			dispatch.Origin != "" || dispatch.ProfileID != "" || !dispatch.NextAttemptAt.Equal(businessAt) {
			return CommandResult{}, fmt.Errorf("repair recovery dispatch is inconsistent")
		}
		if _, allowed := allowedCapabilities[dispatch.Capability]; !allowed || dispatch.Capability == "" {
			return CommandResult{}, fmt.Errorf("repair recovery dispatch capability is not in the recovered batch")
		}
		if _, duplicate := seenDispatchCapabilities[dispatch.Capability]; duplicate {
			return CommandResult{}, fmt.Errorf("repair recovery has more than one initial wake per capability")
		}
		seenDispatchCapabilities[dispatch.Capability] = struct{}{}
		if err := appendExecutionDispatch(ctx, tx, dispatch, businessAt); err != nil {
			return CommandResult{}, err
		}
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, err
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func getRepairIncidentForUpdate(ctx context.Context, tx *sql.Tx, incidentID string) (model.RepairIncident, error) {
	var state []byte
	var repairWorkID, validationWorkID sql.NullString
	var recovered uint64
	err := tx.QueryRowContext(ctx, `SELECT state_json, repair_work_id, validation_work_id, recovered_work_count
FROM recruiting_repair_incidents WHERE incident_id = ? FOR UPDATE`, incidentID).Scan(&state, &repairWorkID, &validationWorkID, &recovered)
	if errors.Is(err, sql.ErrNoRows) {
		return model.RepairIncident{}, ErrNotFound
	}
	if err != nil {
		return model.RepairIncident{}, err
	}
	var incident model.RepairIncident
	if err := json.Unmarshal(state, &incident); err != nil {
		return model.RepairIncident{}, err
	}
	incident.RepairWorkID, incident.ValidationWorkID, incident.RecoveredWorks = repairWorkID.String, validationWorkID.String, recovered
	return incident, nil
}

func verifyRepairValidationWork(ctx context.Context, tx *sql.Tx, incident model.RepairIncident, validationWorkID string) error {
	work, err := getWorkWith(ctx, tx, validationWorkID, true)
	if err != nil {
		return err
	}
	if work.Status != model.WorkCompleted || work.Resolution != model.ResolutionSucceeded || work.CauseWorkID == "" {
		return fmt.Errorf("%w: requires an executor-succeeded causal retry Work", ErrRepairEvidenceRejected)
	}
	var member int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_repair_affected_works
WHERE incident_id = ? AND work_id = ?`, incident.IncidentID, work.CauseWorkID).Scan(&member); err != nil {
		return err
	}
	if member != 1 {
		return fmt.Errorf("%w: Work is not caused by an affected member", ErrRepairEvidenceRejected)
	}
	cause, err := getWorkWith(ctx, tx, work.CauseWorkID, true)
	if err != nil {
		return err
	}
	if !cause.Terminal() || cause.TargetType != work.TargetType || cause.TargetID != work.TargetID || cause.Purpose != work.Purpose {
		return fmt.Errorf("%w: validation Work does not preserve its affected cause target and purpose", ErrRepairEvidenceRejected)
	}
	var succeededAttempts int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_attempts
WHERE work_id = ? AND attempt_status = 'succeeded' AND acceptance_version = ?`, work.WorkID, work.AcceptanceVersion).
		Scan(&succeededAttempts); err != nil {
		return err
	}
	if succeededAttempts != 1 {
		return fmt.Errorf("%w: validation Work has no unique succeeded Attempt", ErrRepairEvidenceRejected)
	}
	return nil
}

func updateRepairIncidentTx(ctx context.Context, tx *sql.Tx, expected uint64, incident model.RepairIncident, at time.Time) error {
	state, err := json.Marshal(incident)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_repair_incidents
SET repair_status = ?, active_repair_key = ?, validation_work_id = ?, recovered_work_count = ?, version = ?, state_json = ?, updated_at = ?
WHERE incident_id = ? AND version = ?`, incident.Status, nullableString(activeRepairKey(incident)),
		nullableString(incident.ValidationWorkID), incident.RecoveredWorks, incident.Version, state, at.UTC(), incident.IncidentID, expected)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrProgressConflict
	}
	return nil
}

func activeRepairKey(incident model.RepairIncident) string {
	if incident.Status == model.RepairOpen || incident.Status == model.RepairValidating {
		return incident.RepairKey
	}
	return ""
}

func updateRecoveredWorkTx(ctx context.Context, tx *sql.Tx, expected uint64, work model.Work, at time.Time) error {
	state, err := json.Marshal(work)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_works
SET status = ?, resolution = ?, blocked_by_repair_work_id = ?, acceptance_version = ?, version = ?, state_json = ?, updated_at = ?
WHERE work_id = ? AND version = ?`, work.Status, nullableString(string(work.Resolution)), nullableString(work.BlockedByRepairWorkID),
		work.AcceptanceVersion, work.Version, state, at.UTC(), work.WorkID, expected)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrProgressConflict
	}
	return nil
}
