package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func (r *Repository) GetSourceProfileBinding(ctx context.Context, sourceID string,
	kind model.RecipeKind) (model.SourceProfileBinding, error) {
	return getSourceProfileBinding(ctx, r.db, sourceID, kind, false)
}

func getSourceProfileBinding(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, sourceID string, kind model.RecipeKind, lock bool) (model.SourceProfileBinding, error) {
	query := `SELECT state_json FROM recruiting_source_profile_bindings WHERE source_id = ? AND recipe_kind = ?`
	if lock {
		query += " FOR UPDATE"
	}
	var state []byte
	if err := queryer.QueryRowContext(ctx, query, sourceID, kind).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.SourceProfileBinding{}, ErrNotFound
		}
		return model.SourceProfileBinding{}, err
	}
	var binding model.SourceProfileBinding
	if err := json.Unmarshal(state, &binding); err != nil {
		return model.SourceProfileBinding{}, fmt.Errorf("decode Source Profile binding: %w", err)
	}
	if err := binding.Validate(); err != nil {
		return model.SourceProfileBinding{}, err
	}
	return binding, nil
}

// ApplySourceProfileBindingCommand moves both the Source acceptance fence and
// its indexed Profile routing fact atomically with immutable history, receipt,
// and audit event. SecretRef and device credentials never enter these facts.
func (r *Repository) ApplySourceProfileBindingCommand(ctx context.Context, expectedSourceVersion,
	expectedBindingVersion uint64, nextSource model.RecruitmentSource, binding model.SourceProfileBinding,
	receipt model.CommandReceipt, event model.EventIntent, businessAt time.Time) (CommandResult, error) {
	if expectedSourceVersion == 0 || nextSource.Version != expectedSourceVersion+1 ||
		binding.SourceID != nextSource.SourceID || binding.Validate() != nil || receipt.CommandID == "" ||
		event.Kind != "source.profile_bound" && event.Kind != "source.profile_unbound" ||
		event.AggregateType != "source_profile_binding" || event.AggregateID != binding.SourceID+"|"+string(binding.RecipeKind) ||
		event.AggregateVersion != binding.Version || event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("Source Profile binding command facts are incomplete or inconsistent")
	}
	wantProfile := nextSource.ListingProfileID
	assignment := nextSource.ListingAssignment
	if binding.RecipeKind == model.RecipeDetail {
		wantProfile, assignment = nextSource.DetailProfileID, nextSource.DetailAssignment
	}
	if wantProfile != binding.ProfileID || assignment == nil {
		return CommandResult{}, fmt.Errorf("Source Profile binding does not match Source projection or Recipe Assignment")
	}
	eventAt, err := time.Parse(time.RFC3339Nano, event.BusinessAt)
	if err != nil || !eventAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("Source Profile binding business times are inconsistent")
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
	var sourceState []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_sources WHERE source_id = ? FOR UPDATE`,
		nextSource.SourceID).Scan(&sourceState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CommandResult{}, ErrNotFound
		}
		return CommandResult{}, err
	}
	var currentSource model.RecruitmentSource
	if err := json.Unmarshal(sourceState, &currentSource); err != nil {
		return CommandResult{}, err
	}
	if currentSource.Version != expectedSourceVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedSourceVersion, Actual: currentSource.Version}
	}
	currentBinding, bindingErr := getSourceProfileBinding(ctx, tx, binding.SourceID, binding.RecipeKind, true)
	if errors.Is(bindingErr, ErrNotFound) {
		if expectedBindingVersion != 0 || binding.Version != 1 || binding.ProfileID == "" {
			return CommandResult{}, fmt.Errorf("new Source Profile binding requires version 1 and a Profile")
		}
	} else if bindingErr != nil {
		return CommandResult{}, bindingErr
	} else if currentBinding.Version != expectedBindingVersion || binding.Version != currentBinding.Version+1 {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedBindingVersion, Actual: currentBinding.Version}
	}

	var recipeState []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_recipes WHERE recipe_id = ? AND recipe_version = ? FOR SHARE`,
		assignment.RecipeID, assignment.RecipeVersion).Scan(&recipeState); err != nil {
		return CommandResult{}, fmt.Errorf("lock Source Profile Recipe: %w", err)
	}
	var recipe model.Recipe
	if err := json.Unmarshal(recipeState, &recipe); err != nil {
		return CommandResult{}, err
	}
	if recipe.Status != model.RecipeActive || recipe.Kind != binding.RecipeKind || recipe.ContractHash != assignment.ContractHash {
		return CommandResult{}, fmt.Errorf("Source Profile binding requires the current active Recipe")
	}
	if binding.ProfileID != "" {
		if recipe.Execution.Transport != model.RecipeTransportBrowser || recipe.Execution.RequiredCapability != "browser.recipe" {
			return CommandResult{}, fmt.Errorf("Profile can only bind a browser.recipe Assignment")
		}
		var profileState []byte
		if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_profiles WHERE profile_id = ? FOR SHARE`,
			binding.ProfileID).Scan(&profileState); err != nil {
			return CommandResult{}, fmt.Errorf("lock bound Profile: %w", err)
		}
		var profile model.BrowserProfile
		if err := json.Unmarshal(profileState, &profile); err != nil {
			return CommandResult{}, err
		}
		if profile.AuthStatus != model.ProfileReady || !executioncontract.ValidToolTarget(profile.DeviceID) ||
			!strings.EqualFold(profile.SecurityDomain, recipe.Scope) {
			return CommandResult{}, fmt.Errorf("bound Profile must be ready on the Recipe security domain and an authorized device")
		}
	} else if recipe.Execution.Transport == model.RecipeTransportBrowser && recipe.Execution.RequiredCapability == "browser.recipe" {
		return CommandResult{}, fmt.Errorf("cannot unbind the Profile while the Source still uses browser.recipe")
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	nextState, _ := json.Marshal(nextSource)
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_sources SET readiness_status = ?, control_status = ?,
health_status = ?, version = ?, state_json = ?, updated_at = ? WHERE source_id = ? AND version = ?`,
		nextSource.ReadinessStatus, nextSource.ControlStatus, nextSource.HealthStatus, nextSource.Version,
		nextState, businessAt.UTC(), nextSource.SourceID, expectedSourceVersion)
	if err != nil {
		return CommandResult{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return CommandResult{}, ErrProgressConflict
	}
	bindingState, _ := json.Marshal(binding)
	if errors.Is(bindingErr, ErrNotFound) {
		_, err = tx.ExecContext(ctx, `INSERT INTO recruiting_source_profile_bindings(
source_id, recipe_kind, profile_id, binding_version, effective_at, state_json, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?)`, binding.SourceID, binding.RecipeKind, nullableString(binding.ProfileID),
			binding.Version, eventAt.UTC(), bindingState, businessAt.UTC())
	} else {
		result, err = tx.ExecContext(ctx, `UPDATE recruiting_source_profile_bindings SET profile_id = ?,
binding_version = ?, effective_at = ?, state_json = ?, updated_at = ?
WHERE source_id = ? AND recipe_kind = ? AND binding_version = ?`, nullableString(binding.ProfileID), binding.Version,
			eventAt.UTC(), bindingState, businessAt.UTC(), binding.SourceID, binding.RecipeKind, expectedBindingVersion)
		if err == nil {
			if changed, _ := result.RowsAffected(); changed != 1 {
				return CommandResult{}, ErrProgressConflict
			}
		}
	}
	if err != nil {
		return CommandResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO recruiting_source_profile_binding_history(
source_id, recipe_kind, binding_version, profile_id, effective_at, state_json, created_at
) VALUES (?, ?, ?, ?, ?, ?, ?)`, binding.SourceID, binding.RecipeKind, binding.Version,
		nullableString(binding.ProfileID), eventAt.UTC(), bindingState, businessAt.UTC()); err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, err
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}
