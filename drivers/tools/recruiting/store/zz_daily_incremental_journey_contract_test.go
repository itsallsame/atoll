package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeexec"
)

type continuousListingItem struct {
	key         string
	activity    time.Time
	fingerprint string
}

type continuousDailyExecution struct {
	offer      ListingExecutionOffer
	occurrence model.SourceOccurrence
	at         time.Time
}

func TestDailyIncrementalD0ThroughD6PreservesBoundaryAndRefreshInvariants(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	d0 := time.Date(2095, 1, 1, 12, 0, 0, 0, time.UTC)
	const sourceID = "continuous-daily-source"

	// D0 is an actual executable baseline: staged listing facts remain hidden
	// until finalize, then materialize to Jobs/Detail Work, all details reach a
	// terminal success, and only then can the Company become daily-eligible.
	source := createContinuousD0Baseline(t, ctx, db, repository, sourceID, d0, []continuousListingItem{
		{key: "a", activity: d0, fingerprint: "fp-a-v1"},
		{key: "b", activity: d0.Add(-time.Hour), fingerprint: "fp-b-v1"},
		{key: "c", activity: d0.Add(-2 * time.Hour), fingerprint: "fp-c-v1"},
	})
	checkpoint, err := repository.GetCheckpoint(ctx, sourceID)
	if err != nil || checkpoint.Version != 1 || checkpoint.FrontierActivityAt != d0.Format(time.RFC3339) {
		t.Fatalf("D0 checkpoint = %+v err=%v", checkpoint, err)
	}
	assertContinuousCounts(t, ctx, db, sourceID, 3, 3, 3)
	restoreRoster := isolateContinuousDailyRoster(t, ctx, db, sourceID)
	defer restoreRoster()

	// D1: unchanged overlap advances only the checkpoint/coverage facts; it
	// must not create another effective detail generation.
	d1 := startContinuousDaily(t, ctx, repository, source, "d1", d0.Add(24*time.Hour))
	checkpoint = acceptContinuousDailyScan(t, ctx, repository, d1, [][]continuousListingItem{
		{{key: "a", activity: d0, fingerprint: "fp-a-v1"}, {key: "b", activity: d0.Add(-time.Hour), fingerprint: "fp-b-v1"}},
		{{key: "c", activity: d0.Add(-2 * time.Hour), fingerprint: "fp-c-v1"}},
	})
	if checkpoint.Version != 2 || checkpoint.FrontierActivityAt != d0.Format(time.RFC3339) {
		t.Fatalf("D1 checkpoint = %+v", checkpoint)
	}
	assertContinuousCounts(t, ctx, db, sourceID, 3, 3, 6)

	// D2: one new job appears at the top; overlap rows are observations but do
	// not create detail refreshes.
	d2Time := d0.Add(48 * time.Hour)
	d2 := startContinuousDaily(t, ctx, repository, source, "d2", d2Time)
	checkpoint = acceptContinuousDailyScan(t, ctx, repository, d2, [][]continuousListingItem{
		{{key: "d", activity: d2Time, fingerprint: "fp-d-v1"}, {key: "a", activity: d0, fingerprint: "fp-a-v1"}, {key: "b", activity: d0.Add(-time.Hour), fingerprint: "fp-b-v1"}},
		{{key: "c", activity: d0.Add(-2 * time.Hour), fingerprint: "fp-c-v1"}},
	})
	if checkpoint.Version != 3 || checkpoint.FrontierActivityAt != d2Time.Format(time.RFC3339) {
		t.Fatalf("D2 checkpoint = %+v", checkpoint)
	}
	assertContinuousCounts(t, ctx, db, sourceID, 4, 4, 10)
	completeContinuousIncrementalDetail(t, ctx, repository, "d", d2.at.Add(10*time.Second))

	// D3: historical job C changes and is re-topped. It remains one Job but
	// advances refresh_generation and creates exactly one new Detail Work.
	d3Time := d0.Add(72 * time.Hour)
	d3 := startContinuousDaily(t, ctx, repository, source, "d3", d3Time)
	checkpoint = acceptContinuousDailyScan(t, ctx, repository, d3, [][]continuousListingItem{
		{{key: "c", activity: d3Time, fingerprint: "fp-c-v2"}, {key: "d", activity: d2Time, fingerprint: "fp-d-v1"}, {key: "a", activity: d0, fingerprint: "fp-a-v1"}},
		{{key: "b", activity: d0.Add(-time.Hour), fingerprint: "fp-b-v1"}},
	})
	if checkpoint.Version != 4 || checkpoint.FrontierActivityAt != d3Time.Format(time.RFC3339) {
		t.Fatalf("D3 checkpoint = %+v", checkpoint)
	}
	assertContinuousCounts(t, ctx, db, sourceID, 4, 5, 14)
	var cJobID string
	if err := db.QueryRowContext(ctx, `SELECT job_id FROM recruiting_source_jobs
WHERE source_id = ? AND source_job_key = 'c'`, sourceID).Scan(&cJobID); err != nil {
		t.Fatal(err)
	}
	cJob, err := repository.GetJob(ctx, cJobID)
	if err != nil {
		t.Fatal(err)
	}
	if cJob.RefreshGeneration != 2 || cJob.ListingFingerprint != "fp-c-v2" {
		t.Fatalf("D3 historical update refresh=%d fingerprint=%s", cJob.RefreshGeneration, cJob.ListingFingerprint)
	}
	failContinuousRefreshDetail(t, ctx, repository, cJob, checkpoint, d3.at.Add(10*time.Second))

	// D4: the newest activity-time group is split across pages. Both E and F
	// must be consumed before the candidate checkpoint is committed.
	d4Time := d0.Add(96 * time.Hour)
	d4 := startContinuousDaily(t, ctx, repository, source, "d4", d4Time)
	checkpoint = acceptContinuousDailyScanAfterExecutorExit(t, ctx, repository, d4, [][]continuousListingItem{
		{{key: "e", activity: d4Time, fingerprint: "fp-e-v1"}},
		{{key: "f", activity: d4Time, fingerprint: "fp-f-v1"}, {key: "c", activity: d3Time, fingerprint: "fp-c-v2"}, {key: "d", activity: d2Time, fingerprint: "fp-d-v1"}},
		{{key: "a", activity: d0, fingerprint: "fp-a-v1"}},
	})
	if checkpoint.Version != 5 || checkpoint.FrontierActivityAt != d4Time.Format(time.RFC3339) ||
		!slices.Contains(checkpoint.FrontierJobKeys, "e") || !slices.Contains(checkpoint.FrontierJobKeys, "f") {
		t.Fatalf("D4 split-time checkpoint = %+v", checkpoint)
	}
	assertContinuousCounts(t, ctx, db, sourceID, 6, 7, 20)

	// D5: two full pages never reach the old boundary. The production Driver
	// rejects the scan before page submission, so only failure evidence and a
	// waiting-human Work are committed; jobs/observations/checkpoint stay put.
	d5Time := d0.Add(120 * time.Hour)
	d5 := startContinuousDaily(t, ctx, repository, source, "d5", d5Time)
	assertContinuousUnsafeScan(t, d5.offer.Checkpoint, [][]continuousListingItem{
		{{key: "g", activity: d5Time, fingerprint: "fp-g-v1"}},
		{{key: "h", activity: d5Time.Add(-time.Hour), fingerprint: "fp-h-v1"}},
	})
	failureArtifact := mustResultArtifact(t, "continuous-d5-boundary-failure", model.ArtifactFailure,
		d5.offer.Work.WorkID, d5.offer.Attempt.AttemptID)
	report := executioncontract.FailureReport{Class: "quality_rejected", NeedsRepair: true, Artifact: failureArtifact}
	policy := ExecutionFailurePolicy{Version: 1, MaxAutomaticAttempts: 3, BaseDelay: time.Second,
		MaxDelay: time.Minute, ThrottledDelay: time.Minute}
	if _, err := repository.FailExecutionWithReport(ctx, d5.offer.Attempt.AttemptID, d5.offer.Attempt.ExecutorActorID,
		d5.offer.Attempt.ExecutorIncarnation, report.Class, report, policy, d5.at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	failedWork, _ := repository.GetWork(ctx, d5.offer.Work.WorkID)
	checkpointAfterD5, _ := repository.GetCheckpoint(ctx, sourceID)
	if failedWork.Status != model.WorkWaitingHuman || checkpointAfterD5.Version != 5 {
		t.Fatalf("D5 work=%+v checkpoint=%+v", failedWork, checkpointAfterD5)
	}
	assertContinuousCounts(t, ctx, db, sourceID, 6, 7, 20)
	var d5Pages, d5Observations int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_listing_page_progress WHERE attempt_id = ?", d5.offer.Attempt.AttemptID).Scan(&d5Pages)
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_listing_observations WHERE occurrence_id = ?", d5.occurrence.OccurrenceID).Scan(&d5Observations)
	if d5Pages != 0 || d5Observations != 0 {
		t.Fatalf("D5 unsafe scan exposed pages=%d observations=%d", d5Pages, d5Observations)
	}

	// D6: a human terminates the failed lifecycle and creates a causal retry.
	// The occurrence is rebound atomically; a new executor incarnation starts
	// at page one, reaches the old D4 boundary including its equal-time group,
	// and advances the same checkpoint lineage exactly once.
	retry := repairContinuousDailyWork(t, ctx, repository, failedWork, d5.at.Add(2*time.Second))
	d6 := startContinuousRetry(t, ctx, repository, retry, d5.occurrence, d5.at.Add(3*time.Second))
	d6Time := d0.Add(144 * time.Hour)
	checkpoint = acceptContinuousDailyScan(t, ctx, repository, d6, [][]continuousListingItem{
		{{key: "g", activity: d6Time, fingerprint: "fp-g-v1"}, {key: "h", activity: d5Time, fingerprint: "fp-h-v1"}, {key: "e", activity: d4Time, fingerprint: "fp-e-v1"}},
		{{key: "f", activity: d4Time, fingerprint: "fp-f-v1"}, {key: "c", activity: d3Time, fingerprint: "fp-c-v2"}},
		{{key: "d", activity: d2Time, fingerprint: "fp-d-v1"}},
	})
	if checkpoint.Version != 6 || checkpoint.FrontierActivityAt != d6Time.Format(time.RFC3339) ||
		checkpoint.LastOccurrenceID != d5.occurrence.OccurrenceID {
		t.Fatalf("D6 recovered checkpoint = %+v", checkpoint)
	}
	assertContinuousCounts(t, ctx, db, sourceID, 8, 9, 26)
	assertContinuousJourneyFinal(t, ctx, db, sourceID, d5.occurrence.OccurrenceID, failedWork.WorkID, retry.WorkID)
}

func completeContinuousIncrementalDetail(t *testing.T, ctx context.Context, repository *Repository,
	wantSourceJobKey string, at time.Time) {
	t.Helper()
	offer := startContinuousDetail(t, ctx, repository, "continuous-"+wantSourceJobKey+"-detail", wantSourceJobKey, at)
	artifact := mustResultArtifact(t, "continuous-"+wantSourceJobKey+"-detail-response", model.ArtifactResponse,
		offer.Work.WorkID, offer.Attempt.AttemptID)
	result := DetailResult{AttemptID: offer.Attempt.AttemptID, ExecutorActorID: offer.Attempt.ExecutorActorID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, Artifact: artifact,
		DetailVersionID:       "continuous-" + wantSourceJobKey + "-detail-version",
		NormalizedContentHash: "sha256:continuous-" + wantSourceJobKey + "-detail",
		DetailJSON:            []byte(fmt.Sprintf(`{"source_job_key":%q}`, wantSourceJobKey)),
		ObservedAt:            at.Add(time.Second), CauseCommandID: "continuous-" + wantSourceJobKey + "-detail-result",
		RequestHash: "sha256:continuous-" + wantSourceJobKey + "-detail-result"}
	accepted, err := repository.AcceptDetailResult(ctx, result)
	if err != nil || accepted.Replayed || accepted.Job.Status != model.JobAvailable {
		t.Fatalf("incremental detail %s = %+v err=%v", wantSourceJobKey, accepted, err)
	}
	if replay, err := repository.AcceptDetailResult(ctx, result); err != nil || !replay.Replayed || replay.Job != accepted.Job {
		t.Fatalf("incremental detail %s replay = %+v err=%v", wantSourceJobKey, replay, err)
	}
}

func failContinuousRefreshDetail(t *testing.T, ctx context.Context, repository *Repository,
	updated model.SourceJob, committedCheckpoint model.IncrementalCheckpoint, at time.Time) {
	t.Helper()
	if updated.DetailVersion != 1 || updated.DetailContentHash == "" || updated.Status != model.JobUpdatePending {
		t.Fatalf("updated job lost its last usable detail before refresh: %+v", updated)
	}
	previousHash := updated.DetailContentHash
	offer := startContinuousDetail(t, ctx, repository, "continuous-c-refresh-detail", updated.SourceJobKey, at)
	failureArtifact := mustResultArtifact(t, "continuous-c-refresh-failure", model.ArtifactFailure,
		offer.Work.WorkID, offer.Attempt.AttemptID)
	report := executioncontract.FailureReport{Class: "parse_error", NeedsRepair: true, Artifact: failureArtifact}
	command := ExecutionTransitionCommand{CommandID: "continuous-c-refresh-failed-command",
		Word: executioncontract.TypeFailed, RequestHash: "sha256:continuous-c-refresh-failed-command",
		CorrelationID: "continuous-c-refresh-correlation", RequestedBy: offer.Attempt.ExecutorActorID,
		AttemptID: offer.Attempt.AttemptID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
		Action: "fail", Reason: report.Class, Failure: &report,
		FailurePolicy: ExecutionFailurePolicy{Version: 1, MaxAutomaticAttempts: 3, BaseDelay: time.Second,
			MaxDelay: time.Minute, ThrottledDelay: time.Minute}}
	first, err := repository.ApplyExecutionTransitionCommand(ctx, command, at.Add(time.Second))
	if err != nil || first.Replayed {
		t.Fatalf("refresh detail failure = %+v err=%v", first, err)
	}
	replay, err := repository.ApplyExecutionTransitionCommand(ctx, command, at.Add(2*time.Second))
	if err != nil || !replay.Replayed || string(replay.Response) != string(first.Response) {
		t.Fatalf("refresh detail failure lost-reply replay = %+v err=%v", replay, err)
	}
	job, jobErr := repository.GetJob(ctx, updated.JobID)
	work, workErr := repository.GetWork(ctx, offer.Work.WorkID)
	checkpoint, checkpointErr := repository.GetCheckpoint(ctx, updated.SourceID)
	if jobErr != nil || workErr != nil || checkpointErr != nil || work.Status != model.WorkWaitingHuman ||
		job.Status != model.JobUpdatePending || job.RefreshGeneration != updated.RefreshGeneration ||
		job.DetailVersion != updated.DetailVersion || job.DetailContentHash != previousHash ||
		!reflect.DeepEqual(checkpoint, committedCheckpoint) {
		t.Fatalf("failed refresh changed accepted facts job=%+v work=%+v checkpoint=%+v errors=%v/%v/%v",
			job, work, checkpoint, jobErr, workErr, checkpointErr)
	}
}

func startContinuousDetail(t *testing.T, ctx context.Context, repository *Repository, prefix, wantSourceJobKey string,
	at time.Time) ExecutionOffer {
	t.Helper()
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: prefix + "-attempt",
		ExecutorActorID: "tool:continuous-detail:2", ExecutorIncarnation: prefix + "-boot",
		Capability: "http.detail", Origin: "https://continuous.example.com", OfferedAt: at,
		BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || offer.Detail == nil || offer.Detail.Job.SourceJobKey != wantSourceJobKey {
		t.Fatalf("detail offer %s = %+v err=%v", wantSourceJobKey, offer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, at); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, at); err != nil {
		t.Fatal(err)
	}
	return offer
}

// The MySQL contract runner intentionally shares one migrated schema across
// package tests. Hide pre-existing active sources from this scenario's global
// daily cutoff, then restore their indexed projection when the scenario ends. Their JSON
// aggregate state and every business fact remain untouched.
func isolateContinuousDailyRoster(t *testing.T, ctx context.Context, db *sql.DB, sourceID string) func() {
	t.Helper()
	rows, err := db.QueryContext(ctx, `SELECT source_id FROM recruiting_sources
WHERE source_id <> ? AND control_status = 'active'`, sourceID)
	if err != nil {
		t.Fatal(err)
	}
	var sourceIDs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		sourceIDs = append(sourceIDs, id)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	for _, id := range sourceIDs {
		if _, err := db.ExecContext(ctx, `UPDATE recruiting_sources SET control_status = 'paused' WHERE source_id = ?`, id); err != nil {
			t.Fatal(err)
		}
	}
	return func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		for _, id := range sourceIDs {
			if _, err := db.ExecContext(cleanupCtx, `UPDATE recruiting_sources SET control_status = 'active' WHERE source_id = ?`, id); err != nil {
				t.Errorf("restore source %s after continuous journey: %v", id, err)
			}
		}
	}
}

func createContinuousD0Baseline(t *testing.T, ctx context.Context, db *sql.DB, repository *Repository,
	sourceID string, now time.Time, items []continuousListingItem) model.RecruitmentSource {
	t.Helper()
	company, _ := model.NewCompany("continuous-daily-company", "Continuous Daily", "https://continuous.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	discovering, _ := company.StartDiscovery(company.Version)
	if err := repository.UpdateCompanyCAS(ctx, company.Version, discovering, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource(sourceID, company.CompanyID, "https://continuous.example.com/jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	validating, _ := source.BeginValidation(source.Version)
	if err := repository.UpdateSourceCAS(ctx, source.Version, validating, now); err != nil {
		t.Fatal(err)
	}
	listingRecipe := activeRecipe(t, "continuous-listing-recipe", model.RecipeListing,
		"continuous.example.com", 1, "continuous-listing-contract")
	if err := repository.CreateRecipe(ctx, listingRecipe, now); err != nil {
		t.Fatal(err)
	}
	listingAssignment, _ := model.NewSourceRecipeAssignment(sourceID, model.RecipeListing, listingRecipe.RecipeID,
		listingRecipe.Version, listingRecipe.ContractHash, now.Format(time.RFC3339Nano))
	ready, err := validating.PublishValidated(validating.Version, listingAssignment,
		verifiedStoreAssessment(validating, listingAssignment, now))
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishSourceAssignment(ctx, validating.Version, 0, ready, listingAssignment, now); err != nil {
		t.Fatal(err)
	}
	detailRecipe := activeRecipe(t, "continuous-detail-recipe", model.RecipeDetail,
		"continuous.example.com", 1, "continuous-detail-contract")
	detailRecipe.Execution.RequiredCapability = "http.detail"
	if err := repository.CreateRecipe(ctx, detailRecipe, now); err != nil {
		t.Fatal(err)
	}
	detailAssignment, _ := model.NewSourceRecipeAssignment(sourceID, model.RecipeDetail, detailRecipe.RecipeID,
		detailRecipe.Version, detailRecipe.ContractHash, now.Format(time.RFC3339Nano))
	withDetail, err := ready.AssignRecipe(ready.Version, detailAssignment, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.PublishSourceAssignment(ctx, ready.Version, 0, withDetail, detailAssignment, now); err != nil {
		t.Fatal(err)
	}
	initializing, _ := discovering.StartInitialization(discovering.Version)
	work, _ := model.NewWork("continuous-d0-baseline-work", "source", sourceID, "baseline_listing", "timer")
	work, _ = work.WithCausality("system:daily", "message-continuous-d0", "")
	baseline, err := model.NewExecutableBaselineGeneration(work.WorkID, initializing, withDetail, 1, listingRecipe)
	if err != nil {
		t.Fatal(err)
	}
	placement := WorkPlacement{BusinessKey: "baseline|continuous-daily-source|1", Priority: 200,
		Capability: "http.fetch", Origin: "https://continuous.example.com", NotBefore: now}
	receipt, _ := model.NewCommandReceipt("continuous-d0-start", "recruiting.baseline.start",
		"sha256:continuous-d0-start", json.RawMessage(`{"day":"D0"}`))
	event, _ := model.NewEventIntent("continuous-d0-event", "baseline.created", "baseline", work.WorkID,
		baseline.Version, now.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	companyEvent, _ := model.NewEventIntent("continuous-d0-company-event", "company.initialization.started", "company",
		initializing.CompanyID, initializing.Version, now.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyCreateBaselineCommand(ctx, discovering.Version, withDetail.Version, initializing,
		baseline, work, placement, receipt, event, &companyEvent, nil, now); err != nil {
		t.Fatal(err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "continuous-d0-attempt",
		ExecutorActorID: "tool:continuous-listing:1", ExecutorIncarnation: "continuous-d0-boot",
		Capability: placement.Capability, Origin: placement.Origin, OfferedAt: now.Add(time.Second),
		BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || offer.Baseline == nil {
		t.Fatalf("D0 offer = %+v err=%v", offer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	pageArtifact := mustResultArtifact(t, "continuous-d0-page", model.ArtifactPage, work.WorkID, offer.Attempt.AttemptID)
	observations := continuousObservations(t, items, work.WorkID, sourceID, offer.Attempt, pageArtifact.ArtifactID, "d0")
	pageInput := ListingPageResult{CommandID: "continuous-d0-page-command", RequestHash: "sha256:continuous-d0-page",
		AttemptID: offer.Attempt.AttemptID, ExecutorActorID: offer.Attempt.ExecutorActorID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, PageSequence: 1, Terminal: true,
		Artifact: pageArtifact, Observations: observations, ObservedAt: now.Add(4 * time.Second)}
	if page, err := repository.AcceptListingPage(ctx, pageInput); err != nil || page.Progress.ItemCount != len(items) {
		t.Fatalf("D0 page = %+v err=%v", page, err)
	}
	if replay, err := repository.AcceptListingPage(ctx, pageInput); err != nil || !replay.Replayed {
		t.Fatalf("D0 page replay = %+v err=%v", replay, err)
	}
	var jobsBeforeFinalize int
	_ = db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_source_jobs WHERE source_id = ?", sourceID).Scan(&jobsBeforeFinalize)
	if jobsBeforeFinalize != 0 {
		t.Fatalf("D0 exposed %d Jobs before baseline finalize", jobsBeforeFinalize)
	}
	completionArtifact := mustResultArtifact(t, "continuous-d0-completion", model.ArtifactListingDelta,
		work.WorkID, offer.Attempt.AttemptID)
	completion := ListingCompletion{RequestHash: "sha256:continuous-d0-completion", AttemptID: offer.Attempt.AttemptID,
		ExecutorActorID: offer.Attempt.ExecutorActorID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
		Artifact: completionArtifact, ItemCount: len(items), CompletedAt: now.Add(5 * time.Second),
		CauseCommandID: "continuous-d0-completion-command", Progress: model.ListingProgress{
			IdentityComplete: true, PaginationStable: true, PreviousFrontierReached: true, OverlapCompleted: true,
			OrderingContractHeld: true, SameTimeGroupCompleted: true,
			Candidate: model.IncrementalCheckpoint{FrontierActivityAt: items[0].activity.Format(time.RFC3339),
				FrontierJobKeys: continuousKeys(items)},
		}}
	completed, err := repository.AcceptListingCompletion(ctx, completion)
	if err != nil || completed.Baseline == nil || completed.Checkpoint.Version != 1 {
		t.Fatalf("D0 completion = %+v err=%v", completed, err)
	}
	if replay, err := repository.AcceptListingCompletion(ctx, completion); err != nil || !replay.Replayed {
		t.Fatalf("D0 completion replay = %+v err=%v", replay, err)
	}
	materialized, err := repository.MaterializeNextBaselinePage(ctx, 500, now.Add(6*time.Second), nil)
	if err != nil || materialized.Processed != len(items) || !materialized.Completed {
		t.Fatalf("D0 materialization = %+v err=%v", materialized, err)
	}
	completeContinuousD0Details(t, ctx, repository, sourceID, len(items), now.Add(7*time.Second))
	promoted, err := repository.PromoteNextReadyCompany(ctx, now.Add(time.Minute))
	if err != nil || promoted == nil || promoted.CompanyID != company.CompanyID || promoted.OnboardingStatus != model.CompanyReady {
		t.Fatalf("D0 company promotion = %+v err=%v", promoted, err)
	}
	return withDetail
}

func completeContinuousD0Details(t *testing.T, ctx context.Context, repository *Repository, sourceID string,
	count int, at time.Time) {
	t.Helper()
	for index := range count {
		offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: fmt.Sprintf("continuous-d0-detail-attempt-%d", index),
			ExecutorActorID: "tool:continuous-detail:1", ExecutorIncarnation: fmt.Sprintf("continuous-detail-boot-%d", index),
			Capability: "http.detail", Origin: "https://continuous.example.com", OfferedAt: at.Add(time.Duration(index) * time.Second),
			BudgetPolicy: testExecutionBudgetPolicy()})
		if err != nil || offer.Detail == nil {
			t.Fatalf("D0 detail offer %d = %+v err=%v", index, offer, err)
		}
		if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
			offer.Attempt.ExecutorIncarnation, at.Add(time.Duration(index)*time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
			offer.Attempt.ExecutorIncarnation, at.Add(time.Duration(index)*time.Second)); err != nil {
			t.Fatal(err)
		}
		artifact := mustResultArtifact(t, fmt.Sprintf("continuous-d0-detail-%d", index), model.ArtifactResponse,
			offer.Work.WorkID, offer.Attempt.AttemptID)
		outcome, err := repository.AcceptDetailResult(ctx, DetailResult{AttemptID: offer.Attempt.AttemptID,
			ExecutorActorID: offer.Attempt.ExecutorActorID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
			Artifact: artifact, DetailVersionID: fmt.Sprintf("continuous-d0-version-%d", index),
			NormalizedContentHash: fmt.Sprintf("sha256:continuous-d0-detail-%d", index),
			DetailJSON:            []byte(fmt.Sprintf(`{"source_job_key":%q}`, offer.Detail.Job.SourceJobKey)),
			ObservedAt:            at.Add(time.Duration(index+1) * time.Second),
			CauseCommandID:        fmt.Sprintf("continuous-d0-detail-command-%d", index),
			RequestHash:           fmt.Sprintf("sha256:continuous-d0-detail-command-%d", index)})
		storedWork, workErr := repository.GetWork(ctx, offer.Work.WorkID)
		if err != nil || workErr != nil || storedWork.Status != model.WorkCompleted || outcome.Job.JobID != offer.Detail.Job.JobID {
			t.Fatalf("D0 detail result %d = %+v err=%v", index, outcome, err)
		}
	}
	var state []byte
	err := repository.db.QueryRowContext(ctx, `SELECT state_json FROM recruiting_baseline_generations
WHERE source_id = ? AND baseline_generation = 1`, sourceID).Scan(&state)
	var baseline model.BaselineGeneration
	if err == nil {
		err = json.Unmarshal(state, &baseline)
	}
	if err != nil || baseline.Status != model.BaselineCompleted || baseline.DetailsAccounted != uint64(count) {
		t.Fatalf("D0 baseline after details = %+v err=%v", baseline, err)
	}
}

func startContinuousDaily(t *testing.T, ctx context.Context, repository *Repository, source model.RecruitmentSource,
	day string, cutoff time.Time) continuousDailyExecution {
	t.Helper()
	request := DailyPlanRequest{DailyRunID: "continuous-" + day + "-daily", ScheduleDate: cutoff.Format("2006-01-02"),
		Schedule: model.DailySchedule{PolicyVersion: 1, CutoffAt: cutoff.Format(time.RFC3339Nano),
			WindowStartAt: cutoff.Add(time.Minute).Format(time.RFC3339Nano),
			WindowEndAt:   cutoff.Add(time.Hour).Format(time.RFC3339Nano)},
		TriggerID: "continuous-" + day + "-trigger", EventID: "continuous-" + day + "-event"}
	plan, err := repository.PlanDailyRunAtCutoff(ctx, request, cutoff)
	if err != nil || plan.Replayed || plan.Occurrences != 1 {
		t.Fatalf("%s plan = %+v err=%v", day, plan, err)
	}
	replay, err := repository.PlanDailyRunAtCutoff(ctx, request, cutoff.Add(time.Second))
	if err != nil || !replay.Replayed || replay.Run.DailyRunID != plan.Run.DailyRunID {
		t.Fatalf("%s plan replay = %+v err=%v", day, replay, err)
	}
	page, err := repository.ListOccurrences(ctx, plan.Run.DailyRunID, "", 10)
	if err != nil || len(page.Items) != 1 || page.Items[0].SourceID != source.SourceID {
		t.Fatalf("%s occurrence = %+v err=%v", day, page, err)
	}
	dueAt, _ := time.Parse(time.RFC3339, page.Items[0].DueAt)
	materialized, err := repository.MaterializeDueOccurrenceWorks(ctx, dueAt.Add(time.Microsecond), 500,
		"recruiting", "continuous-"+day+"-materialize", dueAt)
	if err != nil || materialized.Queued != 1 {
		t.Fatalf("%s materialization = %+v err=%v", day, materialized, err)
	}
	// A second coordinator pass represents a duplicate timer delivery.
	if duplicate, err := repository.MaterializeDueOccurrenceWorks(ctx, dueAt.Add(time.Microsecond), 500,
		"recruiting", "continuous-"+day+"-materialize-replay", dueAt); err != nil || duplicate.Queued != 0 {
		t.Fatalf("%s duplicate materialization = %+v err=%v", day, duplicate, err)
	}
	page, _ = repository.ListOccurrences(ctx, plan.Run.DailyRunID, "", 10)
	occurrence := page.Items[0]
	if occurrence.WorkID == "" {
		t.Fatalf("%s occurrence has no Work", day)
	}
	offerAt := dueAt.Add(time.Second)
	offer, err := repository.OfferListingExecution(ctx, ListingOfferRequest{AttemptID: "continuous-" + day + "-attempt",
		ExecutorActorID: "tool:continuous-listing:1", ExecutorIncarnation: "continuous-" + day + "-boot",
		Capability: "http.fetch", Origin: "https://continuous.example.com", OfferedAt: offerAt,
		BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || offer.Occurrence == nil || offer.Occurrence.OccurrenceID != occurrence.OccurrenceID {
		t.Fatalf("%s offer = %+v err=%v", day, offer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, offerAt); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, offerAt); err != nil {
		t.Fatal(err)
	}
	return continuousDailyExecution{offer: offer, occurrence: occurrence, at: offerAt}
}

func startContinuousRetry(t *testing.T, ctx context.Context, repository *Repository, retry model.Work,
	previous model.SourceOccurrence, at time.Time) continuousDailyExecution {
	t.Helper()
	offer, err := repository.OfferListingExecution(ctx, ListingOfferRequest{AttemptID: "continuous-d6-attempt",
		ExecutorActorID: "tool:continuous-listing:2", ExecutorIncarnation: "continuous-d6-boot",
		Capability: "http.fetch", Origin: "https://continuous.example.com", OfferedAt: at,
		BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || offer.Work.WorkID != retry.WorkID || offer.Occurrence == nil ||
		offer.Occurrence.OccurrenceID != previous.OccurrenceID || offer.Checkpoint == nil {
		t.Fatalf("D6 retry offer = %+v err=%v", offer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, at); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, at); err != nil {
		t.Fatal(err)
	}
	return continuousDailyExecution{offer: offer, occurrence: *offer.Occurrence, at: at}
}

func acceptContinuousDailyScan(t *testing.T, ctx context.Context, repository *Repository,
	execution continuousDailyExecution, pages [][]continuousListingItem) model.IncrementalCheckpoint {
	t.Helper()
	scan := newContinuousScan(t, execution.offer.Checkpoint, len(pages)+2)
	documents := make([]recipeexec.DocumentResult, len(pages))
	for index, items := range pages {
		// Keep a next cursor even on the final consumed page: completion must be
		// caused by the proven boundary+overlap, not by exhausting the board.
		documents[index] = continuousDocument(items, true)
		if err := scan.AddPage(documents[index]); err != nil {
			t.Fatalf("scan page %d: %v", index+1, err)
		}
	}
	if !scan.Complete() || scan.StopReason() != "safe_boundary" || !scan.Quality().MayAdvanceCheckpoint() {
		t.Fatalf("safe scan complete=%v reason=%s quality=%+v", scan.Complete(), scan.StopReason(), scan.Quality())
	}
	candidate, err := scan.CheckpointCandidate()
	if err != nil {
		t.Fatal(err)
	}
	itemCount := 0
	for index, items := range pages {
		artifact := mustResultArtifact(t, fmt.Sprintf("%s-page-%d", execution.occurrence.OccurrenceID, index+1),
			model.ArtifactPage, execution.offer.Work.WorkID, execution.offer.Attempt.AttemptID)
		observations := continuousObservations(t, items, execution.occurrence.OccurrenceID,
			execution.occurrence.SourceID, execution.offer.Attempt, artifact.ArtifactID,
			fmt.Sprintf("%s-p%d", execution.occurrence.OccurrenceID, index+1))
		input := ListingPageResult{CommandID: fmt.Sprintf("%s-page-command-%d", execution.occurrence.OccurrenceID, index+1),
			RequestHash: fmt.Sprintf("sha256:%s-page-%d", execution.occurrence.OccurrenceID, index+1),
			AttemptID:   execution.offer.Attempt.AttemptID, ExecutorActorID: execution.offer.Attempt.ExecutorActorID,
			ExecutorIncarnation: execution.offer.Attempt.ExecutorIncarnation, PageSequence: uint64(index + 1),
			ResumeCursor: fmt.Sprintf("page-%d", index+2), Terminal: index == len(pages)-1,
			Artifact: artifact, Observations: observations, ObservedAt: execution.at.Add(time.Duration(index+1) * time.Second)}
		accepted, err := repository.AcceptListingPage(ctx, input)
		if err != nil || accepted.Replayed || accepted.Progress.PageSequence != uint64(index+1) {
			t.Fatalf("accept page %d = %+v err=%v", index+1, accepted, err)
		}
		if replay, err := repository.AcceptListingPage(ctx, input); err != nil || !replay.Replayed {
			t.Fatalf("replay page %d = %+v err=%v", index+1, replay, err)
		}
		itemCount += len(observations)
	}
	quality := scan.Quality()
	completionArtifact := mustResultArtifact(t, execution.occurrence.OccurrenceID+"-completion",
		model.ArtifactListingDelta, execution.offer.Work.WorkID, execution.offer.Attempt.AttemptID)
	input := ListingCompletion{RequestHash: "sha256:" + execution.occurrence.OccurrenceID + "-completion",
		AttemptID: execution.offer.Attempt.AttemptID, ExecutorActorID: execution.offer.Attempt.ExecutorActorID,
		ExecutorIncarnation: execution.offer.Attempt.ExecutorIncarnation, Artifact: completionArtifact,
		ItemCount: itemCount, CompletedAt: execution.at.Add(time.Duration(len(pages)+2) * time.Second),
		CauseCommandID: execution.occurrence.OccurrenceID + "-completion-command", Progress: model.ListingProgress{
			IdentityComplete: quality.IdentityComplete, PaginationStable: quality.PaginationStable,
			PreviousFrontierReached: quality.PreviousFrontierReached, OverlapCompleted: quality.OverlapCompleted,
			OrderingContractHeld: quality.OrderingContractHeld, SameTimeGroupCompleted: quality.PreviousFrontierReached,
			Candidate: model.IncrementalCheckpoint{FrontierActivityAt: candidate.LastActivityAt,
				FrontierJobKeys: append([]string(nil), candidate.FrontierKeys...)},
		}}
	completed, err := repository.AcceptListingCompletion(ctx, input)
	if err != nil || completed.Replayed || completed.Occurrence == nil ||
		completed.Occurrence.Status != model.OccurrenceCompleted || completed.Work.Status != model.WorkCompleted {
		t.Fatalf("daily completion = %+v err=%v", completed, err)
	}
	if replay, err := repository.AcceptListingCompletion(ctx, input); err != nil || !replay.Replayed ||
		replay.Checkpoint.Version != completed.Checkpoint.Version {
		t.Fatalf("daily completion replay = %+v err=%v", replay, err)
	}
	return completed.Checkpoint
}

func acceptContinuousDailyScanAfterExecutorExit(t *testing.T, ctx context.Context, repository *Repository,
	execution continuousDailyExecution, pages [][]continuousListingItem) model.IncrementalCheckpoint {
	t.Helper()
	if len(pages) < 2 {
		t.Fatal("executor-exit journey requires more than one page")
	}
	scan := newContinuousScan(t, execution.offer.Checkpoint, len(pages)+2)
	for index, items := range pages {
		if err := scan.AddPage(continuousDocument(items, true)); err != nil {
			t.Fatalf("executor-exit scan page %d: %v", index+1, err)
		}
	}
	if !scan.Complete() || scan.StopReason() != "safe_boundary" || !scan.Quality().MayAdvanceCheckpoint() {
		t.Fatalf("executor-exit scan complete=%v reason=%s quality=%+v", scan.Complete(), scan.StopReason(), scan.Quality())
	}
	candidate, err := scan.CheckpointCandidate()
	if err != nil {
		t.Fatal(err)
	}

	// The first executor has already proved the whole scan safe in memory and
	// commits page one, but exits before it can submit the remaining pages or
	// completion. Replaying this page models a lost database acknowledgement.
	firstArtifact := mustResultArtifact(t, execution.occurrence.OccurrenceID+"-exit-page-1", model.ArtifactPage,
		execution.offer.Work.WorkID, execution.offer.Attempt.AttemptID)
	firstPage := ListingPageResult{CommandID: execution.occurrence.OccurrenceID + "-exit-page-command-1",
		RequestHash: "sha256:" + execution.occurrence.OccurrenceID + "-exit-page-1",
		AttemptID:   execution.offer.Attempt.AttemptID, ExecutorActorID: execution.offer.Attempt.ExecutorActorID,
		ExecutorIncarnation: execution.offer.Attempt.ExecutorIncarnation, PageSequence: 1, ResumeCursor: "page-2",
		Artifact: firstArtifact, ObservedAt: execution.at.Add(time.Second),
		Observations: continuousObservations(t, pages[0], execution.occurrence.OccurrenceID,
			execution.occurrence.SourceID, execution.offer.Attempt, firstArtifact.ArtifactID,
			execution.occurrence.OccurrenceID+"-exit-p1")}
	accepted, err := repository.AcceptListingPage(ctx, firstPage)
	if err != nil || accepted.Replayed || accepted.Progress.PageSequence != 1 {
		t.Fatalf("executor-exit first page = %+v err=%v", accepted, err)
	}
	if replay, err := repository.AcceptListingPage(ctx, firstPage); err != nil || !replay.Replayed {
		t.Fatalf("executor-exit first page lost-reply replay = %+v err=%v", replay, err)
	}

	staleBefore := firstPage.ObservedAt.Add(time.Microsecond)
	recoveredAt := staleBefore.Add(time.Second)
	if _, err := repository.RecoverStaleAttempts(ctx, staleBefore, 500, recoveredAt); err != nil {
		t.Fatal(err)
	}
	abandonedAttempt, _ := repository.GetAttempt(ctx, execution.offer.Attempt.AttemptID)
	waitingWork, _ := repository.GetWork(ctx, execution.offer.Work.WorkID)
	runningOccurrence, _ := repository.GetOccurrence(ctx, execution.occurrence.OccurrenceID)
	var abandonedPermit model.BudgetPermitStatus
	permitErr := repository.db.QueryRowContext(ctx, `SELECT permit_status FROM recruiting_budget_permits WHERE attempt_id = ?`,
		execution.offer.Attempt.AttemptID).Scan(&abandonedPermit)
	if abandonedAttempt.Status != model.AttemptExpired || waitingWork.Status != model.WorkWaitingRetry ||
		runningOccurrence.Status != model.OccurrenceRunning || permitErr != nil || abandonedPermit != model.PermitExpired {
		t.Fatalf("executor-exit recovery attempt=%+v work=%+v occurrence=%+v permit=%s err=%v",
			abandonedAttempt, waitingWork, runningOccurrence, abandonedPermit, permitErr)
	}

	// A message buffered by the dead incarnation cannot fill the missing page;
	// only its Artifact survives as rejected diagnostic evidence.
	lateArtifact := mustResultArtifact(t, execution.occurrence.OccurrenceID+"-late-page-2", model.ArtifactPage,
		execution.offer.Work.WorkID, execution.offer.Attempt.AttemptID)
	latePage := ListingPageResult{AttemptID: execution.offer.Attempt.AttemptID,
		ExecutorActorID:     execution.offer.Attempt.ExecutorActorID,
		ExecutorIncarnation: execution.offer.Attempt.ExecutorIncarnation, PageSequence: 2,
		Artifact: lateArtifact, ObservedAt: recoveredAt.Add(time.Second)}
	if _, err := repository.AcceptListingPage(ctx, latePage); !errors.Is(err, ErrResultFenced) {
		t.Fatalf("expired executor late page was not fenced: %v", err)
	}

	retryAt := recoveredAt.Add(2 * time.Second)
	retryOffer, err := repository.OfferListingExecution(ctx, ListingOfferRequest{
		AttemptID:       execution.occurrence.OccurrenceID + "-retry-attempt",
		ExecutorActorID: "tool:continuous-listing:2", ExecutorIncarnation: execution.occurrence.OccurrenceID + "-retry-boot",
		Capability: "http.fetch", Origin: "https://continuous.example.com", OfferedAt: retryAt,
		BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || retryOffer.Work.WorkID != execution.offer.Work.WorkID || retryOffer.Occurrence == nil ||
		retryOffer.Occurrence.OccurrenceID != execution.occurrence.OccurrenceID ||
		retryOffer.Checkpoint == nil || retryOffer.Checkpoint.Version != execution.offer.Checkpoint.Version {
		t.Fatalf("executor-exit retry offer = %+v err=%v", retryOffer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, retryOffer.Attempt.AttemptID, retryOffer.Attempt.ExecutorActorID,
		retryOffer.Attempt.ExecutorIncarnation, retryAt); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, retryOffer.Attempt.AttemptID, retryOffer.Attempt.ExecutorActorID,
		retryOffer.Attempt.ExecutorIncarnation, retryAt); err != nil {
		t.Fatal(err)
	}

	itemCount := 0
	for index, items := range pages {
		artifact := mustResultArtifact(t, fmt.Sprintf("%s-retry-page-%d", execution.occurrence.OccurrenceID, index+1),
			model.ArtifactPage, retryOffer.Work.WorkID, retryOffer.Attempt.AttemptID)
		observations := continuousObservations(t, items, execution.occurrence.OccurrenceID,
			execution.occurrence.SourceID, retryOffer.Attempt, artifact.ArtifactID,
			fmt.Sprintf("%s-retry-p%d", execution.occurrence.OccurrenceID, index+1))
		input := ListingPageResult{CommandID: fmt.Sprintf("%s-retry-page-command-%d", execution.occurrence.OccurrenceID, index+1),
			RequestHash: fmt.Sprintf("sha256:%s-retry-page-%d", execution.occurrence.OccurrenceID, index+1),
			AttemptID:   retryOffer.Attempt.AttemptID, ExecutorActorID: retryOffer.Attempt.ExecutorActorID,
			ExecutorIncarnation: retryOffer.Attempt.ExecutorIncarnation, PageSequence: uint64(index + 1),
			ResumeCursor: fmt.Sprintf("page-%d", index+2), Terminal: index == len(pages)-1,
			Artifact: artifact, Observations: observations, ObservedAt: retryAt.Add(time.Duration(index+1) * time.Second)}
		page, err := repository.AcceptListingPage(ctx, input)
		if err != nil || page.Replayed || page.Progress.PageSequence != uint64(index+1) {
			t.Fatalf("executor-exit retry page %d = %+v err=%v", index+1, page, err)
		}
		if replay, err := repository.AcceptListingPage(ctx, input); err != nil || !replay.Replayed {
			t.Fatalf("executor-exit retry page %d replay = %+v err=%v", index+1, replay, err)
		}
		itemCount += len(observations)
	}
	quality := scan.Quality()
	completionArtifact := mustResultArtifact(t, execution.occurrence.OccurrenceID+"-retry-completion",
		model.ArtifactListingDelta, retryOffer.Work.WorkID, retryOffer.Attempt.AttemptID)
	completion := ListingCompletion{RequestHash: "sha256:" + execution.occurrence.OccurrenceID + "-retry-completion",
		AttemptID: retryOffer.Attempt.AttemptID, ExecutorActorID: retryOffer.Attempt.ExecutorActorID,
		ExecutorIncarnation: retryOffer.Attempt.ExecutorIncarnation, Artifact: completionArtifact,
		ItemCount: itemCount, CompletedAt: retryAt.Add(time.Duration(len(pages)+2) * time.Second),
		CauseCommandID: execution.occurrence.OccurrenceID + "-retry-completion-command", Progress: model.ListingProgress{
			IdentityComplete: quality.IdentityComplete, PaginationStable: quality.PaginationStable,
			PreviousFrontierReached: quality.PreviousFrontierReached, OverlapCompleted: quality.OverlapCompleted,
			OrderingContractHeld: quality.OrderingContractHeld, SameTimeGroupCompleted: quality.PreviousFrontierReached,
			Candidate: model.IncrementalCheckpoint{FrontierActivityAt: candidate.LastActivityAt,
				FrontierJobKeys: append([]string(nil), candidate.FrontierKeys...)}}}
	completed, err := repository.AcceptListingCompletion(ctx, completion)
	if err != nil || completed.Replayed || completed.Work.Status != model.WorkCompleted || completed.Occurrence == nil ||
		completed.Occurrence.Status != model.OccurrenceCompleted {
		t.Fatalf("executor-exit retry completion = %+v err=%v", completed, err)
	}
	if replay, err := repository.AcceptListingCompletion(ctx, completion); err != nil || !replay.Replayed ||
		replay.Checkpoint.Version != completed.Checkpoint.Version {
		t.Fatalf("executor-exit retry completion lost-reply replay = %+v err=%v", replay, err)
	}
	var abandonedPages, retryPages, rejectedLate, eJobs, eDetails int
	queries := []struct {
		query string
		args  []any
		out   *int
	}{
		{"SELECT COUNT(*) FROM recruiting_listing_page_progress WHERE attempt_id = ?", []any{execution.offer.Attempt.AttemptID}, &abandonedPages},
		{"SELECT COUNT(*) FROM recruiting_listing_page_progress WHERE attempt_id = ?", []any{retryOffer.Attempt.AttemptID}, &retryPages},
		{"SELECT COUNT(*) FROM recruiting_artifacts WHERE artifact_id = ? AND rejected = TRUE", []any{lateArtifact.ArtifactID}, &rejectedLate},
		{"SELECT COUNT(*) FROM recruiting_source_jobs WHERE source_id = ? AND source_job_key = 'e'", []any{execution.occurrence.SourceID}, &eJobs},
		{`SELECT COUNT(*) FROM recruiting_works work JOIN recruiting_source_jobs job ON job.job_id = work.target_id
WHERE job.source_id = ? AND job.source_job_key = 'e' AND work.purpose = 'detail_sync'`, []any{execution.occurrence.SourceID}, &eDetails},
	}
	for _, query := range queries {
		if err := repository.db.QueryRowContext(ctx, query.query, query.args...).Scan(query.out); err != nil {
			t.Fatal(err)
		}
	}
	if abandonedPages != 1 || retryPages != len(pages) || rejectedLate != 1 || eJobs != 1 || eDetails != 1 {
		t.Fatalf("executor-exit facts abandoned_pages=%d retry_pages=%d rejected_late=%d e_jobs=%d e_details=%d",
			abandonedPages, retryPages, rejectedLate, eJobs, eDetails)
	}
	return completed.Checkpoint
}

func assertContinuousUnsafeScan(t *testing.T, checkpoint *model.IncrementalCheckpoint,
	pages [][]continuousListingItem) {
	t.Helper()
	scan := newContinuousScan(t, checkpoint, len(pages))
	for index, items := range pages {
		if err := scan.AddPage(continuousDocument(items, true)); err != nil {
			t.Fatal(err)
		}
		if index < len(pages)-1 && scan.Complete() {
			t.Fatalf("unsafe scan stopped early at page %d", index+1)
		}
	}
	if !scan.Complete() || scan.StopReason() != "max_pages" || scan.Quality().MayAdvanceCheckpoint() {
		t.Fatalf("boundary-missing scan reason=%s quality=%+v", scan.StopReason(), scan.Quality())
	}
	if _, err := scan.CheckpointCandidate(); err == nil {
		t.Fatal("boundary-missing scan produced a checkpoint candidate")
	}
}

func newContinuousScan(t *testing.T, checkpoint *model.IncrementalCheckpoint, maxPages int) *recipeexec.ListingScan {
	t.Helper()
	spec := recipeabi.Spec{ABIVersion: recipeabi.Version, Kind: recipeabi.KindListing,
		RequiredCapability: "http.fetch", Transport: recipeabi.TransportHTTPJSON,
		Request: recipeabi.ReadRequest{Method: "GET", TimeoutMS: 1000, MaxResponseBytes: 1 << 20,
			MaxRedirects: 0, UserAgent: "continuous-contract-test"},
		Extraction: recipeabi.Extraction{Collection: "/jobs", Fields: map[string]string{
			"job_key": "/job_key", "activity_at": "/activity_at", "detail_url": "/detail_url", "fingerprint": "/fingerprint",
		}}, Listing: &recipeabi.ListingContract{IdentityField: "job_key", DetailURLField: "detail_url",
			ActivityField: "activity_at", BoundaryMode: "activity_time", Ordering: "newest_activity_desc",
			UpdateRetop: true, OverlapPages: 1, MaxPages: maxPages, MaxItemsPerPage: 500,
			MaxTotalBytes: 1 << 20, FrontierWidth: 100}}
	var input *recipeabi.CheckpointRef
	if checkpoint != nil {
		input = &recipeabi.CheckpointRef{Version: checkpoint.Version,
			LastActivityAt: checkpoint.FrontierActivityAt, FrontierKeys: append([]string(nil), checkpoint.FrontierJobKeys...)}
	}
	scan, err := recipeexec.NewListingScan(spec, input)
	if err != nil {
		t.Fatal(err)
	}
	return scan
}

func continuousDocument(items []continuousListingItem, hasNext bool) recipeexec.DocumentResult {
	rows := make([]map[string]json.RawMessage, len(items))
	for index, item := range items {
		rows[index] = map[string]json.RawMessage{}
		rows[index]["job_key"], _ = json.Marshal(item.key)
		rows[index]["activity_at"], _ = json.Marshal(item.activity.Format(time.RFC3339))
		rows[index]["detail_url"], _ = json.Marshal("https://continuous.example.com/jobs/" + item.key)
		rows[index]["fingerprint"], _ = json.Marshal(item.fingerprint)
	}
	var next json.RawMessage
	if hasNext {
		next = json.RawMessage(`"next"`)
	}
	return recipeexec.DocumentResult{Items: rows, Next: next, Quality: recipeabi.QualityProof{
		IdentityComplete: true, OrderingContractHeld: true, PaginationStable: true, ItemCount: len(rows),
	}}
}

func continuousObservations(t *testing.T, items []continuousListingItem, evidenceID, sourceID string,
	attempt model.Attempt, artifactID, prefix string) []model.ListingObservation {
	t.Helper()
	observations := make([]model.ListingObservation, len(items))
	for index, item := range items {
		observation, err := model.NewListingObservation(model.ListingObservation{
			ObservationID: fmt.Sprintf("%s-observation-%d-%s", prefix, index, item.key), OccurrenceID: evidenceID,
			SourceID: sourceID, SourceJobKey: item.key, DetailURL: "https://continuous.example.com/jobs/" + item.key,
			ActivityAt: item.activity.Format(time.RFC3339), ListingFingerprint: item.fingerprint,
			RecipeID: attempt.RecipeID, RecipeVersion: attempt.RecipeVersion, ArtifactID: artifactID,
		})
		if err != nil {
			t.Fatal(err)
		}
		observations[index] = observation
	}
	return observations
}

func continuousKeys(items []continuousListingItem) []string {
	keys := make([]string, len(items))
	for index := range items {
		keys[index] = items[index].key
	}
	return keys
}

func repairContinuousDailyWork(t *testing.T, ctx context.Context, repository *Repository,
	failed model.Work, at time.Time) model.Work {
	t.Helper()
	record, err := repository.GetWorkRecord(ctx, failed.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := failed.Complete(failed.Version, model.ResolutionTerminated, "human:continuous-operator",
		"boundary disappeared; inspect evidence and retry after repair")
	if err != nil {
		t.Fatal(err)
	}
	resolveReceipt, _ := model.NewCommandReceipt("continuous-d5-resolve", "recruiting.work.resolve",
		"sha256:continuous-d5-resolve", json.RawMessage(`{"resolution":"terminated"}`))
	resolveEvent, _ := model.NewEventIntent("continuous-d5-resolve-event", "work.resolved", "work", resolved.WorkID,
		resolved.Version, at.Format(time.RFC3339Nano), resolveReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyWorkCommand(ctx, failed.Version, resolved, resolveReceipt, resolveEvent, at); err != nil {
		t.Fatal(err)
	}
	retry, err := model.NewRetryWork(resolved, "continuous-d6-retry-work", "human:continuous-operator", "message-continuous-d6-retry")
	if err != nil {
		t.Fatal(err)
	}
	placement := record.Placement
	placement.BusinessKey = "retry|" + failed.WorkID + "|" + retry.WorkID
	placement.NotBefore = at.Add(time.Second)
	retryReceipt, _ := model.NewCommandReceipt("continuous-d6-retry", "recruiting.work.retry",
		"sha256:continuous-d6-retry", json.RawMessage(`{"work_id":"continuous-d6-retry-work"}`))
	retryEvent, _ := model.NewEventIntent("continuous-d6-retry-event", "work.retry_created", "work", retry.WorkID,
		retry.Version, placement.NotBefore.Format(time.RFC3339Nano), retryReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyRetryWorkCommand(ctx, resolved.Version, resolved.WorkID, retry, placement,
		retryReceipt, retryEvent, placement.NotBefore); err != nil {
		t.Fatal(err)
	}
	if replay, err := repository.ApplyRetryWorkCommand(ctx, resolved.Version, resolved.WorkID, retry, placement,
		retryReceipt, retryEvent, placement.NotBefore); err != nil || !replay.Replayed {
		t.Fatalf("D6 retry replay = %+v err=%v", replay, err)
	}
	return retry
}

func assertContinuousCounts(t *testing.T, ctx context.Context, db *sql.DB, sourceID string,
	wantJobs, wantDetailWorks, wantObservations int) {
	t.Helper()
	var jobs, detailWorks, observations int
	err := db.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM recruiting_source_jobs WHERE source_id = ?),
  (SELECT COUNT(*) FROM recruiting_works work JOIN recruiting_source_jobs job ON job.job_id = work.target_id
    WHERE job.source_id = ? AND work.purpose = 'detail_sync'),
  (SELECT COUNT(*) FROM recruiting_listing_observations WHERE source_id = ?)`, sourceID, sourceID, sourceID).
		Scan(&jobs, &detailWorks, &observations)
	if err != nil {
		t.Fatal(err)
	}
	if jobs != wantJobs || detailWorks != wantDetailWorks || observations != wantObservations {
		t.Fatalf("continuous counts jobs=%d/%d detail_works=%d/%d observations=%d/%d",
			jobs, wantJobs, detailWorks, wantDetailWorks, observations, wantObservations)
	}
}

func assertContinuousJourneyFinal(t *testing.T, ctx context.Context, db *sql.DB,
	sourceID, repairedOccurrenceID, failedWorkID, retryWorkID string) {
	t.Helper()
	var checkpointVersion, occurrenceCount, completedOccurrences, pageProgress, grantedPermits int
	var failedStatus, failedResolution, retryStatus, retryResolution, occurrenceWorkID string
	err := db.QueryRowContext(ctx, `SELECT
  (SELECT checkpoint_version FROM recruiting_checkpoints WHERE source_id = ?),
  (SELECT COUNT(*) FROM recruiting_source_occurrences WHERE source_id = ?),
  (SELECT COUNT(*) FROM recruiting_source_occurrences WHERE source_id = ? AND status = 'completed'),
  (SELECT COUNT(*) FROM recruiting_listing_page_progress progress
     JOIN recruiting_works work ON work.work_id = progress.work_id
   WHERE work.target_id = ? AND work.purpose IN ('baseline_listing','listing_sync')),
  (SELECT status FROM recruiting_works WHERE work_id = ?),
  (SELECT resolution FROM recruiting_works WHERE work_id = ?),
  (SELECT status FROM recruiting_works WHERE work_id = ?),
  (SELECT resolution FROM recruiting_works WHERE work_id = ?),
	(SELECT listing_work_id FROM recruiting_source_occurrences WHERE occurrence_id = ?),
  (SELECT COUNT(*) FROM recruiting_budget_permits permit
     JOIN recruiting_attempts attempt ON attempt.attempt_id = permit.attempt_id
     JOIN recruiting_works work ON work.work_id = attempt.work_id
     LEFT JOIN recruiting_source_jobs job ON job.job_id = work.target_id
   WHERE permit.permit_status = 'granted' AND (work.target_id = ? OR job.source_id = ?))`,
		sourceID, sourceID, sourceID, sourceID, failedWorkID, failedWorkID, retryWorkID, retryWorkID,
		repairedOccurrenceID, sourceID, sourceID).
		Scan(&checkpointVersion, &occurrenceCount, &completedOccurrences, &pageProgress,
			&failedStatus, &failedResolution, &retryStatus, &retryResolution, &occurrenceWorkID,
			&grantedPermits)
	if err != nil {
		t.Fatal(err)
	}
	if checkpointVersion != 6 || occurrenceCount != 5 || completedOccurrences != 5 || pageProgress != 14 ||
		failedStatus != string(model.WorkCompleted) || failedResolution != string(model.ResolutionTerminated) ||
		retryStatus != string(model.WorkCompleted) || retryResolution != string(model.ResolutionSucceeded) ||
		occurrenceWorkID != retryWorkID || grantedPermits != 0 {
		t.Fatalf("final facts checkpoint=%d occurrences=%d/%d pages=%d failed=%s/%s retry=%s/%s occurrence_work=%s granted_permits=%d",
			checkpointVersion, completedOccurrences, occurrenceCount, pageProgress, failedStatus, failedResolution,
			retryStatus, retryResolution, occurrenceWorkID, grantedPermits)
	}
}
