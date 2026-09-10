package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

// ApplyRecipeProposalCommand registers only an immutable draft. The Source is
// a versioned provenance and scope fence; no Assignment or production state is
// changed until validation and approval complete in later commands.
func (r *Repository) ApplyRecipeProposalCommand(ctx context.Context, expectedSourceVersion, endpointRevision uint64,
	sourceID string, recipe model.Recipe, receipt model.CommandReceipt, event model.EventIntent,
	businessAt time.Time, proposal *model.RecipeProposal) (CommandResult, error) {
	aggregateID := fmt.Sprintf("%s@%d", recipe.RecipeID, recipe.Version)
	if expectedSourceVersion == 0 || endpointRevision == 0 || sourceID == "" || recipe.Status != model.RecipeDraft ||
		recipe.StateVersion != 1 || receipt.CommandID == "" || event.AggregateType != "recipe" ||
		event.AggregateID != aggregateID || event.AggregateVersion != recipe.StateVersion ||
		event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("Recipe proposal facts are inconsistent")
	}
	if err := recipe.Validate(); err != nil {
		return CommandResult{}, err
	}
	if proposal != nil {
		if err := proposal.Validate(); err != nil {
			return CommandResult{}, err
		}
		if proposal.SourceID != sourceID || proposal.SourceVersion != expectedSourceVersion ||
			proposal.EndpointRevision != endpointRevision || proposal.RecipeID != recipe.RecipeID ||
			proposal.RecipeVersion != recipe.Version || proposal.RecipeContentRef != recipe.Execution.ContentRef ||
			proposal.RecipeContentHash != recipe.ContentHash {
			return CommandResult{}, fmt.Errorf("browser capture proposal does not identify the proposed Recipe and Source fence")
		}
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil || !eventAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("Recipe proposal business times are inconsistent")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin Recipe proposal: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	current, err := getSourceForUpdate(ctx, tx, sourceID)
	if err != nil {
		return CommandResult{}, err
	}
	if current.Version != expectedSourceVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedSourceVersion, Actual: current.Version}
	}
	if current.ReadinessStatus != model.SourceReady || current.ControlStatus != model.ControlActive ||
		current.HealthStatus != model.HealthHealthy || current.ActiveEndpoint == nil ||
		current.ActiveEndpoint.Revision != endpointRevision {
		return CommandResult{}, fmt.Errorf("Recipe proposal requires the exact active endpoint of a ready healthy Source")
	}
	scopeTarget := current.ActiveEndpoint.URL
	if proposal != nil && proposal.SourceURL != current.ActiveEndpoint.URL {
		return CommandResult{}, fmt.Errorf("browser capture proposal URL does not match the locked Source endpoint")
	}
	if proposal != nil && proposal.SampleJobID != "" {
		job, jobErr := getJobWithLock(ctx, tx, proposal.SampleJobID)
		if jobErr != nil || job.SourceID != sourceID || job.Version != proposal.SampleJobVersion ||
			job.DetailURL != proposal.PageURL || recipe.Kind != model.RecipeDetail {
			return CommandResult{}, fmt.Errorf("browser Detail capture sample changed before proposal commit")
		}
		scopeTarget = proposal.PageURL
	} else if proposal != nil {
		pageURL := proposal.PageURL
		if pageURL == "" {
			pageURL = proposal.SourceURL
		}
		if pageURL != current.ActiveEndpoint.URL || recipe.Kind == model.RecipeDetail {
			return CommandResult{}, fmt.Errorf("browser capture target does not match the Recipe kind")
		}
	}
	endpoint, err := url.Parse(scopeTarget)
	if err != nil || endpoint.Hostname() == "" || !strings.EqualFold(recipe.Scope, endpoint.Hostname()) {
		return CommandResult{}, fmt.Errorf("Recipe proposal scope does not match the locked capture target")
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	state, _ := json.Marshal(recipe)
	_, err = tx.ExecContext(ctx, `INSERT INTO recruiting_recipes(
  recipe_id, recipe_version, recipe_kind, scope_key, status, content_hash,
  contract_hash, state_version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, recipe.RecipeID, recipe.Version, recipe.Kind, recipe.Scope,
		recipe.Status, recipe.ContentHash, recipe.ContractHash, recipe.StateVersion, state, businessAt.UTC(), businessAt.UTC())
	if err != nil {
		if isDuplicateKey(err) {
			return CommandResult{}, fmt.Errorf("%w: Recipe ID and version", ErrBusinessKeyExists)
		}
		return CommandResult{}, fmt.Errorf("create proposed Recipe: %w", err)
	}
	if proposal != nil {
		proposalState, marshalErr := json.Marshal(proposal)
		if marshalErr != nil {
			return CommandResult{}, fmt.Errorf("encode browser capture proposal: %w", marshalErr)
		}
		capturedAt, parseErr := time.Parse(time.RFC3339, proposal.CapturedAt)
		if parseErr != nil {
			return CommandResult{}, fmt.Errorf("parse browser capture time: %w", parseErr)
		}
		if capturedAt.After(businessAt.Add(5 * time.Minute)) {
			return CommandResult{}, fmt.Errorf("browser capture time is ahead of the command clock")
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO recruiting_recipe_proposals(
  capture_id, source_id, source_version, endpoint_revision, recipe_id, recipe_version,
  capture_ref, capture_hash, captured_by, captured_at, state_version, state_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, proposal.CaptureID, proposal.SourceID,
			proposal.SourceVersion, proposal.EndpointRevision, proposal.RecipeID, proposal.RecipeVersion,
			proposal.CaptureRef, proposal.CaptureHash, proposal.CapturedBy, capturedAt.UTC(),
			proposal.StateVersion, proposalState, businessAt.UTC())
		if err != nil {
			if isDuplicateKey(err) {
				return CommandResult{}, fmt.Errorf("%w: capture ID or Recipe proposal", ErrBusinessKeyExists)
			}
			return CommandResult{}, fmt.Errorf("create browser capture proposal: %w", err)
		}
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit Recipe proposal: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

// ApplyCompanyRecipeProposalCommand registers a Discovery Recipe draft against
// the Company's versioned website. Discovery is company-scoped: inventing a
// Source merely to reuse Source proposal fencing would corrupt provenance.
func (r *Repository) ApplyCompanyRecipeProposalCommand(ctx context.Context, expectedCompanyVersion uint64,
	companyID string, recipe model.Recipe, receipt model.CommandReceipt, event model.EventIntent,
	businessAt time.Time) (CommandResult, error) {
	aggregateID := fmt.Sprintf("%s@%d", recipe.RecipeID, recipe.Version)
	if expectedCompanyVersion == 0 || companyID == "" || recipe.Kind != model.RecipeDiscovery ||
		recipe.Status != model.RecipeDraft || recipe.StateVersion != 1 || receipt.CommandID == "" ||
		event.AggregateType != "recipe" || event.AggregateID != aggregateID ||
		event.AggregateVersion != recipe.StateVersion || event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("Company Discovery Recipe proposal facts are inconsistent")
	}
	if err := recipe.Validate(); err != nil {
		return CommandResult{}, err
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil || !eventAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("Company Discovery Recipe proposal business times are inconsistent")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin Company Discovery Recipe proposal: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	var companyState []byte
	if err := tx.QueryRowContext(ctx, "SELECT state_json FROM recruiting_companies WHERE company_id = ? FOR UPDATE",
		companyID).Scan(&companyState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CommandResult{}, ErrNotFound
		}
		return CommandResult{}, fmt.Errorf("lock Discovery Recipe Company: %w", err)
	}
	var company model.Company
	if err := json.Unmarshal(companyState, &company); err != nil {
		return CommandResult{}, fmt.Errorf("decode Discovery Recipe Company: %w", err)
	}
	if company.Version != expectedCompanyVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedCompanyVersion, Actual: company.Version}
	}
	website, err := url.Parse(company.Website)
	if err != nil || website.Hostname() == "" || company.ControlStatus != model.ControlActive ||
		!strings.EqualFold(recipe.Scope, website.Hostname()) {
		return CommandResult{}, fmt.Errorf("Discovery Recipe scope does not match the active Company website")
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	state, _ := json.Marshal(recipe)
	_, err = tx.ExecContext(ctx, `INSERT INTO recruiting_recipes(
  recipe_id, recipe_version, recipe_kind, scope_key, status, content_hash,
  contract_hash, state_version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, recipe.RecipeID, recipe.Version, recipe.Kind, recipe.Scope,
		recipe.Status, recipe.ContentHash, recipe.ContractHash, recipe.StateVersion, state, businessAt.UTC(), businessAt.UTC())
	if err != nil {
		if isDuplicateKey(err) {
			return CommandResult{}, fmt.Errorf("%w: Recipe ID and version", ErrBusinessKeyExists)
		}
		return CommandResult{}, fmt.Errorf("create proposed Discovery Recipe: %w", err)
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit Company Discovery Recipe proposal: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func (r *Repository) GetRecipeProposal(ctx context.Context, recipeID string, version uint64) (model.RecipeProposal, error) {
	var state []byte
	err := r.db.QueryRowContext(ctx, `SELECT state_json FROM recruiting_recipe_proposals
WHERE recipe_id = ? AND recipe_version = ?`, recipeID, version).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.RecipeProposal{}, ErrNotFound
	}
	if err != nil {
		return model.RecipeProposal{}, fmt.Errorf("get Recipe proposal: %w", err)
	}
	var proposal model.RecipeProposal
	if err := json.Unmarshal(state, &proposal); err != nil {
		return model.RecipeProposal{}, fmt.Errorf("decode Recipe proposal: %w", err)
	}
	if err := proposal.Validate(); err != nil {
		return model.RecipeProposal{}, fmt.Errorf("invalid stored Recipe proposal: %w", err)
	}
	return proposal, nil
}
