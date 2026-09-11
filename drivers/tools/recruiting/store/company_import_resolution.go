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

type CompanyImportItemResolutionCommand struct {
	CommandID            string
	Word                 string
	RequestHash          string
	ContractVersion      string
	CorrelationID        string
	RequestedBy          string
	ImportID             string
	ItemKey              string
	ExpectedBatchVersion uint64
	ExpectedItemVersion  uint64
	Resolution           model.CompanyImportItemResolution
	Reason               string
	BusinessAt           time.Time
}

type CompanyImportItemResolutionOutcome struct {
	ContractVersion string                  `json:"contract_version"`
	CorrelationID   string                  `json:"correlation_id"`
	RequestedBy     string                  `json:"requested_by"`
	Import          model.CompanyImport     `json:"company_import"`
	Item            CompanyImportItemRecord `json:"item"`
	Work            model.Work              `json:"work"`
	ParentWork      model.Work              `json:"parent_work"`
	NextAction      string                  `json:"next_action"`
	Replayed        bool                    `json:"-"`
}

// ApplyResolveCompanyImportItemCommand resolves one independently committed
// waiting item and reaggregates the parent from normalized item facts in the
// same transaction. Retry never rewrites the immutable preview row: it only
// retries the original ready item after the operator has removed its external
// company-key conflict. Invalid preview rows may only be explicitly skipped.
func (r *Repository) ApplyResolveCompanyImportItemCommand(ctx context.Context,
	input CompanyImportItemResolutionCommand) (CompanyImportItemResolutionOutcome, error) {
	input.CommandID, input.Word, input.RequestHash = strings.TrimSpace(input.CommandID), strings.TrimSpace(input.Word), strings.TrimSpace(input.RequestHash)
	input.ContractVersion, input.CorrelationID = strings.TrimSpace(input.ContractVersion), strings.TrimSpace(input.CorrelationID)
	input.RequestedBy, input.ImportID, input.ItemKey = strings.TrimSpace(input.RequestedBy), strings.TrimSpace(input.ImportID), strings.TrimSpace(input.ItemKey)
	input.Reason = strings.TrimSpace(input.Reason)
	if input.CommandID == "" || input.Word == "" || input.RequestHash == "" || input.ContractVersion == "" ||
		input.RequestedBy == "" || input.ImportID == "" || input.ItemKey == "" || input.ExpectedBatchVersion == 0 ||
		input.ExpectedItemVersion == 0 || input.Reason == "" || input.BusinessAt.IsZero() {
		return CompanyImportItemResolutionOutcome{}, fmt.Errorf("company import item resolution requires command, actor, item fences, reason, and time")
	}
	if input.Resolution != model.CompanyImportItemRetry && input.Resolution != model.CompanyImportItemSkip {
		return CompanyImportItemResolutionOutcome{}, fmt.Errorf("unknown company import item resolution %q", input.Resolution)
	}

	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CompanyImportItemResolutionOutcome{}, fmt.Errorf("begin company import item resolution: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	batch, err := getCompanyImportWith(ctx, tx, input.ImportID, true)
	if err != nil {
		return CompanyImportItemResolutionOutcome{}, err
	}
	// Read the receipt after taking the aggregate lock. A concurrent identical
	// command may have been invisible before it committed; this ordering makes
	// that waiter replay instead of reporting a stale aggregate version.
	if response, found, err := readCommandReceipt(ctx, tx, input.CommandID, input.RequestHash); err != nil {
		return CompanyImportItemResolutionOutcome{}, err
	} else if found {
		var outcome CompanyImportItemResolutionOutcome
		if json.Unmarshal(response, &outcome) != nil || outcome.Item.Item.ItemKey == "" {
			return CompanyImportItemResolutionOutcome{}, fmt.Errorf("stored company import item resolution response is invalid")
		}
		outcome.Replayed = true
		return outcome, nil
	}
	if batch.Version != input.ExpectedBatchVersion {
		return CompanyImportItemResolutionOutcome{}, &model.VersionConflictError{Expected: input.ExpectedBatchVersion, Actual: batch.Version}
	}
	if batch.Status != model.CompanyImportCompleted || batch.Outcome.WaitingHuman == 0 {
		return CompanyImportItemResolutionOutcome{}, fmt.Errorf("completed company import with waiting items is required")
	}

	record, err := getCompanyImportItemForUpdate(ctx, tx, input.ImportID, input.ItemKey)
	if err != nil {
		return CompanyImportItemResolutionOutcome{}, err
	}
	if record.Version != input.ExpectedItemVersion {
		return CompanyImportItemResolutionOutcome{}, &model.VersionConflictError{Expected: input.ExpectedItemVersion, Actual: record.Version}
	}
	if record.Outcome != model.BatchItemWaitingHuman || record.ChildWorkID == "" {
		return CompanyImportItemResolutionOutcome{}, fmt.Errorf("company import item must be waiting_human")
	}
	child, err := getWorkWith(ctx, tx, record.ChildWorkID, true)
	if err != nil {
		return CompanyImportItemResolutionOutcome{}, err
	}
	parent, err := getWorkWith(ctx, tx, batch.ParentWorkID, true)
	if err != nil {
		return CompanyImportItemResolutionOutcome{}, err
	}
	if child.ParentWorkID != parent.WorkID || child.Status != model.WorkWaitingHuman ||
		parent.Status != model.WorkWaitingHuman || parent.WaitingReason != "company_import_items_waiting_human" {
		return CompanyImportItemResolutionOutcome{}, fmt.Errorf("company import item and parent Work must still await human resolution")
	}

	previousChildVersion := child.Version
	previousOutcomeDetail := record.OutcomeDetail
	switch input.Resolution {
	case model.CompanyImportItemRetry:
		if record.Item.PreviewDisposition != model.CompanyImportReady {
			return CompanyImportItemResolutionOutcome{}, fmt.Errorf("only an originally ready company import item can be retried")
		}
		child, err = child.Start(child.Version)
		if err == nil {
			var company model.Company
			company, err = model.NewCompany(record.Item.CompanyID, record.Item.Name, record.Item.Website)
			if err == nil {
				err = insertCompany(ctx, tx, company, input.BusinessAt)
			}
		}
		if err == nil {
			child, err = child.Complete(child.Version, model.ResolutionSucceeded, "", "")
		}
		if err != nil {
			return CompanyImportItemResolutionOutcome{}, err
		}
		record.Outcome, record.OutcomeDetail = model.BatchItemSucceeded, ""
	case model.CompanyImportItemSkip:
		child, err = child.Complete(child.Version, model.ResolutionSkipped, input.RequestedBy, input.Reason)
		if err != nil {
			return CompanyImportItemResolutionOutcome{}, err
		}
		record.Outcome, record.OutcomeDetail = model.BatchItemSkipped, input.Reason
	}
	if err := updateWorkTx(ctx, tx, previousChildVersion, child, input.BusinessAt); err != nil {
		return CompanyImportItemResolutionOutcome{}, err
	}
	result, err := tx.ExecContext(ctx, `
UPDATE recruiting_company_import_items
SET outcome_status = ?, detail = ?, version = version + 1, updated_at = ?
WHERE import_id = ? AND item_key = ? AND version = ? AND outcome_status = 'waiting_human'`,
		record.Outcome, nullableString(record.OutcomeDetail), input.BusinessAt.UTC(), input.ImportID, input.ItemKey, record.Version)
	if err != nil {
		return CompanyImportItemResolutionOutcome{}, fmt.Errorf("resolve company import item: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return CompanyImportItemResolutionOutcome{}, ErrResultFenced
	}
	record.Version++

	records, err := listCompanyImportItemsWith(ctx, tx, batch.ImportID)
	if err != nil {
		return CompanyImportItemResolutionOutcome{}, err
	}
	results := make([]model.BatchItemResult, 0, len(records))
	for _, stored := range records {
		results = append(results, model.BatchItemResult{ItemKey: stored.Item.ItemKey, Status: stored.Outcome, Detail: stored.OutcomeDetail})
	}
	nextBatch, err := batch.ReaggregateCompleted(batch.Version, results)
	if err != nil {
		return CompanyImportItemResolutionOutcome{}, err
	}
	previousParentVersion := parent.Version
	if nextBatch.Outcome.WaitingHuman == 0 && nextBatch.Outcome.Failed == 0 {
		parent, err = parent.Complete(parent.Version, model.ResolutionSucceeded, "", "")
		if err != nil {
			return CompanyImportItemResolutionOutcome{}, err
		}
	}
	if err := updateCompanyImportCAS(ctx, tx, batch.Version, nextBatch, input.BusinessAt); err != nil {
		return CompanyImportItemResolutionOutcome{}, err
	}
	if parent.Version != previousParentVersion {
		if err := updateWorkTx(ctx, tx, previousParentVersion, parent, input.BusinessAt); err != nil {
			return CompanyImportItemResolutionOutcome{}, err
		}
	}

	nextAction := "resolve_remaining_items"
	if nextBatch.Outcome.WaitingHuman == 0 && nextBatch.Outcome.Failed == 0 {
		nextAction = "completed"
	}
	outcome := CompanyImportItemResolutionOutcome{ContractVersion: input.ContractVersion, CorrelationID: input.CorrelationID,
		RequestedBy: input.RequestedBy, Import: nextBatch, Item: record, Work: child, ParentWork: parent, NextAction: nextAction}
	response, _ := json.Marshal(outcome)
	receipt, err := model.NewCommandReceipt(input.CommandID, input.Word, input.RequestHash, response)
	if err != nil {
		return CompanyImportItemResolutionOutcome{}, err
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, input.BusinessAt); err != nil {
		return CompanyImportItemResolutionOutcome{}, err
	}
	payload, _ := json.Marshal(map[string]any{"requested_by": input.RequestedBy, "reason": input.Reason,
		"item_key": input.ItemKey, "resolution": input.Resolution, "previous_detail": previousOutcomeDetail})
	event, err := model.NewEventIntent("event-"+companyImportDigest(input.CommandID+"|company.import.item.resolved"),
		"company.import.item.resolved", "company_import", nextBatch.ImportID, nextBatch.Version,
		input.BusinessAt.UTC().Format(time.RFC3339Nano), input.CommandID, payload)
	if err != nil {
		return CompanyImportItemResolutionOutcome{}, err
	}
	if err := appendEventIntent(ctx, tx, event, input.BusinessAt, input.BusinessAt); err != nil {
		return CompanyImportItemResolutionOutcome{}, err
	}
	if err := tx.Commit(); err != nil {
		return CompanyImportItemResolutionOutcome{}, fmt.Errorf("commit company import item resolution: %w", err)
	}
	return outcome, nil
}

func getCompanyImportItemForUpdate(ctx context.Context, tx *sql.Tx, importID, itemKey string) (CompanyImportItemRecord, error) {
	var record CompanyImportItemRecord
	var state []byte
	var outcome, detail, childWorkID sql.NullString
	err := tx.QueryRowContext(ctx, `
SELECT item_ordinal, state_json, outcome_status, detail, child_work_id, version
FROM recruiting_company_import_items
WHERE import_id = ? AND item_key = ? FOR UPDATE`, importID, itemKey).
		Scan(&record.Ordinal, &state, &outcome, &detail, &childWorkID, &record.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return CompanyImportItemRecord{}, ErrNotFound
	}
	if err != nil {
		return CompanyImportItemRecord{}, fmt.Errorf("get company import item: %w", err)
	}
	if err := json.Unmarshal(state, &record.Item); err != nil {
		return CompanyImportItemRecord{}, fmt.Errorf("decode company import item: %w", err)
	}
	if outcome.Valid {
		record.Outcome = model.BatchItemStatus(outcome.String)
	}
	if detail.Valid {
		record.OutcomeDetail = detail.String
	}
	if childWorkID.Valid {
		record.ChildWorkID = childWorkID.String
	}
	return record, nil
}
