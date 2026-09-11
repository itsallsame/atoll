package store

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestBindWorkPlacementScopeRejectsCallerScopeForgery(t *testing.T) {
	bound, err := bindWorkPlacementScope(WorkPlacement{}, "company-1", "source-1")
	if err != nil || bound.CompanyID != "company-1" || bound.SourceID != "source-1" {
		t.Fatalf("derived Work scope=%+v err=%v", bound, err)
	}
	if _, err := bindWorkPlacementScope(WorkPlacement{CompanyID: "company-2"}, "company-1", "source-1"); err == nil {
		t.Fatal("accepted caller-forged Company Work scope")
	}
	if _, err := bindWorkPlacementScope(WorkPlacement{SourceID: "source-2"}, "company-1", "source-1"); err == nil {
		t.Fatal("accepted caller-forged Source Work scope")
	}
}

func TestInFlightResultDistinguishesDrainFromCancelFence(t *testing.T) {
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

	for _, test := range []struct {
		prefix string
		mode   model.PauseMode
		accept bool
	}{
		{prefix: "scope-drain-result", mode: model.PauseDrain, accept: true},
		{prefix: "scope-cancel-result", mode: model.PauseCancel, accept: false},
	} {
		t.Run(string(test.mode), func(t *testing.T) {
			offerAt, _ := prepareListingExecutionWork(t, ctx, repository, test.prefix, 1)
			offer := startListingAttempt(t, ctx, repository, test.prefix, offerAt)
			if offer.Attempt.SourceConfigurationVersion == 0 || offer.Attempt.SourceExecutionFence == 0 {
				t.Fatalf("offer omitted separated Source fences: %+v", offer.Attempt)
			}
			source, err := repository.GetSource(ctx, offer.Occurrence.SourceID)
			if err != nil {
				t.Fatal(err)
			}
			paused, err := source.Pause(source.Version, test.mode)
			if err != nil {
				t.Fatal(err)
			}
			if err := repository.UpdateSourceCAS(ctx, source.Version, paused, offerAt.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			artifact := mustResultArtifact(t, test.prefix+"-page", model.ArtifactPage,
				offer.Work.WorkID, offer.Attempt.AttemptID)
			outcome, resultErr := repository.AcceptListingPage(ctx, ListingPageResult{
				AttemptID: offer.Attempt.AttemptID, ExecutorActorID: offer.Attempt.ExecutorActorID,
				ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, PageSequence: 1, Terminal: true,
				Artifact: artifact, ObservedAt: offerAt.Add(2 * time.Second),
			})
			if test.accept {
				if resultErr != nil || outcome.Progress.PageSequence != 1 {
					t.Fatalf("drain rejected valid in-flight result: %+v err=%v", outcome, resultErr)
				}
			} else if !errors.Is(resultErr, ErrResultFenced) {
				t.Fatalf("cancel accepted stale in-flight result: %+v err=%v", outcome, resultErr)
			}
		})
	}
}

func TestDetailResultUsesSeparatedScopeFences(t *testing.T) {
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
	now := time.Date(2095, 2, 12, 4, 0, 0, 0, time.UTC)

	for index, test := range []struct {
		name   string
		change func(model.RecruitmentSource) (model.RecruitmentSource, error)
		accept bool
	}{
		{name: "drain", accept: true, change: func(source model.RecruitmentSource) (model.RecruitmentSource, error) {
			return source.Pause(source.Version, model.PauseDrain)
		}},
		{name: "cancel", change: func(source model.RecruitmentSource) (model.RecruitmentSource, error) {
			return source.Pause(source.Version, model.PauseCancel)
		}},
		{name: "configuration_change", change: func(source model.RecruitmentSource) (model.RecruitmentSource, error) {
			return source.StageEndpoint(source.Version, "https://"+source.SourceID+".example.com/jobs-v2", "")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			prefix := "detail-scope-fence-" + test.name
			fixture := createDetailFixture(t, ctx, repository, prefix, now.Add(time.Duration(index)*time.Hour))
			if fixture.attempt.SourceConfigurationVersion == 0 || fixture.attempt.SourceExecutionFence == 0 {
				t.Fatalf("offer omitted separated Source fences: %+v", fixture.attempt)
			}
			current, err := repository.GetSource(ctx, fixture.source.SourceID)
			if err != nil {
				t.Fatal(err)
			}
			changed, err := test.change(current)
			if err != nil {
				t.Fatal(err)
			}
			if err := repository.UpdateSourceCAS(ctx, current.Version, changed, fixture.now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			result := fixture.result(prefix + "-artifact")
			result.ObservedAt = fixture.now.Add(2 * time.Second)
			outcome, resultErr := repository.AcceptDetailResult(ctx, result)
			if test.accept {
				if resultErr != nil || outcome.Job.DetailVersion != 1 {
					t.Fatalf("drain rejected valid in-flight Detail result: %+v err=%v", outcome, resultErr)
				}
				return
			}
			if !errors.Is(resultErr, ErrResultFenced) {
				t.Fatalf("stale Detail result was accepted: %+v err=%v", outcome, resultErr)
			}
			var rejected bool
			if err := db.QueryRowContext(ctx, `SELECT rejected FROM recruiting_artifacts WHERE artifact_id = ?`,
				result.Artifact.ArtifactID).Scan(&rejected); err != nil || !rejected {
				t.Fatalf("rejected Detail Artifact retained=%v err=%v", rejected, err)
			}
		})
	}
}

func TestSourceDiscoveryResultUsesSeparatedCompanyFences(t *testing.T) {
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
	now := time.Date(2095, 2, 13, 4, 0, 0, 0, time.UTC)

	for index, test := range []struct {
		name   string
		change func(model.Company) (model.Company, error)
		accept bool
	}{
		{name: "drain", accept: true, change: func(company model.Company) (model.Company, error) {
			return company.Pause(company.Version, model.PauseDrain)
		}},
		{name: "cancel", change: func(company model.Company) (model.Company, error) {
			return company.Pause(company.Version, model.PauseCancel)
		}},
		{name: "configuration_change", change: func(company model.Company) (model.Company, error) {
			website := "https://" + company.CompanyID + ".example.com/new-careers"
			updated, err := company.Update(company.Version, model.CompanyUpdate{Website: &website})
			return updated.Company, err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			prefix := "discovery-scope-fence-" + test.name
			fixture := createRunningSourceDiscoveryFixture(t, ctx, repository, prefix,
				now.Add(time.Duration(index)*time.Hour))
			if fixture.offer.Attempt.CompanyConfigurationVersion == 0 || fixture.offer.Attempt.CompanyExecutionFence == 0 {
				t.Fatalf("offer omitted separated Company fences: %+v", fixture.offer.Attempt)
			}
			current, err := repository.GetCompany(ctx, fixture.company.CompanyID)
			if err != nil {
				t.Fatal(err)
			}
			changed, err := test.change(current)
			if err != nil {
				t.Fatal(err)
			}
			if err := repository.UpdateCompanyCAS(ctx, current.Version, changed, fixture.now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			outcome, resultErr := repository.AcceptSourceDiscoveryResult(ctx, fixture.result)
			if test.accept {
				if resultErr != nil || outcome.Discovery.Status != model.SourceDiscoveryCompleted ||
					outcome.Discovery.CandidateCount != 1 {
					t.Fatalf("drain rejected valid in-flight Source Discovery result: %+v err=%v", outcome, resultErr)
				}
				return
			}
			if !errors.Is(resultErr, ErrResultFenced) {
				t.Fatalf("stale Source Discovery result was accepted: %+v err=%v", outcome, resultErr)
			}
			var rejected bool
			if err := db.QueryRowContext(ctx, `SELECT rejected FROM recruiting_artifacts WHERE artifact_id = ?`,
				fixture.result.Artifact.ArtifactID).Scan(&rejected); err != nil || !rejected {
				t.Fatalf("rejected Source Discovery Artifact retained=%v err=%v", rejected, err)
			}
			storedDiscovery, err := repository.GetSourceDiscovery(ctx, fixture.discovery.DiscoveryID)
			if err != nil || storedDiscovery.Status != model.SourceDiscoveryRunning || storedDiscovery.CandidateCount != 0 {
				t.Fatalf("fenced result changed Source Discovery: %+v err=%v", storedDiscovery, err)
			}
		})
	}
}

type runningSourceDiscoveryFixture struct {
	company   model.Company
	discovery model.SourceDiscovery
	offer     ExecutionOffer
	result    SourceDiscoveryResult
	now       time.Time
}

func createRunningSourceDiscoveryFixture(t *testing.T, ctx context.Context, repository *Repository,
	prefix string, now time.Time) runningSourceDiscoveryFixture {
	t.Helper()
	website := "https://" + prefix + ".example.com"
	company, err := model.NewCompany(prefix+"-company", "Discovery Scope Fence", website)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	recipe := activeRecipe(t, prefix+"-recipe", model.RecipeDiscovery, prefix+".example.com", 1,
		prefix+"-contract")
	if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
		t.Fatal(err)
	}
	work, _ := model.NewWork(prefix+"-work", "company", company.CompanyID, "source_discovery", "human")
	discovery, err := model.NewSourceDiscovery(prefix+"-discovery", work.WorkID, company, 1, company.Website, recipe)
	if err != nil {
		t.Fatal(err)
	}
	placement := WorkPlacement{BusinessKey: "source-discovery|" + company.CompanyID + "|1", Priority: 80,
		Capability: recipe.Execution.RequiredCapability, Origin: website, NotBefore: now}
	if err := repository.CreateSourceDiscovery(ctx, discovery, work, placement, now); err != nil {
		t.Fatal(err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: prefix + "-attempt",
		ExecutorActorID: "executor-" + prefix, ExecutorIncarnation: "boot-" + prefix,
		Capability: placement.Capability, Origin: placement.Origin, OfferedAt: now,
		BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	artifact := mustResultArtifact(t, prefix+"-artifact", model.ArtifactResponse, work.WorkID, offer.Attempt.AttemptID)
	candidate, err := model.NewSourceDiscoveryCandidate(website+"/careers", "all", website+"/jobs",
		"company careers navigation", artifact.ArtifactID)
	if err != nil {
		t.Fatal(err)
	}
	result := SourceDiscoveryResult{CommandID: prefix + "-result", RequestHash: "sha256:" + prefix + "-result",
		AttemptID: offer.Attempt.AttemptID, ExecutorActorID: offer.Attempt.ExecutorActorID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, Artifact: artifact,
		Candidates: []model.SourceDiscoveryCandidate{candidate}, ObservedAt: now.Add(2 * time.Second)}
	return runningSourceDiscoveryFixture{company: company, discovery: discovery, offer: offer, result: result, now: now}
}

func TestSourceValidationResultUsesSeparatedSourceFences(t *testing.T) {
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
	now := time.Date(2095, 2, 14, 4, 0, 0, 0, time.UTC)

	for index, test := range []struct {
		name   string
		change func(model.RecruitmentSource) (model.RecruitmentSource, error)
		accept bool
	}{
		{name: "drain", accept: true, change: func(source model.RecruitmentSource) (model.RecruitmentSource, error) {
			return source.Pause(source.Version, model.PauseDrain)
		}},
		{name: "cancel", change: func(source model.RecruitmentSource) (model.RecruitmentSource, error) {
			return source.Pause(source.Version, model.PauseCancel)
		}},
		{name: "configuration_change", change: func(source model.RecruitmentSource) (model.RecruitmentSource, error) {
			return source.StageEndpoint(source.Version, "https://"+source.SourceID+".example.com/jobs-v2", "all")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			prefix := "source-validation-scope-fence-" + test.name
			fixture := createRunningSourceValidationFixture(t, ctx, repository, prefix,
				now.Add(time.Duration(index)*time.Hour))
			current, err := repository.GetSource(ctx, fixture.source.SourceID)
			if err != nil {
				t.Fatal(err)
			}
			changed, err := test.change(current)
			if err != nil {
				t.Fatal(err)
			}
			if err := repository.UpdateSourceCAS(ctx, current.Version, changed, fixture.now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			outcome, resultErr := repository.AcceptDiagnosticResult(ctx, fixture.result)
			if test.accept {
				if resultErr != nil || outcome.Work.Status != model.WorkCompleted ||
					outcome.Run.Status != model.ListingRunCompleted {
					t.Fatalf("drain rejected valid Source Validation evidence: %+v err=%v", outcome, resultErr)
				}
				return
			}
			if !errors.Is(resultErr, ErrResultFenced) {
				t.Fatalf("stale Source Validation evidence was accepted: %+v err=%v", outcome, resultErr)
			}
			var rejected int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_artifacts
WHERE attempt_id = ? AND rejected = TRUE`, fixture.offer.Attempt.AttemptID).Scan(&rejected); err != nil || rejected != 2 {
				t.Fatalf("rejected Source Validation Artifacts=%d err=%v", rejected, err)
			}
			storedWork, err := repository.GetWork(ctx, fixture.work.WorkID)
			if err != nil || storedWork.Status != model.WorkRunning {
				t.Fatalf("fenced evidence changed validation Work: %+v err=%v", storedWork, err)
			}
		})
	}
}

type runningSourceValidationFixture struct {
	source model.RecruitmentSource
	work   model.Work
	offer  ExecutionOffer
	result DiagnosticResult
	now    time.Time
}

func createRunningSourceValidationFixture(t *testing.T, ctx context.Context, repository *Repository,
	prefix string, now time.Time) runningSourceValidationFixture {
	t.Helper()
	company, err := model.NewCompany(prefix+"-company", "Source Validation Scope Fence",
		"https://"+prefix+".example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, err := model.NewRecruitmentSource(prefix+"-source", company.CompanyID,
		"https://"+prefix+".example.com/jobs", "all", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	recipe := activeRecipe(t, prefix+"-recipe", model.RecipeListing, prefix+".example.com", 1,
		prefix+"-contract")
	if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
		t.Fatal(err)
	}
	preparation, err := repository.PrepareSourceValidation(ctx, source.SourceID, recipe.RecipeID, recipe.Version)
	if err != nil {
		t.Fatal(err)
	}
	validating, run, err := preparation.NewRun(prefix+"-run", prefix+"-work", 0, now.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	work, _ := model.NewWork(run.WorkID, "source", source.SourceID, "source_validation", "manual")
	work, _ = work.WithCausality("human:scope-fence", prefix+"-message", "")
	placement := WorkPlacement{BusinessKey: "source-validation|" + run.ListingRunID, Priority: 300,
		Capability: run.ListingExecution.Execution.RequiredCapability, Origin: run.ListingExecution.Origin, NotBefore: now}
	receipt, _ := model.NewCommandReceipt(prefix+"-create", "recruiting.source.validate",
		"sha256:"+prefix+"-create", json.RawMessage(`{}`))
	event, _ := model.NewEventIntent(prefix+"-event", "source.validation_started", "source", source.SourceID,
		validating.Version, now.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplySourceValidationCommand(ctx, source.Version, 0, validating, run, work, placement,
		receipt, event, nil, now); err != nil {
		t.Fatal(err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: prefix + "-attempt",
		ExecutorActorID: "executor-" + prefix, ExecutorIncarnation: "boot-" + prefix,
		Capability: placement.Capability, Origin: placement.Origin, OfferedAt: now,
		BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	page := mustResultArtifact(t, prefix+"-page", model.ArtifactPage, work.WorkID, offer.Attempt.AttemptID)
	trace := mustResultArtifact(t, prefix+"-trace", model.ArtifactTrace, work.WorkID, offer.Attempt.AttemptID)
	result := DiagnosticResult{CommandID: prefix + "-result", RequestHash: "sha256:" + prefix + "-result",
		AttemptID: offer.Attempt.AttemptID, ExecutorActorID: offer.Attempt.ExecutorActorID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, ResultKind: "source_validation",
		Artifacts: []model.ArtifactMetadata{page, trace}, Quality: executioncontract.ListingQuality{
			IdentityComplete: true, OrderingContractHeld: true, PaginationStable: true, ItemCount: 3},
		CompletedAt: now.Add(2 * time.Second)}
	return runningSourceValidationFixture{source: validating, work: work, offer: offer, result: result, now: now}
}

func TestDetailRecipeSampleResultUsesSeparatedSourceFences(t *testing.T) {
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
	now := time.Date(2095, 2, 15, 4, 0, 0, 0, time.UTC)

	for index, test := range []struct {
		name   string
		change func(model.RecruitmentSource) (model.RecruitmentSource, error)
		accept bool
	}{
		{name: "drain", accept: true, change: func(source model.RecruitmentSource) (model.RecruitmentSource, error) {
			return source.Pause(source.Version, model.PauseDrain)
		}},
		{name: "cancel", change: func(source model.RecruitmentSource) (model.RecruitmentSource, error) {
			return source.Pause(source.Version, model.PauseCancel)
		}},
		{name: "configuration_change", change: func(source model.RecruitmentSource) (model.RecruitmentSource, error) {
			return source.StageEndpoint(source.Version, "https://"+source.SourceID+".example.com/jobs-v2", "all")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			prefix := "detail-recipe-scope-fence-" + test.name
			fixture := createRunningDetailRecipeSampleFixture(t, ctx, repository, prefix,
				now.Add(time.Duration(index)*time.Hour))
			current, err := repository.GetSource(ctx, fixture.source.SourceID)
			if err != nil {
				t.Fatal(err)
			}
			changed, err := test.change(current)
			if err != nil {
				t.Fatal(err)
			}
			if err := repository.UpdateSourceCAS(ctx, current.Version, changed, fixture.now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			outcome, resultErr := repository.AcceptRecipeSampleValidationResult(ctx, fixture.result)
			if test.accept {
				if resultErr != nil || outcome.Work.Status != model.WorkCompleted ||
					outcome.Run.Status != model.RecipeSampleValidationCompleted || outcome.Artifacts != 2 {
					t.Fatalf("drain rejected valid Detail Recipe evidence: %+v err=%v", outcome, resultErr)
				}
				return
			}
			if !errors.Is(resultErr, ErrResultFenced) {
				t.Fatalf("stale Detail Recipe evidence was accepted: %+v err=%v", outcome, resultErr)
			}
			var rejected, detailVersions int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_artifacts
WHERE attempt_id = ? AND rejected = TRUE`, fixture.offer.Attempt.AttemptID).Scan(&rejected); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_job_detail_versions
WHERE job_id = ?`, fixture.job.JobID).Scan(&detailVersions); err != nil {
				t.Fatal(err)
			}
			if rejected != 2 || detailVersions != 0 {
				t.Fatalf("fenced Detail Recipe evidence rejected=%d detail_versions=%d", rejected, detailVersions)
			}
		})
	}
}

type runningDetailRecipeSampleFixture struct {
	source model.RecruitmentSource
	job    model.SourceJob
	offer  ExecutionOffer
	result RecipeSampleValidationResult
	now    time.Time
}

func createRunningDetailRecipeSampleFixture(t *testing.T, ctx context.Context, repository *Repository,
	prefix string, now time.Time) runningDetailRecipeSampleFixture {
	t.Helper()
	company := persistExecutionReadyCompany(t, ctx, repository, prefix, now)
	source := persistExecutionReadySource(t, ctx, repository, company, prefix, prefix+"-source", now)
	assignment, err := repository.GetAssignment(ctx, source.SourceID, model.RecipeDetail)
	if err != nil {
		t.Fatal(err)
	}
	job, err := model.NewSourceJob(prefix+"-job", source.SourceID, prefix+"-external",
		"https://"+prefix+".example.com/jobs/1")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertJob(ctx, tx, job, now); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	candidate, err := model.NewRecipe(prefix+"-candidate", model.RecipeDetail, prefix+".example.com", 2,
		"sha256:"+prefix+"-candidate", assignment.ContractHash, model.RecipeExecution{
			ABIVersion: model.RecipeABIVersion, ContentRef: "recipe://" + prefix + "/candidate",
			RequiredCapability: "http.fetch", Transport: model.RecipeTransportHTTPHTML})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateRecipe(ctx, candidate, now); err != nil {
		t.Fatal(err)
	}
	preparation, err := repository.PrepareDetailRecipeValidation(ctx, source.SourceID, candidate.RecipeID,
		candidate.Version, job.JobID)
	if err != nil {
		t.Fatal(err)
	}
	validating, run, err := preparation.NewRun(prefix+"-run", prefix+"-work", 2, now.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	targetID := fmt.Sprintf("%s@%d", candidate.RecipeID, candidate.Version)
	work, _ := model.NewWork(run.WorkID, "recipe", targetID, "recipe_validation", "manual")
	work, _ = work.WithCausality("human:scope-fence", prefix+"-message", "")
	placement := WorkPlacement{BusinessKey: "recipe-validation|" + run.ValidationRunID, Priority: 400,
		Capability: candidate.Execution.RequiredCapability, Origin: run.Origin, NotBefore: now}
	receipt, _ := model.NewCommandReceipt(prefix+"-create", "recruiting.recipe.validate",
		"sha256:"+prefix+"-create", json.RawMessage(`{}`))
	event, _ := model.NewEventIntent(prefix+"-event", "recipe.validation_started", "recipe", targetID,
		validating.StateVersion, now.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyDetailRecipeValidationCommand(ctx, candidate.StateVersion, validating, run, work,
		placement, receipt, event, nil, now); err != nil {
		t.Fatal(err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: prefix + "-attempt",
		ExecutorActorID: "executor-" + prefix, ExecutorIncarnation: "boot-" + prefix,
		Capability: placement.Capability, Origin: placement.Origin, OfferedAt: now,
		BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	response := mustResultArtifact(t, prefix+"-response", model.ArtifactResponse, work.WorkID, offer.Attempt.AttemptID)
	trace := mustResultArtifact(t, prefix+"-trace", model.ArtifactTrace, work.WorkID, offer.Attempt.AttemptID)
	result := RecipeSampleValidationResult{CommandID: prefix + "-result", RequestHash: "sha256:" + prefix + "-result",
		AttemptID: offer.Attempt.AttemptID, ExecutorActorID: offer.Attempt.ExecutorActorID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, ResultKind: "recipe_sample_validation",
		RecipeKind: model.RecipeDetail, Artifacts: []model.ArtifactMetadata{response, trace}, RecordCount: 1,
		ExtractedFieldCount: 2, NormalizedContentHash: "sha256:" + prefix + "-normalized",
		CompletedAt: now.Add(2 * time.Second)}
	return runningDetailRecipeSampleFixture{source: source, job: job, offer: offer, result: result, now: now}
}

func TestListingRecipeValidationResultUsesSeparatedSourceFences(t *testing.T) {
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
	now := time.Date(2095, 2, 16, 4, 0, 0, 0, time.UTC)

	for index, test := range []struct {
		name   string
		change func(model.RecruitmentSource) (model.RecruitmentSource, error)
		accept bool
	}{
		{name: "drain", accept: true, change: func(source model.RecruitmentSource) (model.RecruitmentSource, error) {
			return source.Pause(source.Version, model.PauseDrain)
		}},
		{name: "cancel", change: func(source model.RecruitmentSource) (model.RecruitmentSource, error) {
			return source.Pause(source.Version, model.PauseCancel)
		}},
		{name: "configuration_change", change: func(source model.RecruitmentSource) (model.RecruitmentSource, error) {
			return source.StageEndpoint(source.Version, "https://"+source.SourceID+".example.com/jobs-v2", "all")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			prefix := "listing-recipe-scope-fence-" + test.name
			fixture := createRunningListingRecipeValidationFixture(t, ctx, repository, prefix,
				now.Add(time.Duration(index)*time.Hour))
			if fixture.offer.Attempt.SourceConfigurationVersion == 0 {
				t.Fatalf("Recipe validation Attempt omitted Source scope: %+v", fixture.offer.Attempt)
			}
			current, err := repository.GetSource(ctx, fixture.source.SourceID)
			if err != nil {
				t.Fatal(err)
			}
			changed, err := test.change(current)
			if err != nil {
				t.Fatal(err)
			}
			if err := repository.UpdateSourceCAS(ctx, current.Version, changed, fixture.now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			outcome, resultErr := repository.AcceptDiagnosticResult(ctx, fixture.result)
			if test.accept {
				if resultErr != nil || outcome.Work.Status != model.WorkCompleted ||
					outcome.Run.Status != model.ListingRunCompleted {
					t.Fatalf("drain rejected valid Listing Recipe evidence: %+v err=%v", outcome, resultErr)
				}
				return
			}
			if !errors.Is(resultErr, ErrResultFenced) {
				t.Fatalf("stale Listing Recipe evidence was accepted: %+v err=%v", outcome, resultErr)
			}
			var rejected, observations int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_artifacts
WHERE attempt_id = ? AND rejected = TRUE`, fixture.offer.Attempt.AttemptID).Scan(&rejected); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_listing_observations
WHERE source_id = ?`, fixture.source.SourceID).Scan(&observations); err != nil {
				t.Fatal(err)
			}
			if rejected != 2 || observations != 0 {
				t.Fatalf("fenced Listing Recipe evidence rejected=%d observations=%d", rejected, observations)
			}
		})
	}
}

type runningListingRecipeValidationFixture struct {
	source model.RecruitmentSource
	offer  ExecutionOffer
	result DiagnosticResult
	now    time.Time
}

func createRunningListingRecipeValidationFixture(t *testing.T, ctx context.Context, repository *Repository,
	prefix string, now time.Time) runningListingRecipeValidationFixture {
	t.Helper()
	company := persistExecutionReadyCompany(t, ctx, repository, prefix, now)
	source := persistExecutionReadySource(t, ctx, repository, company, prefix, prefix+"-source", now)
	assignment, err := repository.GetAssignment(ctx, source.SourceID, model.RecipeListing)
	if err != nil {
		t.Fatal(err)
	}
	candidate, err := model.NewRecipe(prefix+"-candidate", model.RecipeListing, prefix+".example.com", 2,
		"sha256:"+prefix+"-candidate", assignment.ContractHash, model.RecipeExecution{
			ABIVersion: model.RecipeABIVersion, ContentRef: "recipe://" + prefix + "/candidate",
			RequiredCapability: "http.fetch", Transport: model.RecipeTransportHTTPJSON})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateRecipe(ctx, candidate, now); err != nil {
		t.Fatal(err)
	}
	preparation, err := repository.PrepareRecipeValidation(ctx, source.SourceID, candidate.RecipeID, candidate.Version)
	if err != nil {
		t.Fatal(err)
	}
	validating, run, err := preparation.NewRun(prefix+"-run", prefix+"-work", now.Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	targetID := fmt.Sprintf("%s@%d", candidate.RecipeID, candidate.Version)
	work, _ := model.NewWork(run.WorkID, "recipe", targetID, "recipe_validation", "manual")
	work, _ = work.WithCausality("human:scope-fence", prefix+"-message", "")
	placement := WorkPlacement{BusinessKey: "recipe-validation|" + run.ListingRunID, Priority: 400,
		Capability: validating.Execution.RequiredCapability, Origin: run.ListingExecution.Origin, NotBefore: now}
	receipt, _ := model.NewCommandReceipt(prefix+"-create", "recruiting.recipe.validate",
		"sha256:"+prefix+"-create", json.RawMessage(`{}`))
	event, _ := model.NewEventIntent(prefix+"-event", "recipe.validation_started", "recipe", targetID,
		validating.StateVersion, now.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyRecipeValidationCommand(ctx, candidate.StateVersion, validating, run, work, placement,
		receipt, event, nil, now); err != nil {
		t.Fatal(err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: prefix + "-attempt",
		ExecutorActorID: "executor-" + prefix, ExecutorIncarnation: "boot-" + prefix,
		Capability: placement.Capability, Origin: placement.Origin, OfferedAt: now,
		BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	page := mustResultArtifact(t, prefix+"-page", model.ArtifactPage, work.WorkID, offer.Attempt.AttemptID)
	trace := mustResultArtifact(t, prefix+"-trace", model.ArtifactTrace, work.WorkID, offer.Attempt.AttemptID)
	result := DiagnosticResult{CommandID: prefix + "-result", RequestHash: "sha256:" + prefix + "-result",
		AttemptID: offer.Attempt.AttemptID, ExecutorActorID: offer.Attempt.ExecutorActorID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, ResultKind: "recipe_validation",
		Artifacts: []model.ArtifactMetadata{page, trace}, Quality: executioncontract.ListingQuality{
			IdentityComplete: true, OrderingContractHeld: true, PaginationStable: true, ItemCount: 3},
		CompletedAt: now.Add(2 * time.Second)}
	return runningListingRecipeValidationFixture{source: source, offer: offer, result: result, now: now}
}

func TestDiscoveryRecipeValidationResultUsesSeparatedCompanyFences(t *testing.T) {
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
	now := time.Date(2095, 2, 17, 4, 0, 0, 0, time.UTC)

	for index, test := range []struct {
		name   string
		change func(model.Company) (model.Company, error)
		accept bool
	}{
		{name: "drain", accept: true, change: func(company model.Company) (model.Company, error) {
			return company.Pause(company.Version, model.PauseDrain)
		}},
		{name: "cancel", change: func(company model.Company) (model.Company, error) {
			return company.Pause(company.Version, model.PauseCancel)
		}},
		{name: "configuration_change", change: func(company model.Company) (model.Company, error) {
			website := "https://" + company.CompanyID + ".example.com/new-careers"
			updated, err := company.Update(company.Version, model.CompanyUpdate{Website: &website})
			return updated.Company, err
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			prefix := "discovery-recipe-scope-fence-" + test.name
			fixture := createRunningDiscoveryRecipeValidationFixture(t, ctx, repository, prefix,
				now.Add(time.Duration(index)*time.Hour))
			if fixture.offer.Attempt.CompanyConfigurationVersion == 0 {
				t.Fatalf("Discovery Recipe Attempt omitted Company scope: %+v", fixture.offer.Attempt)
			}
			current, err := repository.GetCompany(ctx, fixture.company.CompanyID)
			if err != nil {
				t.Fatal(err)
			}
			changed, err := test.change(current)
			if err != nil {
				t.Fatal(err)
			}
			if err := repository.UpdateCompanyCAS(ctx, current.Version, changed, fixture.now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			outcome, resultErr := repository.AcceptRecipeSampleValidationResult(ctx, fixture.result)
			if test.accept {
				if resultErr != nil || outcome.Work.Status != model.WorkCompleted ||
					outcome.Run.Status != model.RecipeSampleValidationCompleted {
					t.Fatalf("drain rejected valid Discovery Recipe evidence: %+v err=%v", outcome, resultErr)
				}
				return
			}
			if !errors.Is(resultErr, ErrResultFenced) {
				t.Fatalf("stale Discovery Recipe evidence was accepted: %+v err=%v", outcome, resultErr)
			}
			var rejected, sources int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_artifacts
WHERE attempt_id = ? AND rejected = TRUE`, fixture.offer.Attempt.AttemptID).Scan(&rejected); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_sources
WHERE company_id = ?`, fixture.company.CompanyID).Scan(&sources); err != nil {
				t.Fatal(err)
			}
			if rejected != 2 || sources != 0 {
				t.Fatalf("fenced Discovery Recipe evidence rejected=%d sources=%d", rejected, sources)
			}
		})
	}
}

type runningDiscoveryRecipeValidationFixture struct {
	company model.Company
	offer   ExecutionOffer
	result  RecipeSampleValidationResult
	now     time.Time
}

func createRunningDiscoveryRecipeValidationFixture(t *testing.T, ctx context.Context, repository *Repository,
	prefix string, now time.Time) runningDiscoveryRecipeValidationFixture {
	t.Helper()
	company, err := model.NewCompany(prefix+"-company", "Discovery Recipe Scope Fence",
		"https://"+prefix+".example.com/careers")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	candidate, err := model.NewRecipe(prefix+"-candidate", model.RecipeDiscovery, prefix+".example.com", 1,
		"sha256:"+prefix+"-candidate", "sha256:"+prefix+"-contract", model.RecipeExecution{
			ABIVersion: model.RecipeABIVersion, ContentRef: "recipe://" + prefix + "/candidate",
			RequiredCapability: "http.fetch", Transport: model.RecipeTransportHTTPHTML})
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateRecipe(ctx, candidate, now); err != nil {
		t.Fatal(err)
	}
	preparation, err := repository.PrepareDiscoveryRecipeValidation(ctx, company.CompanyID, candidate.RecipeID,
		candidate.Version)
	if err != nil {
		t.Fatal(err)
	}
	validating, run, err := preparation.NewRun(prefix+"-run", prefix+"-work", 2)
	if err != nil {
		t.Fatal(err)
	}
	targetID := fmt.Sprintf("%s@%d", candidate.RecipeID, candidate.Version)
	work, _ := model.NewWork(run.WorkID, "recipe", targetID, "recipe_validation", "manual")
	work, _ = work.WithCausality("human:scope-fence", prefix+"-message", "")
	placement := WorkPlacement{BusinessKey: "recipe-validation|" + run.ValidationRunID, Priority: 400,
		Capability: candidate.Execution.RequiredCapability, Origin: run.Origin, NotBefore: now}
	receipt, _ := model.NewCommandReceipt(prefix+"-create", "recruiting.recipe.validate",
		"sha256:"+prefix+"-create", json.RawMessage(`{}`))
	event, _ := model.NewEventIntent(prefix+"-event", "recipe.validation_started", "recipe", targetID,
		validating.StateVersion, now.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyDiscoveryRecipeValidationCommand(ctx, candidate.StateVersion, validating, run, work,
		placement, receipt, event, nil, now); err != nil {
		t.Fatal(err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: prefix + "-attempt",
		ExecutorActorID: "executor-" + prefix, ExecutorIncarnation: "boot-" + prefix,
		Capability: placement.Capability, Origin: placement.Origin, OfferedAt: now,
		BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	response := mustResultArtifact(t, prefix+"-response", model.ArtifactResponse, work.WorkID, offer.Attempt.AttemptID)
	trace := mustResultArtifact(t, prefix+"-trace", model.ArtifactTrace, work.WorkID, offer.Attempt.AttemptID)
	result := RecipeSampleValidationResult{CommandID: prefix + "-result", RequestHash: "sha256:" + prefix + "-result",
		AttemptID: offer.Attempt.AttemptID, ExecutorActorID: offer.Attempt.ExecutorActorID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, ResultKind: "recipe_sample_validation",
		RecipeKind: model.RecipeDiscovery, Artifacts: []model.ArtifactMetadata{response, trace}, RecordCount: 2,
		ExtractedFieldCount: 2, NormalizedContentHash: "sha256:" + prefix + "-normalized",
		CompletedAt: now.Add(2 * time.Second)}
	return runningDiscoveryRecipeValidationFixture{company: company, offer: offer, result: result, now: now}
}

func TestBaselineListingResultUsesSeparatedSourceFences(t *testing.T) {
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
	now := time.Date(2095, 2, 18, 4, 0, 0, 0, time.UTC)

	for index, test := range []struct {
		name   string
		change func(model.RecruitmentSource) (model.RecruitmentSource, error)
		accept bool
	}{
		{name: "drain", accept: true, change: func(source model.RecruitmentSource) (model.RecruitmentSource, error) {
			return source.Pause(source.Version, model.PauseDrain)
		}},
		{name: "cancel", change: func(source model.RecruitmentSource) (model.RecruitmentSource, error) {
			return source.Pause(source.Version, model.PauseCancel)
		}},
		{name: "configuration_change", change: func(source model.RecruitmentSource) (model.RecruitmentSource, error) {
			return source.StageEndpoint(source.Version, "https://"+source.SourceID+".example.com/jobs-v2", "all")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			prefix := "baseline-scope-fence-" + test.name
			fixture := createRunningBaselineFixture(t, ctx, repository, prefix,
				now.Add(time.Duration(index)*time.Hour))
			current, err := repository.GetSource(ctx, fixture.source.SourceID)
			if err != nil {
				t.Fatal(err)
			}
			changed, err := test.change(current)
			if err != nil {
				t.Fatal(err)
			}
			if err := repository.UpdateSourceCAS(ctx, current.Version, changed, fixture.now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			outcome, resultErr := repository.AcceptListingPage(ctx, fixture.result)
			if test.accept {
				if resultErr != nil || outcome.Progress.PageSequence != 1 {
					t.Fatalf("drain rejected valid Baseline page: %+v err=%v", outcome, resultErr)
				}
				return
			}
			if !errors.Is(resultErr, ErrResultFenced) {
				t.Fatalf("stale Baseline page was accepted: %+v err=%v", outcome, resultErr)
			}
			var rejected, progress int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_artifacts
WHERE artifact_id = ? AND rejected = TRUE`, fixture.result.Artifact.ArtifactID).Scan(&rejected); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_listing_page_progress
WHERE work_id = ?`, fixture.offer.Work.WorkID).Scan(&progress); err != nil {
				t.Fatal(err)
			}
			if rejected != 1 || progress != 0 {
				t.Fatalf("fenced Baseline page rejected=%d progress=%d", rejected, progress)
			}
		})
	}
}

type runningBaselineFixture struct {
	source model.RecruitmentSource
	offer  ExecutionOffer
	result ListingPageResult
	now    time.Time
}

func createRunningBaselineFixture(t *testing.T, ctx context.Context, repository *Repository,
	prefix string, now time.Time) runningBaselineFixture {
	t.Helper()
	company, err := model.NewCompany(prefix+"-company", "Baseline Scope Fence", "https://"+prefix+".example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	discovering, err := company.StartDiscovery(company.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.UpdateCompanyCAS(ctx, company.Version, discovering, now); err != nil {
		t.Fatal(err)
	}
	listingRecipe := activeRecipe(t, prefix+"-listing", model.RecipeListing, prefix+".example.com", 1,
		prefix+"-listing-contract")
	detailRecipe := activeRecipe(t, prefix+"-detail", model.RecipeDetail, prefix+".example.com", 1,
		prefix+"-detail-contract")
	if err := repository.CreateRecipe(ctx, listingRecipe, now); err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateRecipe(ctx, detailRecipe, now); err != nil {
		t.Fatal(err)
	}
	source, err := model.NewRecruitmentSource(prefix+"-source", company.CompanyID,
		"https://"+prefix+".example.com/jobs", "all", 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	validating, _ := source.BeginValidation(source.Version)
	if err := repository.UpdateSourceCAS(ctx, source.Version, validating, now); err != nil {
		t.Fatal(err)
	}
	listingAssignment, _ := model.NewSourceRecipeAssignment(source.SourceID, model.RecipeListing,
		listingRecipe.RecipeID, listingRecipe.Version, listingRecipe.ContractHash, now.Format(time.RFC3339Nano))
	ready, _ := validating.PublishValidated(validating.Version, listingAssignment,
		verifiedStoreAssessment(validating, listingAssignment, now))
	if err := repository.PublishSourceAssignment(ctx, validating.Version, 0, ready, listingAssignment, now); err != nil {
		t.Fatal(err)
	}
	detailAssignment, _ := model.NewSourceRecipeAssignment(source.SourceID, model.RecipeDetail,
		detailRecipe.RecipeID, detailRecipe.Version, detailRecipe.ContractHash, now.Format(time.RFC3339Nano))
	withDetail, _ := ready.AssignRecipe(ready.Version, detailAssignment, true)
	if err := repository.PublishSourceAssignment(ctx, ready.Version, 0, withDetail, detailAssignment, now); err != nil {
		t.Fatal(err)
	}
	initializing, _ := discovering.StartInitialization(discovering.Version)
	work, _ := model.NewWork(prefix+"-work", "source", source.SourceID, "baseline_listing", "human")
	work, _ = work.WithCausality("human:scope-fence", prefix+"-message", "")
	baseline, err := model.NewExecutableBaselineGeneration(work.WorkID, initializing, withDetail, 1, listingRecipe)
	if err != nil {
		t.Fatal(err)
	}
	placement := WorkPlacement{BusinessKey: "baseline|" + source.SourceID + "|1", Priority: 10,
		Capability: listingRecipe.Execution.RequiredCapability, Origin: "https://" + prefix + ".example.com", NotBefore: now}
	response, _ := json.Marshal(map[string]any{"baseline": baseline, "work": work, "company": initializing})
	receipt, _ := model.NewCommandReceipt(prefix+"-create", "recruiting.baseline.start",
		"sha256:"+prefix+"-create", response)
	event, _ := model.NewEventIntent(prefix+"-event", "baseline.created", "baseline", work.WorkID,
		baseline.Version, now.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	companyEvent, _ := model.NewEventIntent(prefix+"-company-event", "company.initialization.started", "company",
		initializing.CompanyID, initializing.Version, now.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyCreateBaselineCommand(ctx, discovering.Version, withDetail.Version, initializing,
		baseline, work, placement, receipt, event, &companyEvent, nil, now); err != nil {
		t.Fatal(err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: prefix + "-attempt",
		ExecutorActorID: "executor-" + prefix, ExecutorIncarnation: "boot-" + prefix,
		Capability: placement.Capability, Origin: placement.Origin, OfferedAt: now,
		BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	artifact := mustResultArtifact(t, prefix+"-page", model.ArtifactPage, work.WorkID, offer.Attempt.AttemptID)
	result := ListingPageResult{CommandID: prefix + "-result", RequestHash: "sha256:" + prefix + "-result",
		AttemptID: offer.Attempt.AttemptID, ExecutorActorID: offer.Attempt.ExecutorActorID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, PageSequence: 1, Terminal: true,
		Artifact: artifact, ObservedAt: now.Add(2 * time.Second)}
	return runningBaselineFixture{source: withDetail, offer: offer, result: result, now: now}
}

func TestLiveBackfillResultUsesSeparatedSourceFences(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	now := time.Date(2095, 2, 19, 4, 0, 0, 0, time.UTC)

	for index, test := range []struct {
		name   string
		change func(model.RecruitmentSource) (model.RecruitmentSource, error)
		accept bool
	}{
		{name: "drain", accept: true, change: func(source model.RecruitmentSource) (model.RecruitmentSource, error) {
			return source.Pause(source.Version, model.PauseDrain)
		}},
		{name: "cancel", change: func(source model.RecruitmentSource) (model.RecruitmentSource, error) {
			return source.Pause(source.Version, model.PauseCancel)
		}},
		{name: "configuration_change", change: func(source model.RecruitmentSource) (model.RecruitmentSource, error) {
			return source.StageEndpoint(source.Version, "https://"+source.SourceID+".example.com/jobs-v2", "all")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			prefix := "backfill-scope-fence-" + test.name
			fixture := createRunningLiveBackfillFixture(t, ctx, repository, prefix,
				now.Add(time.Duration(index)*time.Hour))
			if fixture.offer.Attempt.SourceConfigurationVersion == 0 {
				t.Fatalf("Backfill Attempt omitted Source scope: %+v", fixture.offer.Attempt)
			}
			current, err := repository.GetSource(ctx, fixture.source.SourceID)
			if err != nil {
				t.Fatal(err)
			}
			changed, err := test.change(current)
			if err != nil {
				t.Fatal(err)
			}
			if err := repository.UpdateSourceCAS(ctx, current.Version, changed, fixture.now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			outcome, resultErr := repository.AcceptBackfillResult(ctx, fixture.result)
			if test.accept {
				if resultErr != nil || outcome.Output.OutputID == "" || outcome.Output.ClaimsHistoricalSnapshot {
					t.Fatalf("drain rejected valid live Backfill result: %+v err=%v", outcome, resultErr)
				}
				return
			}
			if !errors.Is(resultErr, ErrResultFenced) {
				t.Fatalf("stale live Backfill result was accepted: %+v err=%v", outcome, resultErr)
			}
			var rejected, outputs int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_artifacts
WHERE artifact_id = ? AND rejected = TRUE`, fixture.result.Artifact.ArtifactID).Scan(&rejected); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_backfill_outputs
WHERE backfill_id = ?`, fixture.backfill.BackfillID).Scan(&outputs); err != nil {
				t.Fatal(err)
			}
			if rejected != 1 || outputs != 0 {
				t.Fatalf("fenced Backfill result rejected=%d outputs=%d", rejected, outputs)
			}
		})
	}
}

type runningLiveBackfillFixture struct {
	source   model.RecruitmentSource
	backfill model.Backfill
	offer    ExecutionOffer
	result   BackfillResult
	now      time.Time
}

func createRunningLiveBackfillFixture(t *testing.T, ctx context.Context, repository *Repository,
	prefix string, now time.Time) runningLiveBackfillFixture {
	t.Helper()
	detail := createDetailFixture(t, ctx, repository, prefix, now)
	assignment := *detail.source.DetailAssignment
	recipe, err := repository.GetRecipe(ctx, assignment.RecipeID, assignment.RecipeVersion)
	if err != nil {
		t.Fatal(err)
	}
	parent, _ := model.NewWork(prefix+"-parent", "source", detail.source.SourceID, "historical_backfill", "human")
	parent, _ = parent.WithCausality("human:scope-fence", prefix+"-message", "")
	backfill, err := model.NewBackfill(prefix+"-backfill", parent.WorkID, parent.InitiatorActorID,
		"source", detail.source.SourceID, model.BackfillLiveRefetch, now.Add(-time.Hour).Format(time.RFC3339),
		now.Add(time.Hour).Format(time.RFC3339), []string{"title"}, recipe.RecipeID, recipe.Version, 1)
	if err != nil {
		t.Fatal(err)
	}
	response, _ := json.Marshal(backfill)
	receipt, _ := model.NewCommandReceipt(prefix+"-create", "recruiting.backfill.create",
		"sha256:"+prefix+"-create", response)
	event, _ := model.NewEventIntent(prefix+"-event", "backfill.created", "work", parent.WorkID,
		parent.Version, now.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyCreateBackfillCommand(ctx, parent,
		WorkPlacement{BusinessKey: "backfill|" + prefix, NotBefore: now}, backfill, receipt, event, now); err != nil {
		t.Fatal(err)
	}
	backfill, items, err := repository.PreviewBackfillChunk(ctx, backfill.BackfillID, backfill.Version, now.Add(time.Second))
	if err != nil || len(items) != 1 {
		t.Fatalf("preview live Backfill items=%d err=%v", len(items), err)
	}
	parent, err = repository.GetWork(ctx, parent.WorkID)
	if err != nil {
		t.Fatal(err)
	}
	confirmed, _ := backfill.Confirm(backfill.Version, backfill.PreviewHash)
	runningParent, _ := parent.Start(parent.Version)
	confirmResponse, _ := json.Marshal(map[string]any{"backfill": confirmed, "work": runningParent})
	confirmReceipt, _ := model.NewCommandReceipt(prefix+"-confirm", "recruiting.backfill.confirm",
		"sha256:"+prefix+"-confirm", confirmResponse)
	confirmEvent, _ := model.NewEventIntent(prefix+"-confirm-event", "backfill.confirmed", "work", parent.WorkID,
		runningParent.Version, now.Add(2*time.Second).Format(time.RFC3339Nano), confirmReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyConfirmBackfillCommand(ctx, backfill.BackfillID, backfill.Version, parent.Version,
		backfill.PreviewHash, confirmReceipt, confirmEvent, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	targets := []ExecutionDispatchTarget{{ActorID: "tool:" + prefix + "-executor", Capability: recipe.Execution.RequiredCapability}}
	if materialized, err := repository.MaterializeNextBackfillPage(ctx, 10, now.Add(3*time.Second), targets); err != nil ||
		materialized.Queued != 1 {
		t.Fatalf("materialize live Backfill=%+v err=%v", materialized, err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: prefix + "-backfill-attempt",
		ExecutorActorID: "tool:" + prefix + "-executor:one", ExecutorIncarnation: "boot-" + prefix,
		Capability: recipe.Execution.RequiredCapability, Origin: "https://" + prefix + ".example.com",
		OfferedAt: now.Add(4 * time.Second), BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	running, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	outputJSON := json.RawMessage(`{"title":"Refetched Engineer"}`)
	sum := sha256.Sum256(outputJSON)
	artifact := mustResultArtifact(t, prefix+"-artifact", model.ArtifactResponse, offer.Work.WorkID, running.AttemptID)
	result := BackfillResult{CommandID: prefix + "-result", RequestHash: "sha256:" + prefix + "-result",
		AttemptID: running.AttemptID, ExecutorActorID: running.ExecutorActorID,
		ExecutorIncarnation: running.ExecutorIncarnation, Artifact: artifact,
		NormalizedContentHash: fmt.Sprintf("sha256:%x", sum[:]), OutputJSON: outputJSON,
		CompletedAt: now.Add(6 * time.Second)}
	return runningLiveBackfillFixture{source: detail.source, backfill: confirmed, offer: offer, result: result, now: now}
}

func TestDiagnosticResultUsesSeparatedSourceFences(t *testing.T) {
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
	now := time.Date(2095, 2, 20, 4, 0, 0, 0, time.UTC)

	for index, test := range []struct {
		name   string
		change func(model.RecruitmentSource) (model.RecruitmentSource, error)
		accept bool
	}{
		{name: "drain", accept: true, change: func(source model.RecruitmentSource) (model.RecruitmentSource, error) {
			return source.Pause(source.Version, model.PauseDrain)
		}},
		{name: "cancel", change: func(source model.RecruitmentSource) (model.RecruitmentSource, error) {
			return source.Pause(source.Version, model.PauseCancel)
		}},
		{name: "configuration_change", change: func(source model.RecruitmentSource) (model.RecruitmentSource, error) {
			return source.StageEndpoint(source.Version, "https://"+source.SourceID+".example.com/jobs-v2", "all")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			prefix := "diagnostic-scope-fence-" + test.name
			fixture := createRunningDiagnosticFixture(t, ctx, repository, prefix,
				now.Add(time.Duration(index)*time.Hour))
			current, err := repository.GetSource(ctx, fixture.source.SourceID)
			if err != nil {
				t.Fatal(err)
			}
			changed, err := test.change(current)
			if err != nil {
				t.Fatal(err)
			}
			if err := repository.UpdateSourceCAS(ctx, current.Version, changed, fixture.now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			outcome, resultErr := repository.AcceptDiagnosticResult(ctx, fixture.result)
			if test.accept {
				if resultErr != nil || outcome.Work.Status != model.WorkCompleted || outcome.Run.Status != model.ListingRunCompleted {
					t.Fatalf("drain rejected valid diagnostic evidence: %+v err=%v", outcome, resultErr)
				}
				return
			}
			if !errors.Is(resultErr, ErrResultFenced) {
				t.Fatalf("stale diagnostic evidence was accepted: %+v err=%v", outcome, resultErr)
			}
			var rejected, jobs int
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_artifacts
WHERE attempt_id = ? AND rejected = TRUE`, fixture.offer.Attempt.AttemptID).Scan(&rejected); err != nil {
				t.Fatal(err)
			}
			if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_source_jobs
WHERE source_id = ?`, fixture.source.SourceID).Scan(&jobs); err != nil {
				t.Fatal(err)
			}
			if rejected != 2 || jobs != 0 {
				t.Fatalf("fenced diagnostic evidence rejected=%d jobs=%d", rejected, jobs)
			}
		})
	}
}

type runningDiagnosticFixture struct {
	source model.RecruitmentSource
	offer  ExecutionOffer
	result DiagnosticResult
	now    time.Time
}

func createRunningDiagnosticFixture(t *testing.T, ctx context.Context, repository *Repository,
	prefix string, now time.Time) runningDiagnosticFixture {
	t.Helper()
	company := persistExecutionReadyCompany(t, ctx, repository, prefix, now)
	source := persistExecutionReadySource(t, ctx, repository, company, prefix, prefix+"-source", now)
	if _, err := repository.db.ExecContext(ctx, `DELETE FROM recruiting_checkpoints WHERE source_id = ?`, source.SourceID); err != nil {
		t.Fatal(err)
	}
	preparation, err := repository.PrepareListingRun(ctx, source.SourceID)
	if err != nil {
		t.Fatal(err)
	}
	run, err := preparation.NewRun(prefix+"-run", prefix+"-work", model.ListingRunDiagnostic)
	if err != nil {
		t.Fatal(err)
	}
	work, _ := model.NewWork(run.WorkID, "source", source.SourceID, "listing_sync", "manual")
	work, _ = work.WithCausality("human:scope-fence", prefix+"-message", "")
	placement := WorkPlacement{BusinessKey: "manual-listing|" + run.ListingRunID, Priority: 200,
		Capability: run.ListingExecution.Execution.RequiredCapability, Origin: run.ListingExecution.Origin, NotBefore: now}
	receipt, _ := model.NewCommandReceipt(prefix+"-create", "recruiting.run.diagnostic",
		"sha256:"+prefix+"-create", json.RawMessage(`{}`))
	event, _ := model.NewEventIntent(prefix+"-event", "work.created", "work", work.WorkID, work.Version,
		now.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyListingRunCommand(ctx, source.Version, run, work, placement, receipt, event, nil, now); err != nil {
		t.Fatal(err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: prefix + "-attempt",
		ExecutorActorID: "executor-" + prefix, ExecutorIncarnation: "boot-" + prefix,
		Capability: placement.Capability, Origin: placement.Origin, OfferedAt: now,
		BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now); err != nil {
		t.Fatal(err)
	}
	page := mustResultArtifact(t, prefix+"-page", model.ArtifactPage, work.WorkID, offer.Attempt.AttemptID)
	trace := mustResultArtifact(t, prefix+"-trace", model.ArtifactTrace, work.WorkID, offer.Attempt.AttemptID)
	result := DiagnosticResult{CommandID: prefix + "-result", RequestHash: "sha256:" + prefix + "-result",
		AttemptID: offer.Attempt.AttemptID, ExecutorActorID: offer.Attempt.ExecutorActorID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, ResultKind: "diagnostic",
		Artifacts: []model.ArtifactMetadata{page, trace}, Quality: executioncontract.ListingQuality{
			IdentityComplete: true, OrderingContractHeld: true, PaginationStable: true, ItemCount: 3},
		CompletedAt: now.Add(2 * time.Second)}
	return runningDiagnosticFixture{source: source, offer: offer, result: result, now: now}
}
