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

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type BackfillItemPage struct {
	Items      []model.BackfillItem `json:"items"`
	NextCursor string               `json:"next_cursor,omitempty"`
}

type BackfillMaterializationResult struct {
	BackfillID string `json:"backfill_id,omitempty"`
	Queued     int    `json:"queued"`
	Failed     int    `json:"failed"`
	Dispatches int    `json:"dispatches"`
}

func (r *Repository) MaterializeNextBackfillPage(ctx context.Context, limit int, at time.Time,
	targets []ExecutionDispatchTarget) (BackfillMaterializationResult, error) {
	if limit < 1 || limit > 500 || at.IsZero() {
		return BackfillMaterializationResult{}, fmt.Errorf("backfill materialization limit must be in [1,500]")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return BackfillMaterializationResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	var state []byte
	err = tx.QueryRowContext(ctx, `SELECT b.state_json
FROM recruiting_backfills b
WHERE b.backfill_status = 'running' AND EXISTS (
  SELECT 1 FROM recruiting_backfill_items item
  WHERE item.backfill_id = b.backfill_id AND item.item_status = 'pending'
)
ORDER BY b.updated_at, b.backfill_id LIMIT 1 FOR UPDATE SKIP LOCKED`).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return BackfillMaterializationResult{}, nil
	}
	if err != nil {
		return BackfillMaterializationResult{}, err
	}
	var backfill model.Backfill
	if err := json.Unmarshal(state, &backfill); err != nil {
		return BackfillMaterializationResult{}, err
	}
	parent, err := getWorkWith(ctx, tx, backfill.WorkID, true)
	if err != nil || parent.Status != model.WorkRunning {
		return BackfillMaterializationResult{}, fmt.Errorf("running backfill requires running parent Work")
	}
	recipe, err := getRecipeForUpdate(ctx, tx, backfill.RecipeID, backfill.RecipeVersion)
	if err != nil {
		return BackfillMaterializationResult{}, err
	}
	if recipe.Status != model.RecipeActive || recipe.Kind != model.RecipeDetail {
		return BackfillMaterializationResult{}, fmt.Errorf("backfill Recipe is no longer active Detail execution")
	}
	capability := recipe.Execution.RequiredCapability
	if backfill.Mode == model.BackfillArtifactRecompute {
		capability = "artifact.recompute"
	}
	rows, err := tx.QueryContext(ctx, `
SELECT item.state_json, company.version, source.version, job.version,
       binding.binding_version, profile.version, profile.auth_status
FROM recruiting_backfill_items item
JOIN recruiting_companies company ON company.company_id = item.company_id
JOIN recruiting_sources source ON source.source_id = item.source_id AND source.company_id = item.company_id
JOIN recruiting_source_jobs job ON job.job_id = item.job_id AND job.source_id = item.source_id
LEFT JOIN recruiting_source_profile_bindings binding
  ON binding.source_id = item.source_id AND binding.recipe_kind = 'detail' AND binding.profile_id = item.profile_id
LEFT JOIN recruiting_profiles profile ON profile.profile_id = item.profile_id
WHERE item.backfill_id = ? AND item.item_status = 'pending'
ORDER BY item.item_id LIMIT ? FOR UPDATE`, backfill.BackfillID, limit)
	if err != nil {
		return BackfillMaterializationResult{}, err
	}
	type candidate struct {
		item                                      model.BackfillItem
		companyVersion, sourceVersion, jobVersion uint64
		bindingVersion, profileVersion            sql.NullInt64
		profileStatus                             sql.NullString
	}
	candidates := make([]candidate, 0, limit)
	for rows.Next() {
		var candidateState []byte
		var value candidate
		if err := rows.Scan(&candidateState, &value.companyVersion, &value.sourceVersion, &value.jobVersion,
			&value.bindingVersion, &value.profileVersion, &value.profileStatus); err != nil {
			_ = rows.Close()
			return BackfillMaterializationResult{}, err
		}
		if err := json.Unmarshal(candidateState, &value.item); err != nil {
			_ = rows.Close()
			return BackfillMaterializationResult{}, err
		}
		candidates = append(candidates, value)
	}
	if err := rows.Close(); err != nil {
		return BackfillMaterializationResult{}, err
	}
	result := BackfillMaterializationResult{BackfillID: backfill.BackfillID}
	unprofiled := make(map[string]int)
	type profileKey struct{ capability, profileID string }
	profiled := make(map[profileKey]int)
	for _, candidate := range candidates {
		item := candidate.item
		fenceValid := candidate.companyVersion == item.CompanyVersion && candidate.sourceVersion == item.SourceVersion &&
			candidate.jobVersion == item.JobVersion
		if item.ProfileID != "" {
			fenceValid = fenceValid && candidate.bindingVersion.Valid && candidate.profileVersion.Valid &&
				uint64(candidate.bindingVersion.Int64) == item.ProfileBindingVersion &&
				uint64(candidate.profileVersion.Int64) == item.ProfileVersion &&
				candidate.profileStatus.String == string(model.ProfileReady)
		}
		if !fenceValid {
			failed, transitionErr := item.RejectBeforeQueue(item.Version, "contract_violated")
			if transitionErr != nil {
				return BackfillMaterializationResult{}, transitionErr
			}
			if err := updateBackfillItemCASTx(ctx, tx, item.Version, failed, at); err != nil {
				return BackfillMaterializationResult{}, err
			}
			result.Failed++
			continue
		}
		workID := "work-backfill-item-" + backfillStoreDigest(backfill.BackfillID + "\x00" + item.ItemID)[:32]
		targetID := "backfill-item-" + backfillStoreDigest(backfill.BackfillID + "\x00" + item.ItemID)[:32]
		work, err := model.NewChildWork(parent, workID, "backfill_item", targetID, "historical_backfill_item", "parent")
		if err == nil {
			work, err = work.WithCausality(parent.InitiatorActorID, "", parent.WorkID)
		}
		if err != nil {
			return BackfillMaterializationResult{}, err
		}
		origin, err := canonicalOrigin(item.DetailURL)
		if err != nil {
			return BackfillMaterializationResult{}, err
		}
		placement := WorkPlacement{BusinessKey: "backfill-item|" + backfill.BackfillID + "|" + item.ItemID,
			Priority: 50, Capability: capability, Origin: origin, ProfileID: item.ProfileID, NotBefore: at.UTC()}
		placement, err = bindWorkPlacementScope(placement, item.CompanyID, item.SourceID)
		if err != nil {
			return BackfillMaterializationResult{}, err
		}
		if err := insertWork(ctx, tx, work, placement, at); err != nil {
			return BackfillMaterializationResult{}, err
		}
		queued, err := item.Queue(item.Version, work.WorkID)
		if err != nil {
			return BackfillMaterializationResult{}, err
		}
		if err := updateBackfillItemCASTx(ctx, tx, item.Version, queued, at); err != nil {
			return BackfillMaterializationResult{}, err
		}
		if item.ProfileID == "" {
			unprofiled[capability]++
		} else {
			profiled[profileKey{capability, item.ProfileID}]++
		}
		result.Queued++
	}
	causeSeed := backfill.BackfillID + fmt.Sprintf("\x00%d", backfill.Version)
	if len(candidates) > 0 {
		causeSeed += "\x00" + candidates[0].item.ItemID + "\x00" + candidates[len(candidates)-1].item.ItemID
	}
	causeID := "backfill-materialize-" + backfillStoreDigest(causeSeed)
	if len(unprofiled) > 0 {
		created, err := appendCapabilityDispatches(ctx, tx, targets, unprofiled, causeID, at.UTC())
		if err != nil {
			return BackfillMaterializationResult{}, err
		}
		if created == 0 {
			return BackfillMaterializationResult{}, fmt.Errorf("no executor target serves backfill capability")
		}
		result.Dispatches += created
	}
	if len(profiled) > 0 {
		demands := make([]profileDispatchDemand, 0, len(profiled))
		for key, count := range profiled {
			demands = append(demands, profileDispatchDemand{Capability: key.capability, ProfileID: key.profileID, Count: count})
		}
		created, err := appendProfileDispatches(ctx, tx, demands, causeID, at.UTC())
		if err != nil {
			return BackfillMaterializationResult{}, err
		}
		result.Dispatches += created
	}
	if result.Failed > 0 {
		var succeeded, gaps, failed, canceled uint64
		if err := tx.QueryRowContext(ctx, `SELECT
SUM(item_status = 'succeeded'), SUM(item_status = 'accepted_gap'), SUM(item_status = 'failed'),
SUM(item_status = 'canceled') FROM recruiting_backfill_items WHERE backfill_id = ?`, backfill.BackfillID).
			Scan(&succeeded, &gaps, &failed, &canceled); err != nil {
			return BackfillMaterializationResult{}, err
		}
		next, err := backfill.ReconcileCounts(backfill.Version, succeeded, gaps, failed, canceled)
		if err != nil {
			return BackfillMaterializationResult{}, err
		}
		waiting, err := parent.WaitHuman(parent.Version, "backfill_item_failed")
		if err != nil {
			return BackfillMaterializationResult{}, err
		}
		if err := updateBackfillCASTx(ctx, tx, backfill.Version, next, at); err != nil {
			return BackfillMaterializationResult{}, err
		}
		if err := updateWorkTx(ctx, tx, parent.Version, waiting, at); err != nil {
			return BackfillMaterializationResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return BackfillMaterializationResult{}, err
	}
	return result, nil
}

func (r *Repository) ApplyConfirmBackfillCommand(ctx context.Context, backfillID string,
	expectedBackfillVersion, expectedWorkVersion uint64, previewHash string, receipt model.CommandReceipt,
	event model.EventIntent, businessAt time.Time) (CommandResult, error) {
	if strings.TrimSpace(backfillID) == "" || expectedBackfillVersion == 0 || expectedWorkVersion == 0 ||
		strings.TrimSpace(previewHash) == "" || receipt.CommandID == "" || event.AggregateType != "work" ||
		event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("backfill confirmation facts are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil || !eventAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("backfill confirmation time is inconsistent")
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
	current, err := getBackfillWith(ctx, tx, backfillID, true)
	if err != nil {
		return CommandResult{}, err
	}
	if current.Version != expectedBackfillVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedBackfillVersion, Actual: current.Version}
	}
	parent, err := getWorkWith(ctx, tx, current.WorkID, true)
	if err != nil {
		return CommandResult{}, err
	}
	if parent.Version != expectedWorkVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedWorkVersion, Actual: parent.Version}
	}
	next, err := current.Confirm(current.Version, previewHash)
	if err != nil {
		return CommandResult{}, err
	}
	nextParent, err := parent.Start(parent.Version)
	if err != nil {
		return CommandResult{}, err
	}
	if next.Status == model.BackfillCompleted {
		nextParent, err = nextParent.Complete(nextParent.Version, model.ResolutionSucceeded, "", "")
		if err != nil {
			return CommandResult{}, err
		}
	}
	if event.AggregateID != parent.WorkID || event.AggregateVersion != nextParent.Version {
		return CommandResult{}, fmt.Errorf("backfill confirmation event does not match resulting parent Work")
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	if err := updateBackfillCASTx(ctx, tx, current.Version, next, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := updateWorkTx(ctx, tx, parent.Version, nextParent, businessAt); err != nil {
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

func (r *Repository) ApplyCreateBackfillCommand(ctx context.Context, parent model.Work,
	placement WorkPlacement, backfill model.Backfill, receipt model.CommandReceipt,
	event model.EventIntent, businessAt time.Time) (CommandResult, error) {
	if parent.WorkID == "" || parent.Status != model.WorkOpen || parent.Version != 1 || parent.ParentWorkID != "" ||
		parent.Purpose != "historical_backfill" || parent.TargetType != backfill.TargetType ||
		parent.TargetID != backfill.TargetID || parent.InitiatorActorID != backfill.RequestedBy ||
		backfill.WorkID != parent.WorkID || backfill.Status != model.BackfillPreviewing || backfill.Version != 1 ||
		placement.Capability != "" || placement.NotBefore.IsZero() || receipt.CommandID == "" ||
		event.AggregateType != "work" || event.AggregateID != parent.WorkID || event.AggregateVersion != parent.Version ||
		event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("backfill creation facts are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil || !eventAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("backfill creation time is inconsistent")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin backfill creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	if err := validateBackfillTargetTx(ctx, tx, backfill.TargetType, backfill.TargetID); err != nil {
		return CommandResult{}, err
	}
	recipe, err := getRecipeForUpdate(ctx, tx, backfill.RecipeID, backfill.RecipeVersion)
	if err != nil {
		return CommandResult{}, err
	}
	if recipe.Status != model.RecipeActive || recipe.Kind != model.RecipeDetail {
		return CommandResult{}, fmt.Errorf("historical backfill requires an active Detail Recipe")
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
	if err := insertBackfillTx(ctx, tx, backfill, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit backfill creation: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func validateBackfillTargetTx(ctx context.Context, tx *sql.Tx, targetType, targetID string) error {
	query := "SELECT 1 FROM recruiting_companies WHERE company_id = ? FOR UPDATE"
	if targetType == "source" {
		query = "SELECT 1 FROM recruiting_sources WHERE source_id = ? FOR UPDATE"
	}
	var one int
	if err := tx.QueryRowContext(ctx, query, targetID).Scan(&one); errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	} else if err != nil {
		return fmt.Errorf("lock backfill target: %w", err)
	}
	return nil
}

func insertBackfillTx(ctx context.Context, tx *sql.Tx, backfill model.Backfill, businessAt time.Time) error {
	state, err := json.Marshal(backfill)
	if err != nil {
		return err
	}
	fields, _ := json.Marshal(backfill.Fields)
	start, err := time.Parse(time.RFC3339, backfill.RangeStart)
	if err != nil {
		return err
	}
	end, err := time.Parse(time.RFC3339, backfill.RangeEnd)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO recruiting_backfills(
  backfill_id, work_id, requested_by, target_type, target_id, backfill_mode,
  range_start, range_end, fields_json, recipe_id, recipe_version, policy_version,
	  backfill_status, preview_cursor, previewed_items, preview_accumulator, preview_hash,
	  succeeded_items, accepted_gap_items, failed_items, canceled_items, cancel_scope_operation_id,
	  version, state_json, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, 0, NULL, NULL, 0, 0, 0, 0, NULL, ?, ?, ?, ?)`,
		backfill.BackfillID, backfill.WorkID, backfill.RequestedBy, backfill.TargetType, backfill.TargetID,
		backfill.Mode, start.UTC(), end.UTC(), fields, backfill.RecipeID, backfill.RecipeVersion,
		backfill.PolicyVersion, backfill.Status, backfill.Version, state, businessAt.UTC(), businessAt.UTC())
	if err != nil {
		return fmt.Errorf("insert backfill: %w", err)
	}
	return nil
}

// PreviewBackfillChunk freezes at most 500 selections in one transaction. A
// historical recompute selects every accepted DetailVersion in the requested
// interval; a live refetch selects every Job observed by a listing in that
// interval. Neither path reads or writes an incremental Checkpoint.
func (r *Repository) PreviewBackfillChunk(ctx context.Context, backfillID string,
	expectedVersion uint64, businessAt time.Time) (model.Backfill, []model.BackfillItem, error) {
	if strings.TrimSpace(backfillID) == "" || expectedVersion == 0 || businessAt.IsZero() {
		return model.Backfill{}, nil, fmt.Errorf("backfill, expected version, and business time are required")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return model.Backfill{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := getBackfillWith(ctx, tx, backfillID, true)
	if err != nil {
		return model.Backfill{}, nil, err
	}
	if current.Version != expectedVersion {
		return model.Backfill{}, nil, &model.VersionConflictError{Expected: expectedVersion, Actual: current.Version}
	}
	items, hasMore, err := selectBackfillCandidatesTx(ctx, tx, current)
	if err != nil {
		return model.Backfill{}, nil, err
	}
	for _, item := range items {
		if err := insertBackfillItemTx(ctx, tx, item, businessAt); err != nil {
			return model.Backfill{}, nil, err
		}
	}
	accumulator, err := model.AdvanceBackfillPreviewHash(current, current.PreviewAccumulator, items)
	if err != nil {
		return model.Backfill{}, nil, err
	}
	nextCursor := ""
	if hasMore {
		nextCursor = items[len(items)-1].ItemID
	}
	next, err := current.AdvancePreview(current.Version, uint64(len(items)), nextCursor, accumulator, !hasMore)
	if err != nil {
		return model.Backfill{}, nil, err
	}
	if err := updateBackfillCASTx(ctx, tx, current.Version, next, businessAt); err != nil {
		return model.Backfill{}, nil, err
	}
	if !hasMore {
		parent, err := getWorkWith(ctx, tx, current.WorkID, true)
		if err != nil {
			return model.Backfill{}, nil, err
		}
		started, err := parent.Start(parent.Version)
		if err != nil {
			return model.Backfill{}, nil, err
		}
		waiting, err := started.WaitHuman(started.Version, "preview_ready")
		if err != nil {
			return model.Backfill{}, nil, err
		}
		response, _ := json.Marshal(map[string]any{"backfill_id": next.BackfillID, "preview_hash": next.PreviewHash,
			"previewed_items": next.PreviewedItems, "status": next.Status})
		internalID := "backfill-preview-" + backfillStoreDigest(next.BackfillID)
		receipt, _ := model.NewCommandReceipt(internalID, "recruiting.internal.backfill.preview.finished",
			"sha256:"+backfillStoreDigest(string(response)), response)
		if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
			return model.Backfill{}, nil, err
		}
		if err := updateWorkTx(ctx, tx, parent.Version, waiting, businessAt); err != nil {
			return model.Backfill{}, nil, err
		}
		event, _ := model.NewEventIntent("event-"+backfillStoreDigest(internalID), "backfill.previewed", "work",
			waiting.WorkID, waiting.Version, businessAt.UTC().Format(time.RFC3339Nano), internalID, response)
		if err := appendEventIntent(ctx, tx, event, businessAt, businessAt); err != nil {
			return model.Backfill{}, nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return model.Backfill{}, nil, err
	}
	return next, items, nil
}

func backfillStoreDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func nullableUint64(value uint64) any {
	if value == 0 {
		return nil
	}
	return value
}

func selectBackfillCandidatesTx(ctx context.Context, tx *sql.Tx, backfill model.Backfill) ([]model.BackfillItem, bool, error) {
	start, _ := time.Parse(time.RFC3339, backfill.RangeStart)
	end, _ := time.Parse(time.RFC3339, backfill.RangeEnd)
	recipe, err := getRecipeForUpdate(ctx, tx, backfill.RecipeID, backfill.RecipeVersion)
	if err != nil {
		return nil, false, err
	}
	if recipe.Status != model.RecipeActive || recipe.Kind != model.RecipeDetail {
		return nil, false, fmt.Errorf("backfill preview Recipe is no longer active Detail execution")
	}
	if backfill.Mode == model.BackfillArtifactRecompute {
		rows, err := tx.QueryContext(ctx, `
SELECT d.detail_version_id, d.artifact_id, d.refresh_generation, d.detail_version,
       d.content_hash, d.recipe_id, d.recipe_version, d.observed_at,
       j.state_json, s.state_json, c.state_json
FROM recruiting_job_detail_versions d
JOIN recruiting_source_jobs j ON j.job_id = d.job_id
JOIN recruiting_sources s ON s.source_id = j.source_id
JOIN recruiting_companies c ON c.company_id = s.company_id
JOIN recruiting_artifacts a ON a.artifact_id = d.artifact_id AND a.rejected = FALSE
WHERE d.detail_version_id > ? AND d.observed_at >= ? AND d.observed_at < ?
  AND ((? = 'source' AND s.source_id = ?) OR (? = 'company' AND s.company_id = ?))
ORDER BY d.detail_version_id
LIMIT 501`, backfill.PreviewCursor, start.UTC(), end.UTC(), backfill.TargetType, backfill.TargetID,
			backfill.TargetType, backfill.TargetID)
		if err != nil {
			return nil, false, fmt.Errorf("select historical backfill candidates: %w", err)
		}
		defer rows.Close()
		items := make([]model.BackfillItem, 0, 500)
		for rows.Next() {
			var detail model.JobDetailVersion
			var observed time.Time
			var jobState, sourceState, companyState []byte
			if err := rows.Scan(&detail.DetailVersionID, &detail.ArtifactID, &detail.RefreshGeneration,
				&detail.Version, &detail.ContentHash, &detail.RecipeID, &detail.RecipeVersion, &observed,
				&jobState, &sourceState, &companyState); err != nil {
				return nil, false, err
			}
			if len(items) == 500 {
				return items, true, nil
			}
			var job model.SourceJob
			var source model.RecruitmentSource
			var company model.Company
			if err := json.Unmarshal(jobState, &job); err != nil {
				return nil, false, err
			}
			if err := json.Unmarshal(sourceState, &source); err != nil {
				return nil, false, err
			}
			if err := json.Unmarshal(companyState, &company); err != nil {
				return nil, false, err
			}
			detail.JobID, detail.ObservedAt = job.JobID, observed.UTC().Format(time.RFC3339Nano)
			item, err := model.NewBackfillItem(backfill.BackfillID, detail.DetailVersionID, backfill.Mode,
				job, source, company, &detail, nil, nil)
			if err != nil {
				return nil, false, err
			}
			items = append(items, item)
		}
		return items, false, rows.Err()
	}

	rows, err := tx.QueryContext(ctx, `
SELECT j.job_id, j.state_json, s.state_json, c.state_json,
       binding.state_json, profile.state_json
FROM recruiting_source_jobs j
JOIN recruiting_sources s ON s.source_id = j.source_id
JOIN recruiting_companies c ON c.company_id = s.company_id
LEFT JOIN recruiting_source_profile_bindings binding
  ON binding.source_id = s.source_id AND binding.recipe_kind = 'detail'
LEFT JOIN recruiting_profiles profile ON profile.profile_id = binding.profile_id
WHERE j.job_id > ?
  AND ((? = 'source' AND s.source_id = ?) OR (? = 'company' AND s.company_id = ?))
  AND EXISTS (
    SELECT 1 FROM recruiting_listing_observations o
    WHERE o.job_id = j.job_id AND o.observed_at >= ? AND o.observed_at < ?
  )
ORDER BY j.job_id
LIMIT 501`, backfill.PreviewCursor, backfill.TargetType, backfill.TargetID, backfill.TargetType,
		backfill.TargetID, start.UTC(), end.UTC())
	if err != nil {
		return nil, false, fmt.Errorf("select live backfill candidates: %w", err)
	}
	defer rows.Close()
	items := make([]model.BackfillItem, 0, 500)
	for rows.Next() {
		var itemID string
		var jobState, sourceState, companyState, bindingState, profileState []byte
		if err := rows.Scan(&itemID, &jobState, &sourceState, &companyState, &bindingState, &profileState); err != nil {
			return nil, false, err
		}
		if len(items) == 500 {
			return items, true, nil
		}
		var job model.SourceJob
		var source model.RecruitmentSource
		var company model.Company
		if err := json.Unmarshal(jobState, &job); err != nil {
			return nil, false, err
		}
		if err := json.Unmarshal(sourceState, &source); err != nil {
			return nil, false, err
		}
		if err := json.Unmarshal(companyState, &company); err != nil {
			return nil, false, err
		}
		var binding *model.SourceProfileBinding
		var profile *model.BrowserProfile
		if recipe.Execution.RequiredCapability == "browser.recipe" {
			if len(bindingState) == 0 || len(profileState) == 0 {
				return nil, false, fmt.Errorf("live Browser backfill requires a ready frozen Detail Profile")
			}
			var bindingValue model.SourceProfileBinding
			var profileValue model.BrowserProfile
			if err := json.Unmarshal(bindingState, &bindingValue); err != nil {
				return nil, false, err
			}
			if err := json.Unmarshal(profileState, &profileValue); err != nil {
				return nil, false, err
			}
			binding, profile = &bindingValue, &profileValue
		}
		item, err := model.NewBackfillItem(backfill.BackfillID, itemID, backfill.Mode, job, source, company, nil, binding, profile)
		if err != nil {
			return nil, false, err
		}
		items = append(items, item)
	}
	return items, false, rows.Err()
}

func insertBackfillItemTx(ctx context.Context, tx *sql.Tx, item model.BackfillItem, businessAt time.Time) error {
	state, err := json.Marshal(item)
	if err != nil {
		return err
	}
	var observed any
	if item.InputObservedAt != "" {
		parsed, err := time.Parse(time.RFC3339, item.InputObservedAt)
		if err != nil {
			return err
		}
		observed = parsed.UTC()
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO recruiting_backfill_items(
  backfill_id, item_id, job_id, job_version, refresh_generation, source_id, source_version,
  company_id, company_version, detail_url, profile_id, profile_version, profile_binding_version,
  input_detail_version_id, input_artifact_id, input_observed_at, work_id, output_id,
  item_status, failure_class, version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, NULL, ?, ?, ?, ?)`,
		item.BackfillID, item.ItemID, item.JobID, item.JobVersion, item.RefreshGeneration, item.SourceID,
		item.SourceVersion, item.CompanyID, item.CompanyVersion, item.DetailURL, nullableString(item.ProfileID),
		nullableUint64(item.ProfileVersion), nullableUint64(item.ProfileBindingVersion),
		nullableString(item.InputDetailVersionID), nullableString(item.InputArtifactID),
		observed, item.Status, item.Version, state, businessAt.UTC(), businessAt.UTC())
	if err != nil {
		return fmt.Errorf("insert backfill item: %w", err)
	}
	return nil
}

func updateBackfillItemCASTx(ctx context.Context, tx *sql.Tx, expected uint64, next model.BackfillItem,
	businessAt time.Time) error {
	state, err := json.Marshal(next)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_backfill_items
SET work_id = ?, output_id = ?, item_status = ?, failure_class = ?, version = ?, state_json = ?, updated_at = ?
WHERE backfill_id = ? AND item_id = ? AND version = ?`, nullableString(next.WorkID), nullableString(next.OutputID),
		next.Status, nullableString(next.FailureClass), next.Version, state, businessAt.UTC(), next.BackfillID,
		next.ItemID, expected)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return &model.VersionConflictError{Expected: expected, Actual: next.Version}
	}
	return nil
}

func (r *Repository) GetBackfill(ctx context.Context, backfillID string) (model.Backfill, error) {
	return getBackfillWith(ctx, r.db, backfillID, false)
}

func (r *Repository) ListPreviewingBackfills(ctx context.Context, limit int) ([]model.Backfill, error) {
	if limit < 1 || limit > 500 {
		return nil, fmt.Errorf("limit must be in [1,500]")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT state_json FROM recruiting_backfills
WHERE backfill_status = ? ORDER BY updated_at, backfill_id LIMIT ?`, model.BackfillPreviewing, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.Backfill, 0, limit)
	for rows.Next() {
		var state []byte
		var backfill model.Backfill
		if err := rows.Scan(&state); err != nil {
			return nil, err
		}
		if err := json.Unmarshal(state, &backfill); err != nil {
			return nil, err
		}
		result = append(result, backfill)
	}
	return result, rows.Err()
}

func getBackfillWith(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, backfillID string, lock bool) (model.Backfill, error) {
	statement := "SELECT state_json FROM recruiting_backfills WHERE backfill_id = ?"
	if lock {
		statement += " FOR UPDATE"
	}
	var state []byte
	if err := query.QueryRowContext(ctx, statement, backfillID).Scan(&state); errors.Is(err, sql.ErrNoRows) {
		return model.Backfill{}, ErrNotFound
	} else if err != nil {
		return model.Backfill{}, fmt.Errorf("get backfill: %w", err)
	}
	var backfill model.Backfill
	if err := json.Unmarshal(state, &backfill); err != nil {
		return model.Backfill{}, err
	}
	return backfill, nil
}

func updateBackfillCASTx(ctx context.Context, tx *sql.Tx, expected uint64, next model.Backfill, businessAt time.Time) error {
	state, _ := json.Marshal(next)
	result, err := tx.ExecContext(ctx, `
UPDATE recruiting_backfills
SET backfill_status = ?, preview_cursor = ?, previewed_items = ?, preview_accumulator = ?, preview_hash = ?,
    succeeded_items = ?, accepted_gap_items = ?, failed_items = ?, canceled_items = ?, cancel_scope_operation_id = ?,
    version = ?, state_json = ?, updated_at = ?
WHERE backfill_id = ? AND version = ?`, next.Status, nullableString(next.PreviewCursor), next.PreviewedItems,
		nullableString(next.PreviewAccumulator), nullableString(next.PreviewHash), next.SucceededItems,
		next.AcceptedGapItems, next.FailedItems, next.CanceledItems, nullableString(next.CancelScopeOperationID),
		next.Version, state, businessAt.UTC(), next.BackfillID, expected)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return &model.VersionConflictError{Expected: expected, Actual: next.Version}
	}
	return nil
}

func (r *Repository) ListBackfillItems(ctx context.Context, backfillID, afterItemID string,
	limit int) (BackfillItemPage, error) {
	if strings.TrimSpace(backfillID) == "" || limit < 1 || limit > 500 {
		return BackfillItemPage{}, fmt.Errorf("backfill and limit in [1,500] are required")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT state_json FROM recruiting_backfill_items
WHERE backfill_id = ? AND item_id > ? ORDER BY item_id LIMIT ?`, backfillID, afterItemID, limit+1)
	if err != nil {
		return BackfillItemPage{}, err
	}
	defer rows.Close()
	page := BackfillItemPage{Items: make([]model.BackfillItem, 0, limit)}
	for rows.Next() {
		var state []byte
		var item model.BackfillItem
		if err := rows.Scan(&state); err != nil {
			return BackfillItemPage{}, err
		}
		if err := json.Unmarshal(state, &item); err != nil {
			return BackfillItemPage{}, err
		}
		page.Items = append(page.Items, item)
	}
	if err := rows.Err(); err != nil {
		return BackfillItemPage{}, err
	}
	if len(page.Items) > limit {
		page.NextCursor = page.Items[limit-1].ItemID
		page.Items = page.Items[:limit]
	}
	return page, nil
}
