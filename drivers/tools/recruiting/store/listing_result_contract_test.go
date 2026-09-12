package store

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestListingPageAndCompletionAcceptanceAreAtomicAndReplayable(t *testing.T) {
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
	offerAt, _ := prepareListingExecutionWork(t, ctx, repository, "listing-result", 1)
	offer := startListingAttempt(t, ctx, repository, "listing-result", offerAt)

	pageArtifact := mustResultArtifact(t, "listing-result-page", model.ArtifactPage, offer.Work.WorkID, offer.Attempt.AttemptID)
	page := ListingPageResult{
		CommandID: "listing-result-page-command", RequestHash: "sha256:listing-result-page",
		AttemptID: offer.Attempt.AttemptID, ExecutorActorID: "listing-result-executor", ExecutorIncarnation: "listing-result-boot",
		PageSequence: 1, Terminal: true, Artifact: pageArtifact, ObservedAt: offerAt,
		Observations: []model.ListingObservation{{
			ObservationID: "listing-result-observation", OccurrenceID: offer.Occurrence.OccurrenceID,
			SourceID: offer.Occurrence.SourceID, SourceJobKey: "external-job-1",
			DetailURL: "https://listing-result.example.com/jobs/1", ActivityAt: offerAt.Format(time.RFC3339),
			ListingFingerprint: "sha256:listing-1", RecipeID: offer.Attempt.RecipeID,
			RecipeVersion: offer.Attempt.RecipeVersion, ArtifactID: pageArtifact.ArtifactID,
		}},
	}
	type pageCall struct {
		outcome ListingPageOutcome
		err     error
	}
	pageCalls := make(chan pageCall, 2)
	detailTargets := []ExecutionDispatchTarget{
		{ActorID: "tool:listing-result-detail-a", Capability: "http.fetch"},
		{ActorID: "tool:listing-result-detail-b", Capability: "http.fetch"},
	}
	for range 2 {
		go func() {
			outcome, err := repository.AcceptListingPageWithDispatchTargets(ctx, page, detailTargets)
			pageCalls <- pageCall{outcome: outcome, err: err}
		}()
	}
	firstCall, secondCall := <-pageCalls, <-pageCalls
	if firstCall.err != nil || secondCall.err != nil || firstCall.outcome.Replayed == secondCall.outcome.Replayed {
		t.Fatalf("concurrent listing page calls = %+v %+v", firstCall, secondCall)
	}
	acceptedPage, replayedPage := firstCall.outcome, secondCall.outcome
	if acceptedPage.Replayed {
		acceptedPage, replayedPage = replayedPage, acceptedPage
	}
	if len(acceptedPage.Items) != 1 || acceptedPage.Items[0].DetailWork == nil || !acceptedPage.Progress.EndOfInput ||
		!reflect.DeepEqual(replayedPage.Progress, acceptedPage.Progress) || !reflect.DeepEqual(replayedPage.Items, acceptedPage.Items) {
		t.Fatalf("accepted/replayed listing page = %+v %+v", acceptedPage, replayedPage)
	}
	// Page progress is deliberately the high-frequency audit layer: its
	// immutable Artifact, progress row, observations and command receipt stay
	// queryable in MySQL, but it must not amplify every page into the Channel
	// ledger. The terminal completion below is the collaborative event.
	var pageEvents, detailDispatches int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_event_outbox
WHERE cause_command_id = ?`, page.CommandID).Scan(&pageEvents); err != nil {
		t.Fatal(err)
	}
	if pageEvents != 0 {
		t.Fatalf("high-frequency listing page created %d ledger event intents", pageEvents)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox
WHERE cause_kind = 'work_materialized' AND capability = 'http.fetch'
  AND target_actor_id IN ('tool:listing-result-detail-a', 'tool:listing-result-detail-b')`).Scan(&detailDispatches); err != nil {
		t.Fatal(err)
	}
	if detailDispatches != 1 {
		t.Fatalf("one Detail Work produced %d compact capability dispatches", detailDispatches)
	}
	conflictingPage := page
	conflictingPage.RequestHash = "sha256:listing-result-page-conflict"
	if _, err := repository.AcceptListingPage(ctx, conflictingPage); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("listing page command reuse = %v", err)
	}
	detailRecord, err := repository.GetWorkRecord(ctx, acceptedPage.Items[0].DetailWork.WorkID)
	if err != nil || detailRecord.Placement.Capability != "http.fetch" || detailRecord.Placement.Origin != "https://listing-result.example.com" {
		t.Fatalf("detail work placement was not derived from control-plane recipe and URL: %+v err=%v", detailRecord, err)
	}

	candidate := *offer.Checkpoint
	candidate.FrontierActivityAt = offerAt.Format(time.RFC3339)
	candidate.LastOccurrenceID = offer.Occurrence.OccurrenceID
	proof := model.ListingProgress{
		IdentityComplete: true, PaginationStable: true, PreviousFrontierReached: true, OverlapCompleted: true, OrderingContractHeld: true,
		SameTimeGroupCompleted: true, Candidate: candidate,
	}
	incompleteProof := proof
	incompleteProof.PaginationStable = false
	rejectedCompletionArtifact := mustResultArtifact(t, "listing-result-rejected-delta", model.ArtifactListingDelta, offer.Work.WorkID, offer.Attempt.AttemptID)
	if _, err := repository.AcceptListingCompletion(ctx, ListingCompletion{
		AttemptID: offer.Attempt.AttemptID, ExecutorActorID: "listing-result-executor", ExecutorIncarnation: "listing-result-boot",
		Artifact: rejectedCompletionArtifact, Progress: incompleteProof, CompletedAt: offerAt.Add(time.Second),
		CauseCommandID: "listing-result-incomplete-command", RequestHash: "sha256:listing-result-incomplete", ItemCount: 1,
	}); !errors.Is(err, ErrResultFenced) {
		t.Fatalf("incomplete quality proof advanced listing: %v", err)
	}
	checkpointAfterRejection, _ := repository.GetCheckpoint(ctx, offer.Occurrence.SourceID)
	workAfterRejection, _ := repository.GetWork(ctx, offer.Work.WorkID)
	if checkpointAfterRejection.Version != offer.Checkpoint.Version || workAfterRejection.Status != model.WorkRunning {
		t.Fatalf("incomplete proof changed checkpoint/work: checkpoint=%d work=%s", checkpointAfterRejection.Version, workAfterRejection.Status)
	}
	completionArtifact := mustResultArtifact(t, "listing-result-delta", model.ArtifactListingDelta, offer.Work.WorkID, offer.Attempt.AttemptID)
	completion := ListingCompletion{
		AttemptID: offer.Attempt.AttemptID, ExecutorActorID: "listing-result-executor", ExecutorIncarnation: "listing-result-boot",
		Artifact: completionArtifact, Progress: proof, ItemCount: 1, CompletedAt: offerAt.Add(time.Second), CauseCommandID: "listing-result-command",
		RequestHash: "sha256:listing-result-completion",
	}
	completed, err := repository.AcceptListingCompletion(ctx, completion)
	if err != nil || completed.Replayed || completed.Checkpoint.Version != offer.Checkpoint.Version+1 ||
		completed.Work.Status != model.WorkCompleted || completed.Occurrence.Status != model.OccurrenceCompleted {
		t.Fatalf("listing completion = %+v err=%v", completed, err)
	}
	attempt, _ := repository.GetAttempt(ctx, offer.Attempt.AttemptID)
	if attempt.Status != model.AttemptSucceeded {
		t.Fatalf("attempt was not completed: %+v", attempt)
	}
	laterCandidate := completed.Checkpoint
	laterCandidate.FrontierActivityAt = offerAt.Add(time.Hour).Format(time.RFC3339)
	laterCandidate.LastOccurrenceID = "later-occurrence"
	laterCheckpoint, err := completed.Checkpoint.Commit(completed.Checkpoint.Version, model.ListingProgress{
		IdentityComplete: true, PaginationStable: true, PreviousFrontierReached: true, OverlapCompleted: true,
		OrderingContractHeld: true, SameTimeGroupCompleted: true, Candidate: laterCandidate,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CommitCheckpointCAS(ctx, completed.Checkpoint.Version, laterCheckpoint, offerAt.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	replayedCompletion, err := repository.AcceptListingCompletion(ctx, completion)
	if err != nil || !replayedCompletion.Replayed || !reflect.DeepEqual(replayedCompletion.Checkpoint, completed.Checkpoint) {
		t.Fatalf("listing completion replay = %+v err=%v", replayedCompletion, err)
	}
	conflictingCompletion := completion
	conflictingCompletion.RequestHash = "sha256:listing-result-completion-conflict"
	if _, err := repository.AcceptListingCompletion(ctx, conflictingCompletion); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("listing completion command reuse = %v", err)
	}
	var acceptedArtifacts, rejectedArtifacts, observations, details, events, completionEvents, receipts, dispatches int
	queries := []struct {
		query string
		args  []any
		out   *int
	}{
		{"SELECT COUNT(*) FROM recruiting_artifacts WHERE attempt_id = ? AND rejected = FALSE", []any{offer.Attempt.AttemptID}, &acceptedArtifacts},
		{"SELECT COUNT(*) FROM recruiting_artifacts WHERE attempt_id = ? AND rejected = TRUE", []any{offer.Attempt.AttemptID}, &rejectedArtifacts},
		{"SELECT COUNT(*) FROM recruiting_listing_observations WHERE occurrence_id = ?", []any{offer.Occurrence.OccurrenceID}, &observations},
		{"SELECT COUNT(*) FROM recruiting_works WHERE parent_work_id = ? AND purpose = 'detail_sync' AND target_id = ?", []any{offer.Work.WorkID, acceptedPage.Items[0].Job.JobID}, &details},
		{"SELECT COUNT(*) FROM recruiting_event_outbox WHERE event_id = ?", []any{"listing-completed-" + offer.Attempt.AttemptID}, &events},
		{"SELECT COUNT(*) FROM recruiting_event_outbox WHERE cause_command_id = ?", []any{completion.CauseCommandID}, &completionEvents},
		{"SELECT COUNT(*) FROM recruiting_command_receipts WHERE command_id IN (?, ?)", []any{page.CommandID, completion.CauseCommandID}, &receipts},
		{"SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox WHERE target_actor_id = ? AND cause_id = ?", []any{offer.Attempt.ExecutorActorID, completion.CauseCommandID}, &dispatches},
	}
	for _, query := range queries {
		if err := db.QueryRowContext(ctx, query.query, query.args...).Scan(query.out); err != nil {
			t.Fatal(err)
		}
	}
	if acceptedArtifacts != 2 || rejectedArtifacts != 1 || observations != 1 || details != 1 || events != 1 ||
		completionEvents != 1 || receipts != 2 || dispatches != 1 {
		t.Fatalf("accepted facts artifacts=%d rejected=%d observations=%d detail_works=%d events=%d completion_events=%d receipts=%d dispatches=%d",
			acceptedArtifacts, rejectedArtifacts, observations, details, events, completionEvents, receipts, dispatches)
	}
}

func TestListingRetryStartsAtPageOneAndCompletesFromCurrentAttemptOnly(t *testing.T) {
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
	offerAt, _ := prepareListingExecutionWork(t, ctx, repository, "listing-retry-pages", 1)
	first := startListingAttempt(t, ctx, repository, "listing-retry-pages", offerAt)

	firstArtifact := mustResultArtifact(t, "listing-retry-pages-a-page", model.ArtifactPage, first.Work.WorkID, first.Attempt.AttemptID)
	firstPage := ListingPageResult{
		AttemptID: first.Attempt.AttemptID, ExecutorActorID: "listing-retry-pages-executor", ExecutorIncarnation: "listing-retry-pages-boot",
		PageSequence: 1, ResumeCursor: "page-2", Artifact: firstArtifact, ObservedAt: offerAt,
		Observations: []model.ListingObservation{{
			ObservationID: "listing-retry-pages-a-observation", OccurrenceID: first.Occurrence.OccurrenceID,
			SourceID: first.Occurrence.SourceID, SourceJobKey: "old-attempt-job",
			DetailURL: "https://listing-retry-pages.example.com/jobs/old", ActivityAt: offerAt.Format(time.RFC3339),
			ListingFingerprint: "sha256:old-attempt", RecipeID: first.Attempt.RecipeID,
			RecipeVersion: first.Attempt.RecipeVersion, ArtifactID: firstArtifact.ArtifactID,
		}},
	}
	if page, err := repository.AcceptListingPage(ctx, firstPage); err != nil || page.Progress.PageSequence != 1 {
		t.Fatalf("first attempt page = %+v err=%v", page, err)
	}
	if _, err := repository.FailListingExecution(ctx, first.Attempt.AttemptID, "listing-retry-pages-executor", "listing-retry-pages-boot", "executor_crash", offerAt.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	second, err := repository.OfferListingExecution(ctx, ListingOfferRequest{
		AttemptID: "listing-retry-pages-attempt-b", ExecutorActorID: "listing-retry-pages-executor-b", ExecutorIncarnation: "listing-retry-pages-boot-b",
		Capability: "http.fetch", Origin: "https://listing-retry-pages.example.com", OfferedAt: offerAt.Add(2 * time.Second), BudgetPolicy: testExecutionBudgetPolicy(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcceptListingExecution(ctx, second.Attempt.AttemptID, second.Attempt.ExecutorActorID, second.Attempt.ExecutorIncarnation, offerAt.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, second.Attempt.AttemptID, second.Attempt.ExecutorActorID, second.Attempt.ExecutorIncarnation, offerAt.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	secondArtifact := mustResultArtifact(t, "listing-retry-pages-b-page", model.ArtifactPage, second.Work.WorkID, second.Attempt.AttemptID)
	secondPage, err := repository.AcceptListingPage(ctx, ListingPageResult{
		AttemptID: second.Attempt.AttemptID, ExecutorActorID: second.Attempt.ExecutorActorID, ExecutorIncarnation: second.Attempt.ExecutorIncarnation,
		PageSequence: 1, Terminal: true, Artifact: secondArtifact, ObservedAt: offerAt.Add(3 * time.Second),
	})
	if err != nil || secondPage.Progress.PageSequence != 1 || secondPage.Progress.AttemptID != second.Attempt.AttemptID {
		t.Fatalf("retry page one = %+v err=%v", secondPage, err)
	}
	candidate := *second.Checkpoint
	candidate.FrontierActivityAt = offerAt.Format(time.RFC3339)
	proof := model.ListingProgress{
		IdentityComplete: true, PaginationStable: true, PreviousFrontierReached: true, OverlapCompleted: true,
		OrderingContractHeld: true, SameTimeGroupCompleted: true, Candidate: candidate,
	}
	completionArtifact := mustResultArtifact(t, "listing-retry-pages-b-delta", model.ArtifactListingDelta, second.Work.WorkID, second.Attempt.AttemptID)
	completed, err := repository.AcceptListingCompletion(ctx, ListingCompletion{
		AttemptID: second.Attempt.AttemptID, ExecutorActorID: second.Attempt.ExecutorActorID, ExecutorIncarnation: second.Attempt.ExecutorIncarnation,
		Artifact: completionArtifact, Progress: proof, ItemCount: 0, CompletedAt: offerAt.Add(4 * time.Second), CauseCommandID: "listing-retry-pages-complete",
		RequestHash: "sha256:listing-retry-pages-complete",
	})
	if err != nil || completed.Work.Status != model.WorkCompleted {
		t.Fatalf("retry completion = %+v err=%v", completed, err)
	}
	var firstPages, secondPages int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_listing_page_progress WHERE attempt_id = ?", first.Attempt.AttemptID).Scan(&firstPages); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_listing_page_progress WHERE attempt_id = ?", second.Attempt.AttemptID).Scan(&secondPages); err != nil {
		t.Fatal(err)
	}
	if firstPages != 1 || secondPages != 1 {
		t.Fatalf("attempt evidence was not isolated: first=%d second=%d", firstPages, secondPages)
	}
}

func TestStaleListingResultOnlyRetainsRejectedArtifact(t *testing.T) {
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
	offerAt, _ := prepareListingExecutionWork(t, ctx, repository, "listing-stale", 1)
	offer := startListingAttempt(t, ctx, repository, "listing-stale", offerAt)
	source, _ := repository.GetSource(ctx, offer.Occurrence.SourceID)
	reconfigured, _ := source.StageEndpoint(source.Version, "https://listing-stale.example.com/jobs-v2", "")
	if err := repository.UpdateSourceCAS(ctx, source.Version, reconfigured, offerAt); err != nil {
		t.Fatal(err)
	}
	artifact := mustResultArtifact(t, "listing-stale-page", model.ArtifactPage, offer.Work.WorkID, offer.Attempt.AttemptID)
	page := ListingPageResult{
		AttemptID: offer.Attempt.AttemptID, ExecutorActorID: "listing-stale-executor", ExecutorIncarnation: "listing-stale-boot",
		PageSequence: 1, Terminal: true, Artifact: artifact, ObservedAt: offerAt,
	}
	if _, err := repository.AcceptListingPage(ctx, page); !errors.Is(err, ErrResultFenced) {
		t.Fatalf("stale listing page was not fenced: %v", err)
	}
	var rejected, progress int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_artifacts WHERE artifact_id = ? AND rejected = TRUE", artifact.ArtifactID).Scan(&rejected); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_listing_page_progress WHERE work_id = ?", offer.Work.WorkID).Scan(&progress); err != nil {
		t.Fatal(err)
	}
	work, _ := repository.GetWork(ctx, offer.Work.WorkID)
	attempt, _ := repository.GetAttempt(ctx, offer.Attempt.AttemptID)
	checkpoint, _ := repository.GetCheckpoint(ctx, offer.Occurrence.SourceID)
	if rejected != 1 || progress != 0 || work.Status != model.WorkRunning || attempt.Status != model.AttemptRunning || checkpoint.Version != offer.Checkpoint.Version {
		t.Fatalf("stale result changed business facts: rejected=%d progress=%d work=%s attempt=%s checkpoint=%d",
			rejected, progress, work.Status, attempt.Status, checkpoint.Version)
	}
	if _, err := repository.FailListingExecution(ctx, offer.Attempt.AttemptID, "listing-stale-executor", "listing-stale-boot", "fixture_cleanup", offerAt); err != nil {
		t.Fatal(err)
	}
}

func TestDailyCloseFencesNewListingPages(t *testing.T) {
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
	offerAt, _ := prepareListingExecutionWork(t, ctx, repository, "listing-window-closed", 1)
	offer := startListingAttempt(t, ctx, repository, "listing-window-closed", offerAt)
	if _, err := repository.CloseDailyRunAtWindow(ctx, "listing-window-closed-daily", offerAt.Add(2*time.Hour), "window-close"); err != nil {
		t.Fatal(err)
	}

	artifact := mustResultArtifact(t, "listing-window-closed-page", model.ArtifactPage, offer.Work.WorkID, offer.Attempt.AttemptID)
	page := ListingPageResult{
		AttemptID: offer.Attempt.AttemptID, ExecutorActorID: "listing-window-closed-executor",
		ExecutorIncarnation: "listing-window-closed-boot", PageSequence: 1, Terminal: true,
		Artifact: artifact, ObservedAt: offerAt.Add(2*time.Hour + time.Second),
	}
	if _, err := repository.AcceptListingPage(ctx, page); !errors.Is(err, ErrResultFenced) {
		t.Fatalf("post-window listing page was not fenced: %v", err)
	}
	work, _ := repository.GetWork(ctx, offer.Work.WorkID)
	occurrence, _ := repository.GetOccurrence(ctx, offer.Occurrence.OccurrenceID)
	var rejected, progress int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_artifacts WHERE artifact_id = ? AND rejected = TRUE", artifact.ArtifactID).Scan(&rejected)
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_listing_page_progress WHERE work_id = ?", offer.Work.WorkID).Scan(&progress)
	if work.Status != model.WorkCanceled || occurrence.Status != model.OccurrenceException || rejected != 1 || progress != 0 {
		t.Fatalf("post-window facts work=%s occurrence=%s rejected=%d progress=%d", work.Status, occurrence.Status, rejected, progress)
	}
}

func startListingAttempt(t *testing.T, ctx context.Context, repository *Repository, prefix string, at time.Time) ListingExecutionOffer {
	t.Helper()
	executorID := prefix + "-executor"
	incarnation := prefix + "-boot"
	offer, err := repository.OfferListingExecution(ctx, ListingOfferRequest{
		AttemptID: prefix + "-attempt", ExecutorActorID: executorID, ExecutorIncarnation: incarnation,
		Capability: "http.fetch", Origin: "https://" + prefix + ".example.com", OfferedAt: at, BudgetPolicy: testExecutionBudgetPolicy(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if offer.Checkpoint == nil {
		t.Fatal("daily listing offer omitted established checkpoint")
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, executorID, incarnation, at); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, executorID, incarnation, at); err != nil {
		t.Fatal(err)
	}
	return offer
}

func mustResultArtifact(t *testing.T, id string, kind model.ArtifactKind, workID, attemptID string) model.ArtifactMetadata {
	t.Helper()
	artifact, err := model.NewArtifactMetadata(id, kind, "sha256:"+fmt.Sprintf("%064d", len(id)), "artifact://"+id,
		workID, attemptID, "recruiting-operators", "30d", true)
	if err != nil {
		t.Fatal(err)
	}
	return artifact
}
