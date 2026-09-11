package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

// PromoteNextReadyCompany derives onboarding readiness from durable Source and
// latest Baseline facts. It advances at most one Company per short transaction;
// detail result transactions therefore never serialize on a Company row.
func (r *Repository) PromoteNextReadyCompany(ctx context.Context, at time.Time) (*model.Company, error) {
	if at.IsZero() {
		return nil, fmt.Errorf("company onboarding reconciliation time is required")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	var state []byte
	err = tx.QueryRowContext(ctx, `SELECT company.state_json
FROM recruiting_companies company
WHERE company.onboarding_status = 'initializing'
  AND EXISTS (
    SELECT 1 FROM recruiting_sources source
    WHERE source.company_id = company.company_id AND source.control_status = 'active'
      AND source.readiness_status = 'ready'
  )
  AND NOT EXISTS (
    SELECT 1 FROM recruiting_sources source
    WHERE source.company_id = company.company_id AND source.control_status = 'active'
      AND source.readiness_status = 'ready'
      AND (source.health_status <> 'healthy' OR NOT EXISTS (
        SELECT 1 FROM recruiting_baseline_generations baseline
        WHERE baseline.source_id = source.source_id
          AND baseline.baseline_generation = (
            SELECT MAX(latest.baseline_generation) FROM recruiting_baseline_generations latest
            WHERE latest.source_id = source.source_id
          )
          AND baseline.generation_status IN ('completed', 'completed_with_exceptions')
      ))
  )
ORDER BY company.updated_at, company.company_id
LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select ready onboarding Company: %w", err)
	}
	var company model.Company
	if err := json.Unmarshal(state, &company); err != nil {
		return nil, err
	}
	ready, err := company.MarkReady(company.Version)
	if err != nil {
		return nil, err
	}
	readyState, _ := json.Marshal(ready)
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_companies
SET onboarding_status = ?, configuration_version = ?, control_epoch = ?, execution_fence = ?,
    version = ?, state_json = ?, updated_at = ?
WHERE company_id = ? AND version = ?`, ready.OnboardingStatus, ready.ConfigurationVersion, ready.ControlEpoch, ready.ExecutionFence,
		ready.Version, readyState, at.UTC(),
		ready.CompanyID, company.Version)
	if err != nil {
		return nil, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, ErrProgressConflict
	}
	causeID := "onboarding-ready-" + fmt.Sprintf("%x", dispatchDigest(fmt.Sprintf("%s\n%d", ready.CompanyID, ready.Version))[:16])
	event, err := model.NewEventIntent("event-"+fmt.Sprintf("%x", dispatchDigest(causeID)[:16]),
		"company.onboarding.ready", "company", ready.CompanyID, ready.Version,
		at.UTC().Format(time.RFC3339Nano), causeID, json.RawMessage(`{"reason":"all_baselines_completed"}`))
	if err != nil {
		return nil, err
	}
	if err := appendEventIntent(ctx, tx, event, at, at); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return &ready, nil
}
