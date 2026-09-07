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

const baselineStageChunkSize = 500

type BaselineStageRow struct {
	SourceJobKey  string
	ObservationID string
	Value         json.RawMessage
}

func (r *Repository) CreateBaseline(ctx context.Context, baseline model.BaselineGeneration, businessAt time.Time) error {
	if baseline.Version != 1 || baseline.Status != model.BaselineListing {
		return fmt.Errorf("new baseline must be in listing status at version 1")
	}
	state, err := json.Marshal(baseline)
	if err != nil {
		return fmt.Errorf("encode baseline: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
INSERT INTO recruiting_baseline_generations(
  source_id, baseline_generation, generation_status, listing_finalized,
  details_expected, details_accounted, detail_exceptions, version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		baseline.SourceID, baseline.Generation, baseline.Status, baseline.ListingFinalized,
		baseline.DetailsExpected, baseline.DetailsAccounted, baseline.DetailExceptions, baseline.Version, state,
		businessAt.UTC(), businessAt.UTC())
	if err == nil {
		return nil
	}
	var mysqlError *mysql.MySQLError
	if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
		return fmt.Errorf("%w: baseline source and generation", ErrBusinessKeyExists)
	}
	return fmt.Errorf("create baseline: %w", err)
}

// StageBaselineRows bounds every transaction to at most 500 jobs. Re-scanning
// from page one converges by source_job_key instead of growing duplicate rows.
func (r *Repository) StageBaselineRows(ctx context.Context, sourceID string, generation uint64, rows []BaselineStageRow, businessAt time.Time) (int, error) {
	for _, row := range rows {
		if row.SourceJobKey == "" || row.ObservationID == "" || !json.Valid(row.Value) {
			return 0, fmt.Errorf("baseline staging row requires job key, observation, and valid JSON")
		}
	}
	chunks := 0
	for offset := 0; offset < len(rows); offset += baselineStageChunkSize {
		end := offset + baselineStageChunkSize
		if end > len(rows) {
			end = len(rows)
		}
		tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
		if err != nil {
			return chunks, fmt.Errorf("begin baseline staging chunk: %w", err)
		}
		var baselineState []byte
		if err := tx.QueryRowContext(ctx, `
SELECT state_json FROM recruiting_baseline_generations
WHERE source_id = ? AND baseline_generation = ? FOR UPDATE`, sourceID, generation).Scan(&baselineState); err != nil {
			_ = tx.Rollback()
			if errors.Is(err, sql.ErrNoRows) {
				return chunks, ErrNotFound
			}
			return chunks, fmt.Errorf("lock baseline staging generation: %w", err)
		}
		var baseline model.BaselineGeneration
		if err := json.Unmarshal(baselineState, &baseline); err != nil {
			_ = tx.Rollback()
			return chunks, fmt.Errorf("decode baseline staging generation: %w", err)
		}
		if baseline.Status != model.BaselineListing || baseline.ListingFinalized {
			_ = tx.Rollback()
			return chunks, fmt.Errorf("finalized baseline staging is immutable")
		}
		for _, row := range rows[offset:end] {
			_, err = tx.ExecContext(ctx, `
INSERT INTO recruiting_baseline_staging(
  source_id, baseline_generation, source_job_key, observation_id, row_json, staged_at
) VALUES (?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE
  observation_id = VALUES(observation_id), row_json = VALUES(row_json), staged_at = VALUES(staged_at)`,
				sourceID, generation, row.SourceJobKey, row.ObservationID, []byte(row.Value), businessAt.UTC())
			if err != nil {
				_ = tx.Rollback()
				return chunks, fmt.Errorf("stage baseline row: %w", err)
			}
		}
		if err := tx.Commit(); err != nil {
			return chunks, fmt.Errorf("commit baseline staging chunk: %w", err)
		}
		chunks++
	}
	return chunks, nil
}

type BaselineStagePage struct {
	Rows    []BaselineStageRow
	NextKey string
	HasMore bool
}

func (r *Repository) ListBaselineStagePage(ctx context.Context, sourceID string, generation uint64, afterKey string, limit int) (BaselineStagePage, error) {
	if sourceID == "" || generation == 0 || limit <= 0 || limit > baselineStageChunkSize {
		return BaselineStagePage{}, fmt.Errorf("baseline stage page requires source, generation, and limit in [1,500]")
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT source_job_key, observation_id, row_json
FROM recruiting_baseline_staging
WHERE source_id = ? AND baseline_generation = ? AND source_job_key > ?
ORDER BY source_job_key LIMIT ?`, sourceID, generation, afterKey, limit+1)
	if err != nil {
		return BaselineStagePage{}, fmt.Errorf("list baseline stage page: %w", err)
	}
	defer rows.Close()
	var values []BaselineStageRow
	for rows.Next() {
		var row BaselineStageRow
		var value []byte
		if err := rows.Scan(&row.SourceJobKey, &row.ObservationID, &value); err != nil {
			return BaselineStagePage{}, err
		}
		row.Value = append(json.RawMessage(nil), value...)
		values = append(values, row)
	}
	if err := rows.Err(); err != nil {
		return BaselineStagePage{}, err
	}
	page := BaselineStagePage{HasMore: len(values) > limit}
	if page.HasMore {
		values = values[:limit]
	}
	page.Rows = values
	if len(values) > 0 {
		page.NextKey = values[len(values)-1].SourceJobKey
	}
	return page, nil
}

// FinalizeBaselineListing fences the generation and establishes its first
// checkpoint atomically. Staging rows remain in place; no 10k-row move occurs.
func (r *Repository) FinalizeBaselineListing(ctx context.Context, expectedVersion uint64, baseline model.BaselineGeneration, checkpoint model.IncrementalCheckpoint, businessAt time.Time) error {
	if baseline.Version != expectedVersion+1 || !baseline.ListingFinalized || baseline.SourceID != checkpoint.SourceID || checkpoint.Version != 1 {
		return fmt.Errorf("baseline finalize state and checkpoint are inconsistent")
	}
	baselineState, err := json.Marshal(baseline)
	if err != nil {
		return fmt.Errorf("encode baseline: %w", err)
	}
	checkpointState, err := json.Marshal(checkpoint)
	if err != nil {
		return fmt.Errorf("encode checkpoint: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin baseline finalize: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var lockedVersion uint64
	if err := tx.QueryRowContext(ctx, `
SELECT version FROM recruiting_baseline_generations
WHERE source_id = ? AND baseline_generation = ? FOR UPDATE`, baseline.SourceID, baseline.Generation).Scan(&lockedVersion); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock baseline before finalize: %w", err)
	}
	if lockedVersion != expectedVersion {
		return &model.VersionConflictError{Expected: expectedVersion, Actual: lockedVersion}
	}
	var staged uint64
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM recruiting_baseline_staging
WHERE source_id = ? AND baseline_generation = ?`, baseline.SourceID, baseline.Generation).Scan(&staged); err != nil {
		return fmt.Errorf("count baseline staging before finalize: %w", err)
	}
	if staged != baseline.DetailsExpected {
		return fmt.Errorf("baseline expected details do not match staged rows")
	}
	result, err := tx.ExecContext(ctx, `
UPDATE recruiting_baseline_generations
SET generation_status = ?, listing_finalized = ?, details_expected = ?,
    details_accounted = ?, detail_exceptions = ?, version = ?, state_json = ?, updated_at = ?
WHERE source_id = ? AND baseline_generation = ? AND version = ?`,
		baseline.Status, baseline.ListingFinalized, baseline.DetailsExpected, baseline.DetailsAccounted, baseline.DetailExceptions,
		baseline.Version, baselineState, businessAt.UTC(), baseline.SourceID, baseline.Generation, expectedVersion)
	if err != nil {
		return fmt.Errorf("finalize baseline generation: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect baseline finalize: %w", err)
	}
	if changed != 1 {
		var actual uint64
		readErr := tx.QueryRowContext(ctx, `
SELECT version FROM recruiting_baseline_generations
WHERE source_id = ? AND baseline_generation = ?`, baseline.SourceID, baseline.Generation).Scan(&actual)
		if errors.Is(readErr, sql.ErrNoRows) {
			return ErrNotFound
		}
		if readErr != nil {
			return fmt.Errorf("read baseline after failed CAS: %w", readErr)
		}
		return &model.VersionConflictError{Expected: expectedVersion, Actual: actual}
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO recruiting_checkpoints(
  source_id, checkpoint_version, recipe_id, recipe_version, contract_hash,
  frontier_activity_at, frontier_keys_json, last_occurrence_id, state_json, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		checkpoint.SourceID, checkpoint.Version, checkpoint.RecipeID, checkpoint.RecipeVersion, checkpoint.ContractHash,
		nullableTime(checkpoint.FrontierActivityAt), nullableJSONStrings(checkpoint.FrontierJobKeys), checkpoint.LastOccurrenceID,
		checkpointState, businessAt.UTC())
	if err != nil {
		return fmt.Errorf("establish baseline checkpoint: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit baseline finalize: %w", err)
	}
	return nil
}

func nullableTime(value string) any {
	if value == "" {
		return nil
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return value
	}
	return parsed.UTC()
}

func nullableJSONStrings(values []string) any {
	if len(values) == 0 {
		return nil
	}
	content, _ := json.Marshal(values)
	return content
}
