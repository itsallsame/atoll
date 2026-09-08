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
	acceptedPage, err := repository.AcceptListingPage(ctx, page)
	if err != nil || acceptedPage.Replayed || len(acceptedPage.Items) != 1 || acceptedPage.Items[0].DetailWork == nil || !acceptedPage.Progress.EndOfInput {
		t.Fatalf("accepted listing page = %+v err=%v", acceptedPage, err)
	}
	replayedPage, err := repository.AcceptListingPage(ctx, page)
	if err != nil || !replayedPage.Replayed || !reflect.DeepEqual(replayedPage.Progress, acceptedPage.Progress) ||
		!reflect.DeepEqual(replayedPage.Items, acceptedPage.Items) {
		t.Fatalf("listing page replay = %+v err=%v", replayedPage, err)
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
		CauseCommandID: "listing-result-incomplete-command", ItemCount: 1,
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
	var acceptedArtifacts, rejectedArtifacts, observations, details, events int
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
	}
	for _, query := range queries {
		if err := db.QueryRowContext(ctx, query.query, query.args...).Scan(query.out); err != nil {
			t.Fatal(err)
		}
	}
	if acceptedArtifacts != 2 || rejectedArtifacts != 1 || observations != 1 || details != 1 || events != 1 {
		t.Fatalf("accepted facts artifacts=%d rejected=%d observations=%d detail_works=%d events=%d",
			acceptedArtifacts, rejectedArtifacts, observations, details, events)
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
	paused, _ := source.Pause(source.Version, model.PauseDrain)
	if err := repository.UpdateSourceCAS(ctx, source.Version, paused, offerAt); err != nil {
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

func startListingAttempt(t *testing.T, ctx context.Context, repository *Repository, prefix string, at time.Time) ListingExecutionOffer {
	t.Helper()
	executorID := prefix + "-executor"
	incarnation := prefix + "-boot"
	offer, err := repository.OfferListingExecution(ctx, ListingOfferRequest{
		AttemptID: prefix + "-attempt", ExecutorActorID: executorID, ExecutorIncarnation: incarnation,
		Capability: "http.fetch", Origin: "https://" + prefix + ".example.com", OfferedAt: at,
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
