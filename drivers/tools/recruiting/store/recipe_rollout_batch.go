package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type RecipeRolloutBatchItemPage struct {
	Items      []model.RecipeRolloutBatchItem `json:"items"`
	NextCursor int                            `json:"next_cursor,omitempty"`
}

func (r *Repository) ApplyCreateRecipeRolloutBatchCommand(ctx context.Context, parent model.Work,
	placement WorkPlacement, batch model.RecipeRolloutBatch, receipt model.CommandReceipt,
	event model.EventIntent, businessAt time.Time) (CommandResult, error) {
	if parent.WorkID == "" || parent.Status != model.WorkOpen || parent.Version != 1 || parent.ParentWorkID != "" ||
		parent.Purpose != "recipe_rollout_batch" || batch.ParentWorkID != parent.WorkID ||
		batch.Status != model.RecipeRolloutBatchPreviewing || batch.Version != 1 || receipt.CommandID == "" ||
		placement.Capability != "" || placement.NotBefore.IsZero() || event.AggregateType != "work" ||
		event.AggregateID != parent.WorkID || event.AggregateVersion != parent.Version ||
		event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("Recipe rollout batch creation facts are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil || !eventAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("Recipe rollout batch creation time is inconsistent")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin Recipe rollout batch creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	recipe, err := getRecipeForUpdate(ctx, tx, batch.RecipeID, batch.RecipeVersion)
	if err != nil {
		return CommandResult{}, err
	}
	if err := rolloutBatchMatchesRecipe(batch, recipe); err != nil {
		return CommandResult{}, err
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
	if err := insertRecipeRolloutBatch(ctx, tx, batch, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit Recipe rollout batch creation: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func (r *Repository) ApplyRecipeRolloutPreviewChunk(ctx context.Context, expectedBatchVersion uint64,
	sequence int, sourceIDs []string, next model.RecipeRolloutBatch, receipt model.CommandReceipt,
	businessAt time.Time) (CommandResult, error) {
	if expectedBatchVersion == 0 || sequence < 1 || len(sourceIDs) < 1 || len(sourceIDs) > 500 ||
		next.Version != expectedBatchVersion+1 || receipt.CommandID == "" || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("Recipe rollout preview chunk facts are inconsistent")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin Recipe rollout preview chunk: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	current, err := getRecipeRolloutBatchWith(ctx, tx, next.BatchID, true)
	if err != nil {
		return CommandResult{}, err
	}
	if current.Version != expectedBatchVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedBatchVersion, Actual: current.Version}
	}
	derived, err := current.AppendPreviewChunk(current.Version, sequence, len(sourceIDs))
	if err != nil || derived != next {
		if err == nil {
			err = fmt.Errorf("preview chunk does not match persisted batch")
		}
		return CommandResult{}, err
	}
	if err := validateRolloutChunkOrder(ctx, tx, current, sourceIDs); err != nil {
		return CommandResult{}, err
	}

	type sourceFact struct {
		source        model.RecruitmentSource
		assignment    model.SourceRecipeAssignment
		currentRecipe model.Recipe
	}
	lockOrder := append([]string(nil), sourceIDs...)
	sort.Strings(lockOrder)
	facts := make(map[string]sourceFact, len(lockOrder))
	for _, sourceID := range lockOrder {
		source, err := getSourceForUpdate(ctx, tx, sourceID)
		if err != nil {
			return CommandResult{}, err
		}
		assignment, found, err := getAssignmentForUpdate(ctx, tx, sourceID, current.Kind)
		if err != nil {
			return CommandResult{}, err
		}
		if !found {
			return CommandResult{}, fmt.Errorf("%w: Source %s has no current %s assignment", ErrRecipeRolloutRejected, sourceID, current.Kind)
		}
		facts[sourceID] = sourceFact{source: source, assignment: assignment}
	}
	type recipeIdentity struct {
		id      string
		version uint64
	}
	identities := make([]recipeIdentity, 0, len(facts))
	seenRecipes := make(map[recipeIdentity]struct{}, len(facts))
	for _, fact := range facts {
		identity := recipeIdentity{id: fact.assignment.RecipeID, version: fact.assignment.RecipeVersion}
		if _, seen := seenRecipes[identity]; !seen {
			seenRecipes[identity] = struct{}{}
			identities = append(identities, identity)
		}
	}
	sort.Slice(identities, func(left, right int) bool {
		if identities[left].id == identities[right].id {
			return identities[left].version < identities[right].version
		}
		return identities[left].id < identities[right].id
	})
	lockedRecipes := make(map[recipeIdentity]model.Recipe, len(identities))
	for _, identity := range identities {
		recipe, err := getRecipeForUpdate(ctx, tx, identity.id, identity.version)
		if err != nil {
			return CommandResult{}, err
		}
		lockedRecipes[identity] = recipe
	}
	targetRecipe, err := getRecipeForUpdate(ctx, tx, current.RecipeID, current.RecipeVersion)
	if err != nil {
		return CommandResult{}, err
	}
	if err := rolloutBatchMatchesRecipe(current, targetRecipe); err != nil {
		return CommandResult{}, err
	}
	for sourceID, fact := range facts {
		fact.currentRecipe = lockedRecipes[recipeIdentity{id: fact.assignment.RecipeID, version: fact.assignment.RecipeVersion}]
		if err := validateRolloutBatchSource(current, targetRecipe, fact.source, fact.assignment, fact.currentRecipe); err != nil {
			return CommandResult{}, err
		}
		facts[sourceID] = fact
	}
	for index, sourceID := range sourceIDs {
		fact := facts[sourceID]
		item, err := model.NewRecipeRolloutBatchItem(current.BatchID, current.PreviewedCount+index+1,
			fact.source, fact.assignment)
		if err != nil {
			return CommandResult{}, err
		}
		if err := insertRecipeRolloutBatchItem(ctx, tx, item, businessAt); err != nil {
			return CommandResult{}, err
		}
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	if err := updateRecipeRolloutBatchCAS(ctx, tx, current.Version, next, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit Recipe rollout preview chunk: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func validateRolloutChunkOrder(ctx context.Context, tx *sql.Tx, batch model.RecipeRolloutBatch, sourceIDs []string) error {
	seen := make(map[string]struct{}, len(sourceIDs))
	previousKey := ""
	if batch.PreviewedCount > 0 {
		var previousSource string
		if err := tx.QueryRowContext(ctx, `SELECT source_id FROM recruiting_recipe_rollout_items
WHERE batch_id = ? AND ordinal = ?`, batch.BatchID, batch.PreviewedCount).Scan(&previousSource); err != nil {
			return fmt.Errorf("read previous Recipe rollout preview member: %w", err)
		}
		previousKey = model.RecipeRolloutOrderKey(batch.BatchID, previousSource)
	}
	for _, sourceID := range sourceIDs {
		trimmed := strings.TrimSpace(sourceID)
		if sourceID != trimmed {
			return fmt.Errorf("Recipe rollout Source IDs must be canonical without surrounding whitespace")
		}
		sourceID = trimmed
		key := model.RecipeRolloutOrderKey(batch.BatchID, sourceID)
		if sourceID == "" || key <= previousKey {
			return fmt.Errorf("Recipe rollout preview members are not in deterministic canary order")
		}
		if _, duplicate := seen[sourceID]; duplicate {
			return fmt.Errorf("Recipe rollout preview contains duplicate Source %s", sourceID)
		}
		seen[sourceID] = struct{}{}
		previousKey = key
	}
	return nil
}

func validateRolloutBatchSource(batch model.RecipeRolloutBatch, target model.Recipe,
	source model.RecruitmentSource, assignment model.SourceRecipeAssignment, currentRecipe model.Recipe) error {
	if source.ReadinessStatus != model.SourceReady || source.ControlStatus != model.ControlActive ||
		source.HealthStatus != model.HealthHealthy || assignment.SourceID != source.SourceID || assignment.Kind != batch.Kind {
		return fmt.Errorf("%w: Source %s is not healthy, ready, and active", ErrRecipeRolloutRejected, source.SourceID)
	}
	if currentRecipe.Status != model.RecipeActive || currentRecipe.Kind != batch.Kind ||
		currentRecipe.Scope != target.Scope || currentRecipe.ContractHash != target.ContractHash ||
		currentRecipe.Execution.RequiredCapability != target.Execution.RequiredCapability ||
		currentRecipe.ContractHash != assignment.ContractHash ||
		(currentRecipe.RecipeID == target.RecipeID && currentRecipe.Version == target.Version) {
		return fmt.Errorf("%w: Source %s has no compatible distinct current Recipe", ErrRecipeRolloutRejected, source.SourceID)
	}
	return nil
}

func (r *Repository) ApplyFinishRecipeRolloutPreview(ctx context.Context, expectedBatchVersion uint64,
	next model.RecipeRolloutBatch, receipt model.CommandReceipt, event model.EventIntent,
	businessAt time.Time) (CommandResult, error) {
	if expectedBatchVersion == 0 || next.Version != expectedBatchVersion+1 ||
		next.Status != model.RecipeRolloutBatchPreviewed || receipt.CommandID == "" ||
		event.AggregateType != "work" || event.AggregateID != next.ParentWorkID ||
		event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("Recipe rollout preview completion facts are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil || !eventAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("Recipe rollout preview completion time is inconsistent")
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
	current, err := getRecipeRolloutBatchWith(ctx, tx, next.BatchID, true)
	if err != nil {
		return CommandResult{}, err
	}
	if current.Version != expectedBatchVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedBatchVersion, Actual: current.Version}
	}
	items, err := listRecipeRolloutItemsWith(ctx, tx, current.BatchID, 0, current.PreviewedCount)
	if err != nil {
		return CommandResult{}, err
	}
	if len(items) != current.PreviewedCount {
		return CommandResult{}, fmt.Errorf("Recipe rollout preview member count is incomplete")
	}
	recipe, err := getRecipeForUpdate(ctx, tx, current.RecipeID, current.RecipeVersion)
	if err != nil {
		return CommandResult{}, err
	}
	previewHash, err := model.RecipeRolloutPreviewHash(current.BatchID, recipe, current.InputArtifactHash, items)
	if err != nil {
		return CommandResult{}, err
	}
	derived, err := current.FinishPreview(current.Version, previewHash)
	if err != nil || derived != next {
		if err == nil {
			err = fmt.Errorf("preview completion does not match persisted members")
		}
		return CommandResult{}, err
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	if err := updateRecipeRolloutBatchCAS(ctx, tx, current.Version, next, businessAt); err != nil {
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

func (r *Repository) GetRecipeRolloutBatch(ctx context.Context, batchID string) (model.RecipeRolloutBatch, error) {
	return getRecipeRolloutBatchWith(ctx, r.db, batchID, false)
}

func (r *Repository) ListRecipeRolloutBatchItems(ctx context.Context, batchID string, afterOrdinal,
	limit int) (RecipeRolloutBatchItemPage, error) {
	if strings.TrimSpace(batchID) == "" || afterOrdinal < 0 || limit < 1 || limit > 500 {
		return RecipeRolloutBatchItemPage{}, fmt.Errorf("batch, non-negative cursor, and limit in [1,500] are required")
	}
	items, err := listRecipeRolloutItemsWith(ctx, r.db, batchID, afterOrdinal, limit+1)
	if err != nil {
		return RecipeRolloutBatchItemPage{}, err
	}
	page := RecipeRolloutBatchItemPage{Items: items}
	if len(page.Items) > limit {
		page.NextCursor = page.Items[limit-1].Ordinal
		page.Items = page.Items[:limit]
	}
	return page, nil
}

func rolloutBatchMatchesRecipe(batch model.RecipeRolloutBatch, recipe model.Recipe) error {
	if recipe.Status != model.RecipeActive || recipe.RecipeID != batch.RecipeID || recipe.Version != batch.RecipeVersion ||
		recipe.Kind != batch.Kind || recipe.Scope != batch.Scope || recipe.ContractHash != batch.ContractHash ||
		recipe.Execution.RequiredCapability != batch.Capability {
		return fmt.Errorf("%w: rollout batch target is not the exact active Recipe", ErrRecipeRolloutRejected)
	}
	return nil
}

func insertRecipeRolloutBatch(ctx context.Context, tx *sql.Tx, batch model.RecipeRolloutBatch, at time.Time) error {
	state, _ := json.Marshal(batch)
	_, err := tx.ExecContext(ctx, `INSERT INTO recruiting_recipe_rollout_batches(
  batch_id, active_batch_key, parent_work_id, recipe_id, recipe_version, recipe_kind, status, phase,
  source_count, previewed_count, next_chunk_sequence, canary_size, wave_size, active_from, active_through,
  succeeded_count, failed_count, batch_version, input_artifact_ref, input_artifact_hash, preview_hash,
  state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		batch.BatchID, rolloutBatchActiveKey(batch), batch.ParentWorkID, batch.RecipeID, batch.RecipeVersion,
		batch.Kind, batch.Status, batch.Phase, batch.SourceCount, batch.PreviewedCount, batch.NextChunkSequence,
		batch.CanarySize, batch.WaveSize, batch.ActiveFrom, batch.ActiveThrough, batch.SucceededCount,
		batch.FailedCount, batch.Version, batch.InputArtifactRef, batch.InputArtifactHash,
		batch.PreviewHash, state, at.UTC(), at.UTC())
	if err == nil {
		return nil
	}
	var mysqlError *mysql.MySQLError
	if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
		return fmt.Errorf("%w: Recipe rollout batch ID or active scope", ErrBusinessKeyExists)
	}
	return fmt.Errorf("insert Recipe rollout batch: %w", err)
}

func insertRecipeRolloutBatchItem(ctx context.Context, tx *sql.Tx, item model.RecipeRolloutBatchItem, at time.Time) error {
	state, _ := json.Marshal(item)
	_, err := tx.ExecContext(ctx, `INSERT INTO recruiting_recipe_rollout_items(
  batch_id, ordinal, source_id, item_status, expected_source_version, expected_assignment_version,
  applied_source_version, applied_assignment_version, item_version, failure_code, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, NULL, NULL, ?, NULL, ?, ?, ?)`, item.BatchID, item.Ordinal, item.SourceID,
		item.Status, item.ExpectedSourceVersion, item.ExpectedAssignmentVersion, item.Version, state, at.UTC(), at.UTC())
	if err != nil {
		return fmt.Errorf("insert Recipe rollout batch item: %w", err)
	}
	return nil
}

func updateRecipeRolloutBatchCAS(ctx context.Context, tx *sql.Tx, expected uint64,
	batch model.RecipeRolloutBatch, at time.Time) error {
	state, _ := json.Marshal(batch)
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_recipe_rollout_batches SET
  active_batch_key = ?, status = ?, phase = ?, source_count = ?, previewed_count = ?, next_chunk_sequence = ?,
  active_from = ?, active_through = ?, succeeded_count = ?, failed_count = ?, batch_version = ?, preview_hash = ?,
  state_json = ?, updated_at = ? WHERE batch_id = ? AND batch_version = ?`, rolloutBatchActiveKey(batch),
		batch.Status, batch.Phase, batch.SourceCount, batch.PreviewedCount, batch.NextChunkSequence,
		batch.ActiveFrom, batch.ActiveThrough, batch.SucceededCount, batch.FailedCount, batch.Version,
		batch.PreviewHash, state, at.UTC(), batch.BatchID, expected)
	if err != nil {
		return fmt.Errorf("update Recipe rollout batch: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return &model.VersionConflictError{Expected: expected, Actual: batch.Version - 1}
	}
	return nil
}

func rolloutBatchActiveKey(batch model.RecipeRolloutBatch) any {
	switch batch.Status {
	case model.RecipeRolloutBatchCompleted, model.RecipeRolloutBatchCanceled:
		return nil
	default:
		return string(batch.Kind) + "|" + batch.Scope
	}
}

type recipeRolloutBatchQueryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func getRecipeRolloutBatchWith(ctx context.Context, queryer recipeRolloutBatchQueryer, batchID string,
	lock bool) (model.RecipeRolloutBatch, error) {
	query := "SELECT state_json FROM recruiting_recipe_rollout_batches WHERE batch_id = ?"
	if lock {
		query += " FOR UPDATE"
	}
	var state []byte
	if err := queryer.QueryRowContext(ctx, query, batchID).Scan(&state); errors.Is(err, sql.ErrNoRows) {
		return model.RecipeRolloutBatch{}, ErrNotFound
	} else if err != nil {
		return model.RecipeRolloutBatch{}, fmt.Errorf("get Recipe rollout batch: %w", err)
	}
	var batch model.RecipeRolloutBatch
	if err := json.Unmarshal(state, &batch); err != nil {
		return model.RecipeRolloutBatch{}, fmt.Errorf("decode Recipe rollout batch: %w", err)
	}
	return batch, nil
}

type recipeRolloutItemsQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func listRecipeRolloutItemsWith(ctx context.Context, queryer recipeRolloutItemsQueryer, batchID string,
	afterOrdinal, limit int) ([]model.RecipeRolloutBatchItem, error) {
	rows, err := queryer.QueryContext(ctx, `SELECT state_json FROM recruiting_recipe_rollout_items
WHERE batch_id = ? AND ordinal > ? ORDER BY ordinal LIMIT ?`, batchID, afterOrdinal, limit)
	if err != nil {
		return nil, fmt.Errorf("list Recipe rollout batch items: %w", err)
	}
	defer rows.Close()
	items := make([]model.RecipeRolloutBatchItem, 0, limit)
	for rows.Next() {
		var state []byte
		if err := rows.Scan(&state); err != nil {
			return nil, err
		}
		var item model.RecipeRolloutBatchItem
		if err := json.Unmarshal(state, &item); err != nil {
			return nil, fmt.Errorf("decode Recipe rollout batch item: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
