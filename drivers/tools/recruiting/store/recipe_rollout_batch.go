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

func (r *Repository) ApplyFinishRecipeRolloutPreview(ctx context.Context, expectedBatchVersion,
	expectedParentVersion uint64, next model.RecipeRolloutBatch, nextParent model.Work,
	receipt model.CommandReceipt, event model.EventIntent,
	businessAt time.Time) (CommandResult, error) {
	if expectedBatchVersion == 0 || expectedParentVersion == 0 || next.Version != expectedBatchVersion+1 ||
		next.Status != model.RecipeRolloutBatchPreviewed || receipt.CommandID == "" ||
		event.AggregateType != "work" || event.AggregateID != next.ParentWorkID ||
		nextParent.WorkID != next.ParentWorkID || nextParent.Version != expectedParentVersion+2 ||
		nextParent.Status != model.WorkWaitingHuman || nextParent.WaitingReason != "preview_ready" ||
		event.AggregateVersion != nextParent.Version ||
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
	currentParent, err := getWorkWith(ctx, tx, current.ParentWorkID, true)
	if err != nil {
		return CommandResult{}, err
	}
	if currentParent.Version != expectedParentVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedParentVersion, Actual: currentParent.Version}
	}
	startedParent, err := currentParent.Start(currentParent.Version)
	if err != nil {
		return CommandResult{}, err
	}
	derivedParent, err := startedParent.WaitHuman(startedParent.Version, "preview_ready")
	if err != nil || derivedParent != nextParent {
		if err == nil {
			err = fmt.Errorf("preview completion parent Work does not match persisted aggregate")
		}
		return CommandResult{}, err
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
	previewHash, err := model.RecipeRolloutPreviewHash(current, recipe, items)
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
	if err := updateWorkTx(ctx, tx, currentParent.Version, nextParent, businessAt); err != nil {
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

func (r *Repository) PrepareFinishRecipeRolloutPreview(ctx context.Context, batchID string) (
	model.RecipeRolloutBatch, model.RecipeRolloutBatch, model.Work, model.Work, error) {
	current, err := r.GetRecipeRolloutBatch(ctx, batchID)
	if err != nil {
		return model.RecipeRolloutBatch{}, model.RecipeRolloutBatch{}, model.Work{}, model.Work{}, err
	}
	if current.Status != model.RecipeRolloutBatchPreviewing || current.PreviewedCount < 1 {
		return model.RecipeRolloutBatch{}, model.RecipeRolloutBatch{}, model.Work{}, model.Work{},
			fmt.Errorf("Recipe rollout batch is not ready to finish preview")
	}
	items, err := listRecipeRolloutItemsWith(ctx, r.db, current.BatchID, 0, current.PreviewedCount)
	if err != nil {
		return model.RecipeRolloutBatch{}, model.RecipeRolloutBatch{}, model.Work{}, model.Work{}, err
	}
	if len(items) != current.PreviewedCount {
		return model.RecipeRolloutBatch{}, model.RecipeRolloutBatch{}, model.Work{}, model.Work{},
			fmt.Errorf("Recipe rollout preview member count is incomplete")
	}
	recipe, err := r.GetRecipe(ctx, current.RecipeID, current.RecipeVersion)
	if err != nil {
		return model.RecipeRolloutBatch{}, model.RecipeRolloutBatch{}, model.Work{}, model.Work{}, err
	}
	previewHash, err := model.RecipeRolloutPreviewHash(current, recipe, items)
	if err != nil {
		return model.RecipeRolloutBatch{}, model.RecipeRolloutBatch{}, model.Work{}, model.Work{}, err
	}
	next, err := current.FinishPreview(current.Version, previewHash)
	if err != nil {
		return model.RecipeRolloutBatch{}, model.RecipeRolloutBatch{}, model.Work{}, model.Work{}, err
	}
	currentParent, err := r.GetWork(ctx, current.ParentWorkID)
	if err != nil {
		return model.RecipeRolloutBatch{}, model.RecipeRolloutBatch{}, model.Work{}, model.Work{}, err
	}
	startedParent, err := currentParent.Start(currentParent.Version)
	if err != nil {
		return model.RecipeRolloutBatch{}, model.RecipeRolloutBatch{}, model.Work{}, model.Work{}, err
	}
	nextParent, err := startedParent.WaitHuman(startedParent.Version, "preview_ready")
	if err != nil {
		return model.RecipeRolloutBatch{}, model.RecipeRolloutBatch{}, model.Work{}, model.Work{}, err
	}
	return current, next, currentParent, nextParent, nil
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

func (r *Repository) GetRecipeRolloutBatchItem(ctx context.Context, batchID string,
	ordinal int) (model.RecipeRolloutBatchItem, error) {
	if strings.TrimSpace(batchID) == "" || ordinal < 1 {
		return model.RecipeRolloutBatchItem{}, fmt.Errorf("batch and positive ordinal are required")
	}
	return getRecipeRolloutItemWith(ctx, r.db, batchID, ordinal, false)
}

// ApplyRecipeRolloutBatchItemTransition persists one model-proven member
// transition. It locks the parent batch first so cancellation, pause, and wave
// advancement cannot race a member update into a closed range.
func (r *Repository) ApplyRecipeRolloutBatchItemTransition(ctx context.Context, expectedItemVersion uint64,
	next model.RecipeRolloutBatchItem, businessAt time.Time) error {
	if expectedItemVersion == 0 || strings.TrimSpace(next.BatchID) == "" || next.Ordinal < 1 ||
		next.Version != expectedItemVersion+1 || businessAt.IsZero() {
		return fmt.Errorf("Recipe rollout item transition facts are inconsistent")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin Recipe rollout item transition: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	batch, err := getRecipeRolloutBatchWith(ctx, tx, next.BatchID, true)
	if err != nil {
		return err
	}
	current, err := getRecipeRolloutItemWith(ctx, tx, next.BatchID, next.Ordinal, true)
	if err != nil {
		return err
	}
	if current.Version != expectedItemVersion {
		return &model.VersionConflictError{Expected: expectedItemVersion, Actual: current.Version}
	}
	if next.Ordinal < batch.ActiveFrom || next.Ordinal > batch.ActiveThrough {
		return fmt.Errorf("Recipe rollout item is outside the active wave")
	}
	if current.Status == model.RecipeRolloutItemPending &&
		next.Status == model.RecipeRolloutItemAwaitingValidation {
		if err := validateRecipeRolloutAppliedFacts(ctx, tx, batch, next, businessAt); err != nil {
			return err
		}
	}
	if err := validateRecipeRolloutItemTransition(batch, current, next, businessAt); err != nil {
		return err
	}
	if err := updateRecipeRolloutItemCAS(ctx, tx, current.Version, next, businessAt); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit Recipe rollout item transition: %w", err)
	}
	return nil
}

func validateRecipeRolloutAppliedFacts(ctx context.Context, tx *sql.Tx, batch model.RecipeRolloutBatch,
	item model.RecipeRolloutBatchItem, businessAt time.Time) error {
	source, err := getSourceForUpdate(ctx, tx, item.SourceID)
	if err != nil {
		return err
	}
	assignment, found, err := getAssignmentForUpdate(ctx, tx, item.SourceID, batch.Kind)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("%w: applied Recipe Assignment is absent", ErrRecipeRolloutRejected)
	}
	projected := source.DetailAssignment
	if batch.Kind == model.RecipeListing {
		projected = source.ListingAssignment
	}
	if source.Version != item.AppliedSourceVersion || assignment.AssignmentVersion != item.AppliedAssignmentVersion ||
		assignment.RecipeID != batch.RecipeID || assignment.RecipeVersion != batch.RecipeVersion ||
		assignment.ContractHash != batch.ContractHash || assignment.Kind != batch.Kind || projected == nil ||
		*projected != assignment || !rolloutTimeEquals(assignment.EffectiveAt, businessAt) {
		return fmt.Errorf("%w: applied Source/Assignment facts do not match the rollout target", ErrRecipeRolloutRejected)
	}
	if batch.Kind == model.RecipeListing &&
		(source.ReadinessStatus != model.SourceRepairing || source.ContractAssessment != nil || source.CandidateEndpoint == nil) {
		return fmt.Errorf("%w: applied Listing Source is not awaiting recalibration", ErrRecipeRolloutRejected)
	}
	if batch.Kind == model.RecipeDetail && source.ReadinessStatus != model.SourceReady {
		return fmt.Errorf("%w: applied Detail Source is not ready", ErrRecipeRolloutRejected)
	}
	return nil
}

func validateRecipeRolloutItemTransition(batch model.RecipeRolloutBatch, current,
	next model.RecipeRolloutBatchItem, businessAt time.Time) error {
	if current.BatchID != next.BatchID || current.Ordinal != next.Ordinal || current.SourceID != next.SourceID ||
		current.ExpectedSourceVersion != next.ExpectedSourceVersion ||
		current.ExpectedAssignmentVersion != next.ExpectedAssignmentVersion ||
		current.PreviousAssignment != next.PreviousAssignment ||
		current.PreviousEndpointRevision != next.PreviousEndpointRevision ||
		current.PreviousUpdateRetop != next.PreviousUpdateRetop ||
		current.PreviousCheckpointStrategy != next.PreviousCheckpointStrategy ||
		current.PreviousOverlapPages != next.PreviousOverlapPages ||
		current.PreviousAssessmentVersion != next.PreviousAssessmentVersion {
		return fmt.Errorf("Recipe rollout item immutable preview facts changed")
	}
	var derived model.RecipeRolloutBatchItem
	var err error
	switch {
	case current.Status == model.RecipeRolloutItemPending && next.Status == model.RecipeRolloutItemAwaitingValidation:
		if batch.Status != model.RecipeRolloutBatchRunning || !rolloutTimeEquals(next.AppliedAt, businessAt) {
			return fmt.Errorf("Recipe rollout item can only be applied by a running batch at business time")
		}
		derived, err = current.MarkApplied(current.Version, next.AppliedSourceVersion,
			next.AppliedAssignmentVersion, next.AppliedAt)
	case current.Status == model.RecipeRolloutItemAwaitingValidation && next.Status == model.RecipeRolloutItemAwaitingValidation:
		if batch.Status != model.RecipeRolloutBatchRunning {
			return fmt.Errorf("Recipe rollout validation can only be bound by a running batch")
		}
		derived, err = current.BindValidation(current.Version, next.ValidationWorkID, next.ValidationRunID)
	case current.Status == model.RecipeRolloutItemAwaitingValidation && next.Status == model.RecipeRolloutItemSucceeded:
		if batch.Status != model.RecipeRolloutBatchRunning || !rolloutTimeEquals(next.ValidatedAt, businessAt) {
			return fmt.Errorf("Recipe rollout item can only succeed in a running batch at business time")
		}
		derived, err = current.MarkSucceeded(current.Version, next.ValidationWorkID, next.ValidatedAt)
	case (current.Status == model.RecipeRolloutItemPending ||
		current.Status == model.RecipeRolloutItemAwaitingValidation) && next.Status == model.RecipeRolloutItemFailed:
		if batch.Status != model.RecipeRolloutBatchRunning {
			return fmt.Errorf("Recipe rollout item can only fail in a running batch")
		}
		derived, err = current.MarkFailed(current.Version, next.FailureCode)
	case current.Status == model.RecipeRolloutItemFailed &&
		(next.Status == model.RecipeRolloutItemPending || next.Status == model.RecipeRolloutItemAwaitingValidation):
		if batch.Status != model.RecipeRolloutBatchPaused {
			return fmt.Errorf("failed Recipe rollout item can only retry while its batch is paused")
		}
		derived, err = current.Retry(current.Version)
	default:
		return &model.InvalidTransitionError{Entity: "recipe_rollout_item", From: string(current.Status), Action: string(next.Status)}
	}
	if err != nil {
		return err
	}
	if derived != next {
		return fmt.Errorf("Recipe rollout item transition does not match persisted aggregate")
	}
	return nil
}

func (r *Repository) GetRecipeRolloutWaveProgress(ctx context.Context,
	batchID string) (model.RecipeRolloutBatch, model.RecipeRolloutWaveProgress, error) {
	if strings.TrimSpace(batchID) == "" {
		return model.RecipeRolloutBatch{}, model.RecipeRolloutWaveProgress{}, fmt.Errorf("batch is required")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return model.RecipeRolloutBatch{}, model.RecipeRolloutWaveProgress{}, err
	}
	defer func() { _ = tx.Rollback() }()
	batch, err := getRecipeRolloutBatchWith(ctx, tx, batchID, false)
	if err != nil {
		return model.RecipeRolloutBatch{}, model.RecipeRolloutWaveProgress{}, err
	}
	if (batch.Status != model.RecipeRolloutBatchRunning && batch.Status != model.RecipeRolloutBatchPaused) ||
		batch.ActiveFrom < 1 || batch.ActiveThrough < batch.ActiveFrom {
		return model.RecipeRolloutBatch{}, model.RecipeRolloutWaveProgress{},
			fmt.Errorf("Recipe rollout batch has no active wave")
	}
	progress := model.RecipeRolloutWaveProgress{From: batch.ActiveFrom, Through: batch.ActiveThrough}
	rows, err := tx.QueryContext(ctx, `SELECT item_status, COUNT(*)
FROM recruiting_recipe_rollout_items
WHERE batch_id = ? AND ordinal BETWEEN ? AND ?
GROUP BY item_status`, batch.BatchID, batch.ActiveFrom, batch.ActiveThrough)
	if err != nil {
		return model.RecipeRolloutBatch{}, model.RecipeRolloutWaveProgress{}, fmt.Errorf("count Recipe rollout wave: %w", err)
	}
	defer rows.Close()
	total := 0
	for rows.Next() {
		var status model.RecipeRolloutItemStatus
		var count int
		if err := rows.Scan(&status, &count); err != nil {
			return model.RecipeRolloutBatch{}, model.RecipeRolloutWaveProgress{}, err
		}
		switch status {
		case model.RecipeRolloutItemPending:
			progress.Pending = count
		case model.RecipeRolloutItemAwaitingValidation:
			progress.AwaitingValidation = count
		case model.RecipeRolloutItemSucceeded:
			progress.Succeeded = count
		case model.RecipeRolloutItemFailed:
			progress.Failed = count
		default:
			return model.RecipeRolloutBatch{}, model.RecipeRolloutWaveProgress{},
				fmt.Errorf("unknown Recipe rollout item status %q", status)
		}
		total += count
	}
	if err := rows.Err(); err != nil {
		return model.RecipeRolloutBatch{}, model.RecipeRolloutWaveProgress{}, err
	}
	if total != batch.ActiveThrough-batch.ActiveFrom+1 {
		return model.RecipeRolloutBatch{}, model.RecipeRolloutWaveProgress{},
			fmt.Errorf("Recipe rollout active wave member count is inconsistent")
	}
	if err := tx.Commit(); err != nil {
		return model.RecipeRolloutBatch{}, model.RecipeRolloutWaveProgress{}, err
	}
	return batch, progress, nil
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
	schema_version, policy_version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		batch.BatchID, rolloutBatchActiveKey(batch), batch.ParentWorkID, batch.RecipeID, batch.RecipeVersion,
		batch.Kind, batch.Status, batch.Phase, batch.SourceCount, batch.PreviewedCount, batch.NextChunkSequence,
		batch.CanarySize, batch.WaveSize, batch.ActiveFrom, batch.ActiveThrough, batch.SucceededCount,
		batch.FailedCount, batch.Version, batch.InputArtifactRef, batch.InputArtifactHash,
		batch.PreviewHash, batch.SchemaVersion, batch.PolicyVersion, state, at.UTC(), at.UTC())
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

func getRecipeRolloutItemWith(ctx context.Context, queryer recipeRolloutBatchQueryer, batchID string,
	ordinal int, lock bool) (model.RecipeRolloutBatchItem, error) {
	query := "SELECT state_json FROM recruiting_recipe_rollout_items WHERE batch_id = ? AND ordinal = ?"
	if lock {
		query += " FOR UPDATE"
	}
	var state []byte
	if err := queryer.QueryRowContext(ctx, query, batchID, ordinal).Scan(&state); errors.Is(err, sql.ErrNoRows) {
		return model.RecipeRolloutBatchItem{}, ErrNotFound
	} else if err != nil {
		return model.RecipeRolloutBatchItem{}, fmt.Errorf("get Recipe rollout batch item: %w", err)
	}
	var item model.RecipeRolloutBatchItem
	if err := json.Unmarshal(state, &item); err != nil {
		return model.RecipeRolloutBatchItem{}, fmt.Errorf("decode Recipe rollout batch item: %w", err)
	}
	return item, nil
}

func updateRecipeRolloutItemCAS(ctx context.Context, tx *sql.Tx, expected uint64,
	item model.RecipeRolloutBatchItem, at time.Time) error {
	state, _ := json.Marshal(item)
	appliedAt, err := nullableRolloutTime(item.AppliedAt)
	if err != nil {
		return err
	}
	validatedAt, err := nullableRolloutTime(item.ValidatedAt)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_recipe_rollout_items SET
  item_status = ?, applied_source_version = NULLIF(?, 0), applied_assignment_version = NULLIF(?, 0),
  applied_at = ?, validation_work_id = NULLIF(?, ''), validation_run_id = NULLIF(?, ''),
  validated_at = ?, item_version = ?, failure_code = NULLIF(?, ''), state_json = ?, updated_at = ?
WHERE batch_id = ? AND ordinal = ? AND item_version = ?`, item.Status, item.AppliedSourceVersion,
		item.AppliedAssignmentVersion, appliedAt, item.ValidationWorkID, item.ValidationRunID,
		validatedAt, item.Version, item.FailureCode, state, at.UTC(), item.BatchID, item.Ordinal, expected)
	if err != nil {
		return fmt.Errorf("update Recipe rollout batch item: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return &model.VersionConflictError{Expected: expected, Actual: item.Version - 1}
	}
	return nil
}

func rolloutTimeEquals(value string, expected time.Time) bool {
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	return err == nil && parsed.Equal(expected)
}

func nullableRolloutTime(value string) (any, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	if err != nil {
		return nil, fmt.Errorf("Recipe rollout evidence time must be RFC3339: %w", err)
	}
	return parsed.UTC(), nil
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
