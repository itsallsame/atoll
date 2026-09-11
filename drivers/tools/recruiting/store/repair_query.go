package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

// RepairOverview is a bounded operational projection. AffectedWorkIDs in the
// incident JSON is intentionally cleared: the canonical, unbounded membership
// is recruiting_repair_affected_works and is exposed through a seek page.
type RepairOverview struct {
	Incident          model.RepairIncident `json:"incident"`
	AffectedCount     int                  `json:"affected_count"`
	WaitingHumanCount int                  `json:"waiting_human_count"`
}

type RepairIncidentPage struct {
	Items      []RepairOverview
	NextCursor string
	HasMore    bool
}

type RepairRecoveryCandidate struct {
	IncidentID string
	Version    uint64
}

// NextRepairRecoveryCandidate reads the explicit indexed recovery queue. A
// successful batch updates updated_at, so repeated ticks naturally rotate
// across large incidents instead of draining one while starving the rest.
func (r *Repository) NextRepairRecoveryCandidate(ctx context.Context) (RepairRecoveryCandidate, bool, error) {
	var candidate RepairRecoveryCandidate
	err := r.db.QueryRowContext(ctx, `
SELECT i.incident_id, i.version
FROM recruiting_repair_incidents i
WHERE i.recovery_pending = 1 AND i.repair_status = 'resolved'
ORDER BY i.updated_at, i.incident_id
LIMIT 1`).Scan(&candidate.IncidentID, &candidate.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return RepairRecoveryCandidate{}, false, nil
	}
	if err != nil {
		return RepairRecoveryCandidate{}, false, fmt.Errorf("select next repair recovery candidate: %w", err)
	}
	return candidate, true, nil
}

type repairListCursor struct {
	Status     model.RepairStatus `json:"status,omitempty"`
	UpdatedAt  string             `json:"updated_at"`
	IncidentID string             `json:"incident_id"`
}

func validRepairStatus(status model.RepairStatus) bool {
	switch status {
	case model.RepairOpen, model.RepairValidating, model.RepairResolved:
		return true
	default:
		return false
	}
}

func (r *Repository) GetRepairIncident(ctx context.Context, incidentID string) (RepairOverview, error) {
	incidentID = strings.TrimSpace(incidentID)
	if incidentID == "" {
		return RepairOverview{}, fmt.Errorf("repair incident ID is required")
	}
	var state []byte
	var repairWorkID, validationWorkID sql.NullString
	var recovered uint64
	var overview RepairOverview
	err := r.db.QueryRowContext(ctx, `
SELECT i.state_json, i.repair_work_id, i.validation_work_id, i.recovered_work_count,
       (SELECT COUNT(*) FROM recruiting_repair_affected_works a WHERE a.incident_id = i.incident_id),
       (SELECT COUNT(*) FROM recruiting_repair_affected_works a
          JOIN recruiting_works w ON w.work_id = a.work_id
         WHERE a.incident_id = i.incident_id AND w.status = 'waiting_human')
FROM recruiting_repair_incidents i WHERE i.incident_id = ?`, incidentID).Scan(
		&state, &repairWorkID, &validationWorkID, &recovered, &overview.AffectedCount, &overview.WaitingHumanCount)
	if errors.Is(err, sql.ErrNoRows) {
		return RepairOverview{}, ErrNotFound
	}
	if err != nil {
		return RepairOverview{}, fmt.Errorf("get repair incident: %w", err)
	}
	if err := json.Unmarshal(state, &overview.Incident); err != nil {
		return RepairOverview{}, fmt.Errorf("decode repair incident: %w", err)
	}
	overview.Incident.RepairWorkID = repairWorkID.String
	overview.Incident.ValidationWorkID = validationWorkID.String
	overview.Incident.RecoveredWorks = recovered
	overview.Incident.AffectedWorkIDs = nil
	return overview, nil
}

// GetRepairIncidentAggregate returns the complete bounded aggregate state for
// command derivation. Public projections use GetRepairIncident and suppress
// the legacy seed member so callers cannot mistake it for the canonical set.
func (r *Repository) GetRepairIncidentAggregate(ctx context.Context, incidentID string) (model.RepairIncident, error) {
	incidentID = strings.TrimSpace(incidentID)
	if incidentID == "" {
		return model.RepairIncident{}, fmt.Errorf("repair incident ID is required")
	}
	var state []byte
	var repairWorkID, validationWorkID sql.NullString
	var recovered uint64
	err := r.db.QueryRowContext(ctx, `SELECT state_json, repair_work_id, validation_work_id, recovered_work_count
FROM recruiting_repair_incidents WHERE incident_id = ?`, incidentID).Scan(&state, &repairWorkID, &validationWorkID, &recovered)
	if errors.Is(err, sql.ErrNoRows) {
		return model.RepairIncident{}, ErrNotFound
	}
	if err != nil {
		return model.RepairIncident{}, fmt.Errorf("get repair incident aggregate: %w", err)
	}
	var incident model.RepairIncident
	if err := json.Unmarshal(state, &incident); err != nil {
		return model.RepairIncident{}, fmt.Errorf("decode repair incident aggregate: %w", err)
	}
	incident.RepairWorkID, incident.ValidationWorkID, incident.RecoveredWorks = repairWorkID.String, validationWorkID.String, recovered
	return incident, nil
}

func (r *Repository) ListRepairIncidents(ctx context.Context, status model.RepairStatus, cursor string, limit int) (RepairIncidentPage, error) {
	if limit < 1 || limit > 500 || (status != "" && !validRepairStatus(status)) {
		return RepairIncidentPage{}, fmt.Errorf("repair list requires limit in [1,500] and a known status")
	}
	var beforeTime time.Time
	var beforeID string
	if cursor != "" {
		decoded, err := decodeCursor[repairListCursor](cursor)
		if err != nil || decoded.Status != status || decoded.IncidentID == "" {
			return RepairIncidentPage{}, fmt.Errorf("%w: repair selector", ErrInvalidCursor)
		}
		beforeTime, err = time.Parse(time.RFC3339Nano, decoded.UpdatedAt)
		if err != nil {
			return RepairIncidentPage{}, fmt.Errorf("%w: repair time", ErrInvalidCursor)
		}
		beforeID = decoded.IncidentID
	}
	index := "ix_recruiting_repair_updated"
	clauses, args := []string{}, []any{}
	if status != "" {
		index = "ix_recruiting_repair_status"
		clauses, args = append(clauses, "i.repair_status = ?"), append(args, status)
	}
	if cursor != "" {
		clauses = append(clauses, "(i.updated_at < ? OR (i.updated_at = ? AND i.incident_id < ?))")
		args = append(args, beforeTime.UTC(), beforeTime.UTC(), beforeID)
	}
	args = append(args, limit+1)
	query := fmt.Sprintf(`
SELECT i.state_json, i.repair_work_id, i.validation_work_id, i.recovered_work_count, i.updated_at, i.incident_id,
       (SELECT COUNT(*) FROM recruiting_repair_affected_works a WHERE a.incident_id = i.incident_id),
       (SELECT COUNT(*) FROM recruiting_repair_affected_works a
          JOIN recruiting_works w ON w.work_id = a.work_id
         WHERE a.incident_id = i.incident_id AND w.status = 'waiting_human')
FROM recruiting_repair_incidents i FORCE INDEX (%s)
WHERE %s
ORDER BY i.updated_at DESC, i.incident_id DESC LIMIT ?`, index, workListWhere(clauses))
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return RepairIncidentPage{}, fmt.Errorf("list repair incidents: %w", err)
	}
	defer rows.Close()
	type rowValue struct {
		overview RepairOverview
		updated  time.Time
		id       string
	}
	values := make([]rowValue, 0, limit+1)
	for rows.Next() {
		var value rowValue
		var state []byte
		var repairWorkID, validationWorkID sql.NullString
		var recovered uint64
		if err := rows.Scan(&state, &repairWorkID, &validationWorkID, &recovered, &value.updated, &value.id,
			&value.overview.AffectedCount, &value.overview.WaitingHumanCount); err != nil {
			return RepairIncidentPage{}, fmt.Errorf("scan repair incident: %w", err)
		}
		if err := json.Unmarshal(state, &value.overview.Incident); err != nil {
			return RepairIncidentPage{}, fmt.Errorf("decode repair incident: %w", err)
		}
		value.overview.Incident.RepairWorkID = repairWorkID.String
		value.overview.Incident.ValidationWorkID = validationWorkID.String
		value.overview.Incident.RecoveredWorks = recovered
		value.overview.Incident.AffectedWorkIDs = nil
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return RepairIncidentPage{}, fmt.Errorf("read repair incident page: %w", err)
	}
	page := RepairIncidentPage{HasMore: len(values) > limit}
	if page.HasMore {
		values = values[:limit]
	}
	page.Items = make([]RepairOverview, 0, len(values))
	for _, value := range values {
		page.Items = append(page.Items, value.overview)
	}
	if page.HasMore && len(values) > 0 {
		last := values[len(values)-1]
		page.NextCursor = encodeCursor(repairListCursor{Status: status,
			UpdatedAt: last.updated.UTC().Format(time.RFC3339Nano), IncidentID: last.id})
	}
	return page, nil
}

type RepairAffectedWorkPage struct {
	Items      []WorkRecord
	NextCursor string
	HasMore    bool
}

type repairAffectedCursor struct {
	IncidentID string `json:"incident_id"`
	WorkID     string `json:"work_id"`
}

func (r *Repository) ListRepairAffectedWorks(ctx context.Context, incidentID, cursor string, limit int) (RepairAffectedWorkPage, error) {
	incidentID = strings.TrimSpace(incidentID)
	if incidentID == "" || limit < 1 || limit > 500 {
		return RepairAffectedWorkPage{}, fmt.Errorf("repair incident ID and affected work limit in [1,500] are required")
	}
	var afterID string
	if cursor != "" {
		decoded, err := decodeCursor[repairAffectedCursor](cursor)
		if err != nil || decoded.IncidentID != incidentID || decoded.WorkID == "" {
			return RepairAffectedWorkPage{}, fmt.Errorf("%w: affected work selector", ErrInvalidCursor)
		}
		afterID = decoded.WorkID
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT w.state_json, w.business_key, w.priority, w.capability, w.origin, w.profile_id, w.not_before, w.deadline_at, w.work_id
FROM recruiting_repair_affected_works a
JOIN recruiting_works w ON w.work_id = a.work_id
WHERE a.incident_id = ? AND a.work_id > ?
ORDER BY a.work_id LIMIT ?`, incidentID, afterID, limit+1)
	if err != nil {
		return RepairAffectedWorkPage{}, fmt.Errorf("list repair affected works: %w", err)
	}
	defer rows.Close()
	page := RepairAffectedWorkPage{Items: make([]WorkRecord, 0, limit+1)}
	ids := make([]string, 0, limit+1)
	for rows.Next() {
		var record WorkRecord
		var state []byte
		var businessKey, capability, origin, profileID sql.NullString
		var deadline sql.NullTime
		var workID string
		if err := rows.Scan(&state, &businessKey, &record.Placement.Priority, &capability, &origin, &profileID,
			&record.Placement.NotBefore, &deadline, &workID); err != nil {
			return RepairAffectedWorkPage{}, fmt.Errorf("scan repair affected work: %w", err)
		}
		if err := json.Unmarshal(state, &record.Work); err != nil {
			return RepairAffectedWorkPage{}, fmt.Errorf("decode repair affected work: %w", err)
		}
		record.Placement.BusinessKey, record.Placement.Capability = businessKey.String, capability.String
		record.Placement.Origin, record.Placement.ProfileID = origin.String, profileID.String
		record.Placement.NotBefore = record.Placement.NotBefore.UTC()
		if deadline.Valid {
			value := deadline.Time.UTC()
			record.Placement.DeadlineAt = &value
		}
		page.Items, ids = append(page.Items, record), append(ids, workID)
	}
	if err := rows.Err(); err != nil {
		return RepairAffectedWorkPage{}, fmt.Errorf("read repair affected work page: %w", err)
	}
	page.HasMore = len(page.Items) > limit
	if page.HasMore {
		page.Items, ids = page.Items[:limit], ids[:limit]
	}
	if page.HasMore && len(ids) > 0 {
		page.NextCursor = encodeCursor(repairAffectedCursor{IncidentID: incidentID, WorkID: ids[len(ids)-1]})
	}
	return page, nil
}
