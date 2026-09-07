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
	result := fixture.result("detail-artifact-accepted")
	outcome, err := repository.AcceptDetailResult(ctx, result)
	if err != nil || outcome.Replayed || !outcome.ContentChanged || outcome.Job.Status != model.JobAvailable || outcome.Job.DetailVersion != 1 {
		t.Fatalf("detail acceptance = %+v %v", outcome, err)
	}
	replay, err := repository.AcceptDetailResult(ctx, result)
	if err != nil || !replay.Replayed || replay.Job.DetailVersion != 1 {
		t.Fatalf("detail replay = %+v %v", replay, err)
	}
	work, _ := repository.GetWork(ctx, fixture.work.WorkID)
	attempt, _ := repository.GetAttempt(ctx, fixture.attempt.AttemptID)
	if work.Status != model.WorkCompleted || attempt.Status != model.AttemptSucceeded {
		t.Fatalf("terminal work/attempt = %+v %+v", work, attempt)
	}
	var details, artifacts int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_job_detail_versions WHERE job_id = ?", fixture.job.JobID).Scan(&details); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_artifacts WHERE attempt_id = ? AND rejected = FALSE", fixture.attempt.AttemptID).Scan(&artifacts); err != nil {
		t.Fatal(err)
	}
	if details != 1 || artifacts != 1 {
		t.Fatalf("detail facts=%d artifacts=%d", details, artifacts)
	}
}

func TestStaleOrWrongSenderDetailResultOnlyKeepsRejectedArtifact(t *testing.T) {
	repository, db, ctx, cleanup := detailContractRepository(t)
	defer cleanup()
	now := time.Date(2026, 9, 8, 6, 0, 0, 0, time.UTC)
	wrongSender := createDetailFixture(t, ctx, repository, "detail-wrong-sender", now)
	wrong := wrongSender.result("detail-artifact-wrong-sender")
	wrong.ExecutorIncarnation = "stale-incarnation"
	if _, err := repository.AcceptDetailResult(ctx, wrong); !errors.Is(err, ErrResultFenced) {
		t.Fatalf("wrong executor sender = %v", err)
	}
	assertRejectedOnly(t, ctx, db, repository, wrongSender, wrong.Artifact.ArtifactID)

	stale := createDetailFixture(t, ctx, repository, "detail-stale-source", now.Add(time.Minute))
	currentSource, _ := repository.GetSource(ctx, stale.source.SourceID)
	degraded, _ := currentSource.SetHealth(currentSource.Version, model.HealthDegraded)
	if err := repository.UpdateSourceCAS(ctx, currentSource.Version, degraded, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	staleResult := stale.result("detail-artifact-stale-source")
	if _, err := repository.AcceptDetailResult(ctx, staleResult); !errors.Is(err, ErrResultFenced) {
		t.Fatalf("stale source result = %v", err)
	}
	assertRejectedOnly(t, ctx, db, repository, stale, staleResult.Artifact.ArtifactID)
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
		DetailJSON: json.RawMessage(`{"title":"Engineer"}`), ObservedAt: f.now.Add(time.Minute),
	}
}

func createDetailFixture(t *testing.T, ctx context.Context, repository *Repository, prefix string, now time.Time) detailFixture {
	t.Helper()
	company, _ := model.NewCompany(prefix+"-company", prefix, "https://"+prefix+".example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
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
	ready, _ := validating.PublishValidated(validating.Version, listingAssignment)
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
		Origin: prefix + ".example.com", Capability: "http.fetch", Priority: 10, NotBefore: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	work, _ := listing.DetailWork.Start(listing.DetailWork.Version)
	if err := repository.UpdateWorkCAS(ctx, listing.DetailWork.Version, work, now); err != nil {
		t.Fatal(err)
	}
	attempt, _ := model.NewAttempt(prefix+"-attempt", work)
	attempt, _ = attempt.BindExecutor("executor-a", "incarnation-a", "http.fetch")
	attempt, _ = attempt.WithFence(model.AttemptFence{
		CompanyVersion: company.Version, SourceVersion: readyWithDetail.Version,
		AssignmentVersion: detailAssignment.AssignmentVersion, RecipeID: detailRecipe.RecipeID,
		RecipeVersion: detailRecipe.Version, RefreshGeneration: listing.Job.RefreshGeneration,
	})
	if err := repository.CreateAttempt(ctx, attempt, now); err != nil {
		t.Fatal(err)
	}
	accepted, _ := attempt.Accept()
	if err := repository.UpdateAttemptCAS(ctx, attempt.Status, accepted, now); err != nil {
		t.Fatal(err)
	}
	running, _ := accepted.Start()
	if err := repository.UpdateAttemptCAS(ctx, accepted.Status, running, now); err != nil {
		t.Fatal(err)
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
