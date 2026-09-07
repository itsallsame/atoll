package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestWorkAndAttemptRepositoryContract(t *testing.T) {
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
	now := time.Date(2026, 9, 8, 3, 0, 0, 0, time.UTC)
	create := func(id string, priority int, notBefore time.Time, capability, origin, profile string, deadline *time.Time) model.Work {
		work, _ := model.NewWork(id, "fixture", id, "fixture_run", "test")
		err := repository.CreateWork(ctx, work, WorkPlacement{
			BusinessKey: "business-" + id, Priority: priority, Capability: capability,
			Origin: origin, ProfileID: profile, NotBefore: notBefore, DeadlineAt: deadline,
		}, now)
		if err != nil {
			t.Fatal(err)
		}
		return work
	}
	high := create("runnable-high", 100, now.Add(-time.Minute), "fixture.work", "origin-a", "profile-a", nil)
	_ = create("runnable-low", 10, now.Add(-time.Minute), "fixture.work", "origin-a", "profile-a", nil)
	_ = create("runnable-future", 200, now.Add(time.Hour), "fixture.work", "origin-a", "profile-a", nil)
	_ = create("runnable-other-capability", 300, now.Add(-time.Minute), "other.work", "origin-a", "profile-a", nil)
	expiredAt := now.Add(-time.Second)
	_ = create("runnable-expired", 400, now.Add(-time.Hour), "fixture.work", "origin-a", "profile-a", &expiredAt)

	runnable, err := repository.ListRunnableWorks(ctx, RunnableWorkQuery{
		DueAt: now, Capability: "fixture.work", Origin: "origin-a", ProfileID: "profile-a", Limit: 10,
	})
	if err != nil || len(runnable) != 2 || runnable[0].WorkID != "runnable-high" || runnable[1].WorkID != "runnable-low" {
		t.Fatalf("runnable works = %+v %v", runnable, err)
	}
	var explain string
	if err := db.QueryRowContext(ctx, `EXPLAIN FORMAT=JSON
SELECT state_json FROM recruiting_works
WHERE capability = 'fixture.work' AND status IN ('open', 'waiting_retry')
  AND not_before <= ? AND (deadline_at IS NULL OR deadline_at > ?)
ORDER BY priority DESC, not_before, work_id LIMIT 10`, now, now).Scan(&explain); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "ix_recruiting_work_capability") && !strings.Contains(explain, "ix_recruiting_work_runnable") {
		t.Fatalf("runnable query used no intended index: %s", explain)
	}

	started, _ := high.Start(high.Version)
	workErrors := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			workErrors <- repository.UpdateWorkCAS(ctx, high.Version, started, now.Add(time.Second))
		}()
	}
	group.Wait()
	close(workErrors)
	var workSucceeded, workConflicted int
	for updateErr := range workErrors {
		if updateErr == nil {
			workSucceeded++
		} else {
			var conflict *model.VersionConflictError
			if errors.As(updateErr, &conflict) {
				workConflicted++
			} else {
				t.Fatalf("work CAS = %v", updateErr)
			}
		}
	}
	if workSucceeded != 1 || workConflicted != 1 {
		t.Fatalf("work CAS succeeded=%d conflicted=%d", workSucceeded, workConflicted)
	}

	attempt, _ := model.NewAttempt("work-contract-attempt", started)
	attempt, _ = attempt.BindExecutor("executor-a", "incarnation-a", "fixture.work")
	attempt, _ = attempt.WithFence(model.AttemptFence{
		CompanyVersion: 1, SourceVersion: 1, AssignmentVersion: 1,
		RecipeID: "fixture-recipe", RecipeVersion: 1,
	})
	if err := repository.CreateAttempt(ctx, attempt, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	accepted, _ := attempt.Accept()
	attemptErrors := make(chan error, 2)
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			attemptErrors <- repository.UpdateAttemptCAS(ctx, model.AttemptOffered, accepted, now.Add(2*time.Second))
		}()
	}
	group.Wait()
	close(attemptErrors)
	var attemptSucceeded, attemptConflicted int
	for updateErr := range attemptErrors {
		switch {
		case updateErr == nil:
			attemptSucceeded++
		case errors.Is(updateErr, ErrAttemptConflict):
			attemptConflicted++
		default:
			t.Fatalf("attempt CAS = %v", updateErr)
		}
	}
	if attemptSucceeded != 1 || attemptConflicted != 1 {
		t.Fatalf("attempt CAS succeeded=%d conflicted=%d", attemptSucceeded, attemptConflicted)
	}
	storedAttempt, err := repository.GetAttempt(ctx, attempt.AttemptID)
	if err != nil || storedAttempt.Status != model.AttemptAccepted || storedAttempt.ExecutorIncarnation != "incarnation-a" {
		t.Fatalf("stored attempt = %+v %v", storedAttempt, err)
	}
}
