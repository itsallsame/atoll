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

func (r *Repository) ApplyCreateBaselineCommand(ctx context.Context, expectedCompanyVersion, expectedSourceVersion uint64,
	company model.Company, baseline model.BaselineGeneration, work model.Work, placement WorkPlacement,
	receipt model.CommandReceipt, event model.EventIntent, companyEvent *model.EventIntent,
	dispatch *ExecutionDispatchIntent, businessAt time.Time) (CommandResult, error) {
	if expectedCompanyVersion == 0 || expectedSourceVersion == 0 || baseline.WorkID != work.WorkID ||
		baseline.SourceID != work.TargetID || work.TargetType != "source" || work.Purpose != "baseline_listing" ||
		baseline.Version != 1 || baseline.Status != model.BaselineListing || baseline.CompanyVersion != company.Version ||
		baseline.SourceVersion != expectedSourceVersion || placement.Capability != baseline.ListingExecution.Execution.RequiredCapability ||
		placement.Origin != baseline.ListingExecution.Origin || receipt.CommandID == "" ||
		event.AggregateType != "baseline" || event.AggregateID != work.WorkID || event.AggregateVersion != baseline.Version ||
		event.CauseCommandID != receipt.CommandID {
		return CommandResult{}, fmt.Errorf("baseline command facts are inconsistent")
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
	preparation, err := readListingRunPreparation(ctx, tx, baseline.SourceID, true)
	if err != nil {
		return CommandResult{}, err
	}
	if preparation.Company.Version != expectedCompanyVersion || preparation.Source.Version != expectedSourceVersion {
		if preparation.Source.Version != expectedSourceVersion {
			return CommandResult{}, &model.VersionConflictError{Expected: expectedSourceVersion, Actual: preparation.Source.Version}
		}
		return CommandResult{}, &model.VersionConflictError{Expected: expectedCompanyVersion, Actual: preparation.Company.Version}
	}
	nextCompany := preparation.Company
	if nextCompany.OnboardingStatus == model.CompanyDiscoveringSources {
		nextCompany, err = nextCompany.StartInitialization(expectedCompanyVersion)
	} else if nextCompany.OnboardingStatus != model.CompanyInitializing {
		err = &model.InvalidTransitionError{Entity: "company", From: string(nextCompany.OnboardingStatus), Action: "start baseline"}
	}
	if err != nil {
		return CommandResult{}, err
	}
	if !reflect.DeepEqual(nextCompany, company) {
		return CommandResult{}, fmt.Errorf("baseline Company transition does not match locked state")
	}
	expectedBaseline, err := model.NewExecutableBaselineGeneration(work.WorkID, company, preparation.Source,
		baseline.Generation, preparation.Recipe)
	if err != nil {
		return CommandResult{}, err
	}
	if !reflect.DeepEqual(expectedBaseline, baseline) {
		return CommandResult{}, fmt.Errorf("baseline execution fence does not match locked state")
	}
	if company.Version != preparation.Company.Version {
		if companyEvent == nil || companyEvent.AggregateType != "company" || companyEvent.AggregateID != company.CompanyID ||
			companyEvent.AggregateVersion != company.Version || companyEvent.CauseCommandID != receipt.CommandID {
			return CommandResult{}, fmt.Errorf("baseline Company event is inconsistent")
		}
		state, _ := json.Marshal(company)
		result, updateErr := tx.ExecContext(ctx, `UPDATE recruiting_companies
SET onboarding_status = ?, configuration_version = ?, control_epoch = ?, execution_fence = ?,
    version = ?, state_json = ?, updated_at = ? WHERE company_id = ? AND version = ?`,
			company.OnboardingStatus, company.ConfigurationVersion, company.ControlEpoch, company.ExecutionFence,
			company.Version, state, businessAt.UTC(), company.CompanyID, expectedCompanyVersion)
		if updateErr != nil {
			return CommandResult{}, updateErr
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return CommandResult{}, &model.VersionConflictError{Expected: expectedCompanyVersion, Actual: preparation.Company.Version}
		}
	} else if companyEvent != nil {
		return CommandResult{}, fmt.Errorf("unchanged baseline Company cannot emit event")
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	if err := insertWork(ctx, tx, work, placement, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := insertBaselineWith(ctx, tx, baseline, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if companyEvent != nil {
		at, parseErr := time.Parse(time.RFC3339, companyEvent.BusinessAt)
		if parseErr != nil {
			return CommandResult{}, parseErr
		}
		if err := appendEventIntent(ctx, tx, *companyEvent, at, businessAt); err != nil {
			return CommandResult{}, err
		}
	}
	if err := appendWorkCommandDispatch(ctx, tx, dispatch, placement, receipt.CommandID, "baseline_created", businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, err
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}
