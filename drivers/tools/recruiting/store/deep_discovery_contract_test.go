package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

func TestCanonicalizeBrowserResultEvidenceRecoversAttestedLegacyJSONHash(t *testing.T) {
	legacyBody := json.RawMessage(`{"offset":0,"limit":12,"filters":{"location":[],"category":[]}}`)
	sum := sha256.Sum256(legacyBody)
	result := DeepDiscoveryBrowserResult{PublicQueryEvidence: []recipeabi.PublicQueryObservation{{
		EndpointURL: "https://jobs.example.com/api/search", Method: "POST",
		Headers:  map[string]string{"Content-Type": "application/json"},
		JSONBody: legacyBody, BodyHash: "sha256:" + hex.EncodeToString(sum[:]),
	}}}
	if err := canonicalizeBrowserResultEvidence(&result); err != nil {
		t.Fatal(err)
	}
	canonical, err := recipeabi.NewPublicQueryObservation("https://jobs.example.com/api/search", "POST",
		map[string]string{"Content-Type": "application/json"},
		json.RawMessage(`{"filters":{"category":[],"location":[]},"limit":12,"offset":0}`))
	if err != nil {
		t.Fatal(err)
	}
	if !result.PublicQueryEvidence[0].MatchesObservation(canonical) {
		t.Fatal("legacy database evidence was not normalized to its semantic request")
	}
}

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
	preparedSource, _ := model.NewRecruitmentSource("deep-browser-prepared-source", company.CompanyID,
		"https://example.com/careers", "social", 1)
	if err := repository.CreateSource(ctx, preparedSource, now); err != nil {
		t.Fatal(err)
	}
	preparedResponse := json.RawMessage(`{"content_ref":"recipe://recruiting-prepared/example"}`)
	preparedReceipt, _ := model.NewCommandReceipt("deep-browser-recipe-prepare", "recruiting.recipe.prepare",
		"sha256:deep-browser-recipe-prepare", preparedResponse)
	preparedEvent, _ := model.NewEventIntent("event-deep-browser-recipe-prepare", "recipe.prepared", "recipe_preparation",
		preparedReceipt.CommandID, 1, now.Format(time.RFC3339Nano), preparedReceipt.CommandID, json.RawMessage(`{"source_id":"deep-browser-prepared-source"}`))
	firstPreparation, err := repository.ApplyRecipePreparationCommand(ctx, preparedSource.SourceID, preparedSource.Version,
		preparedReceipt, preparedEvent, now)
	if err != nil || firstPreparation.Replayed || string(firstPreparation.Response) != string(preparedResponse) {
		t.Fatalf("first Recipe preparation=%+v err=%v", firstPreparation, err)
	}
	replayedPreparation, err := repository.ApplyRecipePreparationCommand(ctx, preparedSource.SourceID, preparedSource.Version,
		preparedReceipt, preparedEvent, now)
	if err != nil || !replayedPreparation.Replayed || string(replayedPreparation.Response) != string(preparedResponse) {
		t.Fatalf("replayed Recipe preparation=%+v err=%v", replayedPreparation, err)
	}
	mission, _ := model.NewDeepDiscoveryMission("deep-browser-mission", company, 1, 5, 4)
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
	queryEvidence, _ := recipeabi.NewPublicQueryObservation("https://api.example.com/public/jobs", "POST",
		map[string]string{"Content-Type": "application/json"}, json.RawMessage(`{"offset":0,"limit":20}`))
	result := DeepDiscoveryBrowserResult{CommandID: "deep-browser-result", RequestHash: "sha256:deep-browser-result",
		AttemptID: offer.Attempt.AttemptID, ExecutorActorID: "tool:browser:1", ExecutorIncarnation: "boot-1",
		Artifact: artifact, SupportingArtifacts: []model.ArtifactMetadata{trace}, FinalURL: "https://example.com/careers",
		ContentHash: contentHash, Links: []executioncontract.DeepDiscoveryLink{{URL: "https://example.com/jobs/1", Text: "Engineer"}},
		PublicQueryEvidence: []recipeabi.PublicQueryObservation{queryEvidence},
		Attestation: executioncontract.DeepDiscoveryEffectAttestation{DocumentNavigations: 1, ObservedMethods: []string{"GET"},
			PublicEndpoint: true, RobotsAllowed: true, TermsPolicyVersion: 1}, ObservedAt: now.Add(4 * time.Second)}
	outcome, err := repository.AcceptDeepDiscoveryBrowserResult(ctx, result)
	if err != nil || outcome.Probe.Status != model.DeepDiscoveryProbeCompleted || outcome.Work.Status != model.WorkCompleted {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
	storedProbe, storedWork, storedResult, err := repository.GetDeepDiscoveryBrowserResult(ctx, probe.ProbeID)
	if err != nil || storedResult == nil || storedProbe.Status != model.DeepDiscoveryProbeCompleted ||
		storedWork.Status != model.WorkCompleted || storedResult.AttemptID != offer.Attempt.AttemptID ||
		len(storedResult.PublicQueryEvidence) != 1 || storedResult.PublicQueryEvidence[0].BodyHash != queryEvidence.BodyHash {
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

	verificationMission, _ := storedMission.ConsumeOperations(storedMission.Version, 1)
	verificationWork, _ := model.NewWork("deep-public-query-work", "deep_discovery_public_query",
		"deep-public-query-verification", "deep_discovery_public_query", "agent")
	verification, _ := model.NewDeepDiscoveryPublicQueryVerification("deep-public-query-verification", mission.MissionID,
		probe.ProbeID, verificationWork.WorkID, verificationMission.Version, model.PublicQueryRequestEvidence{
			EndpointURL: queryEvidence.EndpointURL, Method: queryEvidence.Method, Headers: queryEvidence.Headers,
			JSONBody: queryEvidence.JSONBody, BodyHash: queryEvidence.BodyHash})
	verificationPlacement := WorkPlacement{BusinessKey: "deep-public-query-verification", CompanyID: company.CompanyID,
		Capability: "http.fetch", Origin: "https://api.example.com", NotBefore: now.Add(6 * time.Second)}
	verificationResponse, _ := json.Marshal(map[string]any{"verification": verification, "mission": verificationMission})
	verificationReceipt, _ := model.NewCommandReceipt("deep-public-query-create", "recruiting.deep_discovery.public_query.verify",
		"sha256:deep-public-query-create", verificationResponse)
	verificationEvent, _ := model.NewEventIntent("event-deep-public-query-create", "deep.discovery.public_query.queued",
		"deep_discovery", mission.MissionID, verificationMission.Version, now.Add(6*time.Second).Format(time.RFC3339Nano),
		verificationReceipt.CommandID, json.RawMessage(`{"verification_id":"deep-public-query-verification"}`))
	if _, err := repository.ApplyCreatePublicQueryVerificationCommand(ctx, storedMission, verificationMission, verification,
		verificationWork, verificationPlacement, verificationReceipt, verificationEvent, nil, now.Add(6*time.Second)); err != nil {
		t.Fatal(err)
	}
	verificationOffer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "deep-public-query-attempt",
		ExecutorActorID: "tool:http:1", ExecutorIncarnation: "boot-http-1", Capability: "http.fetch",
		Origin: "https://api.example.com", OfferedAt: now.Add(7 * time.Second), BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || verificationOffer.Kind != "deep_discovery_public_query" || verificationOffer.PublicQueryVerification == nil {
		t.Fatalf("public query verification offer=%+v err=%v", verificationOffer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, verificationOffer.Attempt.AttemptID, "tool:http:1", "boot-http-1", now.Add(8*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, verificationOffer.Attempt.AttemptID, "tool:http:1", "boot-http-1", now.Add(9*time.Second)); err != nil {
		t.Fatal(err)
	}
	verifiedHash := "sha256:" + strings.Repeat("d", 64)
	verifiedArtifact, _ := model.NewArtifactMetadata("deep-public-query-response", model.ArtifactResponse, verifiedHash,
		"file://worker/recruiting/deep-public-query-response", verificationWork.WorkID, verificationOffer.Attempt.AttemptID,
		"operators", "30d", false)
	verifiedOutcome, err := repository.AcceptPublicQueryVerificationResult(ctx, PublicQueryVerificationResult{
		CommandID: "deep-public-query-result", RequestHash: "sha256:deep-public-query-result",
		AttemptID: verificationOffer.Attempt.AttemptID, ExecutorActorID: "tool:http:1", ExecutorIncarnation: "boot-http-1",
		Artifact: verifiedArtifact, StatusCode: 200, ContentType: "application/json; charset=utf-8",
		ContentHash: verifiedHash, RequestEndpointURL: queryEvidence.EndpointURL, RequestBodyHash: queryEvidence.BodyHash,
		ResponsePreview: json.RawMessage(`{"data":{"list":[{"jobId":"1"}]}}`), ResponsePreviewTruncated: false,
		ObservedAt: now.Add(10 * time.Second)})
	if err != nil || verifiedOutcome.Verification.Status != model.DeepDiscoveryPublicQueryCompleted ||
		verifiedOutcome.Work.Status != model.WorkCompleted {
		t.Fatalf("public query verification outcome=%+v err=%v", verifiedOutcome, err)
	}

	currentMission, _ := repository.GetDeepDiscoveryMission(ctx, mission.MissionID)
	stubMission, _ := currentMission.ConsumeOperations(currentMission.Version, 1)
	stubWork, _ := model.NewWork("deep-stub-browser-work", "deep_discovery_probe", "deep-stub-browser-probe",
		"deep_discovery_browser", "agent")
	stubProbe, _ := model.NewDeepDiscoveryBrowserProbe("deep-stub-browser-probe", mission.MissionID, stubWork.WorkID,
		"https://example.com/careers", "", 0, "", stubMission.Version, verification.VerificationID)
	stubPlacement := WorkPlacement{BusinessKey: "deep-stub-browser-probe", CompanyID: company.CompanyID,
		Capability: "browser.public", Origin: "https://example.com", NotBefore: now.Add(11 * time.Second)}
	stubResponse, _ := json.Marshal(map[string]any{"probe": stubProbe, "mission": stubMission})
	stubReceipt, _ := model.NewCommandReceipt("deep-stub-browser-create", "recruiting.deep_discovery.browser.observe",
		"sha256:deep-stub-browser-create", stubResponse)
	stubEvent, _ := model.NewEventIntent("event-deep-stub-browser-create", "deep.discovery.browser.queued", "deep_discovery",
		mission.MissionID, stubMission.Version, now.Add(11*time.Second).Format(time.RFC3339Nano), stubReceipt.CommandID,
		json.RawMessage(`{"probe_id":"deep-stub-browser-probe"}`))
	if _, err := repository.ApplyCreateDeepDiscoveryBrowserProbeCommand(ctx, currentMission, stubMission, stubProbe,
		stubWork, stubPlacement, stubReceipt, stubEvent, nil, now.Add(11*time.Second)); err != nil {
		t.Fatal(err)
	}
	stubOffer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "deep-stub-browser-attempt",
		ExecutorActorID: "tool:browser:1", ExecutorIncarnation: "boot-1", Capability: "browser.public",
		OfferedAt: now.Add(12 * time.Second), BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || len(stubOffer.PublicQueryStubs) != 1 ||
		stubOffer.PublicQueryStubs[0].Artifact.ArtifactID != verifiedArtifact.ArtifactID ||
		stubOffer.PublicQueryStubs[0].Request.BodyHash != queryEvidence.BodyHash {
		t.Fatalf("Stub browser offer=%+v err=%v", stubOffer, err)
	}
	automation, err := repository.GetDeepDiscoveryAutomationSnapshot(ctx, mission.MissionID)
	if err != nil || len(automation.BrowserProbes) != 2 || len(automation.QueryVerifications) != 1 ||
		automation.BrowserProbes[0].Result == nil ||
		automation.QueryVerifications[0].Verification.Artifact == nil {
		t.Fatalf("onboarding automation snapshot=%+v err=%v", automation, err)
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
	recruitmentType := model.RecruitmentURLType("")
	if kind == model.EvidenceListURL {
		recruitmentType = model.RecruitmentURLAll
	}
	node, err := model.NewDiscoveryEvidenceNodeWithType(kind, value, value, state, sensor, evidenceURL, "", basis, recruitmentType, "")
	if err != nil {
		t.Fatal(err)
	}
	return node
}
