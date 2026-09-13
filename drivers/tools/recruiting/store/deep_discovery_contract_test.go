package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestDeepDiscoveryRepositoryContract(t *testing.T) {
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
	now := time.Date(2026, 9, 13, 8, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("deep-company", "Deep Company", "https://example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	mission, _ := model.NewDeepDiscoveryMission("deep-mission", company, 1, 5, 250)
	if err := repository.CreateDeepDiscoveryMission(ctx, company.Version, mission, now); err != nil {
		t.Fatal(err)
	}

	companyNode := evidenceNode(t, model.EvidenceCompany, "Deep Company", model.EvidenceValidated, model.SensorHuman, "https://example.com", "canonical requested company")
	brandNode := evidenceNode(t, model.EvidenceBrand, "Deep", model.EvidenceValidated, model.SensorOfficialSite, "https://example.com", "official brand")
	edge, _ := model.NewDiscoveryEvidenceEdge(companyNode.NodeID, brandNode.NodeID, "owns_brand", "official corporate identity")
	coverage := model.DiscoveryCoverage{IdentityScoped: true}
	expectedMission, _ := mission.Checkpoint(1, model.DeepDiscoveryBrandExpansion, coverage, 1, 3, 2, 1, 0)
	response, _ := json.Marshal(map[string]any{"mission": expectedMission})
	receipt, _ := model.NewCommandReceipt("deep-checkpoint-1", "recruiting.deep_discovery.checkpoint", "sha256:deep-1", response)
	event, _ := model.NewEventIntent("event-deep-1", "deep.discovery.checkpointed", "deep_discovery", mission.MissionID, expectedMission.Version, now.Add(time.Second).Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{"stage":"brand_expansion"}`))
	commandResult, err := repository.ApplyDeepDiscoveryCheckpointCommand(ctx, mission.MissionID, "deep-checkpoint-1", "company scope established", 1, model.DeepDiscoveryBrandExpansion, coverage, 1, 3, []model.DiscoveryEvidenceNode{companyNode, brandNode}, []model.DiscoveryEvidenceEdge{edge}, receipt, event, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if commandResult.Replayed || string(commandResult.Response) != string(response) {
		t.Fatalf("command result=%+v", commandResult)
	}
	replay, err := repository.ApplyDeepDiscoveryCheckpointCommand(ctx, mission.MissionID, "deep-checkpoint-1", "company scope established", 1, model.DeepDiscoveryBrandExpansion, coverage, 1, 3, []model.DiscoveryEvidenceNode{companyNode, brandNode}, []model.DiscoveryEvidenceEdge{edge}, receipt, event, now.Add(time.Second))
	if err != nil || !replay.Replayed {
		t.Fatalf("checkpoint replay=%+v err=%v", replay, err)
	}
	mission, err = repository.GetDeepDiscoveryMission(ctx, mission.MissionID)
	if err != nil {
		t.Fatal(err)
	}
	newNodes, newEdges, err := repository.PrepareDeepDiscoveryGraphDelta(ctx, mission.MissionID, []model.DiscoveryEvidenceNode{companyNode, brandNode}, []model.DiscoveryEvidenceEdge{edge})
	if err != nil || len(newNodes) != 0 || len(newEdges) != 0 {
		t.Fatalf("repeated graph reference=%+v/%+v err=%v", newNodes, newEdges, err)
	}

	domainNode := evidenceNode(t, model.EvidenceDomain, "https://jobs.example.com", model.EvidenceValidated, model.SensorWebSearch, "https://example.com/careers", "official careers redirect")
	domainEdge, _ := model.NewDiscoveryEvidenceEdge(brandNode.NodeID, domainNode.NodeID, "recruits_at", "official careers navigation")
	coverage.BrandsReviewed = true
	mission, err = repository.CheckpointDeepDiscovery(ctx, mission.MissionID, "deep-checkpoint-2", "brand sites enumerated", mission.Version,
		model.DeepDiscoverySiteEnumeration, coverage, 1, 3, []model.DiscoveryEvidenceNode{domainNode}, []model.DiscoveryEvidenceEdge{domainEdge}, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}

	siteNode := evidenceNode(t, model.EvidenceSite, "https://jobs.example.com/careers", model.EvidenceValidated, model.SensorBrowser, "https://jobs.example.com/careers", "interactive recruitment site")
	siteEdge, _ := model.NewDiscoveryEvidenceEdge(domainNode.NodeID, siteNode.NodeID, "hosts", "browser navigation")
	coverage.SitesEnumerated = true
	mission, err = repository.CheckpointDeepDiscovery(ctx, mission.MissionID, "deep-checkpoint-3", "sites explored", mission.Version, model.DeepDiscoverySiteExploration, coverage, 0, 4, []model.DiscoveryEvidenceNode{siteNode}, []model.DiscoveryEvidenceEdge{siteEdge}, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	poolNode := evidenceNode(t, model.EvidenceListingPool, "https://jobs.example.com/search", model.EvidenceCandidate, model.SensorNetwork, "https://jobs.example.com/careers", "browser network exposed the pool")
	poolEdge, _ := model.NewDiscoveryEvidenceEdge(siteNode.NodeID, poolNode.NodeID, "contains_pool", "network observation")
	coverage.SitesExplored = true
	mission, err = repository.CheckpointDeepDiscovery(ctx, mission.MissionID, "deep-checkpoint-4", "job pools detected", mission.Version, model.DeepDiscoveryPoolDetection, coverage, 0, 5, []model.DiscoveryEvidenceNode{poolNode}, []model.DiscoveryEvidenceEdge{poolEdge}, now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	listNode := evidenceNode(t, model.EvidenceListURL, "https://jobs.example.com/search?sort=updated", model.EvidenceValidated, model.SensorBrowser, "https://jobs.example.com/search?sort=updated", "populated newest-first job list")
	listEdge, _ := model.NewDiscoveryEvidenceEdge(poolNode.NodeID, listNode.NodeID, "lists_jobs_at", "browser validation")
	coverage.PoolsDetected = true
	mission, err = repository.CheckpointDeepDiscovery(ctx, mission.MissionID, "deep-checkpoint-5", "candidate validated", mission.Version, model.DeepDiscoveryCandidateValidation, coverage, 0, 5, []model.DiscoveryEvidenceNode{listNode}, []model.DiscoveryEvidenceEdge{listEdge}, now.Add(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	blindspot := evidenceNode(t, model.EvidenceBlindspot, "campus recruitment", model.EvidenceExcluded, model.SensorHuman, "https://example.com/careers", "outside agreed company scope")
	coverage.CandidatesValidated = true
	coverage.BlindspotsReviewed = true
	mission, err = repository.CheckpointDeepDiscovery(ctx, mission.MissionID, "deep-checkpoint-6", "coverage reviewed", mission.Version, model.DeepDiscoveryCoverageReview, coverage, 0, 1, []model.DiscoveryEvidenceNode{blindspot}, nil, now.Add(6*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	mission, err = repository.UpdateDeepDiscoveryStatus(ctx, mission.MissionID, mission.Version, "complete", "", now.Add(7*time.Second))
	if err != nil || mission.Status != model.DeepDiscoveryDone {
		t.Fatalf("completion=%+v err=%v", mission, err)
	}

	seenNodes, seenEdges, cursor := 0, 0, ""
	for {
		page, err := repository.ListDeepDiscoveryGraph(ctx, mission.MissionID, cursor, 1)
		if err != nil {
			t.Fatal(err)
		}
		seenNodes += len(page.Nodes)
		seenEdges += len(page.Edges)
		if !page.HasMore {
			break
		}
		if page.NextCursor == "" || page.NextCursor == cursor {
			t.Fatal("graph cursor did not advance")
		}
		cursor = page.NextCursor
	}
	if seenNodes != 7 || seenEdges != 5 {
		t.Fatalf("paged graph nodes=%d edges=%d", seenNodes, seenEdges)
	}
	second, _ := model.NewDeepDiscoveryMission("deep-mission-2", company, 2, 5, 250)
	if err := repository.CreateDeepDiscoveryMission(ctx, company.Version, second, now.Add(8*time.Second)); err != nil {
		t.Fatal(err)
	}
	parallel, _ := model.NewDeepDiscoveryMission("deep-mission-parallel", company, 3, 5, 250)
	if err := repository.CreateDeepDiscoveryMission(ctx, company.Version, parallel, now.Add(9*time.Second)); err == nil {
		t.Fatal("parallel active Company mission was accepted")
	}
	second, err = repository.UpdateDeepDiscoveryStatus(ctx, second.MissionID, second.Version, "cancel", "operator restarted discovery", now.Add(10*time.Second))
	if err != nil || second.Status != model.DeepDiscoveryCanceled {
		t.Fatalf("cancel=%+v err=%v", second, err)
	}
	if err := repository.CreateDeepDiscoveryMission(ctx, company.Version, parallel, now.Add(11*time.Second)); err != nil {
		t.Fatalf("canceled mission did not release Company: %v", err)
	}
}

func TestDeepDiscoveryBrowserProbeRunsThroughWorkAttemptAndBudget(t *testing.T) {
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
	now := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("deep-browser-company", "Deep Browser Company", "https://example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	mission, _ := model.NewDeepDiscoveryMission("deep-browser-mission", company, 1, 5, 2)
	if err := repository.CreateDeepDiscoveryMission(ctx, company.Version, mission, now); err != nil {
		t.Fatal(err)
	}
	nextMission, _ := mission.ConsumeOperations(mission.Version, 1)
	work, _ := model.NewWork("deep-browser-work", "deep_discovery_probe", "deep-browser-probe", "deep_discovery_browser", "agent")
	probe, _ := model.NewDeepDiscoveryBrowserProbe("deep-browser-probe", mission.MissionID, work.WorkID,
		"https://example.com/careers", "", 0, "", nextMission.Version)
	placement := WorkPlacement{BusinessKey: "deep-browser-probe", CompanyID: company.CompanyID, Capability: "browser.public",
		Origin: "https://example.com", NotBefore: now}
	response, _ := json.Marshal(map[string]any{"probe": probe, "mission": nextMission})
	receipt, _ := model.NewCommandReceipt("deep-browser-create", "recruiting.deep_discovery.browser.observe", "sha256:deep-browser-create", response)
	event, _ := model.NewEventIntent("event-deep-browser-create", "deep.discovery.browser.queued", "deep_discovery",
		mission.MissionID, nextMission.Version, now.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{"probe_id":"deep-browser-probe"}`))
	if _, err := repository.ApplyCreateDeepDiscoveryBrowserProbeCommand(ctx, mission, nextMission, probe, work,
		placement, receipt, event, nil, now); err != nil {
		t.Fatal(err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "deep-browser-attempt", ExecutorActorID: "tool:browser:1",
		ExecutorIncarnation: "boot-1", Capability: "browser.public", OfferedAt: now.Add(time.Second), BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || offer.Kind != "deep_discovery_browser" || offer.DeepDiscoveryBrowser == nil || offer.Budget.PermitID == "" {
		t.Fatalf("offer=%+v err=%v", offer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, "tool:browser:1", "boot-1", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, "tool:browser:1", "boot-1", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	contentHash := "sha256:" + strings.Repeat("a", 64)
	artifact, _ := model.NewArtifactMetadata("deep-browser-response", model.ArtifactResponse, contentHash,
		"file://worker/recruiting/deep-browser-response", work.WorkID, offer.Attempt.AttemptID, "operators", "30d", false)
	trace, _ := model.NewArtifactMetadata("deep-browser-trace", model.ArtifactTrace, "sha256:"+strings.Repeat("b", 64),
		"file://worker/recruiting/deep-browser-trace", work.WorkID, offer.Attempt.AttemptID, "operators", "30d", false)
	result := DeepDiscoveryBrowserResult{CommandID: "deep-browser-result", RequestHash: "sha256:deep-browser-result",
		AttemptID: offer.Attempt.AttemptID, ExecutorActorID: "tool:browser:1", ExecutorIncarnation: "boot-1",
		Artifact: artifact, SupportingArtifacts: []model.ArtifactMetadata{trace}, FinalURL: "https://example.com/careers",
		ContentHash: contentHash, Links: []executioncontract.DeepDiscoveryLink{{URL: "https://example.com/jobs/1", Text: "Engineer"}},
		Attestation: executioncontract.DeepDiscoveryEffectAttestation{DocumentNavigations: 1, ObservedMethods: []string{"GET"},
			PublicEndpoint: true, RobotsAllowed: true, TermsPolicyVersion: 1}, ObservedAt: now.Add(4 * time.Second)}
	outcome, err := repository.AcceptDeepDiscoveryBrowserResult(ctx, result)
	if err != nil || outcome.Probe.Status != model.DeepDiscoveryProbeCompleted || outcome.Work.Status != model.WorkCompleted {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	storedProbe, storedWork, storedResult, err := repository.GetDeepDiscoveryBrowserResult(ctx, probe.ProbeID)
	if err != nil || storedResult == nil || storedProbe.Status != model.DeepDiscoveryProbeCompleted ||
		storedWork.Status != model.WorkCompleted || storedResult.AttemptID != offer.Attempt.AttemptID {
		t.Fatalf("stored browser result probe=%+v work=%+v result=%+v err=%v", storedProbe, storedWork, storedResult, err)
	}
	storedMission, _ := repository.GetDeepDiscoveryMission(ctx, mission.MissionID)
	if storedMission.Budget.OperationsUsed != 1 {
		t.Fatalf("mission budget=%+v", storedMission.Budget)
	}
	replay, err := repository.AcceptDeepDiscoveryBrowserResult(ctx, result)
	if err != nil || !replay.Replayed {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
	lateArtifact, _ := model.NewArtifactMetadata("deep-browser-response-late", model.ArtifactResponse, contentHash,
		"file://worker/recruiting/deep-browser-response-late", work.WorkID, offer.Attempt.AttemptID, "operators", "30d", false)
	lateTrace, _ := model.NewArtifactMetadata("deep-browser-trace-late", model.ArtifactTrace, "sha256:"+strings.Repeat("c", 64),
		"file://worker/recruiting/deep-browser-trace-late", work.WorkID, offer.Attempt.AttemptID, "operators", "30d", false)
	late := result
	late.CommandID, late.RequestHash = "deep-browser-result-late", "sha256:deep-browser-result-late"
	late.Artifact, late.SupportingArtifacts, late.ObservedAt = lateArtifact, []model.ArtifactMetadata{lateTrace}, now.Add(5*time.Second)
	if _, err := repository.AcceptDeepDiscoveryBrowserResult(ctx, late); !errors.Is(err, ErrResultFenced) {
		t.Fatalf("late Deep Discovery browser result err=%v, want result fence", err)
	}
	var rejected int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_artifacts
WHERE artifact_id IN (?,?) AND rejected=TRUE`, lateArtifact.ArtifactID, lateTrace.ArtifactID).Scan(&rejected); err != nil || rejected != 2 {
		t.Fatalf("rejected browser evidence=%d err=%v", rejected, err)
	}
}

func TestDeepDiscoveryStageEvidenceRejectsSearchOnlyValidation(t *testing.T) {
	node := evidenceNode(t, model.EvidenceListURL, "https://jobs.example.com/search", model.EvidenceValidated, model.SensorWebSearch, "https://example.com/careers", "search result only")
	if deepDiscoveryEvidenceSupportsStage(model.DeepDiscoveryCoverageReview, node) {
		t.Fatal("Web Search alone satisfied candidate validation")
	}
	node.Sensor = model.SensorBrowser
	if !deepDiscoveryEvidenceSupportsStage(model.DeepDiscoveryCoverageReview, node) {
		t.Fatal("browser-validated ListURL was rejected")
	}
}

func evidenceNode(t *testing.T, kind model.DiscoveryEvidenceKind, value string, state model.DiscoveryEvidenceState,
	sensor model.DiscoverySensor, evidenceURL, basis string) model.DiscoveryEvidenceNode {
	t.Helper()
	node, err := model.NewDiscoveryEvidenceNode(kind, value, value, state, sensor, evidenceURL, "", basis)
	if err != nil {
		t.Fatal(err)
	}
	return node
}
