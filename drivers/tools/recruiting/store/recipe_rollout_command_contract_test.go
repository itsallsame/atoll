package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
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
}
