package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

// ApplyCompleteSourceInitializationEvidenceCommand closes the evidence Mission
// only after re-reading the durable Source and Work facts under the Mission
// lock. It deliberately does not manufacture company-discovery graph stages.
func (r *Repository) ApplyCompleteSourceInitializationEvidenceCommand(ctx context.Context, missionID string,
	expected uint64, receipt model.CommandReceipt, event model.EventIntent, at time.Time) (CommandResult, error) {
	if missionID == "" || receipt.CommandID == "" || event.AggregateType != "deep_discovery" ||
		event.AggregateID != missionID || event.CauseCommandID != receipt.CommandID || at.IsZero() {
		return CommandResult{}, fmt.Errorf("source initialization evidence completion command is inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil {
		return CommandResult{}, err
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
	current, err := getDeepDiscoveryMissionWith(ctx, tx, missionID, true)
	if err != nil {
		return CommandResult{}, err
	}
	if current.Version != expected || !current.IsSourceInitializationEvidence() {
		if current.Version != expected {
			return CommandResult{}, &model.VersionConflictError{Expected: expected, Actual: current.Version}
		}
		return CommandResult{}, fmt.Errorf("Mission is not source initialization evidence")
	}

	rows, err := tx.QueryContext(ctx, `SELECT state_json FROM recruiting_sources
WHERE company_id=? AND control_status='active' AND health_status='healthy' AND readiness_status='candidate'
ORDER BY source_id FOR SHARE`, current.CompanyID)
	if err != nil {
		return CommandResult{}, err
	}
	var sources []model.RecruitmentSource
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return CommandResult{}, err
		}
		var source model.RecruitmentSource
		if err := json.Unmarshal(raw, &source); err != nil {
			_ = rows.Close()
			return CommandResult{}, err
		}
		if source.ListingAssignment == nil && source.CandidateEndpoint != nil {
			sources = append(sources, source)
		}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return CommandResult{}, err
	}
	_ = rows.Close()
	if len(sources) == 0 {
		return CommandResult{}, fmt.Errorf("source initialization evidence requires at least one candidate Source")
	}
	for _, source := range sources {
		var count int
		err := tx.QueryRowContext(ctx, `SELECT COUNT(*)
FROM recruiting_deep_discovery_browser_probes p
JOIN recruiting_works w ON w.work_id=p.work_id
WHERE p.mission_id=? AND p.target_url=? AND p.probe_status='completed'
  AND w.status='completed' AND w.resolution='succeeded'`,
			current.MissionID, source.CandidateEndpoint.URL).Scan(&count)
		if err != nil {
			return CommandResult{}, err
		}
		if count == 0 {
			return CommandResult{}, fmt.Errorf("candidate Source %s lacks a successful browser Probe", source.SourceID)
		}
	}
	var unresolved int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*)
FROM recruiting_deep_discovery_public_query_verifications q
JOIN recruiting_works w ON w.work_id=q.work_id
WHERE q.mission_id=? AND q.verification_status<>'completed'
  AND NOT (w.status='completed' AND w.resolution='accepted_gap')`, current.MissionID).Scan(&unresolved); err != nil {
		return CommandResult{}, err
	}
	if unresolved != 0 {
		return CommandResult{}, fmt.Errorf("source initialization evidence has unresolved public-query verification Work")
	}
	next, err := current.CompleteSourceInitializationEvidence(expected, len(sources))
	if err != nil {
		return CommandResult{}, err
	}
	if event.AggregateVersion != next.Version {
		return CommandResult{}, fmt.Errorf("source initialization evidence event version is inconsistent")
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, at); err != nil {
		return CommandResult{}, err
	}
	encoded, _ := json.Marshal(next)
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_deep_discovery_missions
SET active_company_key=NULL,mission_stage=?,mission_status=?,candidate_count=?,version=?,state_json=?,updated_at=?
WHERE mission_id=? AND version=?`, next.Stage, next.Status, next.CandidateCount, next.Version, encoded,
		at.UTC(), next.MissionID, current.Version)
	if err != nil {
		return CommandResult{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return CommandResult{}, &model.VersionConflictError{Expected: expected, Actual: current.Version}
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, at); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, err
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}
