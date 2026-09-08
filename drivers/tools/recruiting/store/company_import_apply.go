package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type CompanyImportApply struct {
	CommandID            string
	RequestHash          string
	CorrelationID        string
	AttemptID            string
	ExecutorActorID      string
	ExecutorIncarnation  string
	ExpectedBatchVersion uint64
	ReceivedAt           time.Time
}

func (r *Repository) AcceptCompanyImportApply(ctx context.Context, input CompanyImportApply) (CompanyImportResultOutcome, error) {
	if strings.TrimSpace(input.CommandID) == "" || strings.TrimSpace(input.RequestHash) == "" ||
		strings.TrimSpace(input.CorrelationID) == "" || strings.TrimSpace(input.AttemptID) == "" ||
		strings.TrimSpace(input.ExecutorActorID) == "" || strings.TrimSpace(input.ExecutorIncarnation) == "" ||
		input.ExpectedBatchVersion == 0 || input.ReceivedAt.IsZero() {
		return CompanyImportResultOutcome{}, fmt.Errorf("company import apply requires command, request, executor, batch version, and time")
	}
	offer, replay, found, err := r.loadCompanyImportApplyOffer(ctx, input)
	if err != nil || found {
		return replay, err
	}
	for _, item := range offer.CompanyImportItems {
		if err := r.applyCompanyImportItem(ctx, offer, item, input.CommandID, input.ReceivedAt); err != nil {
			return CompanyImportResultOutcome{}, err
		}
	}
	return r.finishCompanyImportApplyPage(ctx, input, offer)
}

func (r *Repository) loadCompanyImportApplyOffer(ctx context.Context, input CompanyImportApply) (executioncontract.Offer, CompanyImportResultOutcome, bool, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return executioncontract.Offer{}, CompanyImportResultOutcome{}, false, err
	}
	defer func() { _ = tx.Rollback() }()
	attempt, err := getAttemptWith(ctx, tx, input.AttemptID, true)
	if err != nil {
		return executioncontract.Offer{}, CompanyImportResultOutcome{}, false, err
	}
	if response, found, err := readCommandReceipt(ctx, tx, input.CommandID, input.RequestHash); err != nil {
		return executioncontract.Offer{}, CompanyImportResultOutcome{}, false, err
	} else if found {
		var outcome CompanyImportResultOutcome
		if json.Unmarshal(response, &outcome) != nil || outcome.Attempt == nil {
			return executioncontract.Offer{}, CompanyImportResultOutcome{}, false, fmt.Errorf("stored company import apply response is invalid")
		}
		outcome.Replayed = true
		return executioncontract.Offer{}, outcome, true, nil
	}
	work, err := getWorkWith(ctx, tx, attempt.WorkID, true)
	if err != nil {
		return executioncontract.Offer{}, CompanyImportResultOutcome{}, false, err
	}
	if attempt.ExecutorActorID != strings.TrimSpace(input.ExecutorActorID) ||
		attempt.ExecutorIncarnation != strings.TrimSpace(input.ExecutorIncarnation) || attempt.Status != model.AttemptRunning ||
		attempt.CanSubmit(work) != nil || work.Purpose != "company_import_apply" || attempt.BatchVersion != input.ExpectedBatchVersion {
		return executioncontract.Offer{}, CompanyImportResultOutcome{}, false, ErrResultFenced
	}
	batch, err := getCompanyImportWith(ctx, tx, work.TargetID, true)
	if err != nil || batch.Status != model.CompanyImportRunning || batch.Version != attempt.BatchVersion {
		return executioncontract.Offer{}, CompanyImportResultOutcome{}, false, ErrResultFenced
	}
	var offerState []byte
	if err := tx.QueryRowContext(ctx, "SELECT execution_offer_json FROM recruiting_attempts WHERE attempt_id = ?", attempt.AttemptID).Scan(&offerState); err != nil {
		return executioncontract.Offer{}, CompanyImportResultOutcome{}, false, err
	}
	var offer executioncontract.Offer
	if json.Unmarshal(offerState, &offer) != nil || offer.Kind != "company_import_apply" || offer.CompanyImport == nil ||
		offer.CompanyImport.ImportID != batch.ImportID || offer.CompanyImport.Version != batch.Version ||
		offer.Attempt.AttemptID != attempt.AttemptID || offer.Work.WorkID != work.WorkID ||
		len(offer.CompanyImportItems) > 500 {
		return executioncontract.Offer{}, CompanyImportResultOutcome{}, false, ErrResultFenced
	}
	return offer, CompanyImportResultOutcome{}, false, nil
}

func (r *Repository) applyCompanyImportItem(ctx context.Context, offer executioncontract.Offer,
	input executioncontract.CompanyImportApplyItem, commandID string, at time.Time) error {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	batch, err := getCompanyImportWith(ctx, tx, offer.CompanyImport.ImportID, true)
	if err != nil || batch.Status != model.CompanyImportRunning || batch.Version != offer.CompanyImport.Version {
		return ErrResultFenced
	}
	var ordinal, version uint64
	var state []byte
	var outcome, childID sql.NullString
	err = tx.QueryRowContext(ctx, `
SELECT item_ordinal, state_json, outcome_status, child_work_id, version
FROM recruiting_company_import_items
WHERE import_id = ? AND item_key = ? FOR UPDATE`, batch.ImportID, input.Item.ItemKey).
		Scan(&ordinal, &state, &outcome, &childID, &version)
	if err != nil {
		return err
	}
	var item model.CompanyImportItem
	if json.Unmarshal(state, &item) != nil || ordinal != input.Ordinal || item != input.Item {
		return ErrResultFenced
	}
	if outcome.Valid {
		if version != input.Version+1 || !childID.Valid || childID.String != companyImportItemWorkID(batch.ImportID, item.ItemKey) {
			return ErrResultFenced
		}
		return tx.Commit()
	}
	if version != input.Version {
		return ErrResultFenced
	}
	parent, err := getWorkWith(ctx, tx, batch.ParentWorkID, true)
	if err != nil || parent.Terminal() {
		return ErrResultFenced
	}
	workID := companyImportItemWorkID(batch.ImportID, item.ItemKey)
	targetID := item.CompanyID
	if targetID == "" {
		targetID = item.ItemKey
	}
	child, err := model.NewChildWork(parent, workID, "company", targetID, "company_import_item", "parent")
	if err == nil {
		child, err = child.WithCausality(offer.Attempt.ExecutorActorID, commandID, offer.Work.WorkID)
	}
	if err != nil {
		return err
	}
	placement := WorkPlacement{BusinessKey: "company-import-item|" + companyImportDigest(batch.ImportID+"|"+item.ItemKey),
		Priority: 0, NotBefore: at}
	if err := insertWork(ctx, tx, child, placement, at); err != nil {
		return err
	}
	previousChildVersion := child.Version
	child, err = child.Start(child.Version)
	resultStatus, detail := model.BatchItemSucceeded, ""
	if err == nil {
		switch item.PreviewDisposition {
		case model.CompanyImportReady:
			var company model.Company
			company, err = model.NewCompany(item.CompanyID, item.Name, item.Website)
			if err == nil {
				err = insertCompany(ctx, tx, company, at)
			}
			if errors.Is(err, ErrBusinessKeyExists) {
				err, resultStatus, detail = nil, model.BatchItemWaitingHuman, "company ID or normalized website already exists"
			}
		case model.CompanyImportSkipped:
			resultStatus, detail = model.BatchItemSkipped, item.Detail
		case model.CompanyImportWaitingHuman:
			resultStatus, detail = model.BatchItemWaitingHuman, item.Detail
		default:
			err = fmt.Errorf("unsupported company import preview disposition %q", item.PreviewDisposition)
		}
	}
	if err == nil {
		switch resultStatus {
		case model.BatchItemSucceeded:
			child, err = child.Complete(child.Version, model.ResolutionSucceeded, "", "")
		case model.BatchItemSkipped:
			child, err = child.Complete(child.Version, model.ResolutionSkipped, offer.Attempt.ExecutorActorID, detail)
		case model.BatchItemWaitingHuman:
			child, err = child.WaitHuman(child.Version, detail)
		}
	}
	if err != nil {
		return err
	}
	if err := updateWorkTx(ctx, tx, previousChildVersion, child, at); err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `
UPDATE recruiting_company_import_items
SET outcome_status = ?, child_work_id = ?, detail = ?, version = version + 1, updated_at = ?
WHERE import_id = ? AND item_key = ? AND version = ? AND outcome_status IS NULL`, resultStatus, child.WorkID,
		nullableString(detail), at.UTC(), batch.ImportID, item.ItemKey, input.Version)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrResultFenced
	}
	payload, _ := json.Marshal(model.BatchItemResult{ItemKey: item.ItemKey, Status: resultStatus, Detail: detail})
	event, err := model.NewEventIntent("event-"+companyImportDigest(offer.Attempt.AttemptID+"|"+item.ItemKey),
		"company.import.item.applied", "work", child.WorkID, child.Version, at.UTC().Format(time.RFC3339Nano), commandID, payload)
	if err != nil {
		return err
	}
	if err := appendEventIntent(ctx, tx, event, at, at); err != nil {
		return err
	}
	return tx.Commit()
}

func (r *Repository) finishCompanyImportApplyPage(ctx context.Context, input CompanyImportApply,
	offer executioncontract.Offer) (CompanyImportResultOutcome, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CompanyImportResultOutcome{}, err
	}
	defer func() { _ = tx.Rollback() }()
	attempt, err := getAttemptWith(ctx, tx, input.AttemptID, true)
	if err != nil {
		return CompanyImportResultOutcome{}, err
	}
	if response, found, err := readCommandReceipt(ctx, tx, input.CommandID, input.RequestHash); err != nil {
		return CompanyImportResultOutcome{}, err
	} else if found {
		var outcome CompanyImportResultOutcome
		if json.Unmarshal(response, &outcome) != nil {
			return CompanyImportResultOutcome{}, fmt.Errorf("stored company import apply response is invalid")
		}
		outcome.Replayed = true
		return outcome, nil
	}
	work, err := getWorkWith(ctx, tx, attempt.WorkID, true)
	if err != nil {
		return CompanyImportResultOutcome{}, err
	}
	batch, err := getCompanyImportWith(ctx, tx, offer.CompanyImport.ImportID, true)
	if err != nil || attempt.Status != model.AttemptRunning || attempt.CanSubmit(work) != nil ||
		batch.Status != model.CompanyImportRunning || batch.Version != input.ExpectedBatchVersion {
		return CompanyImportResultOutcome{}, ErrResultFenced
	}
	var pending int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_company_import_items WHERE import_id = ? AND outcome_status IS NULL", batch.ImportID).Scan(&pending); err != nil {
		return CompanyImportResultOutcome{}, err
	}
	previousAttemptStatus, previousWorkVersion := attempt.Status, work.Version
	attempt, err = attempt.Succeed()
	var parent *model.Work
	if err == nil && pending > 0 {
		work, err = work.WaitRetry(work.Version, "company_import_page_pending")
	} else if err == nil {
		work, err = work.Complete(work.Version, model.ResolutionSucceeded, "", "")
	}
	if err != nil {
		return CompanyImportResultOutcome{}, err
	}
	if pending == 0 {
		records, err := listCompanyImportItemsWith(ctx, tx, batch.ImportID)
		if err != nil {
			return CompanyImportResultOutcome{}, err
		}
		results := make([]model.BatchItemResult, 0, len(records))
		for _, record := range records {
			results = append(results, model.BatchItemResult{ItemKey: record.Item.ItemKey, Status: record.Outcome, Detail: record.OutcomeDetail})
		}
		nextBatch, err := batch.Complete(batch.Version, results)
		if err != nil {
			return CompanyImportResultOutcome{}, err
		}
		parentWork, err := getWorkWith(ctx, tx, batch.ParentWorkID, true)
		if err != nil {
			return CompanyImportResultOutcome{}, err
		}
		previousParentVersion := parentWork.Version
		if nextBatch.Outcome.WaitingHuman > 0 || nextBatch.Outcome.Failed > 0 {
			parentWork, err = parentWork.WaitHuman(parentWork.Version, "company_import_items_waiting_human")
		} else {
			parentWork, err = parentWork.Complete(parentWork.Version, model.ResolutionSucceeded, "", "")
		}
		if err != nil {
			return CompanyImportResultOutcome{}, err
		}
		if err := updateCompanyImportCAS(ctx, tx, batch.Version, nextBatch, input.ReceivedAt); err != nil {
			return CompanyImportResultOutcome{}, err
		}
		if err := updateWorkTx(ctx, tx, previousParentVersion, parentWork, input.ReceivedAt); err != nil {
			return CompanyImportResultOutcome{}, err
		}
		batch, parent = nextBatch, &parentWork
	}
	outcome := CompanyImportResultOutcome{ContractVersion: executioncontract.Version, CorrelationID: input.CorrelationID,
		RequestedBy: input.ExecutorActorID, Import: &batch, Work: &work, Attempt: &attempt,
		AcceptedItems: len(offer.CompanyImportItems), HasMore: pending > 0, ParentWork: parent}
	response, _ := json.Marshal(outcome)
	receipt, err := model.NewCommandReceipt(input.CommandID, executioncontract.TypeResult, input.RequestHash, response)
	if err != nil {
		return CompanyImportResultOutcome{}, err
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, input.ReceivedAt); err != nil {
		return CompanyImportResultOutcome{}, err
	}
	if err := updateWorkTx(ctx, tx, previousWorkVersion, work, input.ReceivedAt); err != nil {
		return CompanyImportResultOutcome{}, err
	}
	if err := updateAttemptStatusTx(ctx, tx, previousAttemptStatus, attempt, response, input.ReceivedAt); err != nil {
		return CompanyImportResultOutcome{}, err
	}
	if pending > 0 {
		if err := appendAttemptDispatch(ctx, tx, attempt.AttemptID, attempt.ExecutorActorID, attempt.Capability, "",
			"company_import_page_pending", input.CommandID, input.ReceivedAt, input.ReceivedAt); err != nil {
			return CompanyImportResultOutcome{}, err
		}
	} else if err := appendAttemptDispatch(ctx, tx, attempt.AttemptID, attempt.ExecutorActorID, attempt.Capability, "",
		"capacity_released", input.CommandID, input.ReceivedAt, input.ReceivedAt); err != nil {
		return CompanyImportResultOutcome{}, err
	}
	if err := tx.Commit(); err != nil {
		return CompanyImportResultOutcome{}, fmt.Errorf("commit company import apply page: %w", err)
	}
	return outcome, nil
}

func companyImportItemWorkID(importID, itemKey string) string {
	return "work-company-import-item-" + companyImportDigest(importID+"|"+itemKey)
}

func companyImportDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:16])
}
