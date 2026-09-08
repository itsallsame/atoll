package store

import (
	"context"
	"crypto/sha256"
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

func testExecutionBudgetPolicy() ExecutionBudgetPolicy {
	policy := DefaultExecutionBudgetPolicy()
	policy.MaxActive, policy.MaxPerCapability = 100, 100
	policy.MaxPerOrigin, policy.MaxPerCompany, policy.MaxPerProfile = 100, 100, 100
	return policy
}

func pauseExecutionSource(t *testing.T, ctx context.Context, repository *Repository, sourceID string, at time.Time) {
	t.Helper()
	source, err := repository.GetSource(ctx, sourceID)
	if err != nil || source.ControlStatus != model.ControlActive {
		return
	}
	paused, err := source.Pause(source.Version, model.PauseDrain)
	if err != nil {
		t.Errorf("pause fixture source %s: %v", sourceID, err)
		return
	}
	if err := repository.UpdateSourceCAS(ctx, source.Version, paused, at); err != nil {
		t.Errorf("persist paused fixture source %s: %v", sourceID, err)
	}
}

func TestListingExecutionOfferAndLifecycleAreFenced(t *testing.T) {
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
	offerAt, _ := prepareListingExecutionWork(t, ctx, repository, "execution-life", 1)

	offer, err := repository.OfferListingExecution(ctx, ListingOfferRequest{
		AttemptID: "attempt-execution-life-1", ExecutorActorID: "executor-a", ExecutorIncarnation: "boot-a",
		Capability: "http.fetch", Origin: "https://execution-life.example.com", OfferedAt: offerAt, BudgetPolicy: testExecutionBudgetPolicy(),
	})
	if err != nil {
		t.Fatal(err)
	}
	workID := offer.Work.WorkID
	if workID == "" || offer.Kind != "listing" || offer.Attempt.Status != model.AttemptOffered || offer.Occurrence.Status != model.OccurrenceQueued ||
		offer.Occurrence.ListingExecution.Execution.ContentRef == "" || offer.Attempt.RecipeID != offer.Occurrence.ListingExecution.RecipeID {
		t.Fatalf("incomplete execution offer: %+v", offer)
	}
	replayed, err := repository.OfferListingExecution(ctx, ListingOfferRequest{
		AttemptID: "attempt-execution-life-1", ExecutorActorID: "executor-a", ExecutorIncarnation: "boot-a",
		Capability: "http.fetch", Origin: "https://execution-life.example.com", OfferedAt: offerAt, BudgetPolicy: testExecutionBudgetPolicy(),
	})
	if err != nil || !reflect.DeepEqual(replayed, offer) {
		t.Fatalf("execution offer replay changed: %+v err=%v", replayed, err)
	}
	if _, err := repository.OfferListingExecution(ctx, ListingOfferRequest{
		AttemptID: "attempt-execution-life-1", ExecutorActorID: "executor-a", ExecutorIncarnation: "boot-a",
		Capability: "http.fetch", OfferedAt: offerAt, BudgetPolicy: testExecutionBudgetPolicy(),
	}); !errors.Is(err, ErrAttemptConflict) {
		t.Fatalf("changed offer request replay was accepted: %v", err)
	}
	if _, err := repository.OfferListingExecution(ctx, ListingOfferRequest{
		AttemptID: "attempt-execution-life-duplicate", ExecutorActorID: "executor-b", ExecutorIncarnation: "boot-b",
		Capability: "http.fetch", Origin: "https://execution-life.example.com", OfferedAt: offerAt, BudgetPolicy: testExecutionBudgetPolicy(),
	}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second active offer claimed same work: %v", err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, "executor-a", "wrong-boot", offerAt); !errors.Is(err, ErrAttemptConflict) {
		t.Fatalf("wrong incarnation accepted offer: %v", err)
	}
	accepted, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, "executor-a", "boot-a", offerAt)
	if err != nil || accepted.Status != model.AttemptAccepted {
		t.Fatalf("accepted attempt = %+v err=%v", accepted, err)
	}
	running, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, "executor-a", "boot-a", offerAt.Add(time.Second))
	if err != nil || running.Status != model.AttemptRunning {
		t.Fatalf("running attempt = %+v err=%v", running, err)
	}
	work, _ := repository.GetWork(ctx, workID)
	occurrence, _ := repository.GetOccurrence(ctx, offer.Occurrence.OccurrenceID)
	if work.Status != model.WorkRunning || occurrence.Status != model.OccurrenceRunning {
		t.Fatalf("start was not atomic: work=%+v occurrence=%+v", work, occurrence)
	}
	failed, err := repository.FailListingExecution(ctx, offer.Attempt.AttemptID, "executor-a", "boot-a", "transient_timeout", offerAt.Add(2*time.Second))
	if err != nil || failed.Status != model.AttemptFailed {
		t.Fatalf("failed attempt = %+v err=%v", failed, err)
	}
	work, _ = repository.GetWork(ctx, workID)
	if work.Status != model.WorkWaitingRetry || work.WaitingReason != "transient_timeout" {
		t.Fatalf("failed execution did not become retryable: %+v", work)
	}
	replayed, err = repository.OfferListingExecution(ctx, ListingOfferRequest{
		AttemptID: "attempt-execution-life-1", ExecutorActorID: "executor-a", ExecutorIncarnation: "boot-a",
		Capability: "http.fetch", Origin: "https://execution-life.example.com", OfferedAt: offerAt, BudgetPolicy: testExecutionBudgetPolicy(),
	})
	if err != nil || !reflect.DeepEqual(replayed, offer) {
		t.Fatalf("completed attempt changed persisted offer replay: %+v err=%v", replayed, err)
	}
	retry, err := repository.OfferListingExecution(ctx, ListingOfferRequest{
		AttemptID: "attempt-execution-life-2", ExecutorActorID: "executor-b", ExecutorIncarnation: "boot-b",
		Capability: "http.fetch", Origin: "https://execution-life.example.com", OfferedAt: offerAt, BudgetPolicy: testExecutionBudgetPolicy(),
	})
	if err != nil || retry.Work.WorkID != workID || retry.Attempt.AcceptanceVersion != work.AcceptanceVersion {
		t.Fatalf("retry offer = %+v err=%v", retry, err)
	}
	source, err := repository.GetSource(ctx, offer.Occurrence.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	paused, err := source.Pause(source.Version, model.PauseDrain)
	if err == nil {
		err = repository.UpdateSourceCAS(ctx, source.Version, paused, offerAt)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcceptListingExecution(ctx, retry.Attempt.AttemptID, "executor-b", "boot-b", offerAt); !errors.Is(err, ErrResultFenced) {
		t.Fatalf("changed source fence accepted retry attempt: %v", err)
	}
	expiredRetry, _ := retry.Attempt.Expire()
	if err := repository.UpdateAttemptCAS(ctx, retry.Attempt.Status, expiredRetry, offerAt); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentListingOffersClaimDistinctWorks(t *testing.T) {
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
	offerAt, _ := prepareListingExecutionWork(t, ctx, repository, "execution-race", 2)

	results := make(chan ListingExecutionOffer, 2)
	errorsFound := make(chan error, 2)
	var group sync.WaitGroup
	for index := range 2 {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			result, offerErr := repository.OfferListingExecution(ctx, ListingOfferRequest{
				AttemptID: fmt.Sprintf("attempt-execution-race-%d", worker), ExecutorActorID: fmt.Sprintf("executor-%d", worker),
				ExecutorIncarnation: fmt.Sprintf("boot-%d", worker), Capability: "http.fetch",
				Origin: "https://execution-race.example.com", OfferedAt: offerAt, BudgetPolicy: testExecutionBudgetPolicy(),
			})
			results <- result
			errorsFound <- offerErr
		}(index)
	}
	group.Wait()
	close(results)
	close(errorsFound)
	for offerErr := range errorsFound {
		if offerErr != nil {
			t.Fatal(offerErr)
		}
	}
	works := map[string]bool{}
	for result := range results {
		if works[result.Work.WorkID] {
			t.Fatalf("two executors claimed work %s", result.Work.WorkID)
		}
		works[result.Work.WorkID] = true
	}
	if len(works) != 2 {
		t.Fatalf("concurrent offers claimed %d works", len(works))
	}
}

func prepareListingExecutionWork(t *testing.T, ctx context.Context, repository *Repository, prefix string, sourceCount int) (time.Time, string) {
	t.Helper()
	scheduleKey := sha256.Sum256([]byte(prefix))
	year, month, day := 2050+int(scheduleKey[0])%30, time.Month(1+int(scheduleKey[1])%12), 1+int(scheduleKey[2])%28
	now := time.Date(year, month, day, 0, 0, 0, 0, time.UTC)
	company := persistExecutionReadyCompany(t, ctx, repository, prefix, now)
	for index := range sourceCount {
		persistExecutionReadySource(t, ctx, repository, company, prefix, fmt.Sprintf("%s-source-%d", prefix, index), now)
	}
	scheduleDate := now.Format("2006-01-02")
	request := DailyPlanRequest{
		DailyRunID: prefix + "-daily", ScheduleDate: scheduleDate,
		Schedule: model.DailySchedule{PolicyVersion: 1, CutoffAt: now.Format(time.RFC3339Nano),
			WindowStartAt: now.Add(time.Minute).Format(time.RFC3339Nano), WindowEndAt: now.Add(time.Hour).Format(time.RFC3339Nano)},
		TriggerID: prefix + "-trigger", EventID: prefix + "-event",
	}
	plan, err := repository.PlanDailyRunAtCutoff(ctx, request, now)
	if err != nil {
		t.Fatal(err)
	}
	page, err := repository.ListOccurrences(ctx, plan.Run.DailyRunID, "", 10)
	if err != nil || len(page.Items) < sourceCount {
		t.Fatalf("planned occurrences=%d err=%v", len(page.Items), err)
	}
	latestDue := now
	for _, occurrence := range page.Items {
		dueAt, _ := time.Parse(time.RFC3339, occurrence.DueAt)
		if dueAt.After(latestDue) {
			latestDue = dueAt
		}
	}
	offerAt := latestDue.Add(time.Microsecond)
	materialized, err := repository.MaterializeDueOccurrenceWorks(ctx, offerAt, len(page.Items), "recruiting", prefix+"-materialize", offerAt)
	if err != nil || materialized.Queued != len(page.Items) {
		t.Fatalf("materialized=%+v err=%v", materialized, err)
	}
	page, _ = repository.ListOccurrences(ctx, plan.Run.DailyRunID, "", 10)
	return offerAt, page.Items[0].WorkID
}

func persistExecutionReadyCompany(t *testing.T, ctx context.Context, repository *Repository, prefix string, now time.Time) model.Company {
	t.Helper()
	company, err := model.NewCompany(prefix+"-company", prefix, "https://"+prefix+".example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	company, err = company.StartDiscovery(company.Version)
	if err == nil {
		err = repository.UpdateCompanyCAS(ctx, company.Version-1, company, now)
	}
	if err == nil {
		company, err = company.StartInitialization(company.Version)
	}
	if err == nil {
		err = repository.UpdateCompanyCAS(ctx, company.Version-1, company, now)
	}
	if err == nil {
		company, err = company.MarkReady(company.Version)
	}
	if err == nil {
		err = repository.UpdateCompanyCAS(ctx, company.Version-1, company, now)
	}
	if err != nil {
		t.Fatal(err)
	}
	return company
}

func persistExecutionReadySource(t *testing.T, ctx context.Context, repository *Repository, company model.Company, prefix, sourceID string, now time.Time) model.RecruitmentSource {
	t.Helper()
	host := prefix + ".example.com"
	source, err := model.NewRecruitmentSource(sourceID, company.CompanyID, "https://"+host+"/jobs/"+sourceID, "all", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	validating, err := source.BeginValidation(source.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.UpdateSourceCAS(ctx, source.Version, validating, now); err != nil {
		t.Fatal(err)
	}
	recipe := activeRecipe(t, "recipe-"+sourceID, model.RecipeListing, host, 1, "contract-"+sourceID)
	if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
		t.Fatal(err)
	}
	assignment, err := model.NewSourceRecipeAssignment(sourceID, model.RecipeListing, recipe.RecipeID, recipe.Version, recipe.ContractHash, now.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	ready, err := validating.PublishValidated(validating.Version, assignment, verifiedStoreAssessment(validating, assignment, now))
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishSourceAssignment(ctx, validating.Version, 0, ready, assignment, now); err != nil {
		t.Fatal(err)
	}
	detailRecipe := activeRecipe(t, "detail-recipe-"+sourceID, model.RecipeDetail, host, 1, "detail-contract-"+sourceID)
	if err := repository.CreateRecipe(ctx, detailRecipe, now); err != nil {
		t.Fatal(err)
	}
	detailAssignment, err := model.NewSourceRecipeAssignment(sourceID, model.RecipeDetail, detailRecipe.RecipeID,
		detailRecipe.Version, detailRecipe.ContractHash, now.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	withDetail, err := ready.AssignRecipe(ready.Version, detailAssignment, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishSourceAssignment(ctx, ready.Version, 0, withDetail, detailAssignment, now); err != nil {
		t.Fatal(err)
	}
	checkpoint, err := model.EstablishCheckpoint(model.IncrementalCheckpoint{
		SourceID: sourceID, RecipeID: recipe.RecipeID, RecipeVersion: recipe.Version, ContractHash: recipe.ContractHash,
		Strategy: model.CheckpointActivityTime, FrontierActivityAt: now.Add(-time.Hour).Format(time.RFC3339),
		OverlapPages: 1, LastOccurrenceID: "baseline-" + sourceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	checkpointState, _ := json.Marshal(checkpoint)
	if _, err := repository.db.ExecContext(ctx, `
INSERT INTO recruiting_checkpoints(
  source_id, checkpoint_version, recipe_id, recipe_version, contract_hash,
  frontier_activity_at, frontier_keys_json, last_occurrence_id, state_json, updated_at
) VALUES (?, ?, ?, ?, ?, ?, NULL, ?, ?, ?)`, checkpoint.SourceID, checkpoint.Version, checkpoint.RecipeID,
		checkpoint.RecipeVersion, checkpoint.ContractHash, now.Add(-time.Hour), checkpoint.LastOccurrenceID, checkpointState, now); err != nil {
		t.Fatal(err)
	}
	return withDetail
}
