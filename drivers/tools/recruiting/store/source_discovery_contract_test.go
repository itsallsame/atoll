package store

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestSourceDiscoveryRepositoryContract(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	migrateTestDatabase(t, ctx, db)

	repository, _ := NewRepository(db)
	now := time.Date(2026, 9, 9, 4, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("discovery-company", "Discovery", "https://discovery.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	recipe := activeRecipe(t, "source-discovery-v1", model.RecipeDiscovery, "discovery.example.com", 1, "discovery-contract")
	if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
		t.Fatal(err)
	}
	work, _ := model.NewWork("source-discovery-work", "company", company.CompanyID, "source_discovery", "human")
	discovery, err := model.NewSourceDiscovery("source-discovery-1", work.WorkID, company, 1, company.Website, recipe)
	if err != nil {
		t.Fatal(err)
	}
	placement := WorkPlacement{BusinessKey: "source-discovery|discovery-company|1", Priority: 80,
		Capability: recipe.Execution.RequiredCapability, Origin: "discovery.example.com", NotBefore: now}
	if err := repository.CreateSourceDiscovery(ctx, discovery, work, placement, now); err != nil {
		t.Fatal(err)
	}

	duplicateWork, _ := model.NewWork("source-discovery-work-duplicate", "company", company.CompanyID, "source_discovery", "human")
	duplicate, _ := model.NewSourceDiscovery("source-discovery-duplicate", duplicateWork.WorkID, company, 1, company.Website, recipe)
	if err := repository.CreateSourceDiscovery(ctx, duplicate, duplicateWork, placement, now); !errors.Is(err, ErrBusinessKeyExists) {
		t.Fatalf("duplicate Company generation = %v", err)
	}

	running, err := repository.StartSourceDiscovery(ctx, discovery.DiscoveryID, discovery.Version, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	candidateA, _ := model.NewSourceDiscoveryCandidate(company.Website+"/careers", "engineering",
		"https://boards.example/jobs/discovery", "careers link and ATS redirect", "artifact-discovery-a")
	candidateB, _ := model.NewSourceDiscoveryCandidate(company.Website+"/jobs", "sales",
		company.Website+"/jobs/sales", "jobs navigation", "artifact-discovery-b")
	appended, err := repository.AppendSourceDiscoveryCandidates(ctx, discovery.DiscoveryID, running.Version, 0,
		[]model.SourceDiscoveryCandidate{candidateA, candidateB}, now.Add(2*time.Second))
	if err != nil || appended.CandidateCount != 2 || appended.NextChunkSequence != 1 {
		t.Fatalf("append = %+v, %v", appended, err)
	}
	if _, err := repository.AppendSourceDiscoveryCandidates(ctx, discovery.DiscoveryID, appended.Version, 0,
		[]model.SourceDiscoveryCandidate{candidateA}, now.Add(3*time.Second)); err == nil {
		t.Fatal("stale chunk sequence was accepted")
	}
	completed, err := repository.FinishSourceDiscovery(ctx, discovery.DiscoveryID, appended.Version, now.Add(4*time.Second))
	if err != nil || completed.Status != model.SourceDiscoveryCompleted || completed.CandidateCount != 2 {
		t.Fatalf("finish = %+v, %v", completed, err)
	}
	stored, err := repository.GetSourceDiscovery(ctx, discovery.DiscoveryID)
	if err != nil || stored != completed {
		t.Fatalf("stored = %+v, %v", stored, err)
	}
	var candidates int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_source_discovery_candidates WHERE discovery_id = ?", discovery.DiscoveryID).Scan(&candidates); err != nil || candidates != 2 {
		t.Fatalf("candidate rows = %d, %v", candidates, err)
	}
}
