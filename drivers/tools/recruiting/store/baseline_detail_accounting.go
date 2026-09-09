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

type baselineDetailAccounting struct {
	Baseline    model.BaselineGeneration
	ItemVersion uint64
}

func loadBaselineDetailAccountingTx(ctx context.Context, tx *sql.Tx, work model.Work) (*baselineDetailAccounting, error) {
	if work.Purpose != "detail_sync" || work.TargetType != "job" || work.ParentWorkID == "" {
		return nil, nil
	}
	var baselineState []byte
	var itemVersion uint64
	err := tx.QueryRowContext(ctx, `SELECT baseline.state_json, item.version
FROM recruiting_baseline_detail_items item
JOIN recruiting_baseline_generations baseline
  ON baseline.source_id = item.source_id AND baseline.baseline_generation = item.baseline_generation
WHERE item.job_id = ? AND baseline.work_id = ? AND item.accounting_status = 'pending'
FOR UPDATE`, work.TargetID, work.ParentWorkID).Scan(&baselineState, &itemVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("lock baseline detail accounting: %w", err)
	}
	var baseline model.BaselineGeneration
	if err := json.Unmarshal(baselineState, &baseline); err != nil {
		return nil, fmt.Errorf("decode baseline detail accounting: %w", err)
	}
	return &baselineDetailAccounting{Baseline: baseline, ItemVersion: itemVersion}, nil
}

func accountBaselineDetailSuccessTx(ctx context.Context, tx *sql.Tx, accounting *baselineDetailAccounting,
	jobID string, at time.Time) (*model.BaselineGeneration, error) {
	return accountBaselineDetailTx(ctx, tx, accounting, jobID, "succeeded", 1, 0, 0, at)
}

func accountBaselineDetailAcceptedGapTx(ctx context.Context, tx *sql.Tx, accounting *baselineDetailAccounting,
	jobID string, at time.Time) (*model.BaselineGeneration, error) {
	return accountBaselineDetailTx(ctx, tx, accounting, jobID, "accepted_gap", 0, 1, 0, at)
}

func accountBaselineDetailTx(ctx context.Context, tx *sql.Tx, accounting *baselineDetailAccounting,
	jobID, accountingStatus string, succeeded, acceptedGaps, failed uint64, at time.Time) (*model.BaselineGeneration, error) {
	if accounting == nil {
		return nil, nil
	}
	advanced, err := accounting.Baseline.AccountDetails(accounting.Baseline.Version, succeeded, acceptedGaps, failed)
	if err != nil {
		return nil, err
	}
	state, err := json.Marshal(advanced)
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_baseline_generations
SET generation_status = ?, listing_finalized = ?, details_expected = ?,
    materialization_cursor = ?, materialized_count = ?, materialization_completed = ?,
    details_accounted = ?, detail_exceptions = ?, version = ?, state_json = ?, updated_at = ?
WHERE source_id = ? AND baseline_generation = ? AND version = ?`, advanced.Status, advanced.ListingFinalized,
		advanced.DetailsExpected, nullableString(advanced.MaterializationCursor), advanced.MaterializedCount,
		advanced.MaterializationCompleted, advanced.DetailsAccounted, advanced.DetailExceptions, advanced.Version,
		state, at.UTC(), advanced.SourceID, advanced.Generation, accounting.Baseline.Version)
	if err != nil {
		return nil, fmt.Errorf("account baseline detail success: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, ErrProgressConflict
	}
	result, err = tx.ExecContext(ctx, `UPDATE recruiting_baseline_detail_items
SET accounting_status = ?, version = version + 1, updated_at = ?
WHERE source_id = ? AND baseline_generation = ? AND job_id = ? AND accounting_status = 'pending' AND version = ?`,
		accountingStatus, at.UTC(), advanced.SourceID, advanced.Generation, jobID, accounting.ItemVersion)
	if err != nil {
		return nil, fmt.Errorf("complete baseline detail item: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, ErrProgressConflict
	}
	return &advanced, nil
}
