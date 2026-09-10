package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestDetailRecipeRolloutCommandIsFencedReplayableAndAtomic(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	now := time.Date(2090, 9, 9, 1, 0, 0, 0, time.UTC)

	company, _ := model.NewCompany("rollout-company", "Rollout", "https://rollout.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("rollout-source", company.CompanyID, "https://rollout.example.com/jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	validating, _ := source.BeginValidation(source.Version)
	if err := repository.UpdateSourceCAS(ctx, source.Version, validating, now); err != nil {
		t.Fatal(err)
	}
	listing := activeRecipe(t, "rollout-listing", model.RecipeListing, "rollout.example.com", 1, "listing-contract")
	if err := repository.CreateRecipe(ctx, listing, now); err != nil {
		t.Fatal(err)
	}
	listingAssignment, _ := model.NewSourceRecipeAssignment(source.SourceID, model.RecipeListing, listing.RecipeID,
		listing.Version, listing.ContractHash, now.Format(time.RFC3339))
	ready, _ := validating.PublishValidated(validating.Version, listingAssignment, verifiedStoreAssessment(validating, listingAssignment, now))
	if err := repository.PublishSourceAssignment(ctx, validating.Version, 0, ready, listingAssignment, now); err != nil {
		t.Fatal(err)
	}
	detailV1 := activeRecipe(t, "rollout-detail", model.RecipeDetail, "rollout.example.com", 1, "detail-contract")
	detailV2 := activeRecipe(t, "rollout-detail", model.RecipeDetail, "rollout.example.com", 2, "detail-contract")
	for _, recipe := range []model.Recipe{detailV1, detailV2} {
		if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
			t.Fatal(err)
		}
	}
	detailAssignment, _ := model.NewSourceRecipeAssignment(source.SourceID, model.RecipeDetail, detailV1.RecipeID,
		detailV1.Version, detailV1.ContractHash, now.Format(time.RFC3339))
	withDetail, _ := ready.AssignRecipe(ready.Version, detailAssignment, false)
	if err := repository.PublishSourceAssignment(ctx, ready.Version, 0, withDetail, detailAssignment, now); err != nil {
		t.Fatal(err)
	}

	rolloutAt := now.Add(time.Minute)
	replacement, _ := detailAssignment.Replace(detailAssignment.AssignmentVersion, detailV2.RecipeID, detailV2.Version,
		detailV2.ContractHash, rolloutAt.Format(time.RFC3339Nano))
	next, _ := withDetail.AssignRecipe(withDetail.Version, replacement, false)
	type invocation struct {
		result  CommandResult
		err     error
		receipt model.CommandReceipt
		event   model.EventIntent
	}
	results := make(chan invocation, 2)
	var group sync.WaitGroup
	for index := range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			commandID := fmt.Sprintf("detail-rollout-command-%d", index)
			receipt, _ := model.NewCommandReceipt(commandID, "recruiting.recipe.rollout",
				fmt.Sprintf("sha256:detail-rollout-%d", index), json.RawMessage(fmt.Sprintf(`{"command":%d}`, index)))
			event, _ := model.NewEventIntent(fmt.Sprintf("detail-rollout-event-%d", index), "source.detail_recipe_rolled_out",
				"source", next.SourceID, next.Version, rolloutAt.Format(time.RFC3339Nano), commandID, json.RawMessage(`{}`))
			result, callErr := repository.ApplyDetailRecipeRolloutCommand(ctx, withDetail.Version,
				detailAssignment.AssignmentVersion, next, replacement, receipt, event, rolloutAt)
			results <- invocation{result: result, err: callErr, receipt: receipt, event: event}
		}()
	}
	group.Wait()
	close(results)
	var winner invocation
	var succeeded, conflicted int
	for result := range results {
		if result.err == nil {
			succeeded++
			winner = result
			continue
		}
		var conflict *model.VersionConflictError
		if errors.As(result.err, &conflict) || errors.Is(result.err, ErrAssignmentConflict) {
			conflicted++
			continue
		}
		t.Fatalf("concurrent rollout error = %v", result.err)
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("rollout succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	replay, err := repository.ApplyDetailRecipeRolloutCommand(ctx, withDetail.Version,
		detailAssignment.AssignmentVersion, next, replacement, winner.receipt, winner.event, rolloutAt)
	if err != nil || !replay.Replayed || string(replay.Response) != string(winner.result.Response) {
		t.Fatalf("rollout replay = %+v %v", replay, err)
	}
	storedSource, _ := repository.GetSource(ctx, source.SourceID)
	storedAssignment, _ := repository.GetAssignment(ctx, source.SourceID, model.RecipeDetail)
	if storedSource.Version != next.Version || storedSource.DetailAssignment == nil ||
		storedAssignment != replacement || *storedSource.DetailAssignment != replacement {
		t.Fatalf("source=%+v assignment=%+v", storedSource, storedAssignment)
	}
	historyV1, err := repository.GetAssignmentVersion(ctx, source.SourceID, model.RecipeDetail, 1)
	if err != nil || historyV1 != detailAssignment {
		t.Fatalf("detail assignment history v1=%+v err=%v", historyV1, err)
	}
	historyV2, err := repository.GetAssignmentVersion(ctx, source.SourceID, model.RecipeDetail, 2)
	if err != nil || historyV2 != replacement {
		t.Fatalf("detail assignment history v2=%+v err=%v", historyV2, err)
	}
	var receiptCount, eventCount int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_command_receipts WHERE command_id = ?", winner.receipt.CommandID).Scan(&receiptCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_event_outbox WHERE event_id = ?", winner.event.EventID).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if receiptCount != 1 || eventCount != 1 {
		t.Fatalf("winner receipt=%d event=%d", receiptCount, eventCount)
	}

	// A capability-changing repair must roll back every fact because retry Work
	// placement was created for the existing capability.
	incompatible := activeRecipe(t, "rollout-detail", model.RecipeDetail, "rollout.example.com", 3, "detail-contract")
	incompatible.Execution.RequiredCapability = "browser.public"
	if err := repository.CreateRecipe(ctx, incompatible, rolloutAt); err != nil {
		t.Fatal(err)
	}
	badAssignment, _ := storedAssignment.Replace(storedAssignment.AssignmentVersion, incompatible.RecipeID,
		incompatible.Version, incompatible.ContractHash, rolloutAt.Add(time.Second).Format(time.RFC3339Nano))
	badNext, _ := storedSource.AssignRecipe(storedSource.Version, badAssignment, false)
	badReceipt, _ := model.NewCommandReceipt("detail-rollout-incompatible", "recruiting.recipe.rollout",
		"sha256:detail-rollout-incompatible", json.RawMessage(`{"bad":true}`))
	badEvent, _ := model.NewEventIntent("detail-rollout-incompatible-event", "source.detail_recipe_rolled_out",
		"source", badNext.SourceID, badNext.Version, rolloutAt.Add(time.Second).Format(time.RFC3339Nano), badReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyDetailRecipeRolloutCommand(ctx, storedSource.Version, storedAssignment.AssignmentVersion,
		badNext, badAssignment, badReceipt, badEvent, rolloutAt.Add(time.Second)); !errors.Is(err, ErrRecipeRolloutRejected) {
		t.Fatalf("incompatible rollout error = %v", err)
	}
	var badReceipts, badEvents int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_command_receipts WHERE command_id = ?", badReceipt.CommandID).Scan(&badReceipts)
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_event_outbox WHERE event_id = ?", badEvent.EventID).Scan(&badEvents)
	afterRejected, _ := repository.GetSource(ctx, source.SourceID)
	if badReceipts != 0 || badEvents != 0 || afterRejected.Version != storedSource.Version {
		t.Fatalf("rejected rollout leaked receipt=%d event=%d source_version=%d", badReceipts, badEvents, afterRejected.Version)
	}
	if _, err := repository.GetAssignmentVersion(ctx, source.SourceID, model.RecipeDetail, 3); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected rollout leaked assignment history: %v", err)
	}

	// Quarantine is a constant-size Recipe lifecycle operation: it must not
	// rewrite Source assignments or their immutable history.
	quarantineAt := rolloutAt.Add(2 * time.Second)
	quarantineReceipt, _ := model.NewCommandReceipt("detail-quarantine", "recruiting.recipe.quarantine",
		"sha256:detail-quarantine", json.RawMessage(`{"status":"quarantined"}`))
	quarantineEvent, _ := model.NewEventIntent("detail-quarantine-event", "recipe.quarantined", "recipe",
		fmt.Sprintf("%s@%d", detailV2.RecipeID, detailV2.Version), detailV2.StateVersion+1,
		quarantineAt.Format(time.RFC3339Nano), quarantineReceipt.CommandID, json.RawMessage(`{}`))
	quarantineResult, err := repository.ApplyRecipeQuarantineCommand(ctx, detailV2.StateVersion, detailV2.RecipeID,
		detailV2.Version, quarantineReceipt, quarantineEvent, quarantineAt)
	if err != nil {
		t.Fatal(err)
	}
	quarantineReplay, err := repository.ApplyRecipeQuarantineCommand(ctx, detailV2.StateVersion, detailV2.RecipeID,
		detailV2.Version, quarantineReceipt, quarantineEvent, quarantineAt)
	if err != nil || !quarantineReplay.Replayed || string(quarantineReplay.Response) != string(quarantineResult.Response) {
		t.Fatalf("quarantine replay=%+v err=%v", quarantineReplay, err)
	}
	inspected, assignmentCount, err := repository.InspectRecipe(ctx, detailV2.RecipeID, detailV2.Version)
	if err != nil || inspected.Status != model.RecipeQuarantined || assignmentCount != 1 {
		t.Fatalf("quarantined Recipe=%+v assignments=%d err=%v", inspected, assignmentCount, err)
	}
	afterQuarantine, _ := repository.GetAssignment(ctx, source.SourceID, model.RecipeDetail)
	if afterQuarantine != replacement {
		t.Fatalf("quarantine rewrote assignment: %+v", afterQuarantine)
	}
	if unchangedHistory, err := repository.GetAssignmentVersion(ctx, source.SourceID, model.RecipeDetail, 2); err != nil || unchangedHistory != replacement {
		t.Fatalf("quarantine rewrote history: %+v err=%v", unchangedHistory, err)
	}

	// Rolling back to historical assignment v1 appends v3. Assignment versions
	// are monotonic operational facts and never move backwards.
	rollbackAt := quarantineAt.Add(time.Second)
	historical, _ := repository.GetAssignmentVersion(ctx, source.SourceID, model.RecipeDetail, 1)
	rollbackAssignment, _ := afterQuarantine.Replace(afterQuarantine.AssignmentVersion, historical.RecipeID,
		historical.RecipeVersion, historical.ContractHash, rollbackAt.Format(time.RFC3339Nano))
	rollbackSource, _ := afterRejected.AssignRecipe(afterRejected.Version, rollbackAssignment, false)
	rollbackReceipt, _ := model.NewCommandReceipt("detail-rollback", "recruiting.recipe.rollback",
		"sha256:detail-rollback", json.RawMessage(`{"status":"rolled_back"}`))
	rollbackEvent, _ := model.NewEventIntent("detail-rollback-event", "source.detail_recipe_rolled_back", "source",
		rollbackSource.SourceID, rollbackSource.Version, rollbackAt.Format(time.RFC3339Nano), rollbackReceipt.CommandID,
		json.RawMessage(`{}`))
	if _, err := repository.ApplyDetailRecipeRolloutCommand(ctx, afterRejected.Version,
		afterQuarantine.AssignmentVersion, rollbackSource, rollbackAssignment, rollbackReceipt, rollbackEvent,
		rollbackAt); err != nil {
		t.Fatal(err)
	}
	storedRollback, err := repository.GetAssignmentVersion(ctx, source.SourceID, model.RecipeDetail, 3)
	if err != nil || storedRollback != rollbackAssignment || storedRollback.RecipeVersion != detailV1.Version {
		t.Fatalf("rollback history v3=%+v err=%v", storedRollback, err)
	}
}

func TestListingRecipeRollbackRebindsCheckpointWithoutMovingFrontier(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	now := time.Date(2090, 9, 9, 2, 0, 0, 0, time.UTC)

	company, _ := model.NewCompany("listing-rollback-company", "Listing Rollback", "https://listing-rollback.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("listing-rollback-source", company.CompanyID,
		"https://listing-rollback.example.com/jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	validating, _ := source.BeginValidation(source.Version)
	if err := repository.UpdateSourceCAS(ctx, source.Version, validating, now); err != nil {
		t.Fatal(err)
	}
	listingV1 := activeRecipe(t, "listing-rollback-v1", model.RecipeListing,
		"listing-rollback.example.com", 1, "stable-listing-contract")
	listingV2 := activeRecipe(t, "listing-rollback-v2", model.RecipeListing,
		"listing-rollback.example.com", 1, "stable-listing-contract")
	for _, recipe := range []model.Recipe{listingV1, listingV2} {
		if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
			t.Fatal(err)
		}
	}
	assignmentV1, _ := model.NewSourceRecipeAssignment(source.SourceID, model.RecipeListing,
		listingV1.RecipeID, listingV1.Version, listingV1.ContractHash, now.Format(time.RFC3339Nano))
	readyV1, _ := validating.PublishValidated(validating.Version, assignmentV1,
		verifiedStoreAssessment(validating, assignmentV1, now))
	if err := repository.PublishSourceAssignment(ctx, validating.Version, 0, readyV1, assignmentV1, now); err != nil {
		t.Fatal(err)
	}

	// Seed a previously validated compatible rollout so immutable history has
	// v1 as the known-good rollback target and v2 as the current assignment.
	assignmentV2, _ := assignmentV1.Replace(assignmentV1.AssignmentVersion, listingV2.RecipeID,
		listingV2.Version, listingV2.ContractHash, now.Add(time.Minute).Format(time.RFC3339Nano))
	repairingV2, _ := readyV1.AssignRecipe(readyV1.Version, assignmentV2, true)
	if err := repository.PublishSourceAssignment(ctx, readyV1.Version, assignmentV1.AssignmentVersion,
		repairingV2, assignmentV2, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	validatingV2, _ := repairingV2.BeginValidation(repairingV2.Version)
	if err := repository.UpdateSourceCAS(ctx, repairingV2.Version, validatingV2, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	readyV2, _ := validatingV2.PublishValidated(validatingV2.Version, assignmentV2,
		verifiedStoreAssessment(validatingV2, assignmentV2, now.Add(2*time.Minute)))
	if err := repository.UpdateSourceCAS(ctx, validatingV2.Version, readyV2, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}

	checkpoint, err := model.EstablishCheckpoint(model.IncrementalCheckpoint{
		SourceID: source.SourceID, RecipeID: listingV2.RecipeID, RecipeVersion: listingV2.Version,
		ContractHash: listingV2.ContractHash, Strategy: model.CheckpointActivityTime,
		FrontierActivityAt: now.Add(-24 * time.Hour).Format(time.RFC3339),
		FrontierJobKeys:    []string{"job-frontier-a", "job-frontier-b"}, OverlapPages: 3, OverlapItems: 21,
		LastOccurrenceID: "daily-before-listing-rollback",
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpointState, _ := json.Marshal(checkpoint)
	if _, err := db.ExecContext(ctx, `INSERT INTO recruiting_checkpoints(
  source_id, checkpoint_version, recipe_id, recipe_version, contract_hash,
  frontier_activity_at, frontier_keys_json, last_occurrence_id, state_json, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, checkpoint.SourceID, checkpoint.Version, checkpoint.RecipeID,
		checkpoint.RecipeVersion, checkpoint.ContractHash, now.Add(-24*time.Hour), json.RawMessage(`["job-frontier-a","job-frontier-b"]`),
		checkpoint.LastOccurrenceID, checkpointState, now); err != nil {
		t.Fatal(err)
	}

	rollbackAt := now.Add(3 * time.Minute)
	historical, err := repository.GetAssignmentVersion(ctx, source.SourceID, model.RecipeListing, 1)
	if err != nil {
		t.Fatal(err)
	}
	rollbackAssignment, _ := assignmentV2.Replace(assignmentV2.AssignmentVersion, historical.RecipeID,
		historical.RecipeVersion, historical.ContractHash, rollbackAt.Format(time.RFC3339Nano))
	rollbackUnderstood := readyV2
	rollbackSource, _ := rollbackUnderstood.AssignRecipe(rollbackUnderstood.Version, rollbackAssignment, true)
	receipt, _ := model.NewCommandReceipt("listing-rollback", "recruiting.recipe.rollback",
		"sha256:listing-rollback", json.RawMessage(`{"status":"rolled_back"}`))
	event, _ := model.NewEventIntent("listing-rollback-event", "source.listing_recipe_rolled_back", "source",
		rollbackSource.SourceID, rollbackSource.Version, rollbackAt.Format(time.RFC3339Nano), receipt.CommandID,
		json.RawMessage(`{}`))
	result, err := repository.ApplyRecipeAssignmentChangeCommand(ctx, readyV2.Version,
		assignmentV2.AssignmentVersion, rollbackSource, rollbackAssignment, receipt, event, rollbackAt)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := repository.ApplyRecipeAssignmentChangeCommand(ctx, readyV2.Version,
		assignmentV2.AssignmentVersion, rollbackSource, rollbackAssignment, receipt, event, rollbackAt)
	if err != nil || !replay.Replayed || string(replay.Response) != string(result.Response) {
		t.Fatalf("Listing rollback replay=%+v err=%v", replay, err)
	}

	storedSource, _ := repository.GetSource(ctx, source.SourceID)
	storedAssignment, _ := repository.GetAssignment(ctx, source.SourceID, model.RecipeListing)
	storedCheckpoint, _ := repository.GetCheckpoint(ctx, source.SourceID)
	if storedSource.ReadinessStatus != model.SourceRepairing || storedSource.ContractAssessment != nil ||
		storedSource.CandidateEndpoint == nil || storedSource.ActiveEndpoint == nil ||
		*storedSource.CandidateEndpoint != *storedSource.ActiveEndpoint || storedSource.HasVerifiedIncrementalContract() {
		t.Fatalf("Listing rollback did not require recalibration: %+v", storedSource)
	}
	if storedAssignment != rollbackAssignment || storedAssignment.AssignmentVersion != 3 ||
		storedAssignment.RecipeID != listingV1.RecipeID {
		t.Fatalf("Listing rollback assignment=%+v", storedAssignment)
	}
	if storedCheckpoint.Version != checkpoint.Version+1 || storedCheckpoint.RecipeID != listingV1.RecipeID ||
		storedCheckpoint.RecipeVersion != listingV1.Version || storedCheckpoint.ContractHash != checkpoint.ContractHash ||
		storedCheckpoint.FrontierActivityAt != checkpoint.FrontierActivityAt ||
		storedCheckpoint.LastOccurrenceID != checkpoint.LastOccurrenceID || storedCheckpoint.OverlapPages != checkpoint.OverlapPages ||
		storedCheckpoint.OverlapItems != checkpoint.OverlapItems || !reflect.DeepEqual(storedCheckpoint.FrontierJobKeys, checkpoint.FrontierJobKeys) {
		t.Fatalf("Listing rollback moved checkpoint frontier: before=%+v after=%+v", checkpoint, storedCheckpoint)
	}
}
