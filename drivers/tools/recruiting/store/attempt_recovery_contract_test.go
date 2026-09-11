package store

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestStaleAttemptRecoveryReleasesOffersAndRetriesRunningWork(t *testing.T) {
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

	offerAt, _ := prepareListingExecutionWork(t, ctx, repository, "recovery-offer", 1)
	if _, err := repository.RecoverStaleAttempts(ctx, offerAt.Add(-time.Hour), 500, offerAt); err != nil {
		t.Fatal(err)
	}
	offer, err := repository.OfferListingExecution(ctx, ListingOfferRequest{
		AttemptID: "recovery-offer-attempt", ExecutorActorID: "recovery-offer-executor", ExecutorIncarnation: "boot-1",
		Capability: "http.fetch", Origin: "https://recovery-offer.example.com", OfferedAt: offerAt, BudgetPolicy: testExecutionBudgetPolicy(),
	})
	if err != nil {
		t.Fatal(err)
	}
	recoveredAt := offerAt.Add(2 * time.Hour)
	result, err := repository.RecoverStaleAttempts(ctx, offerAt.Add(time.Hour), 10, recoveredAt)
	if err != nil || result.Expired != 1 || result.RetryQueued != 0 {
		t.Fatalf("offered recovery = %+v err=%v", result, err)
	}
	attempt, _ := repository.GetAttempt(ctx, offer.Attempt.AttemptID)
	work, _ := repository.GetWork(ctx, offer.Work.WorkID)
	if attempt.Status != model.AttemptExpired || work.Status != model.WorkOpen {
		t.Fatalf("offered recovery attempt=%s work=%s", attempt.Status, work.Status)
	}
	var recoveredPermit model.BudgetPermitStatus
	if err := db.QueryRowContext(ctx, "SELECT permit_status FROM recruiting_budget_permits WHERE attempt_id = ?", offer.Attempt.AttemptID).Scan(&recoveredPermit); err != nil || recoveredPermit != model.PermitExpired {
		t.Fatalf("recovered offered permit=%s err=%v", recoveredPermit, err)
	}
	secondOffer, err := repository.OfferListingExecution(ctx, ListingOfferRequest{
		AttemptID: "recovery-offer-attempt-2", ExecutorActorID: "recovery-offer-executor", ExecutorIncarnation: "boot-2",
		Capability: "http.fetch", Origin: "https://recovery-offer.example.com", OfferedAt: offerAt, BudgetPolicy: testExecutionBudgetPolicy(),
	})
	if err != nil {
		t.Fatalf("expired offer did not release active slot: %v", err)
	}
	if _, err := repository.AcceptListingExecution(ctx, secondOffer.Attempt.AttemptID, "recovery-offer-executor", "boot-2", offerAt); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, secondOffer.Attempt.AttemptID, "recovery-offer-executor", "boot-2", offerAt); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.FailListingExecution(ctx, secondOffer.Attempt.AttemptID, "recovery-offer-executor", "boot-2", "fixture_cleanup", offerAt); err != nil {
		t.Fatal(err)
	}

	runningAt, _ := prepareListingExecutionWork(t, ctx, repository, "recovery-running", 1)
	runningOffer := startListingAttempt(t, ctx, repository, "recovery-running", runningAt)
	cutoff := runningAt.Add(time.Hour)
	recoveredAt = runningAt.Add(2 * time.Hour)
	results := make(chan AttemptRecoveryResult, 2)
	errorsFound := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			result, recoveryErr := repository.RecoverStaleAttempts(ctx, cutoff, 10, recoveredAt)
			results <- result
			errorsFound <- recoveryErr
		}()
	}
	group.Wait()
	close(results)
	close(errorsFound)
	for recoveryErr := range errorsFound {
		if recoveryErr != nil {
			t.Fatal(recoveryErr)
		}
	}
	expired, retryQueued := 0, 0
	for result := range results {
		expired += result.Expired
		retryQueued += result.RetryQueued
	}
	if expired != 1 || retryQueued != 1 {
		t.Fatalf("concurrent recovery expired=%d retry=%d", expired, retryQueued)
	}
	attempt, _ = repository.GetAttempt(ctx, runningOffer.Attempt.AttemptID)
	work, _ = repository.GetWork(ctx, runningOffer.Work.WorkID)
	occurrence, _ := repository.GetOccurrence(ctx, runningOffer.Occurrence.OccurrenceID)
	if attempt.Status != model.AttemptExpired || work.Status != model.WorkWaitingRetry ||
		work.WaitingReason != "executor_progress_timeout" || occurrence.Status != model.OccurrenceRunning {
		t.Fatalf("running recovery attempt=%+v work=%+v occurrence=%+v", attempt, work, occurrence)
	}
	var events int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_event_outbox WHERE event_id = ?", "attempt-expired-"+runningOffer.Attempt.AttemptID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if events != 1 {
		t.Fatalf("attempt expiry events=%d", events)
	}
	var explain string
	if err := db.QueryRowContext(ctx, `EXPLAIN FORMAT=JSON
SELECT attempt_id FROM recruiting_attempts
WHERE attempt_status IN ('offered', 'accepted', 'running') AND updated_at <= ?
ORDER BY updated_at, attempt_id LIMIT 100`, cutoff).Scan(&explain); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "ix_recruiting_attempt_stale") {
		t.Fatalf("stale attempt query did not use intended index: %s", explain)
	}
	if err := db.QueryRowContext(ctx, `EXPLAIN FORMAT=JSON
SELECT attempt_id FROM recruiting_budget_permits
WHERE permit_status = 'granted' AND expires_at <= ?
ORDER BY expires_at, attempt_id LIMIT 100`, recoveredAt).Scan(&explain); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "ix_recruiting_permit_expiry") {
		t.Fatalf("permit expiry query did not use intended index: %s", explain)
	}
	for _, sourceID := range []string{"recovery-offer-source-0", "recovery-running-source-0"} {
		source, err := repository.GetSource(ctx, sourceID)
		if err != nil {
			t.Fatal(err)
		}
		paused, err := source.Pause(source.Version, model.PauseDrain)
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.UpdateSourceCAS(ctx, source.Version, paused, recoveredAt); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExecutorIncarnationRecoveryIsActorAndBindTimeFenced(t *testing.T) {
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

	oldAt, _ := prepareListingExecutionWork(t, ctx, repository, "presence-old", 1)
	oldOffer := startListingAttempt(t, ctx, repository, "presence-old", oldAt)
	wake, _ := NewExecutionDispatchIntent("presence-old-recovery-wake", oldOffer.Attempt.ExecutorActorID,
		"http.fetch", "", "", "presence_test", oldOffer.Work.WorkID, oldAt.Add(4*time.Hour))
	if err := repository.EnqueueExecutionDispatch(ctx, wake, oldAt); err != nil {
		t.Fatal(err)
	}
	currentAt, _ := prepareListingExecutionWork(t, ctx, repository, "presence-current", 1)
	currentOffer := startListingAttempt(t, ctx, repository, "presence-current", currentAt)
	recoveredAt := laterTime(oldAt, currentAt).Add(time.Hour)
	result, err := repository.RecoverExecutorAttempts(ctx, oldOffer.Attempt.ExecutorActorID,
		oldAt.Add(time.Second), 1, recoveredAt, "executor_incarnation_replaced")
	if err != nil || result.Scanned != 1 || result.Expired != 1 || result.RetryQueued != 1 || result.HasMore {
		t.Fatalf("incarnation recovery=%+v err=%v", result, err)
	}
	oldAttempt, _ := repository.GetAttempt(ctx, oldOffer.Attempt.AttemptID)
	oldWork, _ := repository.GetWork(ctx, oldOffer.Work.WorkID)
	currentAttempt, _ := repository.GetAttempt(ctx, currentOffer.Attempt.AttemptID)
	currentWork, _ := repository.GetWork(ctx, currentOffer.Work.WorkID)
	if oldAttempt.Status != model.AttemptExpired || oldWork.Status != model.WorkWaitingRetry ||
		oldWork.WaitingReason != "executor_incarnation_replaced" || currentAttempt.Status != model.AttemptRunning ||
		currentWork.Status != model.WorkRunning {
		t.Fatalf("presence recovery old_attempt=%+v old_work=%+v current_attempt=%+v current_work=%+v",
			oldAttempt, oldWork, currentAttempt, currentWork)
	}
	var wakeDue time.Time
	var wakeReason string
	if err := db.QueryRowContext(ctx, `SELECT next_attempt_at, COALESCE(last_error_class, '')
FROM recruiting_execution_dispatch_outbox WHERE dispatch_id = ?`, wake.DispatchID).Scan(&wakeDue, &wakeReason); err != nil {
		t.Fatal(err)
	}
	if wakeDue.After(recoveredAt) || wakeReason != "executor_incarnation_replaced" {
		t.Fatalf("replacement did not accelerate durable wake: due=%s reason=%s", wakeDue, wakeReason)
	}
	if _, err := repository.RecoverExecutorAttempts(ctx, currentOffer.Attempt.ExecutorActorID,
		currentAt, 10, recoveredAt, "executor_incarnation_replaced"); err != nil {
		t.Fatal(err)
	}
	currentAttempt, _ = repository.GetAttempt(ctx, currentOffer.Attempt.AttemptID)
	if currentAttempt.Status != model.AttemptRunning {
		t.Fatal("attempt created at the current bind boundary was invalidated")
	}
	activeExecutors, hasMore, err := repository.ListActiveExecutorActors(ctx, 10_000)
	if err != nil || hasMore || !containsString(activeExecutors, currentOffer.Attempt.ExecutorActorID) ||
		containsString(activeExecutors, oldOffer.Attempt.ExecutorActorID) {
		t.Fatalf("active executor bootstrap=%v has_more=%v err=%v", activeExecutors, hasMore, err)
	}

	var explain string
	if err := db.QueryRowContext(ctx, `EXPLAIN FORMAT=JSON
SELECT attempt_id FROM recruiting_attempts FORCE INDEX (ix_recruiting_attempt_executor)
WHERE executor_actor_id = ? AND attempt_status IN ('offered', 'accepted', 'running') AND created_at < ?
ORDER BY created_at, attempt_id LIMIT 501`, oldOffer.Attempt.ExecutorActorID, recoveredAt).Scan(&explain); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "ix_recruiting_attempt_executor") {
		t.Fatalf("executor attempt recovery missed its bounded index: %s", explain)
	}
	for _, sourceID := range []string{"presence-old-source-0", "presence-current-source-0"} {
		source, err := repository.GetSource(ctx, sourceID)
		if err != nil {
			t.Fatal(err)
		}
		paused, err := source.Pause(source.Version, model.PauseDrain)
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.UpdateSourceCAS(ctx, source.Version, paused, recoveredAt); err != nil {
			t.Fatal(err)
		}
	}
}

func laterTime(left, right time.Time) time.Time {
	if left.After(right) {
		return left
	}
	return right
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
