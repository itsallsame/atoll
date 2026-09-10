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
	businessAt time.Time) (CommandResult, error) {
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
	endpoint, err := url.Parse(current.ActiveEndpoint.URL)
	if err != nil || endpoint.Hostname() == "" || !strings.EqualFold(recipe.Scope, endpoint.Hostname()) {
		return CommandResult{}, fmt.Errorf("Recipe proposal scope does not match the locked Source endpoint")
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
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit Recipe proposal: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}
