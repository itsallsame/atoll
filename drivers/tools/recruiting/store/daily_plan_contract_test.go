package store

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestDailyCutoffPlanFreezesEligibleRosterAtomically(t *testing.T) {
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
	now := time.Date(2090, 1, 2, 0, 0, 0, 123456000, time.UTC)

	company := persistReadyDailyCompany(t, ctx, repository, "cutoff-company", now)
	first := persistReadyDailySource(t, ctx, repository, company, "cutoff-source-a", now)
	second := persistReadyDailySource(t, ctx, repository, company, "cutoff-source-b", now)
	quarantinedSource := persistReadyDailySource(t, ctx, repository, company, "cutoff-source-quarantined", now)
	quarantinedRecipe, err := repository.GetRecipe(ctx, "recipe-"+quarantinedSource.SourceID, 1)
	if err != nil {
		t.Fatal(err)
	}
	quarantinedRecipe, err = quarantinedRecipe.Quarantine(quarantinedRecipe.StateVersion)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.UpdateRecipeCAS(ctx, quarantinedRecipe.StateVersion-1, quarantinedRecipe, now); err != nil {
		t.Fatal(err)
	}
	candidate, _ := model.NewRecruitmentSource("cutoff-source-candidate", company.CompanyID, "https://cutoff.example.com/candidate", "all", 1)
	if err := repository.CreateSource(ctx, candidate, now); err != nil {
		t.Fatal(err)
	}

	request := DailyPlanRequest{
		DailyRunID: "daily-run-2090-01-02", ScheduleDate: "2090-01-02",
		Schedule: model.DailySchedule{
			PolicyVersion: 7, CutoffAt: "2090-01-02T00:00:00.123456Z",
			WindowStartAt: "2090-01-02T00:30:00Z", WindowEndAt: "2090-01-02T06:30:00Z",
		},
		TriggerID: "atoll-timer-2090-01-02", EventID: "daily-started-2090-01-02",
	}
	results := make(chan DailyPlanResult, 2)
	errorsFound := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			result, planErr := repository.PlanDailyRunAtCutoff(ctx, request, now)
			results <- result
			errorsFound <- planErr
		}()
	}
	group.Wait()
	close(results)
	close(errorsFound)
	for planErr := range errorsFound {
		if planErr != nil {
			t.Fatal(planErr)
		}
	}
	var planned DailyPlanResult
	replays := 0
	for result := range results {
		if result.Replayed {
			replays++
		} else {
			planned = result
		}
	}
	if replays != 1 {
		t.Fatalf("concurrent daily plan replays = %d", replays)
	}
	if planned.Replayed || planned.Occurrences != 2 || planned.Run.ExpectedSources != 2 || planned.Run.Status != model.DailyRunRunning {
		t.Fatalf("daily plan = %+v", planned)
	}
	page, err := repository.ListOccurrences(ctx, planned.Run.DailyRunID, "", 10)
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("cutoff occurrences = %+v err=%v", page, err)
	}
	windowStart, _ := time.Parse(time.RFC3339, planned.Run.WindowStartAt)
	windowEnd, _ := time.Parse(time.RFC3339, planned.Run.WindowEndAt)
	versions := map[string]uint64{first.SourceID: first.Version, second.SourceID: second.Version}
	for _, occurrence := range page.Items {
		dueAt, parseErr := time.Parse(time.RFC3339, occurrence.DueAt)
		if parseErr != nil || dueAt.Before(windowStart) || !dueAt.Before(windowEnd) ||
			occurrence.CompanyVersion != company.Version || occurrence.SourceVersion != versions[occurrence.SourceID] {
			t.Fatalf("invalid cutoff occurrence = %+v parse=%v", occurrence, parseErr)
		}
	}

	paused, _ := first.Pause(first.Version, model.PauseDrain)
	if err := repository.UpdateSourceCAS(ctx, first.Version, paused, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	replayed, err := repository.PlanDailyRunAtCutoff(ctx, request, now.Add(2*time.Second))
	if err != nil || !replayed.Replayed || replayed.Occurrences != 2 {
		t.Fatalf("daily replay = %+v err=%v", replayed, err)
	}
	after, err := repository.ListOccurrences(ctx, planned.Run.DailyRunID, "", 10)
	if err != nil || after.Items[0].SourceVersion != page.Items[0].SourceVersion || after.Items[1].SourceVersion != page.Items[1].SourceVersion {
		t.Fatalf("cutoff snapshot changed after source pause: %+v err=%v", after, err)
	}

	changed := request
	changed.Schedule.PolicyVersion++
	if _, err := repository.PlanDailyRunAtCutoff(ctx, changed, now); !errors.Is(err, ErrBusinessKeyExists) {
		t.Fatalf("changed immutable replay = %v", err)
	}
	pending, err := repository.ListPendingEvents(ctx, now.Add(time.Hour), 100)
	if err != nil {
		t.Fatal(err)
	}
	events := 0
	for _, item := range pending {
		if item.Intent.EventID == request.EventID {
			events++
		}
	}
	if events != 1 {
		t.Fatalf("daily started outbox events = %d", events)
	}
}

func persistReadyDailyCompany(t *testing.T, ctx context.Context, repository *Repository, id string, now time.Time) model.Company {
	t.Helper()
	company, err := model.NewCompany(id, "Cutoff Company", "https://cutoff.example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	next, err := company.StartDiscovery(company.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.UpdateCompanyCAS(ctx, company.Version, next, now); err != nil {
		t.Fatal(err)
	}
	company = next
	next, err = company.StartInitialization(company.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.UpdateCompanyCAS(ctx, company.Version, next, now); err != nil {
		t.Fatal(err)
	}
	company = next
	next, err = company.MarkReady(company.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.UpdateCompanyCAS(ctx, company.Version, next, now); err != nil {
		t.Fatal(err)
	}
	return next
}

func persistReadyDailySource(t *testing.T, ctx context.Context, repository *Repository, company model.Company, id string, now time.Time) model.RecruitmentSource {
	t.Helper()
	source, err := model.NewRecruitmentSource(id, company.CompanyID, "https://cutoff.example.com/jobs/"+id, "all", 1)
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
	recipe := activeRecipe(t, "recipe-"+id, model.RecipeListing, "cutoff.example.com", 1, "contract-"+id)
	if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
		t.Fatal(err)
	}
	assignment, err := model.NewSourceRecipeAssignment(id, model.RecipeListing, recipe.RecipeID, recipe.Version, recipe.ContractHash, now.Format(time.RFC3339Nano))
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
	return ready
}
