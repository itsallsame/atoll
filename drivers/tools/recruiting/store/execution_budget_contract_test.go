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

func TestExecutionBudgetCannotOverissueAndReleasesAtomically(t *testing.T) {
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
	offerAt, _ := prepareListingExecutionWork(t, ctx, repository, "budget-race", 2)
	defer pauseExecutionSource(t, ctx, repository, "budget-race-source-0", offerAt.Add(time.Hour))
	defer pauseExecutionSource(t, ctx, repository, "budget-race-source-1", offerAt.Add(time.Hour))
	policy := testExecutionBudgetPolicy()
	policy.Version, policy.MaxPerOrigin = 7, 1

	type result struct {
		offer ExecutionOffer
		err   error
	}
	results := make(chan result, 2)
	var group sync.WaitGroup
	for worker := 1; worker <= 2; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			offer, err := repository.OfferListingExecution(ctx, ListingOfferRequest{
				AttemptID: "budget-race-attempt-" + string(rune('0'+worker)), ExecutorActorID: "budget-race-executor-" + string(rune('0'+worker)),
				ExecutorIncarnation: "boot", Capability: "http.fetch", Origin: "https://budget-race.example.com",
				OfferedAt: offerAt, BudgetPolicy: policy,
			})
			results <- result{offer, err}
		}(worker)
	}
	group.Wait()
	close(results)
	var granted ExecutionOffer
	var grants, blocked int
	for result := range results {
		if result.err == nil {
			grants++
			granted = result.offer
		} else if errors.Is(result.err, ErrBudgetBlocked) {
			blocked++
		} else {
			t.Fatalf("budget race error = %v", result.err)
		}
	}
	if grants != 1 || blocked != 1 || granted.Budget.Status != model.PermitGranted || granted.Budget.PolicyVersion != 7 {
		t.Fatalf("budget race grants=%d blocked=%d offer=%+v", grants, blocked, granted)
	}
	assertBudgetUsage(t, ctx, db, "origin", "https://budget-race.example.com", 1)
	if _, err := repository.AcceptListingExecution(ctx, granted.Attempt.AttemptID, granted.Attempt.ExecutorActorID, "boot", offerAt); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, granted.Attempt.AttemptID, granted.Attempt.ExecutorActorID, "boot", offerAt); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.FailListingExecution(ctx, granted.Attempt.AttemptID, granted.Attempt.ExecutorActorID, "boot", "fixture_failure", offerAt); err != nil {
		t.Fatal(err)
	}
	assertBudgetUsage(t, ctx, db, "origin", "https://budget-race.example.com", 0)
	var permitStatus model.BudgetPermitStatus
	if err := db.QueryRowContext(ctx, "SELECT permit_status FROM recruiting_budget_permits WHERE attempt_id = ?", granted.Attempt.AttemptID).Scan(&permitStatus); err != nil || permitStatus != model.PermitReleased {
		t.Fatalf("released permit status=%s err=%v", permitStatus, err)
	}
	shortPolicy := policy
	shortPolicy.PermitTTL = time.Second
	afterRelease, err := repository.OfferListingExecution(ctx, ListingOfferRequest{
		AttemptID: "budget-race-after-release", ExecutorActorID: "budget-race-executor-3", ExecutorIncarnation: "boot",
		Capability: "http.fetch", Origin: "https://budget-race.example.com", OfferedAt: offerAt, BudgetPolicy: shortPolicy,
	})
	if err != nil {
		t.Fatalf("released capacity was not reusable: %v", err)
	}
	recovered, err := repository.RecoverStaleAttempts(ctx, offerAt.Add(-time.Hour), 10, offerAt.Add(2*time.Second))
	if err != nil || recovered.Expired < 1 {
		t.Fatalf("expired permit recovery = %+v err=%v", recovered, err)
	}
	attempt, _ := repository.GetAttempt(ctx, afterRelease.Attempt.AttemptID)
	if attempt.Status != model.AttemptExpired {
		t.Fatalf("expired permit left attempt active: %+v", attempt)
	}
	assertBudgetUsage(t, ctx, db, "origin", "https://budget-race.example.com", 0)
}

func assertBudgetUsage(t *testing.T, ctx context.Context, db queryRower, dimensionType, key string, want int) {
	t.Helper()
	var active int
	if err := db.QueryRowContext(ctx, `SELECT active_count FROM recruiting_budget_usage WHERE dimension_type = ? AND dimension_key = ?`, dimensionType, key).Scan(&active); err != nil || active != want {
		t.Fatalf("budget usage %s/%s=%d want=%d err=%v", dimensionType, key, active, want, err)
	}
}
