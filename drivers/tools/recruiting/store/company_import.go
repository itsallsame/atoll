package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

// CreateCompanyImportPreview persists the user-visible parent Work and import
// aggregate together. Input bytes remain in the referenced Atoll Resource.
func (r *Repository) CreateCompanyImportPreview(ctx context.Context, parent model.Work, placement WorkPlacement, batch model.CompanyImport, businessAt time.Time) error {
	if batch.ParentWorkID != parent.WorkID || batch.Status != model.CompanyImportPreviewing || batch.Version != 1 ||
		parent.Purpose != "company_import" || parent.ParentWorkID != "" {
		return fmt.Errorf("company import requires a new top-level company_import Work and matching preview aggregate")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin company import preview: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := insertWork(ctx, tx, parent, placement, businessAt); err != nil {
		return err
	}
	if err := insertCompanyImport(ctx, tx, batch, businessAt); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit company import preview: %w", err)
	}
	return nil
}

func (r *Repository) ApplyCreateCompanyImportCommand(ctx context.Context, parent model.Work, placement WorkPlacement,
	batch model.CompanyImport, receipt model.CommandReceipt, event model.EventIntent, dispatch *ExecutionDispatchIntent,
	businessAt time.Time) (CommandResult, error) {
	if batch.ParentWorkID != parent.WorkID || batch.Status != model.CompanyImportPreviewing || batch.Version != 1 ||
		parent.Purpose != "company_import" || parent.ParentWorkID != "" || receipt.CommandID == "" ||
		event.AggregateType != "work" || event.AggregateID != parent.WorkID || event.AggregateVersion != parent.Version ||
		event.CauseCommandID != receipt.CommandID {
		return CommandResult{}, fmt.Errorf("company import command facts are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil {
		return CommandResult{}, fmt.Errorf("company import event business time: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin company import command: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	if err := insertWork(ctx, tx, parent, placement, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := insertCompanyImport(ctx, tx, batch, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendWorkCommandDispatch(ctx, tx, dispatch, placement, receipt.CommandID, "company_import_created", businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit company import command: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

// ApplyConfirmCompanyImportCommand binds an operator's confirmation to the
// exact persisted preview and creates the first bounded apply coordinator in
// the same transaction. No company rows are changed by confirmation itself.
func (r *Repository) ApplyConfirmCompanyImportCommand(ctx context.Context, expectedBatchVersion, expectedParentVersion uint64,
	nextBatch model.CompanyImport, nextParent, applyWork model.Work, placement WorkPlacement,
	receipt model.CommandReceipt, event model.EventIntent, dispatch *ExecutionDispatchIntent,
	businessAt time.Time) (CommandResult, error) {
	if expectedBatchVersion == 0 || expectedParentVersion == 0 || receipt.CommandID == "" ||
		nextBatch.Version != expectedBatchVersion+2 || nextBatch.Status != model.CompanyImportRunning ||
		nextParent.WorkID != nextBatch.ParentWorkID || nextParent.Version != expectedParentVersion+1 ||
		nextParent.Status != model.WorkRunning || applyWork.ParentWorkID != nextParent.WorkID ||
		applyWork.TargetType != "company_import" || applyWork.TargetID != nextBatch.ImportID ||
		applyWork.Purpose != "company_import_apply" || applyWork.Status != model.WorkOpen || applyWork.Version != 1 ||
		event.AggregateType != "work" || event.AggregateID != nextParent.WorkID ||
		event.AggregateVersion != nextParent.Version || event.CauseCommandID != receipt.CommandID {
		return CommandResult{}, fmt.Errorf("company import confirmation facts are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil {
		return CommandResult{}, fmt.Errorf("company import confirmation event business time: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin company import confirmation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	currentParent, err := getWorkWith(ctx, tx, nextParent.WorkID, true)
	if err != nil {
		return CommandResult{}, err
	}
	currentBatch, err := getCompanyImportWith(ctx, tx, nextBatch.ImportID, true)
	if err != nil {
		return CommandResult{}, err
	}
	if currentParent.Version != expectedParentVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedParentVersion, Actual: currentParent.Version}
	}
	if currentBatch.Version != expectedBatchVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedBatchVersion, Actual: currentBatch.Version}
	}
	confirmed, err := currentBatch.Confirm(currentBatch.Version, nextBatch.PreviewHash)
	if err == nil {
		confirmed, err = confirmed.Start(confirmed.Version)
	}
	startedParent := currentParent
	if err == nil {
		startedParent, err = startedParent.Start(startedParent.Version)
	}
	if err != nil {
		return CommandResult{}, err
	}
	if confirmed != nextBatch || startedParent != nextParent {
		return CommandResult{}, fmt.Errorf("company import confirmation does not match persisted aggregates")
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	if err := updateWorkTx(ctx, tx, currentParent.Version, nextParent, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := updateCompanyImportCAS(ctx, tx, currentBatch.Version, nextBatch, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := insertWork(ctx, tx, applyWork, placement, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendWorkCommandDispatch(ctx, tx, dispatch, placement, receipt.CommandID, "company_import_confirmed", businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit company import confirmation: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func insertCompanyImport(ctx context.Context, executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, batch model.CompanyImport, businessAt time.Time) error {
	state, err := json.Marshal(batch)
	if err != nil {
		return fmt.Errorf("encode company import: %w", err)
	}
	_, err = executor.ExecContext(ctx, `
INSERT INTO recruiting_company_imports(
  import_id, parent_work_id, import_status, input_artifact_ref, input_artifact_hash,
  schema_version, policy_version, preview_hash, item_count, next_chunk_sequence,
  version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		batch.ImportID, batch.ParentWorkID, batch.Status, batch.InputArtifactRef, batch.InputArtifactHash,
		batch.SchemaVersion, batch.PolicyVersion, nullableString(batch.PreviewHash), batch.ItemCount, batch.NextChunkSequence,
		batch.Version, state, businessAt.UTC(), businessAt.UTC())
	if err == nil {
		return nil
	}
	var mysqlError *mysql.MySQLError
	if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
		return fmt.Errorf("%w: company import ID or parent Work", ErrBusinessKeyExists)
	}
	return fmt.Errorf("insert company import: %w", err)
}

func (r *Repository) GetCompanyImport(ctx context.Context, importID string) (model.CompanyImport, error) {
	return getCompanyImportWith(ctx, r.db, importID, false)
}

func getCompanyImportByWorkWith(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, workID string, lock bool) (model.CompanyImport, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var state []byte
	err := query.QueryRowContext(ctx, "SELECT state_json FROM recruiting_company_imports WHERE parent_work_id = ?"+suffix, workID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.CompanyImport{}, ErrNotFound
	}
	if err != nil {
		return model.CompanyImport{}, fmt.Errorf("get company import by Work: %w", err)
	}
	var batch model.CompanyImport
	if err := json.Unmarshal(state, &batch); err != nil {
		return model.CompanyImport{}, fmt.Errorf("decode company import by Work: %w", err)
	}
	return batch, nil
}

func getCompanyImportWith(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, importID string, lock bool) (model.CompanyImport, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var state []byte
	err := query.QueryRowContext(ctx, "SELECT state_json FROM recruiting_company_imports WHERE import_id = ?"+suffix, importID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.CompanyImport{}, ErrNotFound
	}
	if err != nil {
		return model.CompanyImport{}, fmt.Errorf("get company import: %w", err)
	}
	var batch model.CompanyImport
	if err := json.Unmarshal(state, &batch); err != nil {
		return model.CompanyImport{}, fmt.Errorf("decode company import: %w", err)
	}
	return batch, nil
}

// AppendCompanyImportPreviewChunk commits at most 500 rows and the next
// sequence fence atomically. A crashed executor resumes at the persisted
// sequence; replaying a committed chunk cannot create duplicates.
func (r *Repository) AppendCompanyImportPreviewChunk(ctx context.Context, importID string, expectedVersion, sequence uint64, items []model.CompanyImportItem, businessAt time.Time) (model.CompanyImport, error) {
	normalized, err := model.NormalizeCompanyImportItems(items)
	if err != nil {
		return model.CompanyImport{}, err
	}
	if len(normalized) == 0 || len(normalized) > 500 {
		return model.CompanyImport{}, fmt.Errorf("company import preview chunk must contain 1..500 items")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return model.CompanyImport{}, fmt.Errorf("begin company import preview chunk: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := getCompanyImportWith(ctx, tx, importID, true)
	if err != nil {
		return model.CompanyImport{}, err
	}
	next, err := current.AppendPreviewChunk(expectedVersion, sequence, len(normalized))
	if err != nil {
		return model.CompanyImport{}, err
	}
	for index, item := range normalized {
		state, _ := json.Marshal(item)
		_, err := tx.ExecContext(ctx, `
INSERT INTO recruiting_company_import_items(
  import_id, item_key, item_ordinal, company_id, normalized_website,
  preview_disposition, outcome_status, child_work_id, detail, version,
  state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, ?, 1, ?, ?, ?)`,
			importID, item.ItemKey, current.ItemCount+index, nullableString(item.CompanyID), nullableString(item.Website),
			item.PreviewDisposition, nullableString(item.Detail), state, businessAt.UTC(), businessAt.UTC())
		if err != nil {
			var mysqlError *mysql.MySQLError
			if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
				return model.CompanyImport{}, fmt.Errorf("%w: company import item key or ordinal", ErrBusinessKeyExists)
			}
			return model.CompanyImport{}, fmt.Errorf("insert company import preview item: %w", err)
		}
	}
	if err := updateCompanyImportCAS(ctx, tx, current.Version, next, businessAt); err != nil {
		return model.CompanyImport{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.CompanyImport{}, fmt.Errorf("commit company import preview chunk: %w", err)
	}
	return next, nil
}

// FinishCompanyImportPreview recomputes the preview digest from every stored
// item. The executor-provided digest is only a claim and is never trusted.
func (r *Repository) FinishCompanyImportPreview(ctx context.Context, importID string, expectedVersion uint64, claimedHash string, businessAt time.Time) (model.CompanyImport, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return model.CompanyImport{}, fmt.Errorf("begin finish company import preview: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := getCompanyImportWith(ctx, tx, importID, true)
	if err != nil {
		return model.CompanyImport{}, err
	}
	if current.Version != expectedVersion {
		return model.CompanyImport{}, &model.VersionConflictError{Expected: expectedVersion, Actual: current.Version}
	}
	items, err := listCompanyImportItemsWith(ctx, tx, importID)
	if err != nil {
		return model.CompanyImport{}, err
	}
	values := make([]model.CompanyImportItem, 0, len(items))
	for _, item := range items {
		values = append(values, item.Item)
	}
	digest, err := model.CompanyImportPreviewHash(current, values)
	if err != nil {
		return model.CompanyImport{}, err
	}
	if claimedHash != digest {
		return model.CompanyImport{}, fmt.Errorf("company import preview hash does not match stored items")
	}
	next, err := current.FinishPreview(current.Version, digest)
	if err != nil {
		return model.CompanyImport{}, err
	}
	if err := updateCompanyImportCAS(ctx, tx, current.Version, next, businessAt); err != nil {
		return model.CompanyImport{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.CompanyImport{}, fmt.Errorf("commit finished company import preview: %w", err)
	}
	return next, nil
}

type CompanyImportItemRecord struct {
	Ordinal     uint64                  `json:"ordinal"`
	Item        model.CompanyImportItem `json:"item"`
	Outcome     model.BatchItemStatus   `json:"outcome,omitempty"`
	ChildWorkID string                  `json:"child_work_id,omitempty"`
	Version     uint64                  `json:"version"`
}

type CompanyImportItemPage struct {
	Items      []CompanyImportItemRecord
	NextCursor string
	HasMore    bool
}

type companyImportItemCursor struct {
	Ordinal uint64 `json:"ordinal"`
}

func (r *Repository) ListCompanyImportItemPage(ctx context.Context, importID, cursor string, limit int) (CompanyImportItemPage, error) {
	if importID == "" || limit < 1 || limit > 500 {
		return CompanyImportItemPage{}, fmt.Errorf("company import item page requires import ID and limit in [1,500]")
	}
	var after uint64
	if cursor != "" {
		content, err := base64.RawURLEncoding.DecodeString(cursor)
		var decoded companyImportItemCursor
		if err != nil || json.Unmarshal(content, &decoded) != nil {
			return CompanyImportItemPage{}, fmt.Errorf("%w: company import item", ErrInvalidCursor)
		}
		after = decoded.Ordinal + 1
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT item_ordinal, state_json, COALESCE(outcome_status, ''), COALESCE(child_work_id, ''), version
FROM recruiting_company_import_items
WHERE import_id = ? AND item_ordinal >= ?
ORDER BY item_ordinal LIMIT ?`, importID, after, limit+1)
	if err != nil {
		return CompanyImportItemPage{}, fmt.Errorf("list company import item page: %w", err)
	}
	defer rows.Close()
	values := make([]CompanyImportItemRecord, 0, limit+1)
	for rows.Next() {
		var record CompanyImportItemRecord
		var state []byte
		if err := rows.Scan(&record.Ordinal, &state, &record.Outcome, &record.ChildWorkID, &record.Version); err != nil {
			return CompanyImportItemPage{}, fmt.Errorf("scan company import item page: %w", err)
		}
		if err := json.Unmarshal(state, &record.Item); err != nil {
			return CompanyImportItemPage{}, fmt.Errorf("decode company import item page: %w", err)
		}
		values = append(values, record)
	}
	if err := rows.Err(); err != nil {
		return CompanyImportItemPage{}, err
	}
	page := CompanyImportItemPage{HasMore: len(values) > limit}
	if page.HasMore {
		values = values[:limit]
	}
	page.Items = values
	if page.HasMore {
		content, _ := json.Marshal(companyImportItemCursor{Ordinal: values[len(values)-1].Ordinal})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(content)
	}
	return page, nil
}

func (r *Repository) ListCompanyImportItems(ctx context.Context, importID string) ([]CompanyImportItemRecord, error) {
	return listCompanyImportItemsWith(ctx, r.db, importID)
}

func listCompanyImportItemsWith(ctx context.Context, query interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}, importID string) ([]CompanyImportItemRecord, error) {
	rows, err := query.QueryContext(ctx, `
SELECT item_ordinal, state_json, COALESCE(outcome_status, ''), COALESCE(child_work_id, ''), version
FROM recruiting_company_import_items
WHERE import_id = ? ORDER BY item_ordinal`, importID)
	if err != nil {
		return nil, fmt.Errorf("list company import items: %w", err)
	}
	defer rows.Close()
	var result []CompanyImportItemRecord
	for rows.Next() {
		var record CompanyImportItemRecord
		var state []byte
		if err := rows.Scan(&record.Ordinal, &state, &record.Outcome, &record.ChildWorkID, &record.Version); err != nil {
			return nil, fmt.Errorf("scan company import item: %w", err)
		}
		if err := json.Unmarshal(state, &record.Item); err != nil {
			return nil, fmt.Errorf("decode company import item: %w", err)
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

func updateCompanyImportCAS(ctx context.Context, executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, expected uint64, batch model.CompanyImport, businessAt time.Time) error {
	state, _ := json.Marshal(batch)
	result, err := executor.ExecContext(ctx, `
UPDATE recruiting_company_imports
SET import_status = ?, preview_hash = ?, item_count = ?, next_chunk_sequence = ?, version = ?, state_json = ?, updated_at = ?
WHERE import_id = ? AND version = ?`, batch.Status, nullableString(batch.PreviewHash), batch.ItemCount,
		batch.NextChunkSequence, batch.Version, state, businessAt.UTC(), batch.ImportID, expected)
	if err != nil {
		return fmt.Errorf("update company import: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect company import update: %w", err)
	}
	if changed != 1 {
		return &model.VersionConflictError{Expected: expected, Actual: batch.Version}
	}
	return nil
}
