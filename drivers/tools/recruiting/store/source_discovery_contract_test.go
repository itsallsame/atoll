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
		Capability: recipe.Execution.RequiredCapability, Origin: "https://discovery.example.com", NotBefore: now}
	wrongPlacement := placement
	wrongPlacement.Origin = "https://other.example.com"
	if err := repository.CreateSourceDiscovery(ctx, discovery, work, wrongPlacement, now); err == nil {
		t.Fatal("source discovery accepted a placement for another origin")
	}
	if err := repository.CreateSourceDiscovery(ctx, discovery, work, placement, now); err != nil {
		t.Fatal(err)
	}

	duplicateWork, _ := model.NewWork("source-discovery-work-duplicate", "company", company.CompanyID, "source_discovery", "human")
	duplicate, _ := model.NewSourceDiscovery("source-discovery-duplicate", duplicateWork.WorkID, company, 1, company.Website, recipe)
	if err := repository.CreateSourceDiscovery(ctx, duplicate, duplicateWork, placement, now); !errors.Is(err, ErrBusinessKeyExists) {
		t.Fatalf("duplicate Company generation = %v", err)
	}

	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "source-discovery-attempt", ExecutorActorID: "executor-discovery",
		ExecutorIncarnation: "boot-discovery", Capability: recipe.Execution.RequiredCapability,
		Origin: "https://discovery.example.com", OfferedAt: now.Add(time.Second), BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || offer.Kind != "source_discovery" || offer.Discovery == nil || offer.Recipe == nil ||
		offer.Attempt.DiscoveryGeneration != discovery.Generation || offer.Discovery.DiscoveryID != discovery.DiscoveryID {
		t.Fatalf("discovery offer = %+v, %v", offer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	running, err := repository.GetSourceDiscovery(ctx, discovery.DiscoveryID)
	if err != nil || running.Status != model.SourceDiscoveryRunning {
		t.Fatalf("running discovery = %+v, %v", running, err)
	}
	artifact, err := model.NewArtifactMetadata("artifact-source-discovery", model.ArtifactResponse, "sha256:discovery-response",
		"object://recruiting/source-discovery", work.WorkID, offer.Attempt.AttemptID, "operators", "30d", true)
	if err != nil {
		t.Fatal(err)
	}
	candidateA, _ := model.NewSourceDiscoveryCandidate(company.Website+"/careers", "engineering",
		"https://boards.example/jobs/discovery", "careers link and ATS redirect", artifact.ArtifactID)
	candidateB, _ := model.NewSourceDiscoveryCandidate(company.Website+"/jobs", "sales",
		company.Website+"/jobs/sales", "jobs navigation", artifact.ArtifactID)
	input := SourceDiscoveryResult{CommandID: "source-discovery-result-command", RequestHash: "sha256:source-discovery-result",
		AttemptID: offer.Attempt.AttemptID, ExecutorActorID: offer.Attempt.ExecutorActorID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, Artifact: artifact,
		Candidates: []model.SourceDiscoveryCandidate{candidateA, candidateB}, ObservedAt: now.Add(4 * time.Second)}
	outcome, err := repository.AcceptSourceDiscoveryResult(ctx, input)
	if err != nil || outcome.Discovery.Status != model.SourceDiscoveryCompleted || outcome.Discovery.CandidateCount != 2 ||
		outcome.Work.Status != model.WorkCompleted {
		t.Fatalf("accept result = %+v, %v", outcome, err)
	}
	replay, err := repository.AcceptSourceDiscoveryResult(ctx, input)
	if err != nil || !replay.Replayed || replay.Discovery != outcome.Discovery {
		t.Fatalf("result replay = %+v, %v", replay, err)
	}
	firstPage, err := repository.ListSourceDiscoveryCandidates(ctx, discovery.DiscoveryID, "", 1)
	if err != nil || len(firstPage.Items) != 1 || !firstPage.HasMore || firstPage.NextCursor == "" {
		t.Fatalf("first candidate page = %+v, %v", firstPage, err)
	}
	secondPage, err := repository.ListSourceDiscoveryCandidates(ctx, discovery.DiscoveryID, firstPage.NextCursor, 1)
	if err != nil || len(secondPage.Items) != 1 || secondPage.HasMore {
		t.Fatalf("second candidate page = %+v, %v", secondPage, err)
	}
	completed := outcome.Discovery
	stored, err := repository.GetSourceDiscovery(ctx, discovery.DiscoveryID)
	if err != nil || stored != completed {
		t.Fatalf("stored = %+v, %v", stored, err)
	}
	var candidates int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_source_discovery_candidates WHERE discovery_id = ?", discovery.DiscoveryID).Scan(&candidates); err != nil || candidates != 2 {
		t.Fatalf("candidate rows = %d, %v", candidates, err)
	}
}

func TestSourceDiscoveryCreateCommandIsAtomicAndReplayable(t *testing.T) {
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
	now := time.Date(2026, 9, 9, 5, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("discovery-command-company", "Discovery Command", "https://source-discovery-command.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	recipe := activeRecipe(t, "discovery-command-recipe", model.RecipeDiscovery, "source-discovery-command.example.com", 1, "command-contract")
	if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
		t.Fatal(err)
	}
	work, _ := model.NewWork("discovery-command-work", "company", company.CompanyID, "source_discovery", "human")
	discovery, _ := model.NewSourceDiscovery("discovery-command", work.WorkID, company, 1, company.Website, recipe)
	placement := WorkPlacement{BusinessKey: "source-discovery|discovery-command-company|1", Priority: 10,
		Capability: recipe.Execution.RequiredCapability, Origin: "https://source-discovery-command.example.com", NotBefore: now}
	receipt, _ := model.NewCommandReceipt("discovery-command-create", "recruiting.source.discover", "sha256:request", []byte(`{"discovery_id":"discovery-command"}`))
	event, _ := model.NewEventIntent("event-discovery-command", "source.discovery.created", "source_discovery",
		discovery.DiscoveryID, discovery.Version, now.Format(time.RFC3339Nano), receipt.CommandID, []byte(`{"requested_by":"human:operator:1"}`))
	dispatch, _ := NewExecutionDispatchIntent("dispatch-discovery-command", "tool:recruiting-executor", placement.Capability,
		placement.Origin, "", "source_discovery_created", receipt.CommandID, now)
	first, err := repository.ApplyCreateSourceDiscoveryCommand(ctx, discovery, work, placement, receipt, event, &dispatch, now)
	if err != nil || first.Replayed {
		t.Fatalf("first source discovery command = %+v, %v", first, err)
	}
	replay, err := repository.ApplyCreateSourceDiscoveryCommand(ctx, discovery, work, placement, receipt, event, &dispatch, now)
	if err != nil || !replay.Replayed || string(replay.Response) != string(first.Response) {
		t.Fatalf("source discovery command replay = %+v, %v", replay, err)
	}
	var works, discoveries, receipts, events, dispatches int
	for query, destination := range map[string]*int{
		"SELECT COUNT(*) FROM recruiting_works WHERE work_id = 'discovery-command-work'":                             &works,
		"SELECT COUNT(*) FROM recruiting_source_discoveries WHERE discovery_id = 'discovery-command'":                &discoveries,
		"SELECT COUNT(*) FROM recruiting_command_receipts WHERE command_id = 'discovery-command-create'":             &receipts,
		"SELECT COUNT(*) FROM recruiting_event_outbox WHERE event_id = 'event-discovery-command'":                    &events,
		"SELECT COUNT(*) FROM recruiting_execution_dispatch_outbox WHERE dispatch_id = 'dispatch-discovery-command'": &dispatches,
	} {
		if err := db.QueryRowContext(ctx, query).Scan(destination); err != nil || *destination != 1 {
			t.Fatalf("atomic source discovery fact count = %d, %v", *destination, err)
		}
	}
}
