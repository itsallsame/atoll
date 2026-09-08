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
		Capability: "http.fetch", Origin: "https://recovery-offer.example.com", OfferedAt: offerAt,
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
	secondOffer, err := repository.OfferListingExecution(ctx, ListingOfferRequest{
		AttemptID: "recovery-offer-attempt-2", ExecutorActorID: "recovery-offer-executor", ExecutorIncarnation: "boot-2",
		Capability: "http.fetch", Origin: "https://recovery-offer.example.com", OfferedAt: offerAt,
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
