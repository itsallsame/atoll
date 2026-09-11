package store

import (
	"context"
	"encoding/json"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestSourceReassignmentPreservesHistoryAndCreatesIndependentCandidate(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	now := time.Date(2092, 1, 2, 3, 4, 5, 6000, time.UTC)

	from := readyMergeCompany(t, ctx, repository,
		createMergeCompany(t, ctx, repository, "reassign-from", "https://from.reassign.example", now), now)
	to := readyMergeCompany(t, ctx, repository,
		createMergeCompany(t, ctx, repository, "reassign-to", "https://to.reassign.example", now), now)
	oldSource := persistReadyDailySource(t, ctx, repository, from, "reassign-old-source", now)
	job, err := model.NewSourceJob("reassign-old-job", oldSource.SourceID, "job-1",
		"https://cutoff.example.com/jobs/reassign-old-source/1")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertJob(ctx, tx, job, now); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	checkpoint, err := model.EstablishCheckpoint(model.IncrementalCheckpoint{
		SourceID: oldSource.SourceID, RecipeID: oldSource.ListingAssignment.RecipeID,
		RecipeVersion: oldSource.ListingAssignment.RecipeVersion, ContractHash: oldSource.ListingAssignment.ContractHash,
		Strategy: model.CheckpointActivityTime, FrontierActivityAt: now.Add(-time.Hour).Format(time.RFC3339),
		OverlapPages: 1, LastOccurrenceID: "reassign-baseline",
	})
	if err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	checkpointState, _ := json.Marshal(checkpoint)
	if _, err := tx.ExecContext(ctx, `INSERT INTO recruiting_checkpoints(
  source_id, checkpoint_version, recipe_id, recipe_version, contract_hash,
  frontier_activity_at, frontier_keys_json, last_occurrence_id, state_json, updated_at
) VALUES (?, ?, ?, ?, ?, ?, NULL, ?, ?, ?)`, checkpoint.SourceID, checkpoint.Version, checkpoint.RecipeID,
		checkpoint.RecipeVersion, checkpoint.ContractHash, now.Add(-time.Hour), checkpoint.LastOccurrenceID,
		checkpointState, now); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	preview := inspectAndApplySourceReassignmentPreview(t, ctx, repository, SourceReassignmentPreviewRequest{
		PreviewID: "reassign-preview", Relation: model.SourceSupersedes, SourceID: oldSource.SourceID,
		ExpectedSourceVersion: oldSource.Version, TargetCompanyID: to.CompanyID, ExpectedCompanyVersion: to.Version,
		NewSourceID: "reassign-new-source", DiscoveryGeneration: 1, RequestedBy: "human:initiator",
		Reason: "board belongs to target company", ObservedAt: now,
	}, "reassign-preview-command", now)
	if preview.JobCount != 1 || preview.CheckpointVersion != checkpoint.Version ||
		preview.ListingAssignmentVersion != oldSource.ListingAssignment.AssignmentVersion {
		t.Fatalf("preview did not freeze impact and execution facts: %+v", preview)
	}
	result := confirmSourceReassignment(t, ctx, repository, preview, "reassign-confirm-command",
		"reassign-lineage", "reassign-event", "reassign-cancel-operation", now.Add(time.Minute))
	var outcome SourceReassignmentConfirmOutcome
	if err := json.Unmarshal(result.Response, &outcome); err != nil {
		t.Fatal(err)
	}
	if result.Replayed || outcome.PreviousSource.ControlStatus != model.ControlArchived ||
		outcome.PreviousSource.CompanyID != from.CompanyID || outcome.NewSource.CompanyID != to.CompanyID ||
		outcome.NewSource.ReadinessStatus != model.SourceCandidate || outcome.NewSource.ListingAssignment != nil ||
		outcome.Lineage.ChangedBy != "human:reviewer" || outcome.Lineage.Reason != "confirm ownership move" {
		t.Fatalf("unexpected reassignment outcome: %+v", outcome)
	}
	storedOld, _ := repository.GetSource(ctx, oldSource.SourceID)
	storedNew, _ := repository.GetSource(ctx, outcome.NewSource.SourceID)
	if storedOld.CompanyID != from.CompanyID || storedOld.ListingAssignment == nil ||
		storedNew.CompanyID != to.CompanyID || storedNew.ListingAssignment != nil {
		t.Fatalf("historical facts were rewritten or copied: old=%+v new=%+v", storedOld, storedNew)
	}
	if _, err := repository.GetCheckpoint(ctx, storedOld.SourceID); err != nil {
		t.Fatalf("old checkpoint was not preserved: %v", err)
	}
	if _, err := repository.GetCheckpoint(ctx, storedNew.SourceID); err != ErrNotFound {
		t.Fatalf("new Source inherited checkpoint: %v", err)
	}
	var oldJobs, newJobs int
	if err := db.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM recruiting_source_jobs WHERE source_id = ?),
  (SELECT COUNT(*) FROM recruiting_source_jobs WHERE source_id = ?)`, storedOld.SourceID, storedNew.SourceID).
		Scan(&oldJobs, &newJobs); err != nil || oldJobs != 1 || newJobs != 0 {
		t.Fatalf("Job history old=%d new=%d err=%v", oldJobs, newJobs, err)
	}
	operation, err := repository.GetScopeControlOperation(ctx, "reassign-cancel-operation")
	if err != nil || operation.ScopeID != oldSource.SourceID || operation.Mode != model.PauseCancel {
		t.Fatalf("superseded Source cancellation operation=%+v err=%v", operation, err)
	}
	replay := confirmSourceReassignment(t, ctx, repository, preview, "reassign-confirm-command",
		"reassign-lineage", "reassign-event", "reassign-cancel-operation", now.Add(time.Minute))
	if !replay.Replayed || string(replay.Response) != string(result.Response) {
		t.Fatalf("confirmation replay changed: first=%s replay=%s", result.Response, replay.Response)
	}
}

func TestSourceSplitReassignmentRetainsOldSourceAndSerializesConfirmation(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	now := time.Date(2092, 2, 3, 4, 5, 6, 0, time.UTC)
	from := readyMergeCompany(t, ctx, repository,
		createMergeCompany(t, ctx, repository, "split-from", "https://from.split.example", now), now)
	to := readyMergeCompany(t, ctx, repository,
		createMergeCompany(t, ctx, repository, "split-to", "https://to.split.example", now), now)
	oldSource := persistReadyDailySource(t, ctx, repository, from, "split-old-source", now)
	defer pauseExecutionSource(t, ctx, repository, oldSource.SourceID, now.Add(10*time.Minute))
	preview := inspectAndApplySourceReassignmentPreview(t, ctx, repository, SourceReassignmentPreviewRequest{
		PreviewID: "split-preview", Relation: model.SourceSplitFrom, SourceID: oldSource.SourceID,
		ExpectedSourceVersion: oldSource.Version, TargetCompanyID: to.CompanyID, ExpectedCompanyVersion: to.Version,
		NewSourceID: "split-new-source", DiscoveryGeneration: 1, RequestedBy: "human:initiator",
		Reason: "target company shares the board", ObservedAt: now,
	}, "split-preview-command", now)

	type result struct {
		command string
		err     error
	}
	results := make(chan result, 2)
	var start sync.WaitGroup
	start.Add(1)
	for _, commandID := range []string{"split-confirm-a", "split-confirm-b"} {
		go func(commandID string) {
			start.Wait()
			response, _ := json.Marshal(map[string]any{"next_action": "validate_new_source"})
			receipt, _ := model.NewCommandReceipt(commandID, "recruiting.source.reassign.confirm",
				"hash-"+commandID, response)
			_, confirmErr := repository.ApplySourceReassignmentConfirmCommand(ctx, preview.PreviewID, preview.Version,
				preview.PreviewHash, receipt, "lineage-"+commandID, "event-"+commandID, "human:reviewer",
				"confirm split", "", now.Add(time.Minute))
			results <- result{command: commandID, err: confirmErr}
		}(commandID)
	}
	start.Done()
	winners := 0
	for range 2 {
		if (<-results).err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("concurrent split confirmations winners=%d", winners)
	}
	storedOld, err := repository.GetSource(ctx, oldSource.SourceID)
	if err != nil || storedOld.ControlStatus != model.ControlActive || storedOld.CompanyID != from.CompanyID {
		t.Fatalf("split changed old Source: %+v err=%v", storedOld, err)
	}
	var newSources, lineages, operations int
	if err := db.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM recruiting_sources WHERE company_id = ? AND canonical_source_key = ?),
  (SELECT COUNT(*) FROM recruiting_source_lineage WHERE from_source_id = ?),
  (SELECT COUNT(*) FROM recruiting_scope_control_operations WHERE scope_type = 'source' AND scope_id = ?)`,
		to.CompanyID, oldSource.ActiveEndpoint.CanonicalKey, oldSource.SourceID, oldSource.SourceID).
		Scan(&newSources, &lineages, &operations); err != nil || newSources != 1 || lineages != 1 || operations != 0 {
		t.Fatalf("split projections new=%d lineage=%d operations=%d err=%v", newSources, lineages, operations, err)
	}
}

func inspectAndApplySourceReassignmentPreview(t *testing.T, ctx context.Context, repository *Repository,
	request SourceReassignmentPreviewRequest, commandID string, now time.Time) model.SourceReassignmentPreview {
	t.Helper()
	preview, err := repository.InspectSourceReassignment(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	response, _ := json.Marshal(map[string]any{"preview": preview, "next_action": "confirm_exact_preview"})
	receipt, _ := model.NewCommandReceipt(commandID, "recruiting.source.reassign.preview", "hash-"+commandID, response)
	event, _ := model.NewEventIntent("event-"+commandID, "source.reassignment.previewed",
		"source_reassignment_preview", preview.PreviewID, preview.Version, now.Format(time.RFC3339Nano), commandID,
		json.RawMessage(`{"requested_by":"human:initiator"}`))
	if _, err := repository.ApplySourceReassignmentPreviewCommand(ctx, preview, receipt, event, now); err != nil {
		t.Fatal(err)
	}
	return preview
}

func confirmSourceReassignment(t *testing.T, ctx context.Context, repository *Repository,
	preview model.SourceReassignmentPreview, commandID, lineageID, eventPrefix, operationID string,
	now time.Time) CommandResult {
	t.Helper()
	response, _ := json.Marshal(map[string]any{"next_action": "validate_new_source"})
	receipt, _ := model.NewCommandReceipt(commandID, "recruiting.source.reassign.confirm", "hash-"+commandID, response)
	result, err := repository.ApplySourceReassignmentConfirmCommand(ctx, preview.PreviewID, preview.Version,
		preview.PreviewHash, receipt, lineageID, eventPrefix, "human:reviewer", "confirm ownership move",
		operationID, now)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
