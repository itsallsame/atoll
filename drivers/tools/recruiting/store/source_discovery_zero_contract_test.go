package store

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestSourceDiscoveryZeroCandidatesClosesCompanyOnboardingBranch(t *testing.T) {
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
	now := time.Date(2088, 9, 9, 0, 0, 0, 0, time.UTC)

	run := func(suffix string, existingSource bool) SourceDiscoveryResultOutcome {
		t.Helper()
		host := "zero-" + suffix + ".example.com"
		company, err := model.NewCompany("zero-company-"+suffix, "Zero "+suffix, "https://"+host)
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
			t.Fatalf("persist discovering Company: %v", err)
		}
		if existingSource {
			source, sourceErr := model.NewRecruitmentSource("zero-existing-source-"+suffix, company.CompanyID,
				"https://"+host+"/jobs", "all", 1)
			if sourceErr != nil || repository.CreateSource(ctx, source, now) != nil {
				t.Fatalf("create existing Source: %v", sourceErr)
			}
		}
		recipe := activeRecipe(t, "zero-discovery-recipe-"+suffix, model.RecipeDiscovery, host, 1,
			"zero-discovery-contract-"+suffix)
		if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
			t.Fatal(err)
		}
		work, _ := model.NewWork("zero-discovery-work-"+suffix, "company", company.CompanyID, "source_discovery", "human")
		discovery, err := model.NewSourceDiscovery("zero-discovery-"+suffix, work.WorkID, discovering, 1,
			discovering.Website, recipe)
		if err != nil {
			t.Fatal(err)
		}
		placement := WorkPlacement{BusinessKey: "source-discovery|" + company.CompanyID + "|1", Priority: 80,
			Capability: recipe.Execution.RequiredCapability, Origin: "https://" + host, NotBefore: now}
		if err := repository.CreateSourceDiscovery(ctx, discovery, work, placement, now); err != nil {
			t.Fatal(err)
		}
		offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "zero-discovery-attempt-" + suffix,
			ExecutorActorID: "executor-zero", ExecutorIncarnation: "boot-zero", Capability: placement.Capability,
			Origin: placement.Origin, OfferedAt: now.Add(time.Second), BudgetPolicy: testExecutionBudgetPolicy()})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
			offer.Attempt.ExecutorIncarnation, now.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
			offer.Attempt.ExecutorIncarnation, now.Add(3*time.Second)); err != nil {
			t.Fatal(err)
		}
		artifact, err := model.NewArtifactMetadata("zero-discovery-artifact-"+suffix, model.ArtifactResponse,
			"sha256:zero-discovery-"+suffix, "object://recruiting/zero-discovery/"+suffix, work.WorkID,
			offer.Attempt.AttemptID, "operators", "30d", true)
		if err != nil {
			t.Fatal(err)
		}
		input := SourceDiscoveryResult{CommandID: "zero-discovery-result-" + suffix,
			RequestHash: "sha256:zero-result-" + suffix, AttemptID: offer.Attempt.AttemptID,
			ExecutorActorID: offer.Attempt.ExecutorActorID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
			Artifact: artifact, Candidates: []model.SourceDiscoveryCandidate{}, ObservedAt: now.Add(4 * time.Second)}
		outcome, err := repository.AcceptSourceDiscoveryResult(ctx, input)
		if err != nil || outcome.Discovery.Status != model.SourceDiscoveryCompleted || outcome.Discovery.CandidateCount != 0 ||
			outcome.Work.Status != model.WorkCompleted {
			t.Fatalf("zero discovery result = %+v, %v", outcome, err)
		}
		replay, err := repository.AcceptSourceDiscoveryResult(ctx, input)
		if err != nil || !replay.Replayed || replay.Discovery != outcome.Discovery || replay.Work != outcome.Work ||
			(replay.Company == nil) != (outcome.Company == nil) ||
			(replay.Company != nil && *replay.Company != *outcome.Company) {
			t.Fatalf("zero discovery replay = %+v, %v", replay, err)
		}
		return outcome
	}

	blocked := run("empty", false)
	if blocked.Company == nil || blocked.Company.OnboardingStatus != model.CompanyBlockedNoSources || blocked.Company.Version != 3 {
		t.Fatalf("zero-candidate Company was not explicitly blocked: %+v", blocked.Company)
	}
	storedBlocked, err := repository.GetCompany(ctx, blocked.Company.CompanyID)
	if err != nil || storedBlocked != *blocked.Company {
		t.Fatalf("stored zero-candidate Company = %+v, %v", storedBlocked, err)
	}
	var blockedEvents int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_event_outbox
WHERE event_id = ?`, "company-no-sources-zero-discovery-empty").Scan(&blockedEvents); err != nil || blockedEvents != 1 {
		t.Fatalf("zero-candidate Company event count = %d, %v", blockedEvents, err)
	}

	withSource := run("existing", true)
	if withSource.Company != nil {
		t.Fatalf("zero candidates incorrectly blocked a Company with an existing Source: %+v", withSource.Company)
	}
	storedWithSource, err := repository.GetCompany(ctx, "zero-company-existing")
	if err != nil || storedWithSource.OnboardingStatus != model.CompanyDiscoveringSources || storedWithSource.Version != 2 {
		t.Fatalf("Company with existing Source = %+v, %v", storedWithSource, err)
	}
	var unexpectedEvents int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_event_outbox
WHERE event_id = ?`, "company-no-sources-zero-discovery-existing").Scan(&unexpectedEvents); err != nil || unexpectedEvents != 0 {
		t.Fatalf("Company with existing Source blocked event count = %d, %v", unexpectedEvents, err)
	}
}
