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

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func testExecutionBudgetPolicy() ExecutionBudgetPolicy {
	policy := DefaultExecutionBudgetPolicy()
	policy.MaxActive, policy.MaxPerCapability = 100, 100
	policy.MaxPerOrigin, policy.MaxPerCompany, policy.MaxPerProfile = 100, 100, 100
	policy.MaxBaselineActive, policy.MaxCalibrationActive, policy.MaxBackfillActive = 100, 100, 100
	return policy
}

func testExecutionFailurePolicy() ExecutionFailurePolicy {
	return ExecutionFailurePolicy{Version: 7, MaxAutomaticAttempts: 4, BaseDelay: 30 * time.Second,
		MaxDelay: 10 * time.Minute, ThrottledDelay: 5 * time.Minute}
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
	failureArtifact := mustResultArtifact(t, "execution-life-failure", model.ArtifactFailure, offer.Work.WorkID, offer.Attempt.AttemptID)
	failed, err := repository.FailExecutionWithReport(ctx, offer.Attempt.AttemptID, "executor-a", "boot-a", "transport_timeout",
		executioncontract.FailureReport{Class: "transport_timeout", Retryable: true, Artifact: failureArtifact},
		testExecutionFailurePolicy(), offerAt.Add(2*time.Second))
	if err != nil || failed.Status != model.AttemptFailed {
		t.Fatalf("failed attempt = %+v err=%v", failed, err)
	}
	work, _ = repository.GetWork(ctx, workID)
	if work.Status != model.WorkWaitingRetry || work.WaitingReason != "transport_timeout" || work.RetryPolicyVersion != 7 ||
		work.AutomaticAttempts != 1 || work.RetryNotBefore != offerAt.Add(32*time.Second).Format(time.RFC3339Nano) {
		t.Fatalf("failed execution did not become retryable: %+v", work)
	}
	var failureArtifacts int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_artifacts WHERE artifact_id = ? AND rejected = FALSE", failureArtifact.ArtifactID).Scan(&failureArtifacts); err != nil || failureArtifacts != 1 {
		t.Fatalf("failure Artifact metadata was not committed atomically: count=%d err=%v", failureArtifacts, err)
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
		Capability: "http.fetch", Origin: "https://execution-life.example.com", OfferedAt: offerAt.Add(32 * time.Second), BudgetPolicy: testExecutionBudgetPolicy(),
	})
	if err != nil || retry.Work.WorkID != workID || retry.Attempt.AcceptanceVersion != work.AcceptanceVersion {
		t.Fatalf("retry offer = %+v err=%v", retry, err)
	}
	source, err := repository.GetSource(ctx, offer.Occurrence.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	reconfigured, err := source.StageEndpoint(source.Version, "https://execution-life.example.com/jobs-v2", "")
	if err == nil {
		err = repository.UpdateSourceCAS(ctx, source.Version, reconfigured, offerAt)
	}
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcceptListingExecution(ctx, retry.Attempt.AttemptID, "executor-b", "boot-b", offerAt); !errors.Is(err, ErrResultFenced) {
		t.Fatalf("changed source configuration fence accepted retry attempt: %v", err)
	}
	expiredRetry, _ := retry.Attempt.Expire()
	if err := repository.UpdateAttemptCAS(ctx, retry.Attempt.Status, expiredRetry, offerAt); err != nil {
		t.Fatal(err)
	}
}

func TestExecutionTransitionCommandsReplayAtomically(t *testing.T) {
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
	offerAt, _ := prepareListingExecutionWork(t, ctx, repository, "execution-command", 1)
	offer, err := repository.OfferListingExecution(ctx, ListingOfferRequest{
		AttemptID: "execution-command-attempt", ExecutorActorID: "execution-command-executor", ExecutorIncarnation: "execution-command-boot",
		Capability: "http.fetch", Origin: "https://execution-command.example.com", OfferedAt: offerAt, BudgetPolicy: testExecutionBudgetPolicy(),
	})
	if err != nil {
		t.Fatal(err)
	}
	command := ExecutionTransitionCommand{CommandID: "execution-command-accept", Word: executioncontract.TypeAccept,
		RequestHash: "sha256:accept", CorrelationID: "correlation-accept", RequestedBy: offer.Attempt.ExecutorActorID,
		AttemptID: offer.Attempt.AttemptID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, Action: "accept"}
	results := make(chan CommandResult, 2)
	errorsFound := make(chan error, 2)
	for range 2 {
		go func() {
			result, applyErr := repository.ApplyExecutionTransitionCommand(ctx, command, offerAt)
			results <- result
			errorsFound <- applyErr
		}()
	}
	accepted := <-results
	second := <-results
	for range 2 {
		if applyErr := <-errorsFound; applyErr != nil {
			t.Fatal(applyErr)
		}
	}
	if !json.Valid(accepted.Response) || !json.Valid(second.Response) || !reflect.DeepEqual(accepted.Response, second.Response) || accepted.Replayed == second.Replayed {
		t.Fatalf("concurrent accept commands = %+v / %+v", accepted, second)
	}
	replay, err := repository.ApplyExecutionTransitionCommand(ctx, command, offerAt.Add(time.Second))
	if err != nil || !replay.Replayed || !reflect.DeepEqual(replay.Response, accepted.Response) {
		t.Fatalf("accept command replay = %+v err=%v", replay, err)
	}
	conflict := command
	conflict.RequestHash = "sha256:different"
	if _, err := repository.ApplyExecutionTransitionCommand(ctx, conflict, offerAt); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("changed execution command replay = %v", err)
	}
	command = ExecutionTransitionCommand{CommandID: "execution-command-start", Word: executioncontract.TypeStarted,
		RequestHash: "sha256:start", CorrelationID: "correlation-start", RequestedBy: offer.Attempt.ExecutorActorID,
		AttemptID: offer.Attempt.AttemptID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, Action: "start"}
	if _, err := repository.ApplyExecutionTransitionCommand(ctx, command, offerAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	failureArtifact := mustResultArtifact(t, "execution-command-failure", model.ArtifactFailure, offer.Work.WorkID, offer.Attempt.AttemptID)
	responseArtifact := mustResultArtifact(t, "execution-command-response", model.ArtifactResponse, offer.Work.WorkID, offer.Attempt.AttemptID)
	report := executioncontract.FailureReport{Class: "transport_timeout", Retryable: true, Artifact: failureArtifact,
		Artifacts: []model.ArtifactMetadata{responseArtifact, failureArtifact}}
	command = ExecutionTransitionCommand{CommandID: "execution-command-fail", Word: executioncontract.TypeFailed,
		RequestHash: "sha256:fail", CorrelationID: "correlation-fail", RequestedBy: offer.Attempt.ExecutorActorID,
		AttemptID: offer.Attempt.AttemptID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, Action: "fail",
		Reason: report.Class, Failure: &report, FailurePolicy: testExecutionFailurePolicy()}
	if _, err := repository.ApplyExecutionTransitionCommand(ctx, command, offerAt.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	var receipts, artifacts, events, dispatches int
	var observedPhases int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_command_receipts WHERE command_id LIKE 'execution-command-%'").Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_artifacts WHERE artifact_id IN (?, ?)",
		responseArtifact.ArtifactID, failureArtifact.ArtifactID).Scan(&artifacts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_event_outbox WHERE event_id = ? AND event_kind = 'work.retry_scheduled'",
		"execution-failed-"+offer.Attempt.AttemptID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox WHERE target_actor_id = ? AND cause_id = ?",
		offer.Attempt.ExecutorActorID, command.CommandID).Scan(&dispatches); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_attempts
WHERE attempt_id = ? AND offered_observed_at IS NOT NULL AND accepted_observed_at IS NOT NULL
  AND started_observed_at IS NOT NULL AND terminal_observed_at IS NOT NULL
  AND offered_observed_at <= accepted_observed_at AND accepted_observed_at <= started_observed_at
  AND started_observed_at <= terminal_observed_at`, offer.Attempt.AttemptID).Scan(&observedPhases); err != nil {
		t.Fatal(err)
	}
	storedAttempt, _ := repository.GetAttempt(ctx, offer.Attempt.AttemptID)
	work, _ := repository.GetWork(ctx, offer.Work.WorkID)
	if receipts != 3 || artifacts != 2 || events != 1 || dispatches != 2 || observedPhases != 1 ||
		storedAttempt.Status != model.AttemptFailed || work.Status != model.WorkWaitingRetry {
		t.Fatalf("execution command facts receipts=%d artifacts=%d events=%d dispatches=%d observed=%d attempt=%s work=%s",
			receipts, artifacts, events, dispatches, observedPhases, storedAttempt.Status, work.Status)
	}
}

func TestExecutionClaimCommandAtomicallyAcceptsAndStarts(t *testing.T) {
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
	offerAt, _ := prepareListingExecutionWork(t, ctx, repository, "execution-claim-command", 1)
	offer, err := repository.OfferListingExecution(ctx, ListingOfferRequest{
		AttemptID: "execution-claim-attempt", ExecutorActorID: "execution-claim-executor", ExecutorIncarnation: "execution-claim-boot",
		Capability: "http.fetch", Origin: "https://execution-claim-command.example.com", SupplyBatchID: "execution-supply-1",
		OfferedAt: offerAt, BudgetPolicy: testExecutionBudgetPolicy(),
	})
	if err != nil {
		t.Fatal(err)
	}
	changedBatchRequest := ListingOfferRequest{
		AttemptID: offer.Attempt.AttemptID, ExecutorActorID: offer.Attempt.ExecutorActorID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
		Capability: offer.Attempt.Capability, Origin: "https://execution-claim-command.example.com", SupplyBatchID: "execution-supply-other",
		OfferedAt: offerAt, BudgetPolicy: testExecutionBudgetPolicy(),
	}
	if _, err := repository.OfferListingExecution(ctx, changedBatchRequest); !errors.Is(err, ErrAttemptConflict) {
		t.Fatalf("changed supply batch replay = %v", err)
	}
	command := ExecutionTransitionCommand{CommandID: "execution-command-claim", Word: executioncontract.TypeClaimBatch,
		RequestHash: "sha256:claim", CorrelationID: "correlation-claim", RequestedBy: offer.Attempt.ExecutorActorID,
		AttemptID: offer.Attempt.AttemptID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, Action: "claim"}
	first, err := repository.ApplyExecutionTransitionCommand(ctx, command, offerAt.Add(time.Second))
	if err != nil || first.Replayed {
		t.Fatalf("claim command: result=%+v err=%v", first, err)
	}
	replay, err := repository.ApplyExecutionTransitionCommand(ctx, command, offerAt.Add(2*time.Second))
	if err != nil || !replay.Replayed || !reflect.DeepEqual(first.Response, replay.Response) {
		t.Fatalf("claim replay: first=%+v replay=%+v err=%v", first, replay, err)
	}
	storedAttempt, err := repository.GetAttempt(ctx, offer.Attempt.AttemptID)
	if err != nil {
		t.Fatal(err)
	}
	work, err := repository.GetWork(ctx, offer.Work.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	var receipts, observed int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_command_receipts WHERE command_id = ?", command.CommandID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_attempts
WHERE attempt_id = ? AND accepted_observed_at IS NOT NULL AND started_observed_at IS NOT NULL
  AND accepted_observed_at <= started_observed_at`, offer.Attempt.AttemptID).Scan(&observed); err != nil {
		t.Fatal(err)
	}
	if storedAttempt.Status != model.AttemptRunning || work.Status != model.WorkRunning || receipts != 1 || observed != 1 {
		t.Fatalf("claim facts attempt=%s work=%s receipts=%d observed=%d", storedAttempt.Status, work.Status, receipts, observed)
	}
}

func TestAttemptSupplyBatchMembersVerifyAsOneBoundedEnvelope(t *testing.T) {
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
	offerAt, _ := prepareListingExecutionWork(t, ctx, repository, "execution-supply-members", 2)
	const supplyBatchID = "execution-supply-members-batch"
	attemptIDs := make([]string, 0, 2)
	for index := 0; index < 2; index++ {
		offer, err := repository.OfferListingExecution(ctx, ListingOfferRequest{
			AttemptID:       fmt.Sprintf("execution-supply-members-attempt-%d", index),
			ExecutorActorID: "execution-supply-members-executor", ExecutorIncarnation: "execution-supply-members-boot",
			Capability: "http.fetch", OfferedAt: offerAt, BudgetPolicy: testExecutionBudgetPolicy(), SupplyBatchID: supplyBatchID,
		})
		if err != nil {
			t.Fatal(err)
		}
		attemptIDs = append(attemptIDs, offer.Attempt.AttemptID)
	}
	if err := repository.VerifyAttemptSupplyBatchMembers(ctx, attemptIDs, supplyBatchID,
		"execution-supply-members-executor", "execution-supply-members-boot"); err != nil {
		t.Fatalf("verify complete supply batch: %v", err)
	}
	if err := repository.VerifyAttemptSupplyBatchMembers(ctx, append(attemptIDs, "missing-attempt"), supplyBatchID,
		"execution-supply-members-executor", "execution-supply-members-boot"); !errors.Is(err, ErrAttemptConflict) {
		t.Fatalf("verify batch with missing Attempt = %v", err)
	}
	if err := repository.VerifyAttemptSupplyBatchMembers(ctx, []string{attemptIDs[0], attemptIDs[0]}, supplyBatchID,
		"execution-supply-members-executor", "execution-supply-members-boot"); err == nil {
		t.Fatal("duplicate Attempt IDs were accepted")
	}
}

func TestClassifiedFailureBackoffIsBoundedAndRepairStopsAutomaticOffers(t *testing.T) {
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
	policy := testExecutionFailurePolicy()
	policy.MaxAutomaticAttempts = 3

	offerAt, _ := prepareListingExecutionWork(t, ctx, repository, "execution-backoff", 1)
	nextAt := offerAt
	for number := 1; number <= 3; number++ {
		executorID := fmt.Sprintf("backoff-executor-%d", number)
		incarnation := fmt.Sprintf("backoff-boot-%d", number)
		offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: fmt.Sprintf("backoff-attempt-%d", number),
			ExecutorActorID: executorID, ExecutorIncarnation: incarnation, Capability: "http.fetch",
			Origin: "https://execution-backoff.example.com", OfferedAt: nextAt, BudgetPolicy: testExecutionBudgetPolicy()})
		if err != nil {
			t.Fatalf("offer %d: %v", number, err)
		}
		if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, executorID, incarnation, nextAt); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, executorID, incarnation, nextAt); err != nil {
			t.Fatal(err)
		}
		failedAt := nextAt.Add(time.Second)
		artifact := mustResultArtifact(t, fmt.Sprintf("backoff-failure-%d", number), model.ArtifactFailure, offer.Work.WorkID, offer.Attempt.AttemptID)
		if _, err := repository.FailExecutionWithReport(ctx, offer.Attempt.AttemptID, executorID, incarnation, "transport_timeout",
			executioncontract.FailureReport{Class: "transport_timeout", Retryable: true, Artifact: artifact}, policy, failedAt); err != nil {
			t.Fatal(err)
		}
		work, _ := repository.GetWork(ctx, offer.Work.WorkID)
		if work.AutomaticAttempts != uint64(number) || work.RetryPolicyVersion != policy.Version {
			t.Fatalf("failure %d audit = %+v", number, work)
		}
		if number == 3 {
			if work.Status != model.WorkWaitingHuman || work.RetryNotBefore != "" {
				t.Fatalf("exhausted failure did not stop = %+v", work)
			}
			if _, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "backoff-forbidden-attempt", ExecutorActorID: "executor-x",
				ExecutorIncarnation: "boot-x", Capability: "http.fetch", Origin: "https://execution-backoff.example.com",
				OfferedAt: failedAt.Add(24 * time.Hour), BudgetPolicy: testExecutionBudgetPolicy()}); !errors.Is(err, ErrNotFound) {
				t.Fatalf("waiting-human work was offered: %v", err)
			}
			break
		}
		retryAt, err := time.Parse(time.RFC3339Nano, work.RetryNotBefore)
		if err != nil || work.Status != model.WorkWaitingRetry {
			t.Fatalf("retry %d = %+v err=%v", number, work, err)
		}
		if _, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: fmt.Sprintf("backoff-early-%d", number), ExecutorActorID: "executor-early",
			ExecutorIncarnation: "boot-early", Capability: "http.fetch", Origin: "https://execution-backoff.example.com",
			OfferedAt: retryAt.Add(-time.Millisecond), BudgetPolicy: testExecutionBudgetPolicy()}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("retry %d was offered before not_before: %v", number, err)
		}
		nextAt = retryAt
	}

	repairAt, _ := prepareListingExecutionWork(t, ctx, repository, "execution-repair-stop", 1)
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "repair-stop-attempt", ExecutorActorID: "repair-executor",
		ExecutorIncarnation: "repair-boot", Capability: "http.fetch", Origin: "https://execution-repair-stop.example.com",
		OfferedAt: repairAt, BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, "repair-executor", "repair-boot", repairAt)
	_, _ = repository.StartListingExecution(ctx, offer.Attempt.AttemptID, "repair-executor", "repair-boot", repairAt)
	artifact := mustResultArtifact(t, "repair-stop-failure", model.ArtifactFailure, offer.Work.WorkID, offer.Attempt.AttemptID)
	report := executioncontract.FailureReport{Class: "parse_error", NeedsRepair: true, Artifact: artifact}
	if _, err := repository.ApplyExecutionTransitionCommand(ctx, ExecutionTransitionCommand{
		CommandID: "repair-stop-command", Word: executioncontract.TypeFailed, RequestHash: "sha256:repair-stop",
		CorrelationID: "correlation-repair-stop", RequestedBy: "repair-executor", AttemptID: offer.Attempt.AttemptID,
		ExecutorIncarnation: "repair-boot", Action: "fail", Reason: report.Class, Failure: &report, FailurePolicy: policy,
	}, repairAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	work, _ := repository.GetWork(ctx, offer.Work.WorkID)
	if work.Status != model.WorkWaitingHuman || work.WaitingReason != "parse_error" || work.AutomaticAttempts != 1 {
		t.Fatalf("repair failure routing = %+v", work)
	}
	var waitingHumanEvents int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_event_outbox WHERE event_id = ? AND event_kind = 'work.waiting_human'",
		"execution-failed-"+offer.Attempt.AttemptID).Scan(&waitingHumanEvents); err != nil || waitingHumanEvents != 1 {
		t.Fatalf("waiting-human event count=%d err=%v", waitingHumanEvents, err)
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
	// Materialization is intentionally a global due queue. The contract suite
	// shares one schema, so drain a bounded full batch and assert this run's
	// occurrences below instead of assuming they are the only due rows.
	materialized, err := repository.MaterializeDueOccurrenceWorks(ctx, offerAt, 500, "recruiting", prefix+"-materialize", offerAt)
	if err != nil {
		t.Fatalf("materialized=%+v err=%v", materialized, err)
	}
	page, _ = repository.ListOccurrences(ctx, plan.Run.DailyRunID, "", 10)
	for _, occurrence := range page.Items {
		if occurrence.WorkID == "" {
			t.Fatalf("target occurrence was not materialized: %+v (batch=%+v)", occurrence, materialized)
		}
	}
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
