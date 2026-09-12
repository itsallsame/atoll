package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestDetailResultAcceptsAtomicallyAndReplays(t *testing.T) {
	repository, db, ctx, cleanup := detailContractRepository(t)
	defer cleanup()
	fixture := createDetailFixture(t, ctx, repository, "detail-accept", time.Date(2026, 9, 8, 5, 0, 0, 0, time.UTC))
	defer pauseExecutionSource(t, ctx, repository, fixture.source.SourceID, fixture.now.Add(10*time.Minute))
	// Listing completion may establish/advance its Source checkpoint after a
	// detail offer was accepted. That waterline does not identify the detail
	// generation and must not fence this result.
	listingAssignment := *fixture.source.ListingAssignment
	checkpoint, _ := model.EstablishCheckpoint(model.IncrementalCheckpoint{
		SourceID: fixture.source.SourceID, RecipeID: listingAssignment.RecipeID, RecipeVersion: listingAssignment.RecipeVersion,
		ContractHash: listingAssignment.ContractHash, Strategy: model.CheckpointActivityTime,
		FrontierActivityAt: fixture.now.Add(-time.Hour).Format(time.RFC3339), OverlapPages: 1, LastOccurrenceID: "listing-completed-after-detail-offer",
	})
	checkpointState, _ := json.Marshal(checkpoint)
	if _, err := db.ExecContext(ctx, `
INSERT INTO recruiting_checkpoints(
  source_id, checkpoint_version, recipe_id, recipe_version, contract_hash,
  frontier_activity_at, frontier_keys_json, last_occurrence_id, state_json, updated_at
) VALUES (?, ?, ?, ?, ?, ?, NULL, ?, ?, ?)`, checkpoint.SourceID, checkpoint.Version, checkpoint.RecipeID,
		checkpoint.RecipeVersion, checkpoint.ContractHash, fixture.now.Add(-time.Hour), checkpoint.LastOccurrenceID, checkpointState, fixture.now); err != nil {
		t.Fatal(err)
	}
	result := fixture.result("detail-artifact-accepted")
	result.SupportingArtifacts = []model.ArtifactMetadata{
		mustResultArtifact(t, "detail-trace-accepted", model.ArtifactTrace, fixture.work.WorkID, fixture.attempt.AttemptID),
	}
	outcome, err := repository.AcceptDetailResult(ctx, result)
	if err != nil || outcome.Replayed || !outcome.ContentChanged || outcome.Job.Status != model.JobAvailable || outcome.Job.DetailVersion != 1 {
		t.Fatalf("detail acceptance = %+v %v", outcome, err)
	}
	replay, err := repository.AcceptDetailResult(ctx, result)
	if err != nil || !replay.Replayed || replay.Job.DetailVersion != 1 {
		t.Fatalf("detail replay = %+v %v", replay, err)
	}
	alteredReplay := result
	alteredReplay.DetailJSON = json.RawMessage(`{"title":"Altered"}`)
	alteredReplay.RequestHash = "sha256:altered-" + result.Artifact.ArtifactID
	if _, err := repository.AcceptDetailResult(ctx, alteredReplay); err == nil {
		t.Fatal("accepted detail artifact replay allowed a different structured result")
	}
	work, _ := repository.GetWork(ctx, fixture.work.WorkID)
	attempt, _ := repository.GetAttempt(ctx, fixture.attempt.AttemptID)
	if work.Status != model.WorkCompleted || attempt.Status != model.AttemptSucceeded {
		t.Fatalf("terminal work/attempt = %+v %+v", work, attempt)
	}
	var details, artifacts, events, receipts, dispatches int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_job_detail_versions WHERE job_id = ?", fixture.job.JobID).Scan(&details); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_artifacts WHERE attempt_id = ? AND rejected = FALSE", fixture.attempt.AttemptID).Scan(&artifacts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_event_outbox WHERE event_id = ?", "detail-completed-"+fixture.attempt.AttemptID).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_command_receipts WHERE command_id = ?", result.CauseCommandID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox WHERE target_actor_id = ? AND cause_id = ?",
		fixture.attempt.ExecutorActorID, result.CauseCommandID).Scan(&dispatches); err != nil {
		t.Fatal(err)
	}
	if details != 1 || artifacts != 2 || events != 1 || receipts != 1 || dispatches != 1 {
		t.Fatalf("detail facts=%d artifacts=%d events=%d receipts=%d dispatches=%d", details, artifacts, events, receipts, dispatches)
	}
}

func TestStaleOrWrongSenderDetailResultOnlyKeepsRejectedArtifact(t *testing.T) {
	repository, db, ctx, cleanup := detailContractRepository(t)
	defer cleanup()
	now := time.Date(2026, 9, 8, 6, 0, 0, 0, time.UTC)
	wrongSender := createDetailFixture(t, ctx, repository, "detail-wrong-sender", now)
	defer pauseExecutionSource(t, ctx, repository, wrongSender.source.SourceID, now.Add(10*time.Minute))
	wrong := wrongSender.result("detail-artifact-wrong-sender")
	wrong.ExecutorIncarnation = "stale-incarnation"
	if _, err := repository.AcceptDetailResult(ctx, wrong); !errors.Is(err, ErrResultFenced) {
		t.Fatalf("wrong executor sender = %v", err)
	}
	assertRejectedOnly(t, ctx, db, repository, wrongSender, wrong.Artifact.ArtifactID)

	stale := createDetailFixture(t, ctx, repository, "detail-stale-source", now.Add(time.Minute))
	defer pauseExecutionSource(t, ctx, repository, stale.source.SourceID, now.Add(10*time.Minute))
	currentSource, _ := repository.GetSource(ctx, stale.source.SourceID)
	reconfigured, _ := currentSource.StageEndpoint(currentSource.Version, "https://detail-stale-source.example.com/jobs-v2", "")
	if err := repository.UpdateSourceCAS(ctx, currentSource.Version, reconfigured, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	staleResult := stale.result("detail-artifact-stale-source")
	if _, err := repository.AcceptDetailResult(ctx, staleResult); !errors.Is(err, ErrResultFenced) {
		t.Fatalf("stale source result = %v", err)
	}
	assertRejectedOnly(t, ctx, db, repository, stale, staleResult.Artifact.ArtifactID)
}

func TestDetailExecutionFailureReturnsWorkToGenericOffer(t *testing.T) {
	repository, _, ctx, cleanup := detailContractRepository(t)
	defer cleanup()
	now := time.Date(2026, 9, 8, 7, 0, 0, 0, time.UTC)
	fixture := createDetailFixture(t, ctx, repository, "detail-retry", now)
	defer pauseExecutionSource(t, ctx, repository, fixture.source.SourceID, now.Add(10*time.Minute))
	failed, err := repository.FailListingExecution(ctx, fixture.attempt.AttemptID, fixture.attempt.ExecutorActorID,
		fixture.attempt.ExecutorIncarnation, "upstream_timeout", now.Add(time.Minute))
	if err != nil || failed.Status != model.AttemptFailed {
		t.Fatalf("failed detail attempt = %+v err=%v", failed, err)
	}
	work, _ := repository.GetWork(ctx, fixture.work.WorkID)
	if work.Status != model.WorkWaitingRetry || work.WaitingReason != "upstream_timeout" {
		t.Fatalf("retryable detail work = %+v", work)
	}
	retry, err := repository.OfferExecution(ctx, ListingOfferRequest{
		AttemptID: "detail-retry-attempt-2", ExecutorActorID: "executor-b", ExecutorIncarnation: "incarnation-b",
		Capability: "http.fetch", Origin: "https://detail-retry.example.com", OfferedAt: now.Add(2 * time.Minute), BudgetPolicy: testExecutionBudgetPolicy(),
	})
	if err != nil || retry.Kind != "detail" || retry.Work.WorkID != work.WorkID || retry.Detail == nil {
		t.Fatalf("retried generic detail offer = %+v err=%v", retry, err)
	}
}

func TestDetailExecutionReturnsToBatchOnlyCandidateQuery(t *testing.T) {
	repository, _, ctx, cleanup := detailContractRepository(t)
	defer cleanup()
	now := time.Date(2026, 9, 8, 7, 30, 0, 0, time.UTC)
	fixture := createDetailFixture(t, ctx, repository, "detail-batch-retry", now)
	defer pauseExecutionSource(t, ctx, repository, fixture.source.SourceID, now.Add(10*time.Minute))
	if _, err := repository.FailListingExecution(ctx, fixture.attempt.AttemptID, fixture.attempt.ExecutorActorID,
		fixture.attempt.ExecutorIncarnation, "upstream_timeout", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	retry, err := repository.OfferExecution(ctx, ListingOfferRequest{
		AttemptID: "detail-batch-retry-attempt-2", ExecutorActorID: "executor-b", ExecutorIncarnation: "incarnation-b",
		Capability: "http.fetch", Origin: "https://detail-batch-retry.example.com", OfferedAt: now.Add(2 * time.Minute),
		BudgetPolicy: testExecutionBudgetPolicy(), SupplyBatchID: "detail-batch-retry-supply", SupplyBatchOnly: true,
	})
	if err != nil || retry.Kind != "detail" || retry.Work.WorkID != fixture.work.WorkID || retry.Detail == nil {
		t.Fatalf("batch-only detail offer = %+v err=%v", retry, err)
	}
	if err := repository.VerifyAttemptSupplyBatch(ctx, retry.Attempt.AttemptID, "detail-batch-retry-supply",
		"executor-b", "incarnation-b"); err != nil {
		t.Fatalf("batch-only detail Attempt did not retain supply identity: %v", err)
	}
}

type detailFixture struct {
	source  model.RecruitmentSource
	job     model.SourceJob
	work    model.Work
	attempt model.Attempt
	now     time.Time
}

func (f detailFixture) result(artifactID string) DetailResult {
	artifact, _ := model.NewArtifactMetadata(artifactID, model.ArtifactResponse, "sha256:raw-"+artifactID,
		"object://detail/"+artifactID, f.work.WorkID, f.attempt.AttemptID, "operators", "30d", true)
	return DetailResult{
		AttemptID: f.attempt.AttemptID, ExecutorActorID: f.attempt.ExecutorActorID,
		ExecutorIncarnation: f.attempt.ExecutorIncarnation, Artifact: artifact,
		DetailVersionID: "version-" + artifactID, NormalizedContentHash: "sha256:normalized-" + f.job.JobID,
		DetailJSON: json.RawMessage(`{"title":"Engineer"}`), ObservedAt: f.now.Add(time.Minute), CauseCommandID: "command-" + artifactID,
		RequestHash: "sha256:request-" + artifactID,
	}
}

func createDetailFixture(t *testing.T, ctx context.Context, repository *Repository, prefix string, now time.Time) detailFixture {
	t.Helper()
	company := persistExecutionReadyCompany(t, ctx, repository, prefix, now)
	source, _ := model.NewRecruitmentSource(prefix+"-source", company.CompanyID, "https://"+prefix+".example.com/jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	validating, _ := source.BeginValidation(source.Version)
	if err := repository.UpdateSourceCAS(ctx, source.Version, validating, now); err != nil {
		t.Fatal(err)
	}
	listingRecipe := activeRecipe(t, prefix+"-listing-recipe", model.RecipeListing, prefix+".example.com", 1, prefix+"-listing-contract")
	detailRecipe := activeRecipe(t, prefix+"-detail-recipe", model.RecipeDetail, prefix+".example.com", 1, prefix+"-detail-contract")
	if err := repository.CreateRecipe(ctx, listingRecipe, now); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateRecipe(ctx, detailRecipe, now); err != nil {
		t.Fatal(err)
	}
	listingAssignment, _ := model.NewSourceRecipeAssignment(source.SourceID, model.RecipeListing, listingRecipe.RecipeID, 1, listingRecipe.ContractHash, now.Format(time.RFC3339))
	ready, _ := validating.PublishValidated(validating.Version, listingAssignment, verifiedStoreAssessment(validating, listingAssignment, now))
	if err := repository.PublishSourceAssignment(ctx, validating.Version, 0, ready, listingAssignment, now); err != nil {
		t.Fatal(err)
	}
	detailAssignment, _ := model.NewSourceRecipeAssignment(source.SourceID, model.RecipeDetail, detailRecipe.RecipeID, 1, detailRecipe.ContractHash, now.Format(time.RFC3339))
	readyWithDetail, _ := ready.AssignRecipe(ready.Version, detailAssignment, false)
	if err := repository.PublishSourceAssignment(ctx, ready.Version, 0, readyWithDetail, detailAssignment, now); err != nil {
		t.Fatal(err)
	}
	listing, err := repository.ApplyListingObservation(ctx, ListingIngest{
		Observation: model.ListingObservation{
			ObservationID: prefix + "-observation", OccurrenceID: prefix + "-occurrence", SourceID: source.SourceID,
			SourceJobKey: prefix + "-external-job", DetailURL: "https://" + prefix + ".example.com/jobs/1",
			ActivityAt: now.Add(-time.Minute).Format(time.RFC3339), ListingFingerprint: prefix + "-fingerprint",
			RecipeID: listingRecipe.RecipeID, RecipeVersion: 1, ArtifactID: prefix + "-listing-artifact",
		},
		ObservedAt: now, NewJobID: prefix + "-job", DetailWorkID: prefix + "-work",
		Origin: "https://" + prefix + ".example.com", Capability: "http.fetch", Priority: 10, NotBefore: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{
		AttemptID: prefix + "-attempt", ExecutorActorID: "executor-a", ExecutorIncarnation: "incarnation-a",
		Capability: "http.fetch", Origin: "https://" + prefix + ".example.com", OfferedAt: now, BudgetPolicy: testExecutionBudgetPolicy(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if offer.Kind != "detail" || offer.Detail == nil || offer.Detail.Job.JobID != listing.Job.JobID ||
		offer.Detail.Assignment != detailAssignment || offer.Detail.Recipe != detailRecipe || offer.Checkpoint != nil || offer.Occurrence != nil {
		t.Fatalf("detail execution offer = %+v", offer)
	}
	replay, err := repository.OfferExecution(ctx, ListingOfferRequest{
		AttemptID: prefix + "-attempt", ExecutorActorID: "executor-a", ExecutorIncarnation: "incarnation-a",
		Capability: "http.fetch", Origin: "https://" + prefix + ".example.com", OfferedAt: now, BudgetPolicy: testExecutionBudgetPolicy(),
	})
	if err != nil || replay.Detail == nil || replay.Detail.Job.JobID != listing.Job.JobID {
		t.Fatalf("detail execution offer replay = %+v err=%v", replay, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, "executor-a", "incarnation-a", now); err != nil {
		t.Fatal(err)
	}
	running, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, "executor-a", "incarnation-a", now)
	if err != nil {
		t.Fatal(err)
	}
	work, err := repository.GetWork(ctx, offer.Work.WorkID)
	if err != nil || work.Status != model.WorkRunning {
		t.Fatalf("started detail work = %+v err=%v", work, err)
	}
	return detailFixture{source: readyWithDetail, job: listing.Job, work: work, attempt: running, now: now}
}

func assertRejectedOnly(t *testing.T, ctx context.Context, db queryRower, repository *Repository, fixture detailFixture, artifactID string) {
	t.Helper()
	var rejected bool
	if err := db.QueryRowContext(ctx, "SELECT rejected FROM recruiting_artifacts WHERE artifact_id = ?", artifactID).Scan(&rejected); err != nil || !rejected {
		t.Fatalf("rejected artifact = %v %v", rejected, err)
	}
	job, _ := repository.GetJob(ctx, fixture.job.JobID)
	work, _ := repository.GetWork(ctx, fixture.work.WorkID)
	attempt, _ := repository.GetAttempt(ctx, fixture.attempt.AttemptID)
	if job.Status != model.JobDetailPending || job.DetailVersion != 0 || work.Status != model.WorkRunning || attempt.Status != model.AttemptRunning {
		t.Fatalf("rejected result changed business state: job=%+v work=%+v attempt=%+v", job, work, attempt)
	}
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func detailContractRepository(t *testing.T) (*Repository, *sql.DB, context.Context, func()) {
	t.Helper()
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	return repository, db, ctx, func() { cancel(); _ = db.Close() }
}
