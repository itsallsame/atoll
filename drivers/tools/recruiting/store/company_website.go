package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type CompanyWebsiteChangeOutcome struct {
	Company  model.Company                `json:"company"`
	Revision model.CompanyWebsiteRevision `json:"website_revision"`
	Work     model.Work                   `json:"review_work"`
	Replayed bool                         `json:"replayed"`
}

func (r *Repository) GetCompanyWebsiteRevision(ctx context.Context, revisionID string) (model.CompanyWebsiteRevision, error) {
	return getCompanyWebsiteRevisionWith(ctx, r.db, revisionID, false)
}

func (r *Repository) GetCurrentCompanyWebsiteRevision(ctx context.Context,
	companyID string) (model.CompanyWebsiteRevision, uint64, error) {
	var revisionID string
	var headVersion uint64
	err := r.db.QueryRowContext(ctx, `SELECT revision_id, head_version
FROM recruiting_company_website_heads WHERE company_id = ?`, strings.TrimSpace(companyID)).Scan(&revisionID, &headVersion)
	if errors.Is(err, sql.ErrNoRows) {
		return model.CompanyWebsiteRevision{}, 0, ErrNotFound
	}
	if err != nil {
		return model.CompanyWebsiteRevision{}, 0, fmt.Errorf("get Company website head: %w", err)
	}
	revision, err := r.GetCompanyWebsiteRevision(ctx, revisionID)
	return revision, headVersion, err
}

// ApplyCompanyWebsiteChangeCommand atomically advances Company configuration,
// appends immutable website history, moves its small current-head projection,
// and creates the human Source-relationship review Work. Existing Sources,
// Assignments, Checkpoints, Jobs, and accepted observations are untouched.
func (r *Repository) ApplyCompanyWebsiteChangeCommand(ctx context.Context, expectedCompanyVersion uint64,
	company model.Company, revision model.CompanyWebsiteRevision, reviewWork model.Work, placement WorkPlacement,
	receipt model.CommandReceipt, companyEvent, reviewEvent model.EventIntent,
	businessAt time.Time) (CommandResult, error) {
	if expectedCompanyVersion == 0 || company.Version != expectedCompanyVersion+1 ||
		company.CompanyID != revision.CompanyID || company.ConfigurationVersion != revision.ConfigurationVersion ||
		revision.ReviewWorkID != reviewWork.WorkID || receipt.CommandID == "" || businessAt.IsZero() ||
		reviewWork.TargetType != "company" || reviewWork.TargetID != company.CompanyID ||
		reviewWork.Purpose != "source_relationship_review" || reviewWork.Trigger != "human" ||
		reviewWork.Status != model.WorkOpen || reviewWork.Version != 1 ||
		reviewWork.InitiatorActorID != revision.ChangedBy || reviewWork.CauseMessageID == "" ||
		placement.BusinessKey != fmt.Sprintf("company-website-review|%s|%d", company.CompanyID, company.ConfigurationVersion) ||
		placement.Capability != "" || placement.Origin != "" || placement.ProfileID != "" || placement.NotBefore.IsZero() ||
		companyEvent.AggregateType != "company" || companyEvent.AggregateID != company.CompanyID ||
		companyEvent.AggregateVersion != company.Version || companyEvent.CauseCommandID != receipt.CommandID ||
		reviewEvent.AggregateType != "work" || reviewEvent.AggregateID != reviewWork.WorkID ||
		reviewEvent.AggregateVersion != reviewWork.Version || reviewEvent.CauseCommandID != receipt.CommandID {
		return CommandResult{}, fmt.Errorf("Company website change, review Work, placement, receipt, and events are inconsistent")
	}
	if err := revision.Validate(); err != nil {
		return CommandResult{}, err
	}
	revisionAt, _ := time.Parse(time.RFC3339Nano, revision.ChangedAt)
	if !revisionAt.Equal(businessAt.UTC()) {
		return CommandResult{}, fmt.Errorf("website revision time must equal command business time")
	}
	companyEventAt, err := time.Parse(time.RFC3339Nano, companyEvent.BusinessAt)
	if err != nil {
		return CommandResult{}, err
	}
	reviewEventAt, err := time.Parse(time.RFC3339Nano, reviewEvent.BusinessAt)
	if err != nil {
		return CommandResult{}, err
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin Company website change: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	var currentState []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_companies
WHERE company_id = ? FOR UPDATE`, company.CompanyID).Scan(&currentState); errors.Is(err, sql.ErrNoRows) {
		return CommandResult{}, ErrNotFound
	} else if err != nil {
		return CommandResult{}, fmt.Errorf("lock Company for website change: %w", err)
	}
	var current model.Company
	if err := json.Unmarshal(currentState, &current); err != nil {
		return CommandResult{}, err
	}
	if current.Version != expectedCompanyVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedCompanyVersion, Actual: current.Version}
	}
	name, website := company.Name, company.Website
	expected, err := current.Update(current.Version, model.CompanyUpdate{Name: &name, Website: &website})
	if err != nil || !expected.Changed || !expected.WebsiteChanged || !reflect.DeepEqual(expected.Company, company) ||
		revision.PreviousWebsite != current.Website || revision.PreviousConfigurationVersion != current.ConfigurationVersion {
		return CommandResult{}, fmt.Errorf("website revision does not match the locked Company transition")
	}
	var currentHeadID string
	var currentHeadVersion uint64
	headErr := tx.QueryRowContext(ctx, `SELECT revision_id, head_version FROM recruiting_company_website_heads
WHERE company_id = ? FOR UPDATE`, company.CompanyID).Scan(&currentHeadID, &currentHeadVersion)
	if headErr != nil && !errors.Is(headErr, sql.ErrNoRows) {
		return CommandResult{}, fmt.Errorf("lock Company website head: %w", headErr)
	}
	if revision.RevertsRevisionID != "" {
		if errors.Is(headErr, sql.ErrNoRows) || currentHeadID != revision.RevertsRevisionID {
			return CommandResult{}, fmt.Errorf("%w: rollback must reverse the current revision", ErrWebsiteRevisionConflict)
		}
		target, err := getCompanyWebsiteRevisionWith(ctx, tx, revision.RevertsRevisionID, true)
		if err != nil {
			return CommandResult{}, err
		}
		if target.CompanyID != company.CompanyID || target.Website != current.Website ||
			target.PreviousWebsite != company.Website {
			return CommandResult{}, fmt.Errorf("%w: rollback target does not restore its immutable previous website", ErrWebsiteRevisionConflict)
		}
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	companyState, _ := json.Marshal(company)
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_companies
SET normalized_website = ?, name = ?, onboarding_status = ?, control_status = ?, configuration_version = ?,
    control_epoch = ?, execution_fence = ?, version = ?, state_json = ?, updated_at = ?
WHERE company_id = ? AND version = ?`, nullableString(company.Website), company.Name, company.OnboardingStatus,
		company.ControlStatus, company.ConfigurationVersion, company.ControlEpoch, company.ExecutionFence,
		company.Version, companyState, businessAt.UTC(), company.CompanyID, expectedCompanyVersion)
	if err != nil {
		if isDuplicateKey(err) {
			return CommandResult{}, fmt.Errorf("%w: normalized company website", ErrBusinessKeyExists)
		}
		return CommandResult{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return CommandResult{}, ErrProgressConflict
	}
	if err := insertWork(ctx, tx, reviewWork, placement, businessAt); err != nil {
		return CommandResult{}, err
	}
	revisionState, _ := json.Marshal(revision)
	if _, err := tx.ExecContext(ctx, `INSERT INTO recruiting_company_website_revisions(
  revision_id, company_id, previous_website, website, previous_configuration_version,
  configuration_version, review_work_id, reverts_revision_id, state_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, revision.RevisionID, revision.CompanyID,
		nullableString(revision.PreviousWebsite), nullableString(revision.Website), revision.PreviousConfigurationVersion,
		revision.ConfigurationVersion, revision.ReviewWorkID, nullableString(revision.RevertsRevisionID),
		revisionState, businessAt.UTC()); err != nil {
		if isDuplicateKey(err) {
			return CommandResult{}, fmt.Errorf("%w: Company website revision or review Work", ErrBusinessKeyExists)
		}
		return CommandResult{}, err
	}
	if errors.Is(headErr, sql.ErrNoRows) {
		if _, err := tx.ExecContext(ctx, `INSERT INTO recruiting_company_website_heads(
  company_id, revision_id, head_version, updated_at
) VALUES (?, ?, 1, ?)`, company.CompanyID, revision.RevisionID, businessAt.UTC()); err != nil {
			return CommandResult{}, err
		}
	} else {
		result, err := tx.ExecContext(ctx, `UPDATE recruiting_company_website_heads
SET revision_id = ?, head_version = ?, updated_at = ?
WHERE company_id = ? AND head_version = ?`, revision.RevisionID, currentHeadVersion+1, businessAt.UTC(),
			company.CompanyID, currentHeadVersion)
		if err != nil {
			return CommandResult{}, err
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return CommandResult{}, ErrProgressConflict
		}
	}
	if err := appendEventIntent(ctx, tx, companyEvent, companyEventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, reviewEvent, reviewEventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit Company website change: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func getCompanyWebsiteRevisionWith(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, revisionID string, lock bool) (model.CompanyWebsiteRevision, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var state []byte
	err := query.QueryRowContext(ctx, `SELECT state_json FROM recruiting_company_website_revisions
WHERE revision_id = ?`+suffix, strings.TrimSpace(revisionID)).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.CompanyWebsiteRevision{}, ErrNotFound
	}
	if err != nil {
		return model.CompanyWebsiteRevision{}, fmt.Errorf("get Company website revision: %w", err)
	}
	var revision model.CompanyWebsiteRevision
	if err := json.Unmarshal(state, &revision); err != nil {
		return model.CompanyWebsiteRevision{}, err
	}
	return revision, nil
}
