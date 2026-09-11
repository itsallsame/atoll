package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestSourcePauseAtomicallyCapturesActiveCausalRootsAndReplays(t *testing.T) {
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
	now := time.Date(2026, 9, 12, 4, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("pause-company", "Pause", "https://pause.example")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("pause-source", company.CompanyID, "https://pause.example/jobs", "", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	work, _ := model.NewWork("pause-root-work", "source", source.SourceID, "listing_sync", "timer")
	if err := repository.CreateWork(ctx, work, WorkPlacement{Priority: 1, NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}
	running, _ := work.Start(work.Version)
	if err := repository.UpdateWorkCAS(ctx, work.Version, running, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	attempt, _ := model.NewAttempt("pause-root-attempt", running)
	if err := repository.CreateAttempt(ctx, attempt, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	paused, _ := source.Pause(source.Version, model.PauseFinishCausalChain)
	response := json.RawMessage(`{"source_id":"pause-source","status":"paused"}`)
	receipt, _ := model.NewCommandReceipt("pause-source-command", "recruiting.source.pause", "sha256:scope-pause", response)
	event, _ := model.NewEventIntent("pause-source-event", "source.paused", "source", source.SourceID,
		paused.Version, now.Add(2*time.Second).Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{"mode":"finish_causal_chain"}`))
	operationID := "pause-source-operation"
	first, err := repository.ApplySourcePauseCommand(ctx, source.Version, paused, receipt, event, operationID, now.Add(2*time.Second))
	if err != nil || first.Replayed {
		t.Fatalf("first pause=%+v err=%v", first, err)
	}
	replay, err := repository.ApplySourcePauseCommand(ctx, source.Version, paused, receipt, event, operationID, now.Add(2*time.Second))
	if err != nil || !replay.Replayed || string(replay.Response) != string(response) {
		t.Fatalf("pause replay=%+v err=%v", replay, err)
	}
	operation, err := repository.GetScopeControlOperation(ctx, operationID)
	if err != nil || operation.ActiveRoots != 1 || operation.ControlEpoch != paused.ControlEpoch ||
		operation.ConfigurationVersion != paused.ConfigurationVersion {
		t.Fatalf("atomic pause operation=%+v err=%v", operation, err)
	}
	var roots, operations int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_scope_control_roots
WHERE operation_id = ? AND root_work_id = ? AND root_status = 'active'`, operationID, work.WorkID).Scan(&roots); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_scope_control_operations
WHERE operation_id = ?`, operationID).Scan(&operations); err != nil {
		t.Fatal(err)
	}
	if roots != 1 || operations != 1 {
		t.Fatalf("pause facts roots=%d operations=%d", roots, operations)
	}
	projected, err := repository.ReconcileScopeControlOperation(ctx, operationID, operation.Version, 500, now.Add(3*time.Second))
	if err != nil || !projected.ProjectionCompleted || projected.Status != model.ScopeControlApplying ||
		projected.ActiveRoots != 1 || projected.WorksPaused != 0 {
		t.Fatalf("finish causal projection=%+v err=%v", projected, err)
	}
	storedWork, err := repository.GetWork(ctx, work.WorkID)
	if err != nil || storedWork.Status != model.WorkRunning {
		t.Fatalf("active causal root was frozen: %+v err=%v", storedWork, err)
	}
	completedWork, _ := storedWork.Complete(storedWork.Version, model.ResolutionSucceeded, "", "")
	if err := repository.UpdateWorkCAS(ctx, storedWork.Version, completedWork, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	settled, count, err := repository.SettleCompletedScopeControlRoots(ctx, operationID, projected.Version, 500,
		now.Add(5*time.Second))
	if err != nil || count != 1 || settled.Status != model.ScopeControlCompleted || settled.ActiveRoots != 0 {
		t.Fatalf("causal root settlement operation=%+v count=%d err=%v", settled, count, err)
	}
}

func TestCancelScopeControlFencesWorkAndExpiresAttempt(t *testing.T) {
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
	now := time.Date(2026, 9, 12, 5, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("cancel-company", "Cancel", "https://cancel.example")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("cancel-source", company.CompanyID, "https://cancel.example/jobs", "", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	work, _ := model.NewWork("cancel-running-work", "source", source.SourceID, "listing_sync", "timer")
	if err := repository.CreateWork(ctx, work, WorkPlacement{Priority: 1, NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}
	running, _ := work.Start(work.Version)
	if err := repository.UpdateWorkCAS(ctx, work.Version, running, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	attempt, _ := model.NewAttempt("cancel-running-attempt", running)
	if err := repository.CreateAttempt(ctx, attempt, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	paused, _ := source.Pause(source.Version, model.PauseCancel)
	receipt, _ := model.NewCommandReceipt("cancel-source-command", "recruiting.source.pause", "sha256:cancel-scope",
		json.RawMessage(`{"source_id":"cancel-source","status":"paused"}`))
	event, _ := model.NewEventIntent("cancel-source-event", "source.paused", "source", source.SourceID,
		paused.Version, now.Add(2*time.Second).Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{"mode":"cancel"}`))
	if _, err := repository.ApplySourcePauseCommand(ctx, source.Version, paused, receipt, event,
		"cancel-source-operation", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	operation, _ := repository.GetScopeControlOperation(ctx, "cancel-source-operation")
	projected, err := repository.ReconcileScopeControlOperation(ctx, operation.OperationID, operation.Version, 500, now.Add(3*time.Second))
	if err != nil || projected.Status != model.ScopeControlApplying || !projected.ProjectionCompleted ||
		projected.CancellationCompleted || projected.WorksCanceled != 1 || projected.AttemptsExpired != 1 {
		t.Fatalf("cancel Work projection=%+v err=%v", projected, err)
	}
	completed, err := repository.ReconcileScopeControlOperation(ctx, operation.OperationID, projected.Version, 500, now.Add(4*time.Second))
	if err != nil || completed.Status != model.ScopeControlCompleted || !completed.CancellationCompleted {
		t.Fatalf("cancel dependency reconciliation=%+v err=%v", completed, err)
	}
	storedWork, _ := repository.GetWork(ctx, work.WorkID)
	storedAttempt, _ := repository.GetAttempt(ctx, attempt.AttemptID)
	if storedWork.Status != model.WorkCanceled || storedWork.AcceptanceVersion != running.AcceptanceVersion+1 ||
		storedAttempt.Status != model.AttemptExpired {
		t.Fatalf("cancel result Work=%+v Attempt=%+v", storedWork, storedAttempt)
	}
}

func TestCancelScopeControlClosesExecutionOwnerAggregates(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Second)
	defer cancel()
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	now := time.Date(2095, 2, 21, 4, 0, 0, 0, time.UTC)

	t.Run("daily_occurrence", func(t *testing.T) {
		offerAt, _ := prepareListingExecutionWork(t, ctx, repository, "scope-owner-occurrence", 1)
		offer := startListingAttempt(t, ctx, repository, "scope-owner-occurrence", offerAt)
		operation := cancelSourceScopeAndReconcile(t, ctx, repository, offer.Occurrence.SourceID,
			"scope-owner-occurrence", offerAt.Add(10*time.Second))
		occurrence, err := repository.GetOccurrence(ctx, offer.Occurrence.OccurrenceID)
		assertScopeCanceledExecution(t, ctx, db, repository, operation, offer.Work.WorkID, offer.Attempt.AttemptID)
		if err != nil || occurrence.Status != model.OccurrenceException ||
			!strings.HasPrefix(occurrence.Outcome, "scope_canceled:") {
			t.Fatalf("canceled occurrence=%+v err=%v", occurrence, err)
		}
	})

	t.Run("source_discovery", func(t *testing.T) {
		fixture := createRunningSourceDiscoveryFixture(t, ctx, repository, "scope-owner-discovery", now.Add(time.Hour))
		operation := cancelCompanyScopeAndReconcile(t, ctx, repository, fixture.company.CompanyID,
			"scope-owner-discovery", fixture.now.Add(10*time.Second))
		discovery, err := repository.GetSourceDiscovery(ctx, fixture.discovery.DiscoveryID)
		assertScopeCanceledExecution(t, ctx, db, repository, operation, fixture.offer.Work.WorkID,
			fixture.offer.Attempt.AttemptID)
		if err != nil || discovery.Status != model.SourceDiscoveryCanceled {
			t.Fatalf("canceled Source Discovery=%+v err=%v", discovery, err)
		}
	})

	t.Run("listing_run", func(t *testing.T) {
		fixture := createRunningSourceValidationFixture(t, ctx, repository, "scope-owner-validation", now.Add(2*time.Hour))
		operation := cancelSourceScopeAndReconcile(t, ctx, repository, fixture.source.SourceID,
			"scope-owner-validation", fixture.now.Add(10*time.Second))
		run, err := repository.GetListingRunByWork(ctx, fixture.work.WorkID)
		assertScopeCanceledExecution(t, ctx, db, repository, operation, fixture.work.WorkID,
			fixture.offer.Attempt.AttemptID)
		source, sourceErr := repository.GetSource(ctx, fixture.source.SourceID)
		if err != nil || sourceErr != nil || run.Status != model.ListingRunCanceled ||
			source.ReadinessStatus != model.SourceCandidate {
			t.Fatalf("canceled ListingRun=%+v Source=%+v err=%v/%v", run, source, err, sourceErr)
		}
		resumed, _ := source.Resume(source.Version)
		if _, err := resumed.BeginValidation(resumed.Version); err != nil {
			t.Fatalf("canceled Source validation cannot retry after resume: %v", err)
		}
	})

	t.Run("recipe_sample_validation", func(t *testing.T) {
		prefix := "scope-owner-recipe-sample"
		fixture := createRunningDetailRecipeSampleFixture(t, ctx, repository, prefix,
			now.Add(3*time.Hour))
		operation := cancelSourceScopeAndReconcile(t, ctx, repository, fixture.source.SourceID,
			"scope-owner-recipe-sample", fixture.now.Add(10*time.Second))
		run, err := repository.GetRecipeSampleValidationByWork(ctx, fixture.offer.Work.WorkID)
		assertScopeCanceledExecution(t, ctx, db, repository, operation, fixture.offer.Work.WorkID,
			fixture.offer.Attempt.AttemptID)
		recipe, recipeErr := repository.GetRecipe(ctx, prefix+"-candidate", 2)
		if err != nil || recipeErr != nil || run.Status != model.RecipeSampleValidationCanceled ||
			recipe.Status != model.RecipeDraft {
			t.Fatalf("canceled Recipe sample validation=%+v Recipe=%+v err=%v/%v", run, recipe, err, recipeErr)
		}
		if _, err := recipe.BeginValidation(recipe.StateVersion); err != nil {
			t.Fatalf("canceled Detail Recipe validation cannot retry: %v", err)
		}
	})

	t.Run("listing_recipe_validation", func(t *testing.T) {
		prefix := "scope-owner-listing-recipe"
		fixture := createRunningListingRecipeValidationFixture(t, ctx, repository, prefix, now.Add(3500*time.Millisecond))
		operation := cancelSourceScopeAndReconcile(t, ctx, repository, fixture.source.SourceID, prefix,
			fixture.now.Add(10*time.Second))
		run, runErr := repository.GetListingRunByWork(ctx, fixture.offer.Work.WorkID)
		recipe, recipeErr := repository.GetRecipe(ctx, prefix+"-candidate", 2)
		assertScopeCanceledExecution(t, ctx, db, repository, operation, fixture.offer.Work.WorkID,
			fixture.offer.Attempt.AttemptID)
		if runErr != nil || recipeErr != nil || run.Status != model.ListingRunCanceled || recipe.Status != model.RecipeDraft {
			t.Fatalf("canceled Listing Recipe run=%+v Recipe=%+v err=%v/%v", run, recipe, runErr, recipeErr)
		}
	})

	t.Run("discovery_recipe_validation", func(t *testing.T) {
		prefix := "scope-owner-discovery-recipe"
		fixture := createRunningDiscoveryRecipeValidationFixture(t, ctx, repository, prefix, now.Add(3750*time.Millisecond))
		operation := cancelCompanyScopeAndReconcile(t, ctx, repository, prefix+"-company", prefix,
			fixture.now.Add(10*time.Second))
		run, runErr := repository.GetRecipeSampleValidationByWork(ctx, fixture.offer.Work.WorkID)
		recipe, recipeErr := repository.GetRecipe(ctx, prefix+"-candidate", 1)
		assertScopeCanceledExecution(t, ctx, db, repository, operation, fixture.offer.Work.WorkID,
			fixture.offer.Attempt.AttemptID)
		if runErr != nil || recipeErr != nil || run.Status != model.RecipeSampleValidationCanceled ||
			recipe.Status != model.RecipeDraft {
			t.Fatalf("canceled Discovery Recipe run=%+v Recipe=%+v err=%v/%v", run, recipe, runErr, recipeErr)
		}
	})

	t.Run("baseline", func(t *testing.T) {
		fixture := createRunningBaselineFixture(t, ctx, repository, "scope-owner-baseline", now.Add(4*time.Hour))
		operation := cancelSourceScopeAndReconcile(t, ctx, repository, fixture.source.SourceID,
			"scope-owner-baseline", fixture.now.Add(10*time.Second))
		assertScopeCanceledExecution(t, ctx, db, repository, operation, fixture.offer.Work.WorkID,
			fixture.offer.Attempt.AttemptID)
		var state []byte
		if err := db.QueryRowContext(ctx, `SELECT state_json FROM recruiting_baseline_generations
WHERE work_id = ?`, fixture.offer.Work.WorkID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		var baseline model.BaselineGeneration
		if err := json.Unmarshal(state, &baseline); err != nil || baseline.Status != model.BaselineCanceled {
			t.Fatalf("canceled Baseline=%+v err=%v", baseline, err)
		}
	})

	t.Run("backfill", func(t *testing.T) {
		fixture := createRunningLiveBackfillFixture(t, ctx, repository, "scope-owner-backfill", now.Add(5*time.Hour))
		operation := cancelSourceScopeAndReconcile(t, ctx, repository, fixture.source.SourceID,
			"scope-owner-backfill", fixture.now.Add(10*time.Second))
		assertScopeCanceledExecution(t, ctx, db, repository, operation, fixture.offer.Work.WorkID,
			fixture.offer.Attempt.AttemptID)
		backfill, err := repository.GetBackfill(ctx, fixture.backfill.BackfillID)
		items, itemErr := repository.ListBackfillItems(ctx, fixture.backfill.BackfillID, "", 10)
		if err != nil || itemErr != nil || backfill.Status != model.BackfillCanceled || len(items.Items) != 1 ||
			items.Items[0].Status != model.BackfillItemCanceled {
			t.Fatalf("canceled Backfill=%+v items=%+v err=%v/%v", backfill, items, err, itemErr)
		}
	})

}

func TestCompletedExecutionOwnersSurviveLaterScopeCancel(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	repository, _, ctx, cleanup := detailContractRepository(t)
	defer cleanup()
	now := time.Date(2095, 2, 22, 4, 0, 0, 0, time.UTC)

	assertCompletedExecution := func(t *testing.T, workID, attemptID string) {
		t.Helper()
		work, workErr := repository.GetWork(ctx, workID)
		attempt, attemptErr := repository.GetAttempt(ctx, attemptID)
		if workErr != nil || attemptErr != nil || work.Status != model.WorkCompleted ||
			work.Resolution != model.ResolutionSucceeded || attempt.Status != model.AttemptSucceeded {
			t.Fatalf("completed execution was rewritten: Work=%+v Attempt=%+v err=%v/%v",
				work, attempt, workErr, attemptErr)
		}
	}

	t.Run("source_discovery", func(t *testing.T) {
		prefix := "scope-result-first-discovery"
		fixture := createRunningSourceDiscoveryFixture(t, ctx, repository, prefix, now)
		outcome, err := repository.AcceptSourceDiscoveryResult(ctx, fixture.result)
		if err != nil || outcome.Discovery.Status != model.SourceDiscoveryCompleted {
			t.Fatalf("Source Discovery result=%+v err=%v", outcome, err)
		}
		operation := cancelCompanyScopeAndReconcile(t, ctx, repository, fixture.company.CompanyID, prefix,
			fixture.now.Add(10*time.Second))
		discovery, discoveryErr := repository.GetSourceDiscovery(ctx, fixture.discovery.DiscoveryID)
		assertCompletedExecution(t, fixture.offer.Work.WorkID, fixture.offer.Attempt.AttemptID)
		if discoveryErr != nil || operation.Status != model.ScopeControlCompleted ||
			discovery.Status != model.SourceDiscoveryCompleted || discovery.CandidateCount != 1 {
			t.Fatalf("late cancel rewrote Source Discovery: operation=%+v discovery=%+v err=%v",
				operation, discovery, discoveryErr)
		}
	})

	t.Run("source_validation", func(t *testing.T) {
		prefix := "scope-result-first-source-validation"
		fixture := createRunningSourceValidationFixture(t, ctx, repository, prefix, now.Add(time.Hour))
		outcome, err := repository.AcceptDiagnosticResult(ctx, fixture.result)
		if err != nil || outcome.Run.Status != model.ListingRunCompleted {
			t.Fatalf("Source validation result=%+v err=%v", outcome, err)
		}
		before, err := repository.GetSource(ctx, fixture.source.SourceID)
		if err != nil {
			t.Fatal(err)
		}
		operation := cancelSourceScopeAndReconcile(t, ctx, repository, fixture.source.SourceID, prefix,
			fixture.now.Add(10*time.Second))
		run, runErr := repository.GetListingRunByWork(ctx, fixture.work.WorkID)
		after, sourceErr := repository.GetSource(ctx, fixture.source.SourceID)
		assertCompletedExecution(t, fixture.work.WorkID, fixture.offer.Attempt.AttemptID)
		if runErr != nil || sourceErr != nil || operation.Status != model.ScopeControlCompleted ||
			run.Status != model.ListingRunCompleted || after.ReadinessStatus != before.ReadinessStatus {
			t.Fatalf("late cancel rewrote Source validation: operation=%+v run=%+v before=%+v after=%+v err=%v/%v",
				operation, run, before, after, runErr, sourceErr)
		}
	})

	t.Run("detail_recipe_sample", func(t *testing.T) {
		prefix := "scope-result-first-detail-recipe"
		fixture := createRunningDetailRecipeSampleFixture(t, ctx, repository, prefix, now.Add(2*time.Hour))
		outcome, err := repository.AcceptRecipeSampleValidationResult(ctx, fixture.result)
		if err != nil || outcome.Run.Status != model.RecipeSampleValidationCompleted {
			t.Fatalf("Detail Recipe sample result=%+v err=%v", outcome, err)
		}
		before, err := repository.GetRecipe(ctx, prefix+"-candidate", 2)
		if err != nil {
			t.Fatal(err)
		}
		operation := cancelSourceScopeAndReconcile(t, ctx, repository, fixture.source.SourceID, prefix,
			fixture.now.Add(10*time.Second))
		run, runErr := repository.GetRecipeSampleValidationByWork(ctx, fixture.offer.Work.WorkID)
		after, recipeErr := repository.GetRecipe(ctx, prefix+"-candidate", 2)
		assertCompletedExecution(t, fixture.offer.Work.WorkID, fixture.offer.Attempt.AttemptID)
		if runErr != nil || recipeErr != nil || operation.Status != model.ScopeControlCompleted ||
			run.Status != model.RecipeSampleValidationCompleted || after.Status != before.Status ||
			after.StateVersion != before.StateVersion {
			t.Fatalf("late cancel rewrote Detail Recipe validation: operation=%+v run=%+v before=%+v after=%+v err=%v/%v",
				operation, run, before, after, runErr, recipeErr)
		}
	})

	t.Run("listing_recipe_validation", func(t *testing.T) {
		prefix := "scope-result-first-listing-recipe"
		fixture := createRunningListingRecipeValidationFixture(t, ctx, repository, prefix, now.Add(3*time.Hour))
		outcome, err := repository.AcceptDiagnosticResult(ctx, fixture.result)
		if err != nil || outcome.Run.Status != model.ListingRunCompleted {
			t.Fatalf("Listing Recipe result=%+v err=%v", outcome, err)
		}
		before, err := repository.GetRecipe(ctx, prefix+"-candidate", 2)
		if err != nil {
			t.Fatal(err)
		}
		operation := cancelSourceScopeAndReconcile(t, ctx, repository, fixture.source.SourceID, prefix,
			fixture.now.Add(10*time.Second))
		run, runErr := repository.GetListingRunByWork(ctx, fixture.offer.Work.WorkID)
		after, recipeErr := repository.GetRecipe(ctx, prefix+"-candidate", 2)
		assertCompletedExecution(t, fixture.offer.Work.WorkID, fixture.offer.Attempt.AttemptID)
		if runErr != nil || recipeErr != nil || operation.Status != model.ScopeControlCompleted ||
			run.Status != model.ListingRunCompleted || after.Status != before.Status ||
			after.StateVersion != before.StateVersion {
			t.Fatalf("late cancel rewrote Listing Recipe validation: operation=%+v run=%+v before=%+v after=%+v err=%v/%v",
				operation, run, before, after, runErr, recipeErr)
		}
	})

	t.Run("discovery_recipe_validation", func(t *testing.T) {
		prefix := "scope-result-first-discovery-recipe"
		fixture := createRunningDiscoveryRecipeValidationFixture(t, ctx, repository, prefix, now.Add(4*time.Hour))
		outcome, err := repository.AcceptRecipeSampleValidationResult(ctx, fixture.result)
		if err != nil || outcome.Run.Status != model.RecipeSampleValidationCompleted {
			t.Fatalf("Discovery Recipe result=%+v err=%v", outcome, err)
		}
		before, err := repository.GetRecipe(ctx, prefix+"-candidate", 1)
		if err != nil {
			t.Fatal(err)
		}
		operation := cancelCompanyScopeAndReconcile(t, ctx, repository, fixture.company.CompanyID, prefix,
			fixture.now.Add(10*time.Second))
		run, runErr := repository.GetRecipeSampleValidationByWork(ctx, fixture.offer.Work.WorkID)
		after, recipeErr := repository.GetRecipe(ctx, prefix+"-candidate", 1)
		assertCompletedExecution(t, fixture.offer.Work.WorkID, fixture.offer.Attempt.AttemptID)
		if runErr != nil || recipeErr != nil || operation.Status != model.ScopeControlCompleted ||
			run.Status != model.RecipeSampleValidationCompleted || after.Status != before.Status ||
			after.StateVersion != before.StateVersion {
			t.Fatalf("late cancel rewrote Discovery Recipe validation: operation=%+v run=%+v before=%+v after=%+v err=%v/%v",
				operation, run, before, after, runErr, recipeErr)
		}
	})

	t.Run("detail", func(t *testing.T) {
		prefix := "scope-result-first-detail"
		fixture := createDetailFixture(t, ctx, repository, prefix, now.Add(5*time.Hour))
		result := fixture.result(prefix + "-artifact")
		outcome, err := repository.AcceptDetailResult(ctx, result)
		if err != nil || outcome.Job.DetailVersion != 1 {
			t.Fatalf("Detail result=%+v err=%v", outcome, err)
		}
		operation := cancelSourceScopeAndReconcile(t, ctx, repository, fixture.source.SourceID, prefix,
			result.ObservedAt.Add(10*time.Second))
		job, jobErr := repository.GetJob(ctx, fixture.job.JobID)
		assertCompletedExecution(t, fixture.work.WorkID, fixture.attempt.AttemptID)
		if jobErr != nil || operation.Status != model.ScopeControlCompleted || job.DetailVersion != 1 ||
			job.Status != model.JobAvailable {
			t.Fatalf("late cancel rewrote Detail result: operation=%+v Job=%+v err=%v", operation, job, jobErr)
		}
	})

	t.Run("diagnostic", func(t *testing.T) {
		prefix := "scope-result-first-diagnostic"
		fixture := createRunningDiagnosticFixture(t, ctx, repository, prefix, now.Add(6*time.Hour))
		outcome, err := repository.AcceptDiagnosticResult(ctx, fixture.result)
		if err != nil || outcome.Run.Status != model.ListingRunCompleted {
			t.Fatalf("diagnostic result=%+v err=%v", outcome, err)
		}
		operation := cancelSourceScopeAndReconcile(t, ctx, repository, fixture.source.SourceID, prefix,
			fixture.now.Add(10*time.Second))
		run, runErr := repository.GetListingRunByWork(ctx, fixture.offer.Work.WorkID)
		assertCompletedExecution(t, fixture.offer.Work.WorkID, fixture.offer.Attempt.AttemptID)
		if runErr != nil || operation.Status != model.ScopeControlCompleted || run.Status != model.ListingRunCompleted {
			t.Fatalf("late cancel rewrote diagnostic: operation=%+v run=%+v err=%v", operation, run, runErr)
		}
	})
}

func TestScopeCancelRejectsLateExecutionOwnerResults(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	repository, db, ctx, cleanup := detailContractRepository(t)
	defer cleanup()
	now := time.Date(2095, 2, 23, 4, 0, 0, 0, time.UTC)

	assertRejectedArtifacts := func(t *testing.T, attemptID string, want int) {
		t.Helper()
		var rejected int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_artifacts
WHERE attempt_id = ? AND rejected = TRUE`, attemptID).Scan(&rejected); err != nil || rejected != want {
			t.Fatalf("rejected Artifacts=%d want=%d err=%v", rejected, want, err)
		}
	}

	t.Run("source_discovery", func(t *testing.T) {
		prefix := "scope-cancel-first-discovery"
		fixture := createRunningSourceDiscoveryFixture(t, ctx, repository, prefix, now)
		operation := cancelCompanyScopeAndReconcile(t, ctx, repository, fixture.company.CompanyID, prefix,
			fixture.now.Add(5*time.Second))
		if _, err := repository.AcceptSourceDiscoveryResult(ctx, fixture.result); !errors.Is(err, ErrResultFenced) {
			t.Fatalf("late Source Discovery result crossed scope fence: %v", err)
		}
		discovery, err := repository.GetSourceDiscovery(ctx, fixture.discovery.DiscoveryID)
		assertScopeCanceledExecution(t, ctx, db, repository, operation, fixture.offer.Work.WorkID,
			fixture.offer.Attempt.AttemptID)
		assertRejectedArtifacts(t, fixture.offer.Attempt.AttemptID, 1)
		if err != nil || discovery.Status != model.SourceDiscoveryCanceled || discovery.CandidateCount != 0 {
			t.Fatalf("late result rewrote canceled Source Discovery: %+v err=%v", discovery, err)
		}
	})

	t.Run("source_validation", func(t *testing.T) {
		prefix := "scope-cancel-first-source-validation"
		fixture := createRunningSourceValidationFixture(t, ctx, repository, prefix, now.Add(time.Hour))
		operation := cancelSourceScopeAndReconcile(t, ctx, repository, fixture.source.SourceID, prefix,
			fixture.now.Add(5*time.Second))
		if _, err := repository.AcceptDiagnosticResult(ctx, fixture.result); !errors.Is(err, ErrResultFenced) {
			t.Fatalf("late Source validation result crossed scope fence: %v", err)
		}
		run, runErr := repository.GetListingRunByWork(ctx, fixture.work.WorkID)
		source, sourceErr := repository.GetSource(ctx, fixture.source.SourceID)
		assertScopeCanceledExecution(t, ctx, db, repository, operation, fixture.work.WorkID,
			fixture.offer.Attempt.AttemptID)
		assertRejectedArtifacts(t, fixture.offer.Attempt.AttemptID, 2)
		if runErr != nil || sourceErr != nil || run.Status != model.ListingRunCanceled ||
			source.ReadinessStatus != model.SourceCandidate {
			t.Fatalf("late result rewrote canceled Source validation: run=%+v Source=%+v err=%v/%v",
				run, source, runErr, sourceErr)
		}
	})

	t.Run("detail_recipe_sample", func(t *testing.T) {
		prefix := "scope-cancel-first-detail-recipe"
		fixture := createRunningDetailRecipeSampleFixture(t, ctx, repository, prefix, now.Add(2*time.Hour))
		operation := cancelSourceScopeAndReconcile(t, ctx, repository, fixture.source.SourceID, prefix,
			fixture.now.Add(5*time.Second))
		if _, err := repository.AcceptRecipeSampleValidationResult(ctx, fixture.result); !errors.Is(err, ErrResultFenced) {
			t.Fatalf("late Detail Recipe result crossed scope fence: %v", err)
		}
		run, runErr := repository.GetRecipeSampleValidationByWork(ctx, fixture.offer.Work.WorkID)
		recipe, recipeErr := repository.GetRecipe(ctx, prefix+"-candidate", 2)
		assertScopeCanceledExecution(t, ctx, db, repository, operation, fixture.offer.Work.WorkID,
			fixture.offer.Attempt.AttemptID)
		assertRejectedArtifacts(t, fixture.offer.Attempt.AttemptID, 2)
		if runErr != nil || recipeErr != nil || run.Status != model.RecipeSampleValidationCanceled ||
			recipe.Status != model.RecipeDraft {
			t.Fatalf("late result rewrote canceled Detail Recipe validation: run=%+v Recipe=%+v err=%v/%v",
				run, recipe, runErr, recipeErr)
		}
	})

	t.Run("listing_recipe_validation", func(t *testing.T) {
		prefix := "scope-cancel-first-listing-recipe"
		fixture := createRunningListingRecipeValidationFixture(t, ctx, repository, prefix, now.Add(3*time.Hour))
		operation := cancelSourceScopeAndReconcile(t, ctx, repository, fixture.source.SourceID, prefix,
			fixture.now.Add(5*time.Second))
		if _, err := repository.AcceptDiagnosticResult(ctx, fixture.result); !errors.Is(err, ErrResultFenced) {
			t.Fatalf("late Listing Recipe result crossed scope fence: %v", err)
		}
		run, runErr := repository.GetListingRunByWork(ctx, fixture.offer.Work.WorkID)
		recipe, recipeErr := repository.GetRecipe(ctx, prefix+"-candidate", 2)
		assertScopeCanceledExecution(t, ctx, db, repository, operation, fixture.offer.Work.WorkID,
			fixture.offer.Attempt.AttemptID)
		assertRejectedArtifacts(t, fixture.offer.Attempt.AttemptID, 2)
		if runErr != nil || recipeErr != nil || run.Status != model.ListingRunCanceled ||
			recipe.Status != model.RecipeDraft {
			t.Fatalf("late result rewrote canceled Listing Recipe validation: run=%+v Recipe=%+v err=%v/%v",
				run, recipe, runErr, recipeErr)
		}
	})

	t.Run("discovery_recipe_validation", func(t *testing.T) {
		prefix := "scope-cancel-first-discovery-recipe"
		fixture := createRunningDiscoveryRecipeValidationFixture(t, ctx, repository, prefix, now.Add(4*time.Hour))
		operation := cancelCompanyScopeAndReconcile(t, ctx, repository, fixture.company.CompanyID, prefix,
			fixture.now.Add(5*time.Second))
		if _, err := repository.AcceptRecipeSampleValidationResult(ctx, fixture.result); !errors.Is(err, ErrResultFenced) {
			t.Fatalf("late Discovery Recipe result crossed scope fence: %v", err)
		}
		run, runErr := repository.GetRecipeSampleValidationByWork(ctx, fixture.offer.Work.WorkID)
		recipe, recipeErr := repository.GetRecipe(ctx, prefix+"-candidate", 1)
		assertScopeCanceledExecution(t, ctx, db, repository, operation, fixture.offer.Work.WorkID,
			fixture.offer.Attempt.AttemptID)
		assertRejectedArtifacts(t, fixture.offer.Attempt.AttemptID, 2)
		if runErr != nil || recipeErr != nil || run.Status != model.RecipeSampleValidationCanceled ||
			recipe.Status != model.RecipeDraft {
			t.Fatalf("late result rewrote canceled Discovery Recipe validation: run=%+v Recipe=%+v err=%v/%v",
				run, recipe, runErr, recipeErr)
		}
	})

	t.Run("detail", func(t *testing.T) {
		prefix := "scope-cancel-first-detail"
		fixture := createDetailFixture(t, ctx, repository, prefix, now.Add(5*time.Hour))
		operation := cancelSourceScopeAndReconcile(t, ctx, repository, fixture.source.SourceID, prefix,
			fixture.now.Add(5*time.Second))
		result := fixture.result(prefix + "-artifact")
		if _, err := repository.AcceptDetailResult(ctx, result); !errors.Is(err, ErrResultFenced) {
			t.Fatalf("late Detail result crossed scope fence: %v", err)
		}
		job, jobErr := repository.GetJob(ctx, fixture.job.JobID)
		assertScopeCanceledExecution(t, ctx, db, repository, operation, fixture.work.WorkID,
			fixture.attempt.AttemptID)
		assertRejectedArtifacts(t, fixture.attempt.AttemptID, 1)
		if jobErr != nil || job.Status != model.JobDetailPending || job.DetailVersion != 0 {
			t.Fatalf("late result rewrote canceled Detail: Job=%+v err=%v", job, jobErr)
		}
	})

	t.Run("diagnostic", func(t *testing.T) {
		prefix := "scope-cancel-first-diagnostic"
		fixture := createRunningDiagnosticFixture(t, ctx, repository, prefix, now.Add(6*time.Hour))
		operation := cancelSourceScopeAndReconcile(t, ctx, repository, fixture.source.SourceID, prefix,
			fixture.now.Add(5*time.Second))
		if _, err := repository.AcceptDiagnosticResult(ctx, fixture.result); !errors.Is(err, ErrResultFenced) {
			t.Fatalf("late diagnostic result crossed scope fence: %v", err)
		}
		run, runErr := repository.GetListingRunByWork(ctx, fixture.offer.Work.WorkID)
		assertScopeCanceledExecution(t, ctx, db, repository, operation, fixture.offer.Work.WorkID,
			fixture.offer.Attempt.AttemptID)
		assertRejectedArtifacts(t, fixture.offer.Attempt.AttemptID, 2)
		if runErr != nil || run.Status != model.ListingRunCanceled {
			t.Fatalf("late result rewrote canceled diagnostic: run=%+v err=%v", run, runErr)
		}
	})

	t.Run("baseline_page", func(t *testing.T) {
		prefix := "scope-cancel-first-baseline"
		fixture := createRunningBaselineFixture(t, ctx, repository, prefix, now.Add(7*time.Hour))
		operation := cancelSourceScopeAndReconcile(t, ctx, repository, fixture.source.SourceID, prefix,
			fixture.now.Add(5*time.Second))
		if _, err := repository.AcceptListingPage(ctx, fixture.result); !errors.Is(err, ErrResultFenced) {
			t.Fatalf("late Baseline page crossed scope fence: %v", err)
		}
		assertScopeCanceledExecution(t, ctx, db, repository, operation, fixture.offer.Work.WorkID,
			fixture.offer.Attempt.AttemptID)
		assertRejectedArtifacts(t, fixture.offer.Attempt.AttemptID, 1)
		var state []byte
		if err := db.QueryRowContext(ctx, `SELECT state_json FROM recruiting_baseline_generations
WHERE work_id = ?`, fixture.offer.Work.WorkID).Scan(&state); err != nil {
			t.Fatal(err)
		}
		var baseline model.BaselineGeneration
		if err := json.Unmarshal(state, &baseline); err != nil || baseline.Status != model.BaselineCanceled {
			t.Fatalf("late page rewrote canceled Baseline=%+v err=%v", baseline, err)
		}
	})
}

func TestScopeCancelAndResultsConvergeUnderConcurrentStress(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	repository, db, ctx, cleanup := detailContractRepository(t)
	defer cleanup()
	now := time.Date(2095, 2, 24, 4, 0, 0, 0, time.UTC)

	type raceFixture struct {
		workID, attemptID string
		artifactCount     int
		acceptResult      func() error
		applyPause        func() error
		operationID       string
	}
	newSourcePause := func(t *testing.T, sourceID, prefix string, at time.Time) (func() error, string) {
		t.Helper()
		source, err := repository.GetSource(ctx, sourceID)
		if err != nil {
			t.Fatal(err)
		}
		paused, err := source.Pause(source.Version, model.PauseCancel)
		if err != nil {
			t.Fatal(err)
		}
		receipt, _ := model.NewCommandReceipt(prefix+"-pause", "recruiting.source.pause",
			"sha256:"+prefix+"-pause", json.RawMessage(`{}`))
		event, _ := model.NewEventIntent(prefix+"-pause-event", "source.paused", "source", source.SourceID,
			paused.Version, at.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{"mode":"cancel"}`))
		operationID := prefix + "-operation"
		return func() error {
			_, err := repository.ApplySourcePauseCommand(ctx, source.Version, paused, receipt, event, operationID, at)
			return err
		}, operationID
	}
	newCompanyPause := func(t *testing.T, companyID, prefix string, at time.Time) (func() error, string) {
		t.Helper()
		company, err := repository.GetCompany(ctx, companyID)
		if err != nil {
			t.Fatal(err)
		}
		paused, err := company.Pause(company.Version, model.PauseCancel)
		if err != nil {
			t.Fatal(err)
		}
		receipt, _ := model.NewCommandReceipt(prefix+"-pause", "recruiting.company.pause",
			"sha256:"+prefix+"-pause", json.RawMessage(`{}`))
		event, _ := model.NewEventIntent(prefix+"-pause-event", "company.paused", "company", company.CompanyID,
			paused.Version, at.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{"mode":"cancel"}`))
		operationID := prefix + "-operation"
		return func() error {
			_, err := repository.ApplyCompanyPauseCommand(ctx, company.Version, paused, receipt, event, operationID, at)
			return err
		}, operationID
	}

	kinds := []string{"source_discovery", "source_validation", "detail", "diagnostic",
		"listing_recipe_validation", "detail_recipe_sample", "discovery_recipe_validation"}
	resultFirst, cancelFirst := 0, 0
	for iteration := 0; iteration < 5; iteration++ {
		for kindIndex, kind := range kinds {
			kind, iteration := kind, iteration
			t.Run(fmt.Sprintf("%s/%02d", kind, iteration), func(t *testing.T) {
				prefix := fmt.Sprintf("scope-concurrent-%s-%02d", strings.ReplaceAll(kind, "_", "-"), iteration)
				at := now.Add(time.Duration(iteration*len(kinds)+kindIndex) * time.Hour)
				var fixture raceFixture
				switch kind {
				case "source_discovery":
					value := createRunningSourceDiscoveryFixture(t, ctx, repository, prefix, at)
					fixture.workID, fixture.attemptID, fixture.artifactCount = value.offer.Work.WorkID, value.offer.Attempt.AttemptID, 1
					fixture.acceptResult = func() error { _, err := repository.AcceptSourceDiscoveryResult(ctx, value.result); return err }
					fixture.applyPause, fixture.operationID = newCompanyPause(t, value.company.CompanyID, prefix, at.Add(5*time.Second))
				case "source_validation":
					value := createRunningSourceValidationFixture(t, ctx, repository, prefix, at)
					fixture.workID, fixture.attemptID, fixture.artifactCount = value.work.WorkID, value.offer.Attempt.AttemptID, 2
					fixture.acceptResult = func() error { _, err := repository.AcceptDiagnosticResult(ctx, value.result); return err }
					fixture.applyPause, fixture.operationID = newSourcePause(t, value.source.SourceID, prefix, at.Add(5*time.Second))
				case "detail":
					value := createDetailFixture(t, ctx, repository, prefix, at)
					result := value.result(prefix + "-artifact")
					fixture.workID, fixture.attemptID, fixture.artifactCount = value.work.WorkID, value.attempt.AttemptID, 1
					fixture.acceptResult = func() error { _, err := repository.AcceptDetailResult(ctx, result); return err }
					fixture.applyPause, fixture.operationID = newSourcePause(t, value.source.SourceID, prefix, at.Add(5*time.Second))
				case "diagnostic":
					value := createRunningDiagnosticFixture(t, ctx, repository, prefix, at)
					fixture.workID, fixture.attemptID, fixture.artifactCount = value.offer.Work.WorkID, value.offer.Attempt.AttemptID, 2
					fixture.acceptResult = func() error { _, err := repository.AcceptDiagnosticResult(ctx, value.result); return err }
					fixture.applyPause, fixture.operationID = newSourcePause(t, value.source.SourceID, prefix, at.Add(5*time.Second))
				case "listing_recipe_validation":
					value := createRunningListingRecipeValidationFixture(t, ctx, repository, prefix, at)
					fixture.workID, fixture.attemptID, fixture.artifactCount = value.offer.Work.WorkID, value.offer.Attempt.AttemptID, 2
					fixture.acceptResult = func() error { _, err := repository.AcceptDiagnosticResult(ctx, value.result); return err }
					fixture.applyPause, fixture.operationID = newSourcePause(t, value.source.SourceID, prefix, at.Add(5*time.Second))
				case "detail_recipe_sample":
					value := createRunningDetailRecipeSampleFixture(t, ctx, repository, prefix, at)
					fixture.workID, fixture.attemptID, fixture.artifactCount = value.offer.Work.WorkID, value.offer.Attempt.AttemptID, 2
					fixture.acceptResult = func() error { _, err := repository.AcceptRecipeSampleValidationResult(ctx, value.result); return err }
					fixture.applyPause, fixture.operationID = newSourcePause(t, value.source.SourceID, prefix, at.Add(5*time.Second))
				case "discovery_recipe_validation":
					value := createRunningDiscoveryRecipeValidationFixture(t, ctx, repository, prefix, at)
					fixture.workID, fixture.attemptID, fixture.artifactCount = value.offer.Work.WorkID, value.offer.Attempt.AttemptID, 2
					fixture.acceptResult = func() error { _, err := repository.AcceptRecipeSampleValidationResult(ctx, value.result); return err }
					fixture.applyPause, fixture.operationID = newCompanyPause(t, value.company.CompanyID, prefix, at.Add(5*time.Second))
				default:
					t.Fatalf("unknown race kind %q", kind)
				}

				start := make(chan struct{})
				resultErr := make(chan error, 1)
				pauseErr := make(chan error, 1)
				var wait sync.WaitGroup
				wait.Add(2)
				go func() { defer wait.Done(); <-start; resultErr <- fixture.acceptResult() }()
				go func() { defer wait.Done(); <-start; pauseErr <- fixture.applyPause() }()
				close(start)
				wait.Wait()
				if err := <-pauseErr; err != nil {
					t.Fatalf("concurrent scope pause failed: %v", err)
				}
				acceptedErr := <-resultErr
				if acceptedErr != nil && !errors.Is(acceptedErr, ErrResultFenced) {
					t.Fatalf("concurrent result returned a third outcome: %v", acceptedErr)
				}
				operation := reconcileCompletedScopeOperation(t, ctx, repository, fixture.operationID, at.Add(6*time.Second))
				work, workErr := repository.GetWork(ctx, fixture.workID)
				attempt, attemptErr := repository.GetAttempt(ctx, fixture.attemptID)
				var activePermits, rejectedArtifacts int
				permitErr := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_budget_permits
WHERE attempt_id = ? AND permit_status = 'active'`, fixture.attemptID).Scan(&activePermits)
				artifactErr := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_artifacts
WHERE attempt_id = ? AND rejected = TRUE`, fixture.attemptID).Scan(&rejectedArtifacts)
				if workErr != nil || attemptErr != nil || permitErr != nil || artifactErr != nil ||
					operation.Status != model.ScopeControlCompleted || activePermits != 0 {
					t.Fatalf("concurrent convergence read failed: operation=%+v Work=%+v Attempt=%+v permits=%d rejected=%d err=%v/%v/%v/%v",
						operation, work, attempt, activePermits, rejectedArtifacts, workErr, attemptErr, permitErr, artifactErr)
				}
				if acceptedErr == nil {
					resultFirst++
					if work.Status != model.WorkCompleted || attempt.Status != model.AttemptSucceeded || rejectedArtifacts != 0 {
						t.Fatalf("result-first did not preserve success: Work=%+v Attempt=%+v rejected=%d",
							work, attempt, rejectedArtifacts)
					}
				} else {
					cancelFirst++
					if work.Status != model.WorkCanceled || attempt.Status != model.AttemptExpired ||
						rejectedArtifacts != fixture.artifactCount {
						t.Fatalf("cancel-first did not preserve cancellation: Work=%+v Attempt=%+v rejected=%d want=%d",
							work, attempt, rejectedArtifacts, fixture.artifactCount)
					}
				}
			})
		}
	}
	if resultFirst+cancelFirst != len(kinds)*5 {
		t.Fatalf("stress matrix lost outcomes: result_first=%d cancel_first=%d", resultFirst, cancelFirst)
	}
	t.Logf("35 concurrent cutpoints converged: result_first=%d cancel_first=%d", resultFirst, cancelFirst)
}

func TestScopeAndGeneralBackfillCancelCoordinatorsConvergeUnderConcurrentStress(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	repository, db, ctx, cleanup := detailContractRepository(t)
	defer cleanup()
	now := time.Date(2095, 2, 25, 4, 0, 0, 0, time.UTC)

	for iteration := 0; iteration < 10; iteration++ {
		t.Run(fmt.Sprintf("%02d", iteration), func(t *testing.T) {
			prefix := fmt.Sprintf("scope-general-backfill-concurrent-%02d", iteration)
			at := now.Add(time.Duration(iteration) * time.Hour)
			detail := createDetailFixture(t, ctx, repository, prefix, at)
			assignment := *detail.source.DetailAssignment
			recipe, err := repository.GetRecipe(ctx, assignment.RecipeID, assignment.RecipeVersion)
			if err != nil {
				t.Fatal(err)
			}
			parent, _ := model.NewWork(prefix+"-parent", "source", detail.source.SourceID,
				"historical_backfill", "human")
			parent, _ = parent.WithCausality("human:scope-concurrent", prefix+"-message", "")
			backfill, err := model.NewBackfill(prefix+"-backfill", parent.WorkID, parent.InitiatorActorID,
				"source", detail.source.SourceID, model.BackfillLiveRefetch,
				at.Add(-time.Hour).Format(time.RFC3339), at.Add(time.Hour).Format(time.RFC3339),
				[]string{"title"}, recipe.RecipeID, recipe.Version, 1)
			if err != nil {
				t.Fatal(err)
			}
			response, _ := json.Marshal(backfill)
			receipt, _ := model.NewCommandReceipt(prefix+"-create", "recruiting.backfill.create",
				"sha256:"+prefix+"-create", response)
			event, _ := model.NewEventIntent(prefix+"-created", "backfill.created", "work", parent.WorkID,
				parent.Version, at.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
			if _, err := repository.ApplyCreateBackfillCommand(ctx, parent,
				WorkPlacement{BusinessKey: "backfill|" + prefix, NotBefore: at}, backfill, receipt, event, at); err != nil {
				t.Fatal(err)
			}
			backfill, items, err := repository.PreviewBackfillChunk(ctx, backfill.BackfillID, backfill.Version,
				at.Add(time.Second))
			if err != nil || len(items) != 1 {
				t.Fatalf("preview Backfill items=%d err=%v", len(items), err)
			}
			parent, _ = repository.GetWork(ctx, parent.WorkID)
			confirmed, _ := backfill.Confirm(backfill.Version, backfill.PreviewHash)
			runningParent, _ := parent.Start(parent.Version)
			confirmResponse, _ := json.Marshal(map[string]any{"backfill": confirmed, "work": runningParent})
			confirmReceipt, _ := model.NewCommandReceipt(prefix+"-confirm", "recruiting.backfill.confirm",
				"sha256:"+prefix+"-confirm", confirmResponse)
			confirmEvent, _ := model.NewEventIntent(prefix+"-confirmed", "backfill.confirmed", "work", parent.WorkID,
				runningParent.Version, at.Add(2*time.Second).Format(time.RFC3339Nano), confirmReceipt.CommandID,
				json.RawMessage(`{}`))
			if _, err := repository.ApplyConfirmBackfillCommand(ctx, backfill.BackfillID, backfill.Version,
				parent.Version, backfill.PreviewHash, confirmReceipt, confirmEvent, at.Add(2*time.Second)); err != nil {
				t.Fatal(err)
			}

			operation := startSourceCancelScope(t, ctx, repository, detail.source.SourceID, prefix, at.Add(5*time.Second))
			for !operation.ProjectionCompleted {
				operation, err = repository.ReconcileScopeControlOperation(ctx, operation.OperationID, operation.Version,
					500, at.Add(6*time.Second))
				if err != nil {
					t.Fatal(err)
				}
			}
			if operation.Status != model.ScopeControlApplying || operation.CancellationCompleted {
				t.Fatalf("scope operation skipped dependency race: %+v", operation)
			}

			start := make(chan struct{})
			scopeResult := make(chan error, 1)
			generalResult := make(chan error, 1)
			var wait sync.WaitGroup
			wait.Add(2)
			go func(version uint64) {
				defer wait.Done()
				<-start
				_, err := repository.ReconcileScopeControlOperation(ctx, operation.OperationID, version, 1,
					at.Add(7*time.Second))
				scopeResult <- err
			}(operation.Version)
			go func() {
				defer wait.Done()
				<-start
				_, err := repository.CancelNextBackfillPage(ctx, 1, at.Add(7*time.Second))
				generalResult <- err
			}()
			close(start)
			wait.Wait()
			if err := <-scopeResult; err != nil {
				t.Fatalf("scope Backfill coordinator failed under concurrency: %v", err)
			}
			if err := <-generalResult; err != nil {
				t.Fatalf("general Backfill coordinator failed under concurrency: %v", err)
			}
			operation = reconcileCompletedScopeOperation(t, ctx, repository, operation.OperationID,
				at.Add(8*time.Second))
			stored, storedErr := repository.GetBackfill(ctx, backfill.BackfillID)
			page, pageErr := repository.ListBackfillItems(ctx, backfill.BackfillID, "", 10)
			parent, parentErr := repository.GetWork(ctx, parent.WorkID)
			if storedErr != nil || pageErr != nil || parentErr != nil || operation.Status != model.ScopeControlCompleted ||
				stored.Status != model.BackfillCanceled || stored.CanceledItems != 1 || len(page.Items) != 1 ||
				page.Items[0].Status != model.BackfillItemCanceled || parent.Status != model.WorkCanceled {
				t.Fatalf("coordinators did not converge: operation=%+v Backfill=%+v items=%+v parent=%+v err=%v/%v/%v",
					operation, stored, page, parent, storedErr, pageErr, parentErr)
			}
			var events int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_event_outbox
WHERE aggregate_type = 'backfill' AND aggregate_id = ? AND event_kind = 'backfill.canceled'`,
				backfill.BackfillID).Scan(&events); err != nil || events != 1 {
				t.Fatalf("concurrent Backfill terminal events=%d err=%v", events, err)
			}
		})
	}
}

func TestCancelScopeControlWaitsForBoundedUnmaterializedBackfillItems(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	repository, db, ctx, cleanup := detailContractRepository(t)
	defer cleanup()
	now := time.Date(2091, 9, 12, 0, 0, 0, 0, time.UTC)
	fixture := createDetailFixture(t, ctx, repository, "scope-backfill-dependencies", now)
	second, err := repository.ApplyListingObservation(ctx, ListingIngest{
		Observation: model.ListingObservation{
			ObservationID: "scope-backfill-dependencies-observation-2", OccurrenceID: "scope-backfill-dependencies-occurrence-2",
			SourceID: fixture.source.SourceID, SourceJobKey: "scope-backfill-dependencies-external-job-2",
			DetailURL:  "https://scope-backfill-dependencies.example.com/jobs/2",
			ActivityAt: now.Add(-30 * time.Second).Format(time.RFC3339), ListingFingerprint: "scope-backfill-dependencies-fingerprint-2",
			RecipeID: fixture.source.ListingAssignment.RecipeID, RecipeVersion: fixture.source.ListingAssignment.RecipeVersion,
			ArtifactID: "scope-backfill-dependencies-listing-artifact-2",
		},
		ObservedAt: now.Add(time.Second), NewJobID: "scope-backfill-dependencies-job-2",
		DetailWorkID: "scope-backfill-dependencies-detail-work-2", Origin: "https://scope-backfill-dependencies.example.com",
		Capability: "http.fetch", Priority: 10, NotBefore: now.Add(time.Second),
	})
	if err != nil || second.Job.JobID == "" {
		t.Fatalf("second listing observation=%+v err=%v", second, err)
	}
	assignment := *fixture.source.DetailAssignment
	recipe, err := repository.GetRecipe(ctx, assignment.RecipeID, assignment.RecipeVersion)
	if err != nil {
		t.Fatal(err)
	}
	parent, _ := model.NewWork("scope-backfill-dependencies-parent", "source", fixture.source.SourceID,
		"historical_backfill", "human")
	parent, _ = parent.WithCausality("human:scope-backfill", "scope-backfill-dependencies-message", "")
	backfill, err := model.NewBackfill("scope-backfill-dependencies-backfill", parent.WorkID, parent.InitiatorActorID,
		"source", fixture.source.SourceID, model.BackfillLiveRefetch, now.Add(-time.Hour).Format(time.RFC3339),
		now.Add(time.Hour).Format(time.RFC3339), []string{"title"}, recipe.RecipeID, recipe.Version, 1)
	if err != nil {
		t.Fatal(err)
	}
	response, _ := json.Marshal(backfill)
	receipt, _ := model.NewCommandReceipt("scope-backfill-dependencies-create", "recruiting.backfill.create",
		"sha256:scope-backfill-dependencies-create", response)
	event, _ := model.NewEventIntent("scope-backfill-dependencies-created", "backfill.created", "work", parent.WorkID,
		parent.Version, now.Add(2*time.Second).Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyCreateBackfillCommand(ctx, parent,
		WorkPlacement{BusinessKey: "backfill|scope-backfill-dependencies", NotBefore: now.Add(2 * time.Second)},
		backfill, receipt, event, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	backfill, items, err := repository.PreviewBackfillChunk(ctx, backfill.BackfillID, backfill.Version, now.Add(3*time.Second))
	if err != nil || len(items) != 2 {
		t.Fatalf("previewed Backfill=%+v items=%+v err=%v", backfill, items, err)
	}
	parent, _ = repository.GetWork(ctx, parent.WorkID)
	confirmed, _ := backfill.Confirm(backfill.Version, backfill.PreviewHash)
	runningParent, _ := parent.Start(parent.Version)
	confirmResponse, _ := json.Marshal(map[string]any{"backfill": confirmed, "work": runningParent})
	confirmReceipt, _ := model.NewCommandReceipt("scope-backfill-dependencies-confirm", "recruiting.backfill.confirm",
		"sha256:scope-backfill-dependencies-confirm", confirmResponse)
	confirmEvent, _ := model.NewEventIntent("scope-backfill-dependencies-confirmed", "backfill.confirmed", "work", parent.WorkID,
		runningParent.Version, now.Add(4*time.Second).Format(time.RFC3339Nano), confirmReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyConfirmBackfillCommand(ctx, backfill.BackfillID, backfill.Version, parent.Version,
		backfill.PreviewHash, confirmReceipt, confirmEvent, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	// Deliberately do not materialize either item into a child Work.
	pausedAt := now.Add(10 * time.Second)
	source, _ := repository.GetSource(ctx, fixture.source.SourceID)
	paused, _ := source.Pause(source.Version, model.PauseCancel)
	pauseReceipt, _ := model.NewCommandReceipt("scope-backfill-dependencies-pause", "recruiting.source.pause",
		"sha256:scope-backfill-dependencies-pause", json.RawMessage(`{}`))
	pauseEvent, _ := model.NewEventIntent("scope-backfill-dependencies-paused", "source.paused", "source", source.SourceID,
		paused.Version, pausedAt.Format(time.RFC3339Nano), pauseReceipt.CommandID, json.RawMessage(`{"mode":"cancel"}`))
	operationID := "scope-backfill-dependencies-operation"
	if _, err := repository.ApplySourcePauseCommand(ctx, source.Version, paused, pauseReceipt, pauseEvent, operationID, pausedAt); err != nil {
		t.Fatal(err)
	}
	operation, _ := repository.GetScopeControlOperation(ctx, operationID)
	for !operation.ProjectionCompleted {
		operation, err = repository.ReconcileScopeControlOperation(ctx, operationID, operation.Version, 1,
			pausedAt.Add(time.Duration(operation.Version)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
	}
	if operation.Status != model.ScopeControlApplying || operation.CancellationCompleted {
		t.Fatalf("Work projection prematurely completed cancellation: %+v", operation)
	}
	operation, err = repository.ReconcileScopeControlOperation(ctx, operationID, operation.Version, 1, pausedAt.Add(time.Minute))
	if err != nil || operation.Status != model.ScopeControlApplying || operation.BackfillItemsCanceled != 1 {
		t.Fatalf("first bounded dependency page=%+v err=%v", operation, err)
	}
	general, err := repository.CancelNextBackfillPage(ctx, 1, pausedAt.Add(90*time.Second))
	if err != nil || general.BackfillID != backfill.BackfillID || general.CanceledItems != 1 || !general.Completed {
		t.Fatalf("general Backfill coordinator handoff=%+v err=%v", general, err)
	}
	operation, err = repository.ReconcileScopeControlOperation(ctx, operationID, operation.Version, 1, pausedAt.Add(2*time.Minute))
	if err != nil || operation.Status != model.ScopeControlCompleted || !operation.CancellationCompleted ||
		operation.BackfillItemsCanceled != 1 {
		t.Fatalf("completed bounded dependencies=%+v err=%v", operation, err)
	}
	stored, _ := repository.GetBackfill(ctx, backfill.BackfillID)
	page, _ := repository.ListBackfillItems(ctx, backfill.BackfillID, "", 10)
	if stored.Status != model.BackfillCanceled || stored.CanceledItems != 2 || len(page.Items) != 2 ||
		page.Items[0].Status != model.BackfillItemCanceled || page.Items[1].Status != model.BackfillItemCanceled {
		t.Fatalf("canceled Backfill=%+v items=%+v", stored, page)
	}
	var activeChildren int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_backfill_items
WHERE backfill_id = ? AND work_id IS NOT NULL`, backfill.BackfillID).Scan(&activeChildren); err != nil || activeChildren != 0 {
		t.Fatalf("unmaterialized child Work count=%d err=%v", activeChildren, err)
	}
	var canceledEvents int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_event_outbox
WHERE aggregate_type = 'backfill' AND aggregate_id = ? AND event_kind = 'backfill.canceled'`,
		backfill.BackfillID).Scan(&canceledEvents); err != nil || canceledEvents != 1 {
		t.Fatalf("Backfill cancellation events=%d err=%v", canceledEvents, err)
	}
}

func TestSourceCancelIsolatesCompanyBackfillMembers(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	repository, _, ctx, cleanup := detailContractRepository(t)
	defer cleanup()
	now := time.Date(2091, 9, 13, 0, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("scope-multisource-company", "Scope Multisource",
		"https://scope-multisource.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	recipe := activeRecipe(t, "scope-multisource-recipe", model.RecipeDetail,
		"scope-multisource.example.com", 1, "scope-multisource-contract")
	if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
		t.Fatal(err)
	}
	sources := make([]model.RecruitmentSource, 0, 2)
	itemsBySource := make(map[string]model.BackfillItem)
	seedTx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for index, suffix := range []string{"a", "b"} {
		source, _ := model.NewRecruitmentSource("scope-multisource-source-"+suffix, company.CompanyID,
			"https://scope-multisource.example.com/jobs/"+suffix, suffix, 1)
		if err := repository.CreateSource(ctx, source, now.Add(time.Duration(index)*time.Millisecond)); err != nil {
			_ = seedTx.Rollback()
			t.Fatal(err)
		}
		sources = append(sources, source)
		job, _ := model.NewSourceJob("scope-multisource-job-"+suffix, source.SourceID, "remote-"+suffix,
			"https://scope-multisource.example.com/jobs/"+suffix+"/1")
		if err := insertJob(ctx, seedTx, job, now); err != nil {
			_ = seedTx.Rollback()
			t.Fatal(err)
		}
		if _, err := seedTx.ExecContext(ctx, `INSERT INTO recruiting_listing_observations(
observation_id, occurrence_id, source_id, job_id, source_job_key, detail_url, activity_at,
listing_fingerprint, recipe_id, recipe_version, artifact_id, observed_at, observation_json)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, JSON_OBJECT('job_id', ?))`,
			"scope-multisource-observation-"+suffix, "scope-multisource-occurrence-"+suffix,
			source.SourceID, job.JobID, job.SourceJobKey, job.DetailURL, now, "fingerprint-"+suffix,
			recipe.RecipeID, recipe.Version, "artifact-"+suffix, now, job.JobID); err != nil {
			_ = seedTx.Rollback()
			t.Fatal(err)
		}
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatal(err)
	}
	parent, _ := model.NewWork("scope-multisource-parent", "company", company.CompanyID,
		"historical_backfill", "human")
	parent, _ = parent.WithCausality("human:scope-multisource", "scope-multisource-message", "")
	backfill, _ := model.NewBackfill("scope-multisource-backfill", parent.WorkID, parent.InitiatorActorID,
		"company", company.CompanyID, model.BackfillLiveRefetch, now.Add(-time.Hour).Format(time.RFC3339),
		now.Add(time.Hour).Format(time.RFC3339), []string{"title"}, recipe.RecipeID, recipe.Version, 1)
	response, _ := json.Marshal(backfill)
	receipt, _ := model.NewCommandReceipt("scope-multisource-create", "recruiting.backfill.create",
		"sha256:scope-multisource-create", response)
	event, _ := model.NewEventIntent("scope-multisource-created", "backfill.created", "work", parent.WorkID,
		parent.Version, now.Add(time.Second).Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyCreateBackfillCommand(ctx, parent,
		WorkPlacement{BusinessKey: "backfill|scope-multisource", NotBefore: now.Add(time.Second)}, backfill,
		receipt, event, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	backfill, preview, err := repository.PreviewBackfillChunk(ctx, backfill.BackfillID, backfill.Version,
		now.Add(2*time.Second))
	if err != nil || len(preview) != 2 {
		t.Fatalf("company Backfill preview=%+v items=%+v err=%v", backfill, preview, err)
	}
	parent, _ = repository.GetWork(ctx, parent.WorkID)
	confirmed, _ := backfill.Confirm(backfill.Version, backfill.PreviewHash)
	runningParent, _ := parent.Start(parent.Version)
	confirmResponse, _ := json.Marshal(map[string]any{"backfill": confirmed, "work": runningParent})
	confirmReceipt, _ := model.NewCommandReceipt("scope-multisource-confirm", "recruiting.backfill.confirm",
		"sha256:scope-multisource-confirm", confirmResponse)
	confirmEvent, _ := model.NewEventIntent("scope-multisource-confirmed", "backfill.confirmed", "work", parent.WorkID,
		runningParent.Version, now.Add(3*time.Second).Format(time.RFC3339Nano), confirmReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyConfirmBackfillCommand(ctx, backfill.BackfillID, backfill.Version, parent.Version,
		backfill.PreviewHash, confirmReceipt, confirmEvent, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if materialized, err := repository.MaterializeNextBackfillPage(ctx, 10, now.Add(4*time.Second),
		[]ExecutionDispatchTarget{{ActorID: "tool:scope-multisource-executor", Capability: "http.fetch"}}); err != nil || materialized.Queued != 2 {
		t.Fatalf("company Backfill materialization=%+v err=%v", materialized, err)
	}
	page, _ := repository.ListBackfillItems(ctx, backfill.BackfillID, "", 10)
	for _, item := range page.Items {
		itemsBySource[item.SourceID] = item
	}
	if len(itemsBySource) != 2 {
		t.Fatalf("Backfill items were not split by Source: %+v", page.Items)
	}

	firstOperation := cancelSourceScopeAndReconcile(t, ctx, repository, sources[0].SourceID,
		"scope-multisource-first", now.Add(10*time.Second))
	firstItemWork, _ := repository.GetWork(ctx, itemsBySource[sources[0].SourceID].WorkID)
	secondItemWork, _ := repository.GetWork(ctx, itemsBySource[sources[1].SourceID].WorkID)
	partial, _ := repository.GetBackfill(ctx, backfill.BackfillID)
	parentAfterFirst, _ := repository.GetWork(ctx, parent.WorkID)
	if firstOperation.Status != model.ScopeControlCompleted || firstItemWork.Status != model.WorkCanceled ||
		secondItemWork.Status != model.WorkOpen || partial.Status != model.BackfillRunning || partial.CanceledItems != 1 ||
		parentAfterFirst.Status != model.WorkRunning {
		t.Fatalf("first Source cancel leaked across Backfill: operation=%+v first=%+v second=%+v Backfill=%+v parent=%+v",
			firstOperation, firstItemWork, secondItemWork, partial, parentAfterFirst)
	}

	secondOperation := cancelSourceScopeAndReconcile(t, ctx, repository, sources[1].SourceID,
		"scope-multisource-second", now.Add(20*time.Second))
	finished, _ := repository.GetBackfill(ctx, backfill.BackfillID)
	finishedParent, _ := repository.GetWork(ctx, parent.WorkID)
	if secondOperation.Status != model.ScopeControlCompleted || finished.Status != model.BackfillCompleted ||
		finished.CanceledItems != 2 || finishedParent.Status != model.WorkCompleted ||
		finishedParent.Resolution != model.ResolutionTerminated {
		t.Fatalf("second Source cancel did not explicitly close partial Backfill: operation=%+v Backfill=%+v parent=%+v",
			secondOperation, finished, finishedParent)
	}
}

func TestBackfillResultAndScopeCancelConvergeAtBothCommitCutpoints(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	repository, db, ctx, cleanup := detailContractRepository(t)
	defer cleanup()
	now := time.Date(2091, 9, 14, 0, 0, 0, 0, time.UTC)

	t.Run("scope_fence_commits_first", func(t *testing.T) {
		prefix := "scope-backfill-cutpoint-cancel-first"
		fixture := createRunningLiveBackfillFixture(t, ctx, repository, prefix, now)
		operation := startSourceCancelScope(t, ctx, repository, fixture.source.SourceID, prefix,
			fixture.now.Add(5*time.Second))
		if _, err := repository.AcceptBackfillResult(ctx, fixture.result); !errors.Is(err, ErrResultFenced) {
			t.Fatalf("result crossed committed scope fence: %v", err)
		}
		operation = reconcileCompletedScopeOperation(t, ctx, repository, operation.OperationID,
			fixture.now.Add(7*time.Second))
		backfill, _ := repository.GetBackfill(ctx, fixture.backfill.BackfillID)
		if operation.Status != model.ScopeControlCompleted || backfill.Status != model.BackfillCanceled {
			t.Fatalf("cancel-first convergence operation=%+v Backfill=%+v", operation, backfill)
		}
		var rejected, outputs int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_artifacts
WHERE artifact_id = ? AND rejected = TRUE`, fixture.result.Artifact.ArtifactID).Scan(&rejected); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_backfill_outputs
WHERE backfill_id = ?`, fixture.backfill.BackfillID).Scan(&outputs); err != nil {
			t.Fatal(err)
		}
		if rejected != 1 || outputs != 0 {
			t.Fatalf("cancel-first evidence rejected=%d outputs=%d", rejected, outputs)
		}
	})

	t.Run("result_commits_first", func(t *testing.T) {
		prefix := "scope-backfill-cutpoint-result-first"
		fixture := createRunningLiveBackfillFixture(t, ctx, repository, prefix, now.Add(time.Hour))
		outcome, err := repository.AcceptBackfillResult(ctx, fixture.result)
		if err != nil || outcome.Backfill.Status != model.BackfillCompleted || outcome.Output.OutputID == "" {
			t.Fatalf("result-first outcome=%+v err=%v", outcome, err)
		}
		operation := cancelSourceScopeAndReconcile(t, ctx, repository, fixture.source.SourceID, prefix,
			fixture.now.Add(7*time.Second))
		backfill, _ := repository.GetBackfill(ctx, fixture.backfill.BackfillID)
		work, _ := repository.GetWork(ctx, fixture.offer.Work.WorkID)
		attempt, _ := repository.GetAttempt(ctx, fixture.offer.Attempt.AttemptID)
		if operation.Status != model.ScopeControlCompleted || backfill.Status != model.BackfillCompleted ||
			backfill.CanceledItems != 0 || work.Status != model.WorkCompleted ||
			attempt.Status != model.AttemptSucceeded {
			t.Fatalf("result-first convergence operation=%+v Backfill=%+v Work=%+v Attempt=%+v",
				operation, backfill, work, attempt)
		}
	})
}

func cancelSourceScopeAndReconcile(t *testing.T, ctx context.Context, repository *Repository,
	sourceID, prefix string, at time.Time) model.ScopeControlOperation {
	t.Helper()
	operation := startSourceCancelScope(t, ctx, repository, sourceID, prefix, at)
	return reconcileCompletedScopeOperation(t, ctx, repository, operation.OperationID, at.Add(time.Second))
}

func startSourceCancelScope(t *testing.T, ctx context.Context, repository *Repository,
	sourceID, prefix string, at time.Time) model.ScopeControlOperation {
	t.Helper()
	source, err := repository.GetSource(ctx, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	paused, err := source.Pause(source.Version, model.PauseCancel)
	if err != nil {
		t.Fatal(err)
	}
	receipt, _ := model.NewCommandReceipt(prefix+"-pause", "recruiting.source.pause",
		"sha256:"+prefix+"-pause", json.RawMessage(`{}`))
	event, _ := model.NewEventIntent(prefix+"-pause-event", "source.paused", "source", source.SourceID,
		paused.Version, at.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{"mode":"cancel"}`))
	operationID := prefix + "-operation"
	if _, err := repository.ApplySourcePauseCommand(ctx, source.Version, paused, receipt, event, operationID, at); err != nil {
		t.Fatal(err)
	}
	operation, err := repository.GetScopeControlOperation(ctx, operationID)
	if err != nil {
		t.Fatal(err)
	}
	return operation
}

func cancelCompanyScopeAndReconcile(t *testing.T, ctx context.Context, repository *Repository,
	companyID, prefix string, at time.Time) model.ScopeControlOperation {
	t.Helper()
	company, err := repository.GetCompany(ctx, companyID)
	if err != nil {
		t.Fatal(err)
	}
	paused, err := company.Pause(company.Version, model.PauseCancel)
	if err != nil {
		t.Fatal(err)
	}
	receipt, _ := model.NewCommandReceipt(prefix+"-pause", "recruiting.company.pause",
		"sha256:"+prefix+"-pause", json.RawMessage(`{}`))
	event, _ := model.NewEventIntent(prefix+"-pause-event", "company.paused", "company", company.CompanyID,
		paused.Version, at.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{"mode":"cancel"}`))
	operationID := prefix + "-operation"
	if _, err := repository.ApplyCompanyPauseCommand(ctx, company.Version, paused, receipt, event, operationID, at); err != nil {
		t.Fatal(err)
	}
	return reconcileCompletedScopeOperation(t, ctx, repository, operationID, at.Add(time.Second))
}

func reconcileCompletedScopeOperation(t *testing.T, ctx context.Context, repository *Repository,
	operationID string, at time.Time) model.ScopeControlOperation {
	t.Helper()
	operation, err := repository.GetScopeControlOperation(ctx, operationID)
	if err != nil {
		t.Fatal(err)
	}
	for step := 0; step < 20 && operation.Status != model.ScopeControlCompleted; step++ {
		operation, err = repository.ReconcileScopeControlOperation(ctx, operation.OperationID, operation.Version, 500,
			at.Add(time.Duration(step)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
	}
	if operation.Status != model.ScopeControlCompleted {
		t.Fatalf("scope control did not complete: %+v", operation)
	}
	return operation
}

func assertScopeCanceledExecution(t *testing.T, ctx context.Context, db queryRower, repository *Repository,
	operation model.ScopeControlOperation, workID, attemptID string) {
	t.Helper()
	work, workErr := repository.GetWork(ctx, workID)
	attempt, attemptErr := repository.GetAttempt(ctx, attemptID)
	var activePermits int
	permitErr := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_budget_permits
WHERE attempt_id = ? AND permit_status = 'active'`, attemptID).Scan(&activePermits)
	if workErr != nil || attemptErr != nil || permitErr != nil || operation.WorksCanceled == 0 ||
		work.Status != model.WorkCanceled || attempt.Status != model.AttemptExpired || activePermits != 0 {
		t.Fatalf("scope cancel operation=%+v Work=%+v Attempt=%+v active_permits=%d err=%v/%v/%v",
			operation, work, attempt, activePermits, workErr, attemptErr, permitErr)
	}
}

func TestSourceResumeIsAtomicBoundedAndRestoresOnlyItsPauseProjection(t *testing.T) {
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
	now := time.Date(2026, 9, 12, 7, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("resume-company", "Resume", "https://resume.example")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("resume-source", company.CompanyID, "https://resume.example/jobs", "", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}

	open, _ := model.NewWork("resume-001-open", "source", source.SourceID, "listing_sync", "timer")
	waiting, _ := model.NewWork("resume-002-waiting", "source", source.SourceID, "detail_sync", "event")
	if err := repository.CreateWork(ctx, open, WorkPlacement{Priority: 1, NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateWork(ctx, waiting, WorkPlacement{Priority: 1, NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}
	running, _ := waiting.Start(waiting.Version)
	if err := repository.UpdateWorkCAS(ctx, waiting.Version, running, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	waiting, _ = running.WaitHuman(running.Version, "operator_review")
	if err := repository.UpdateWorkCAS(ctx, running.Version, waiting, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}

	paused, _ := source.Pause(source.Version, model.PauseDrain)
	pauseReceipt, _ := model.NewCommandReceipt("resume-pause-command", "recruiting.source.pause", "sha256:resume-pause",
		json.RawMessage(`{"source_id":"resume-source","status":"paused"}`))
	pauseEvent, _ := model.NewEventIntent("resume-pause-event", "source.paused", "source", source.SourceID,
		paused.Version, now.Add(3*time.Second).Format(time.RFC3339Nano), pauseReceipt.CommandID, json.RawMessage(`{"mode":"drain"}`))
	if _, err := repository.ApplySourcePauseCommand(ctx, source.Version, paused, pauseReceipt, pauseEvent,
		"resume-pause-operation", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	pauseOperation, _ := repository.GetScopeControlOperation(ctx, "resume-pause-operation")
	firstPause, err := repository.ReconcileScopeControlOperation(ctx, pauseOperation.OperationID, pauseOperation.Version, 1, now.Add(4*time.Second))
	if err != nil || firstPause.Status != model.ScopeControlApplying || firstPause.WorksPaused != 1 {
		t.Fatalf("first bounded pause=%+v err=%v", firstPause, err)
	}

	resumed, _ := paused.Resume(paused.Version)
	earlyReceipt, _ := model.NewCommandReceipt("early-resume-command", "recruiting.source.resume", "sha256:early-resume",
		json.RawMessage(`{"source_id":"resume-source","status":"active"}`))
	earlyEvent, _ := model.NewEventIntent("early-resume-event", "source.resumed", "source", source.SourceID,
		resumed.Version, now.Add(5*time.Second).Format(time.RFC3339Nano), earlyReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplySourceResumeCommand(ctx, paused.Version, resumed, earlyReceipt, earlyEvent,
		"early-resume-operation", now.Add(5*time.Second)); err == nil {
		t.Fatal("resume committed before the pause projection completed")
	}
	storedSource, err := repository.GetSource(ctx, source.SourceID)
	if err != nil || storedSource.ControlStatus != model.ControlPaused || storedSource.Version != paused.Version {
		t.Fatalf("early resume was not rolled back: source=%+v err=%v", storedSource, err)
	}
	if _, found, err := repository.LookupCommand(ctx, earlyReceipt.CommandID, earlyReceipt.RequestHash); err != nil || found {
		t.Fatalf("early resume receipt survived rollback: found=%v err=%v", found, err)
	}

	completedPause, err := repository.ReconcileScopeControlOperation(ctx, pauseOperation.OperationID, firstPause.Version, 1, now.Add(6*time.Second))
	if err != nil || completedPause.Status != model.ScopeControlCompleted || completedPause.WorksPaused != 2 {
		t.Fatalf("completed bounded pause=%+v err=%v", completedPause, err)
	}
	manual, _ := model.NewWork("resume-003-manual", "source", source.SourceID, "diagnostic", "manual")
	if err := repository.CreateWork(ctx, manual, WorkPlacement{Priority: 1, NotBefore: now}, now.Add(7*time.Second)); err != nil {
		t.Fatal(err)
	}
	manualPaused, _ := manual.Pause(manual.Version)
	if err := repository.UpdateWorkCAS(ctx, manual.Version, manualPaused, now.Add(8*time.Second)); err != nil {
		t.Fatal(err)
	}

	resumeReceipt, _ := model.NewCommandReceipt("resume-source-command", "recruiting.source.resume", "sha256:resume-source",
		json.RawMessage(`{"source_id":"resume-source","status":"active"}`))
	resumeEvent, _ := model.NewEventIntent("resume-source-event", "source.resumed", "source", source.SourceID,
		resumed.Version, now.Add(9*time.Second).Format(time.RFC3339Nano), resumeReceipt.CommandID, json.RawMessage(`{}`))
	first, err := repository.ApplySourceResumeCommand(ctx, paused.Version, resumed, resumeReceipt, resumeEvent,
		"resume-source-operation", now.Add(9*time.Second))
	if err != nil || first.Replayed {
		t.Fatalf("first resume=%+v err=%v", first, err)
	}
	replay, err := repository.ApplySourceResumeCommand(ctx, paused.Version, resumed, resumeReceipt, resumeEvent,
		"resume-source-operation", now.Add(9*time.Second))
	if err != nil || !replay.Replayed {
		t.Fatalf("resume replay=%+v err=%v", replay, err)
	}
	resumeOperation, err := repository.GetScopeControlOperation(ctx, "resume-source-operation")
	if err != nil || resumeOperation.Action != model.ScopeControlResume ||
		resumeOperation.ReversesOperationID != completedPause.OperationID {
		t.Fatalf("atomic resume operation=%+v err=%v", resumeOperation, err)
	}
	firstResume, err := repository.ReconcileScopeControlOperation(ctx, resumeOperation.OperationID, resumeOperation.Version, 1, now.Add(10*time.Second))
	if err != nil || firstResume.Status != model.ScopeControlApplying || firstResume.WorksResumed != 1 {
		t.Fatalf("first bounded resume=%+v err=%v", firstResume, err)
	}
	completedResume, err := repository.ReconcileScopeControlOperation(ctx, resumeOperation.OperationID, firstResume.Version, 1, now.Add(11*time.Second))
	if err != nil || completedResume.Status != model.ScopeControlApplying || !completedResume.ProjectionCompleted || completedResume.WorksResumed != 2 {
		t.Fatalf("completed bounded resume=%+v err=%v", completedResume, err)
	}
	catchUp, err := repository.ReconcileScopeControlCatchUpSource(ctx, completedResume.OperationID, completedResume.Version,
		now.Add(12*time.Second), nil)
	if err != nil || catchUp.Occurrence == nil || catchUp.Occurrence.Disposition != model.ScopeCatchUpSkipped ||
		catchUp.Occurrence.Reason != "source_not_currently_eligible" {
		t.Fatalf("Source catch-up decision=%+v err=%v", catchUp, err)
	}
	catchUp, err = repository.ReconcileScopeControlCatchUpSource(ctx, completedResume.OperationID, catchUp.Operation.Version,
		now.Add(13*time.Second), nil)
	if err != nil || catchUp.Occurrence != nil || catchUp.Operation.Status != model.ScopeControlCompleted ||
		!catchUp.Operation.CatchUpCompleted {
		t.Fatalf("Source catch-up completion=%+v err=%v", catchUp, err)
	}
	storedOpen, _ := repository.GetWork(ctx, open.WorkID)
	storedWaiting, _ := repository.GetWork(ctx, waiting.WorkID)
	storedManual, _ := repository.GetWork(ctx, manual.WorkID)
	if storedOpen.Status != model.WorkOpen || storedOpen.PausedByScopeOperationID != "" ||
		storedWaiting.Status != model.WorkWaitingHuman || storedWaiting.WaitingReason != "operator_review" ||
		storedWaiting.PausedByScopeOperationID != "" || storedManual.Status != model.WorkPaused {
		t.Fatalf("resume results open=%+v waiting=%+v manual=%+v", storedOpen, storedWaiting, storedManual)
	}
	var remainingMarkers int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_works
WHERE paused_by_scope_operation_id = ?`, completedPause.OperationID).Scan(&remainingMarkers); err != nil {
		t.Fatal(err)
	}
	if remainingMarkers != 0 {
		t.Fatalf("scope resume left %d owned pause markers", remainingMarkers)
	}
}

func TestCompanyResumeProjectsAllAndOnlyItsSourceWorks(t *testing.T) {
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
	now := time.Date(2026, 9, 12, 8, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("company-resume-scope", "Company Scope", "https://company-scope.example")
	otherCompany, _ := model.NewCompany("company-resume-other", "Other Scope", "https://other-scope.example")
	for _, value := range []model.Company{company, otherCompany} {
		if err := repository.CreateCompany(ctx, value, now); err != nil {
			t.Fatal(err)
		}
	}
	sources := []model.RecruitmentSource{}
	for _, item := range []struct{ id, companyID, endpoint string }{
		{"company-resume-source-a", company.CompanyID, "https://company-scope.example/a"},
		{"company-resume-source-b", company.CompanyID, "https://company-scope.example/b"},
		{"company-resume-source-other", otherCompany.CompanyID, "https://other-scope.example/jobs"},
	} {
		source, _ := model.NewRecruitmentSource(item.id, item.companyID, item.endpoint, "", 1)
		if err := repository.CreateSource(ctx, source, now); err != nil {
			t.Fatal(err)
		}
		sources = append(sources, source)
	}
	works := []model.Work{}
	for index, source := range sources {
		work, _ := model.NewWork(fmt.Sprintf("company-resume-work-%d", index), "source", source.SourceID, "listing_sync", "timer")
		if err := repository.CreateWork(ctx, work, WorkPlacement{Priority: 1, NotBefore: now}, now); err != nil {
			t.Fatal(err)
		}
		works = append(works, work)
	}

	paused, _ := company.Pause(company.Version, model.PauseDrain)
	pauseReceipt, _ := model.NewCommandReceipt("company-resume-pause-command", "recruiting.company.pause", "sha256:company-resume-pause", json.RawMessage(`{}`))
	pauseEvent, _ := model.NewEventIntent("company-resume-pause-event", "company.paused", "company", company.CompanyID,
		paused.Version, now.Add(time.Second).Format(time.RFC3339Nano), pauseReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyCompanyPauseCommand(ctx, company.Version, paused, pauseReceipt, pauseEvent,
		"company-resume-pause-operation", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	pauseOperation, _ := repository.GetScopeControlOperation(ctx, "company-resume-pause-operation")
	pauseOperation, err = repository.ReconcileScopeControlOperation(ctx, pauseOperation.OperationID, pauseOperation.Version, 500, now.Add(2*time.Second))
	if err != nil || pauseOperation.Status != model.ScopeControlCompleted || pauseOperation.WorksPaused != 2 {
		t.Fatalf("company pause projection=%+v err=%v", pauseOperation, err)
	}

	resumed, _ := paused.Resume(paused.Version)
	resumeReceipt, _ := model.NewCommandReceipt("company-resume-command", "recruiting.company.resume", "sha256:company-resume", json.RawMessage(`{}`))
	resumeEvent, _ := model.NewEventIntent("company-resume-event", "company.resumed", "company", company.CompanyID,
		resumed.Version, now.Add(3*time.Second).Format(time.RFC3339Nano), resumeReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyCompanyResumeCommand(ctx, paused.Version, resumed, resumeReceipt, resumeEvent,
		"company-resume-operation", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	resumeOperation, _ := repository.GetScopeControlOperation(ctx, "company-resume-operation")
	if resumeOperation.CatchUpUpperSourceID != sources[1].SourceID {
		t.Fatalf("Company resume did not freeze its Source upper bound: %+v", resumeOperation)
	}
	lateSource, _ := model.NewRecruitmentSource("company-resume-source-aa", company.CompanyID,
		"https://company-scope.example/aa", "", 1)
	if err := repository.CreateSource(ctx, lateSource, now.Add(3500*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	first, err := repository.ReconcileScopeControlOperation(ctx, resumeOperation.OperationID, resumeOperation.Version, 1, now.Add(4*time.Second))
	if err != nil || first.Status != model.ScopeControlApplying || first.WorksResumed != 1 {
		t.Fatalf("first Company resume page=%+v err=%v", first, err)
	}
	completed, err := repository.ReconcileScopeControlOperation(ctx, resumeOperation.OperationID, first.Version, 1, now.Add(5*time.Second))
	if err != nil || completed.Status != model.ScopeControlApplying || !completed.ProjectionCompleted || completed.WorksResumed != 2 ||
		completed.ReversesOperationID != pauseOperation.OperationID {
		t.Fatalf("completed Company resume=%+v err=%v", completed, err)
	}
	catchUpA, err := repository.ReconcileScopeControlCatchUpSource(ctx, completed.OperationID, completed.Version, now.Add(6*time.Second), nil)
	if err != nil || catchUpA.Occurrence == nil || catchUpA.Occurrence.SourceID != sources[0].SourceID ||
		catchUpA.Occurrence.Disposition != model.ScopeCatchUpSkipped {
		t.Fatalf("first Company catch-up=%+v err=%v", catchUpA, err)
	}
	catchUpB, err := repository.ReconcileScopeControlCatchUpSource(ctx, completed.OperationID, catchUpA.Operation.Version, now.Add(7*time.Second), nil)
	if err != nil || catchUpB.Occurrence == nil || catchUpB.Occurrence.SourceID != sources[1].SourceID ||
		catchUpB.Occurrence.Disposition != model.ScopeCatchUpSkipped {
		t.Fatalf("second Company catch-up=%+v err=%v", catchUpB, err)
	}
	catchUpDone, err := repository.ReconcileScopeControlCatchUpSource(ctx, completed.OperationID, catchUpB.Operation.Version, now.Add(8*time.Second), nil)
	if err != nil || catchUpDone.Occurrence != nil || catchUpDone.Operation.Status != model.ScopeControlCompleted ||
		catchUpDone.Operation.SourcesScanned != 2 || catchUpDone.Operation.CatchUpsSkipped != 2 {
		t.Fatalf("Company catch-up completion=%+v err=%v", catchUpDone, err)
	}
	var lateCatchUps int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_scope_catchup_occurrences
WHERE resume_operation_id = ? AND source_id = ?`, resumeOperation.OperationID, lateSource.SourceID).Scan(&lateCatchUps); err != nil {
		t.Fatal(err)
	}
	if lateCatchUps != 0 {
		t.Fatalf("Company resume included Source created after its immutable cut")
	}
	for index, expectedStatus := range []model.WorkStatus{model.WorkOpen, model.WorkOpen, model.WorkOpen} {
		stored, err := repository.GetWork(ctx, works[index].WorkID)
		if err != nil || stored.Status != expectedStatus {
			t.Fatalf("Work %d=%+v err=%v", index, stored, err)
		}
		if index < 2 && stored.PausedByScopeOperationID != "" {
			t.Fatalf("Company-scoped Work %d retained pause owner %+v", index, stored)
		}
		if index < 2 && (stored.Version != 3 || stored.AcceptanceVersion != 2) {
			t.Fatalf("Company-scoped Work %d did not cross exactly pause/resume: %+v", index, stored)
		}
		if index == 2 && (stored.Version != 1 || stored.AcceptanceVersion != 1) {
			t.Fatalf("other Company Work was mutated: %+v", stored)
		}
	}
}

func TestResumeCreatesOneCurrentProductionCatchUpWithoutMovingCheckpoint(t *testing.T) {
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
	now := time.Date(2026, 9, 12, 10, 0, 0, 0, time.UTC)
	company := persistExecutionReadyCompany(t, ctx, repository, "scope-catchup", now)
	source := persistExecutionReadySource(t, ctx, repository, company, "scope-catchup", "scope-catchup-source", now)
	checkpointBefore, err := repository.GetCheckpoint(ctx, source.SourceID)
	if err != nil {
		t.Fatal(err)
	}

	paused, _ := source.Pause(source.Version, model.PauseDrain)
	pauseReceipt, _ := model.NewCommandReceipt("scope-catchup-pause-command", "recruiting.source.pause", "sha256:scope-catchup-pause", json.RawMessage(`{}`))
	pauseEvent, _ := model.NewEventIntent("scope-catchup-pause-event", "source.paused", "source", source.SourceID,
		paused.Version, now.Add(time.Second).Format(time.RFC3339Nano), pauseReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplySourcePauseCommand(ctx, source.Version, paused, pauseReceipt, pauseEvent,
		"scope-catchup-pause-operation", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	pauseOperation, _ := repository.GetScopeControlOperation(ctx, "scope-catchup-pause-operation")
	pauseOperation, err = repository.ReconcileScopeControlOperation(ctx, pauseOperation.OperationID, pauseOperation.Version, 500, now.Add(2*time.Second))
	if err != nil || pauseOperation.Status != model.ScopeControlCompleted {
		t.Fatalf("catch-up pause projection=%+v err=%v", pauseOperation, err)
	}

	resumed, _ := paused.Resume(paused.Version)
	resumeReceipt, _ := model.NewCommandReceipt("scope-catchup-resume-command", "recruiting.source.resume", "sha256:scope-catchup-resume", json.RawMessage(`{}`))
	resumeEvent, _ := model.NewEventIntent("scope-catchup-resume-event", "source.resumed", "source", source.SourceID,
		resumed.Version, now.Add(3*time.Second).Format(time.RFC3339Nano), resumeReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplySourceResumeCommand(ctx, paused.Version, resumed, resumeReceipt, resumeEvent,
		"scope-catchup-resume-operation", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	resumeOperation, _ := repository.GetScopeControlOperation(ctx, "scope-catchup-resume-operation")
	resumeOperation, err = repository.ReconcileScopeControlOperation(ctx, resumeOperation.OperationID, resumeOperation.Version, 500, now.Add(4*time.Second))
	if err != nil || !resumeOperation.ProjectionCompleted || resumeOperation.Status != model.ScopeControlApplying {
		t.Fatalf("catch-up resume projection=%+v err=%v", resumeOperation, err)
	}
	targets := []ExecutionDispatchTarget{{ActorID: "tool:scope-catchup-executor", Capability: "http.fetch"}}
	step, err := repository.ReconcileScopeControlCatchUpSource(ctx, resumeOperation.OperationID, resumeOperation.Version,
		now.Add(5*time.Second), targets)
	if err != nil || step.Occurrence == nil || step.Occurrence.Disposition != model.ScopeCatchUpQueued || step.Dispatches != 1 {
		t.Fatalf("queued current catch-up=%+v err=%v", step, err)
	}
	work, err := repository.GetWorkRecord(ctx, step.Occurrence.WorkID)
	if err != nil || work.Work.Status != model.WorkOpen || work.Work.Trigger != "event" ||
		work.Placement.BusinessKey != "scope-catchup|"+resumeOperation.OperationID+"|"+source.SourceID {
		t.Fatalf("catch-up Work=%+v err=%v", work, err)
	}
	run, err := repository.GetListingRunByWork(ctx, work.Work.WorkID)
	if err != nil || run.Mode != model.ListingRunProduction || run.CheckpointVersion != checkpointBefore.Version {
		t.Fatalf("catch-up run=%+v err=%v", run, err)
	}
	offer, err := repository.OfferListingExecution(ctx, ListingOfferRequest{
		AttemptID: "scope-catchup-attempt", ExecutorActorID: "tool:scope-catchup-executor",
		ExecutorIncarnation: "scope-catchup-boot", Capability: work.Placement.Capability,
		Origin: work.Placement.Origin, OfferedAt: now.Add(5*time.Second + time.Microsecond), BudgetPolicy: testExecutionBudgetPolicy(),
	})
	if err != nil || offer.Work.WorkID != work.Work.WorkID || offer.Kind != "listing" {
		t.Fatalf("catch-up was not executable through the shared queue: offer=%+v err=%v", offer, err)
	}
	done, err := repository.ReconcileScopeControlCatchUpSource(ctx, resumeOperation.OperationID, step.Operation.Version,
		now.Add(6*time.Second), targets)
	if err != nil || done.Occurrence != nil || done.Operation.Status != model.ScopeControlCompleted || done.Operation.CatchUpsQueued != 1 {
		t.Fatalf("catch-up completion=%+v err=%v", done, err)
	}
	checkpointAfter, err := repository.GetCheckpoint(ctx, source.SourceID)
	if err != nil || !reflect.DeepEqual(checkpointAfter, checkpointBefore) {
		t.Fatalf("catch-up planning moved Checkpoint before=%+v after=%+v err=%v", checkpointBefore, checkpointAfter, err)
	}
	var occurrences, works, runs, dispatches int
	if err := db.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM recruiting_scope_catchup_occurrences WHERE resume_operation_id = ?),
  (SELECT COUNT(*) FROM recruiting_works WHERE business_key = ?),
  (SELECT COUNT(*) FROM recruiting_listing_runs WHERE listing_run_id = ?),
  (SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox WHERE cause_id = ?)`, resumeOperation.OperationID,
		work.Placement.BusinessKey, run.ListingRunID, step.Occurrence.OccurrenceID).
		Scan(&occurrences, &works, &runs, &dispatches); err != nil {
		t.Fatal(err)
	}
	if occurrences != 1 || works != 1 || runs != 1 || dispatches != 1 {
		t.Fatalf("catch-up facts occurrences=%d works=%d runs=%d dispatches=%d", occurrences, works, runs, dispatches)
	}
	page, err := repository.ListScopeCatchUpOccurrences(ctx, resumeOperation.OperationID, "", 1)
	if err != nil || len(page.Items) != 1 || page.Items[0].OccurrenceID != step.Occurrence.OccurrenceID || page.HasMore {
		t.Fatalf("catch-up occurrence page=%+v err=%v", page, err)
	}
}

func TestScopeControlPersistsCutAndPagesImmutableWorkScope(t *testing.T) {
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
	now := time.Date(2026, 9, 12, 3, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("scope-company", "Scope", "https://scope.example")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("scope-source", company.CompanyID, "https://scope.example/jobs", "", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	root, _ := model.NewWork("scope-001-root", "source", source.SourceID, "listing_sync", "timer")
	if err := repository.CreateWork(ctx, root, WorkPlacement{Priority: 1, NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}
	child, _ := model.NewChildWork(root, "scope-002-child", "fixture", "child", "detail_sync", "event")
	if err := repository.CreateWork(ctx, child, WorkPlacement{Priority: 1, NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}
	childRecord, err := repository.GetWorkRecord(ctx, child.WorkID)
	if err != nil || childRecord.Placement.CompanyID != company.CompanyID || childRecord.Placement.SourceID != source.SourceID ||
		childRecord.Placement.RootWorkID != root.WorkID {
		t.Fatalf("inherited immutable Work scope=%+v err=%v", childRecord.Placement, err)
	}
	paused, _ := source.Pause(source.Version, model.PauseFinishCausalChain)
	if err := repository.UpdateSourceCAS(ctx, source.Version, paused, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	operation, err := model.NewScopeControlOperation("scope-operation", "source", source.SourceID,
		model.PauseFinishCausalChain, paused.Version, paused.ConfigurationVersion, paused.ControlEpoch,
		paused.ExecutionFence, 1, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateScopeControlOperation(ctx, operation, []string{root.WorkID}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	late, _ := model.NewWork("scope-003-late", "source", source.SourceID, "listing_sync", "timer")
	if err := repository.CreateWork(ctx, late, WorkPlacement{Priority: 1, NotBefore: now}, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	first, err := repository.ListScopeControlWorks(ctx, operation, 1)
	if err != nil || len(first.Items) != 1 || first.Items[0].Work.WorkID != root.WorkID || !first.HasMore {
		t.Fatalf("first scope page=%+v err=%v", first, err)
	}
	operation.WorkCursor = first.NextCursor
	second, err := repository.ListScopeControlWorks(ctx, operation, 1)
	if err != nil || len(second.Items) != 1 || second.Items[0].Work.WorkID != child.WorkID || second.HasMore {
		t.Fatalf("second scope page=%+v err=%v", second, err)
	}
	stored, err := repository.GetScopeControlOperation(ctx, operation.OperationID)
	if err != nil || stored.ControlEpoch != paused.ControlEpoch || stored.ActiveRoots != 1 {
		t.Fatalf("stored operation=%+v err=%v", stored, err)
	}
	var explain string
	if err := db.QueryRowContext(ctx, `EXPLAIN FORMAT=JSON SELECT state_json FROM recruiting_works
FORCE INDEX (ix_recruiting_work_source_scope)
WHERE source_id = ? AND work_id > ? AND created_at <= ? ORDER BY work_id LIMIT 501`,
		source.SourceID, "", now.Add(time.Second)).Scan(&explain); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "ix_recruiting_work_source_scope") {
		t.Fatalf("scope projection used no intended index: %s", explain)
	}
}
