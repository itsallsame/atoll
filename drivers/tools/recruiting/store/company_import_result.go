package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type CompanyImportPreviewChunk struct {
	CommandID            string
	RequestHash          string
	CorrelationID        string
	AttemptID            string
	ExecutorActorID      string
	ExecutorIncarnation  string
	ExpectedBatchVersion uint64
	ChunkSequence        uint64
	Items                []model.CompanyImportItem
	ReceivedAt           time.Time
}

type CompanyImportPreviewCompletion struct {
	CommandID            string
	RequestHash          string
	CorrelationID        string
	AttemptID            string
	ExecutorActorID      string
	ExecutorIncarnation  string
	ExpectedBatchVersion uint64
	PreviewHash          string
	CompletedAt          time.Time
}

type CompanyImportResultOutcome struct {
	ContractVersion string               `json:"contract_version"`
	CorrelationID   string               `json:"correlation_id"`
	RequestedBy     string               `json:"requested_by"`
	Import          *model.CompanyImport `json:"company_import,omitempty"`
	Work            *model.Work          `json:"work,omitempty"`
	Attempt         *model.Attempt       `json:"attempt,omitempty"`
	AcceptedItems   int                  `json:"accepted_items,omitempty"`
	HasMore         bool                 `json:"has_more,omitempty"`
	ParentWork      *model.Work          `json:"parent_work,omitempty"`
	Replayed        bool                 `json:"replayed,omitempty"`
}

func (r *Repository) AcceptCompanyImportPreviewChunk(ctx context.Context, input CompanyImportPreviewChunk) (CompanyImportResultOutcome, error) {
	if strings.TrimSpace(input.CommandID) == "" || strings.TrimSpace(input.RequestHash) == "" ||
		strings.TrimSpace(input.CorrelationID) == "" || input.ExpectedBatchVersion == 0 || input.ReceivedAt.IsZero() {
		return CompanyImportResultOutcome{}, fmt.Errorf("company import preview chunk requires command, hash, correlation, expected version, and time")
	}
	normalized, err := model.NormalizeCompanyImportItems(input.Items)
	if err != nil {
		return CompanyImportResultOutcome{}, err
	}
	if len(normalized) == 0 || len(normalized) > 500 {
		return CompanyImportResultOutcome{}, fmt.Errorf("company import preview chunk must contain 1..500 items")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CompanyImportResultOutcome{}, err
	}
	defer func() { _ = tx.Rollback() }()
	attempt, work, batch, err := lockCompanyImportExecution(ctx, tx, input.AttemptID, input.ExecutorActorID, input.ExecutorIncarnation)
	if err != nil {
		return CompanyImportResultOutcome{}, err
	}
	if response, found, err := readCommandReceipt(ctx, tx, input.CommandID, input.RequestHash); err != nil {
		return CompanyImportResultOutcome{}, err
	} else if found {
		var outcome CompanyImportResultOutcome
		if json.Unmarshal(response, &outcome) != nil || outcome.Import == nil {
			return CompanyImportResultOutcome{}, fmt.Errorf("stored company import chunk response is invalid")
		}
		outcome.Replayed = true
		return outcome, nil
	}
	if err := validateCompanyImportRunning(attempt, work, batch); err != nil {
		return CompanyImportResultOutcome{}, err
	}
	next, err := batch.AppendPreviewChunk(input.ExpectedBatchVersion, input.ChunkSequence, len(normalized))
	if err != nil {
		return CompanyImportResultOutcome{}, err
	}
	if err := insertCompanyImportPreviewItems(ctx, tx, batch, normalized, input.ReceivedAt); err != nil {
		return CompanyImportResultOutcome{}, err
	}
	if err := updateCompanyImportCAS(ctx, tx, batch.Version, next, input.ReceivedAt); err != nil {
		return CompanyImportResultOutcome{}, err
	}
	outcome := CompanyImportResultOutcome{ContractVersion: executioncontract.Version, CorrelationID: input.CorrelationID,
		RequestedBy: input.ExecutorActorID, Import: &next, Work: &work, Attempt: &attempt, AcceptedItems: len(normalized)}
	response, _ := json.Marshal(outcome)
	receipt, err := model.NewCommandReceipt(input.CommandID, executioncontract.TypeResult, input.RequestHash, response)
	if err != nil {
		return CompanyImportResultOutcome{}, err
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, input.ReceivedAt); err != nil {
		return CompanyImportResultOutcome{}, err
	}
	if err := tx.Commit(); err != nil {
		return CompanyImportResultOutcome{}, fmt.Errorf("commit company import preview chunk: %w", err)
	}
	return outcome, nil
}

func (r *Repository) AcceptCompanyImportPreviewCompletion(ctx context.Context, input CompanyImportPreviewCompletion) (CompanyImportResultOutcome, error) {
	if strings.TrimSpace(input.CommandID) == "" || strings.TrimSpace(input.RequestHash) == "" ||
		strings.TrimSpace(input.CorrelationID) == "" || input.ExpectedBatchVersion == 0 || input.CompletedAt.IsZero() {
		return CompanyImportResultOutcome{}, fmt.Errorf("company import preview completion requires command, hash, correlation, expected version, and time")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CompanyImportResultOutcome{}, err
	}
	defer func() { _ = tx.Rollback() }()
	attempt, work, batch, err := lockCompanyImportExecution(ctx, tx, input.AttemptID, input.ExecutorActorID, input.ExecutorIncarnation)
	if err != nil {
		return CompanyImportResultOutcome{}, err
	}
	if response, found, err := readCommandReceipt(ctx, tx, input.CommandID, input.RequestHash); err != nil {
		return CompanyImportResultOutcome{}, err
	} else if found {
		var outcome CompanyImportResultOutcome
		if json.Unmarshal(response, &outcome) != nil || outcome.Import == nil || outcome.Work == nil || outcome.Attempt == nil {
			return CompanyImportResultOutcome{}, fmt.Errorf("stored company import completion response is invalid")
		}
		outcome.Replayed = true
		return outcome, nil
	}
	if err := validateCompanyImportRunning(attempt, work, batch); err != nil {
		return CompanyImportResultOutcome{}, err
	}
	if batch.Version != input.ExpectedBatchVersion {
		return CompanyImportResultOutcome{}, &model.VersionConflictError{Expected: input.ExpectedBatchVersion, Actual: batch.Version}
	}
	records, err := listCompanyImportItemsWith(ctx, tx, batch.ImportID)
	if err != nil {
		return CompanyImportResultOutcome{}, err
	}
	items := make([]model.CompanyImportItem, 0, len(records))
	for _, record := range records {
		items = append(items, record.Item)
	}
	digest, err := model.CompanyImportPreviewHash(batch, items)
	if err != nil || input.PreviewHash != digest {
		return CompanyImportResultOutcome{}, fmt.Errorf("company import preview completion hash does not match stored items")
	}
	nextBatch, err := batch.FinishPreview(batch.Version, digest)
	if err != nil {
		return CompanyImportResultOutcome{}, err
	}
	previousAttemptStatus, previousWorkVersion := attempt.Status, work.Version
	attempt, err = attempt.Succeed()
	if err == nil {
		work, err = work.WaitHuman(work.Version, "preview_ready")
	}
	if err != nil {
		return CompanyImportResultOutcome{}, err
	}
	outcome := CompanyImportResultOutcome{ContractVersion: executioncontract.Version, CorrelationID: input.CorrelationID,
		RequestedBy: input.ExecutorActorID, Import: &nextBatch, Work: &work, Attempt: &attempt}
	response, _ := json.Marshal(outcome)
	receipt, err := model.NewCommandReceipt(input.CommandID, executioncontract.TypeResult, input.RequestHash, response)
	if err != nil {
		return CompanyImportResultOutcome{}, err
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, input.CompletedAt); err != nil {
		return CompanyImportResultOutcome{}, err
	}
	if err := updateCompanyImportCAS(ctx, tx, batch.Version, nextBatch, input.CompletedAt); err != nil {
		return CompanyImportResultOutcome{}, err
	}
	if err := updateWorkTx(ctx, tx, previousWorkVersion, work, input.CompletedAt); err != nil {
		return CompanyImportResultOutcome{}, err
	}
	if err := updateAttemptStatusTx(ctx, tx, previousAttemptStatus, attempt, response, input.CompletedAt); err != nil {
		return CompanyImportResultOutcome{}, err
	}
	payload, _ := json.Marshal(map[string]any{"import_id": nextBatch.ImportID, "preview_hash": nextBatch.PreviewHash,
		"item_count": nextBatch.ItemCount, "input_artifact_hash": nextBatch.InputArtifactHash})
	event, err := model.NewEventIntent("company-import-preview-"+attempt.AttemptID, "company.import.previewed", "work", work.WorkID,
		work.Version, input.CompletedAt.UTC().Format(time.RFC3339Nano), input.CommandID, payload)
	if err != nil {
		return CompanyImportResultOutcome{}, err
	}
	if err := appendEventIntent(ctx, tx, event, input.CompletedAt, input.CompletedAt); err != nil {
		return CompanyImportResultOutcome{}, err
	}
	if err := appendAttemptDispatch(ctx, tx, attempt.AttemptID, attempt.ExecutorActorID, attempt.Capability, "",
		"capacity_released", input.CommandID, input.CompletedAt, input.CompletedAt); err != nil {
		return CompanyImportResultOutcome{}, err
	}
	if err := tx.Commit(); err != nil {
		return CompanyImportResultOutcome{}, fmt.Errorf("commit company import preview completion: %w", err)
	}
	return outcome, nil
}

func lockCompanyImportExecution(ctx context.Context, tx *sql.Tx, attemptID, actorID, incarnation string) (model.Attempt, model.Work, model.CompanyImport, error) {
	attempt, err := getAttemptWith(ctx, tx, strings.TrimSpace(attemptID), true)
	if err != nil {
		return model.Attempt{}, model.Work{}, model.CompanyImport{}, err
	}
	work, err := getWorkWith(ctx, tx, attempt.WorkID, true)
	if err != nil {
		return model.Attempt{}, model.Work{}, model.CompanyImport{}, err
	}
	if attempt.ExecutorActorID != strings.TrimSpace(actorID) || attempt.ExecutorIncarnation != strings.TrimSpace(incarnation) ||
		attempt.BatchVersion == 0 || work.Purpose != "company_import" || attempt.WorkID != work.WorkID {
		return model.Attempt{}, model.Work{}, model.CompanyImport{}, ErrResultFenced
	}
	batch, err := getCompanyImportByWorkWith(ctx, tx, work.WorkID, true)
	if err != nil {
		return model.Attempt{}, model.Work{}, model.CompanyImport{}, err
	}
	return attempt, work, batch, nil
}

func validateCompanyImportRunning(attempt model.Attempt, work model.Work, batch model.CompanyImport) error {
	if attempt.Status != model.AttemptRunning || attempt.CanSubmit(work) != nil || batch.Status != model.CompanyImportPreviewing ||
		batch.Version < attempt.BatchVersion {
		return ErrResultFenced
	}
	return nil
}

func insertCompanyImportPreviewItems(ctx context.Context, tx *sql.Tx, batch model.CompanyImport, items []model.CompanyImportItem, at time.Time) error {
	for index, item := range items {
		state, _ := json.Marshal(item)
		_, err := tx.ExecContext(ctx, `
INSERT INTO recruiting_company_import_items(
  import_id, item_key, item_ordinal, company_id, normalized_website,
  preview_disposition, outcome_status, child_work_id, detail, version,
  state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, ?, 1, ?, ?, ?)`, batch.ImportID, item.ItemKey,
			batch.ItemCount+index, nullableString(item.CompanyID), nullableString(item.Website), item.PreviewDisposition,
			nullableString(item.Detail), state, at.UTC(), at.UTC())
		if err == nil {
			continue
		}
		if isDuplicateKey(err) {
			return fmt.Errorf("%w: company import item key or ordinal", ErrBusinessKeyExists)
		}
		return fmt.Errorf("insert company import preview item: %w", err)
	}
	return nil
}

func isDuplicateKey(err error) bool {
	var driverError *mysql.MySQLError
	return errors.As(err, &driverError) && driverError.Number == 1062
}
