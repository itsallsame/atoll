package model

import "testing"

func verifiedAssessment(source RecruitmentSource, assignment SourceRecipeAssignment) SourceContractAssessment {
	revision := uint64(0)
	if source.CandidateEndpoint != nil {
		revision = source.CandidateEndpoint.Revision
	}
	return SourceContractAssessment{
		SourceID: source.SourceID, EndpointRevision: revision, RecipeID: assignment.RecipeID,
		RecipeVersion: assignment.RecipeVersion, ContractHash: assignment.ContractHash,
		Identity: ContractVerified, Pagination: ContractVerified, Ordering: ContractVerified, UpdateRetop: ContractVerified,
		CheckpointStrategy: CheckpointActivityTime, OverlapPages: 1,
		EvidenceArtifactIDs: []string{"artifact-calibration-a", "artifact-calibration-b"},
		AssessedAt:          "2026-09-07T00:00:00Z", Version: 1,
	}
}

func validatedSource(t *testing.T) (Company, RecruitmentSource) {
	t.Helper()
	company := readyCompany(t)
	source, err := NewRecruitmentSource("source-1", company.CompanyID, "HTTPS://Jobs.Example.com/openings/?utm_source=x&team=eng", "Engineering", 1)
	if err != nil {
		t.Fatal(err)
	}
	source, err = source.BeginValidation(source.Version)
	if err != nil {
		t.Fatal(err)
	}
	assignment, err := NewSourceRecipeAssignment(source.SourceID, RecipeListing, "recipe-1", 3, "contract-a", "2026-09-07T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	source, err = source.PublishValidated(source.Version, assignment, verifiedAssessment(source, assignment))
	if err != nil {
		t.Fatal(err)
	}
	return company, source
}

func TestSourceCandidateCanBeRejected(t *testing.T) {
	s, err := NewRecruitmentSource("source-1", "company-1", "https://jobs.example.com", "all", 1)
	if err != nil {
		t.Fatal(err)
	}
	s, err = s.RejectCandidate(1)
	if err != nil || s.ReadinessStatus != SourceRejected {
		t.Fatalf("reject = %+v, %v", s, err)
	}
	if _, err := s.BeginValidation(s.Version); err == nil {
		t.Fatal("rejected candidate re-entered validation without a new generation")
	}
}

func TestSourcePublishesCandidateAtomicallyAndKeepsOldEndpointDuringRepair(t *testing.T) {
	company, source := validatedSource(t)
	if !source.EligibleForDailyRun(company) {
		t.Fatal("validated source is not daily eligible")
	}
	old := *source.ActiveEndpoint
	staged, err := source.StageEndpoint(source.Version, "https://jobs.example.com/v2", "Engineering")
	if err != nil {
		t.Fatal(err)
	}
	if staged.ReadinessStatus != SourceRepairing || staged.ActiveEndpoint.URL != old.URL || staged.CandidateEndpoint == nil {
		t.Fatalf("staging replaced production endpoint: %+v", staged)
	}
	validating, err := staged.BeginValidation(staged.Version)
	if err != nil {
		t.Fatal(err)
	}
	rejected, err := validating.RejectCandidate(validating.Version)
	if err != nil || rejected.ReadinessStatus != SourceReady || rejected.ActiveEndpoint.URL != old.URL || rejected.CandidateEndpoint != nil {
		t.Fatalf("repair candidate rejection did not retain production: %+v, %v", rejected, err)
	}
	staged, _ = rejected.StageEndpoint(rejected.Version, "https://jobs.example.com/v2", "Engineering")
	validating, _ = staged.BeginValidation(staged.Version)
	assignment, err := NewSourceRecipeAssignment(validating.SourceID, RecipeListing, "recipe-2", 1, "contract-b", "2026-09-07T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	published, err := validating.PublishValidated(validating.Version, assignment, verifiedAssessment(validating, assignment))
	if err != nil {
		t.Fatal(err)
	}
	if published.ActiveEndpoint.URL != "https://jobs.example.com/v2" || published.CandidateEndpoint != nil {
		t.Fatalf("candidate was not atomically published: %+v", published)
	}
}

func TestSourceCannotPublishAnUnverifiedIncrementalContract(t *testing.T) {
	source, _ := NewRecruitmentSource("source-1", "company-1", "https://jobs.example.com", "all", 1)
	source, _ = source.BeginValidation(source.Version)
	assignment, _ := NewSourceRecipeAssignment(source.SourceID, RecipeListing, "recipe-1", 1, "contract-a", "2026-09-07T00:00:00Z")
	assessment := verifiedAssessment(source, assignment)
	assessment.UpdateRetop = ContractUnverified
	if _, err := source.PublishValidated(source.Version, assignment, assessment); err == nil {
		t.Fatal("source published without verified update-retop evidence")
	}
	assessment.UpdateRetop = ContractVerified
	assessment.Ordering = ContractViolated
	if _, err := source.PublishValidated(source.Version, assignment, assessment); err == nil {
		t.Fatal("source published despite an ordering-contract violation")
	}
}

func TestDailyEligibilityRequiresAssessmentToMatchCurrentFacts(t *testing.T) {
	company, source := validatedSource(t)
	for name, mutate := range map[string]func(*RecruitmentSource){
		"endpoint": func(s *RecruitmentSource) { s.ActiveEndpoint.Revision++ },
		"recipe":   func(s *RecruitmentSource) { s.ListingAssignment.RecipeVersion++ },
		"contract": func(s *RecruitmentSource) { s.ListingAssignment.ContractHash = "contract-b" },
		"missing":  func(s *RecruitmentSource) { s.ContractAssessment = nil },
	} {
		t.Run(name, func(t *testing.T) {
			changed := source
			endpoint := *source.ActiveEndpoint
			assignment := *source.ListingAssignment
			assessment := *source.ContractAssessment
			changed.ActiveEndpoint, changed.ListingAssignment, changed.ContractAssessment = &endpoint, &assignment, &assessment
			mutate(&changed)
			if changed.EligibleForDailyRun(company) {
				t.Fatal("stale or missing assessment remained daily eligible")
			}
		})
	}
}

func TestSourceControlHealthAndReadinessAreOrthogonal(t *testing.T) {
	company, source := validatedSource(t)
	paused, err := source.Pause(source.Version, PauseDrain)
	if err != nil {
		t.Fatal(err)
	}
	degraded, err := paused.SetHealth(paused.Version, HealthCircuitOpen)
	if err != nil {
		t.Fatal(err)
	}
	if degraded.ControlStatus != ControlPaused || degraded.ReadinessStatus != SourceReady || degraded.HealthStatus != HealthCircuitOpen {
		t.Fatalf("orthogonal status collapsed: %+v", degraded)
	}
	resumed, err := degraded.Resume(degraded.Version)
	if err != nil {
		t.Fatal(err)
	}
	if resumed.EligibleForDailyRun(company) {
		t.Fatal("circuit-open source became eligible merely because user resumed it")
	}
}

func TestSourceArchiveRestoreRequiresValidation(t *testing.T) {
	_, source := validatedSource(t)
	archived, err := source.Archive(source.Version)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := archived.Restore(archived.Version)
	if err != nil {
		t.Fatal(err)
	}
	if restored.ControlStatus != ControlPaused || restored.ReadinessStatus != SourceRepairing {
		t.Fatalf("restored source skipped paused validation: %+v", restored)
	}
	if restored.CandidateEndpoint == nil {
		t.Fatal("restored source did not stage its last active endpoint for compatibility validation")
	}
}

func TestRecipeAssignmentHasIndependentCAS(t *testing.T) {
	assignment, err := NewSourceRecipeAssignment("source-1", RecipeListing, "recipe-1", 2, "contract-a", "2026-09-07T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := assignment.Replace(assignment.AssignmentVersion-1, "recipe-2", 3, "contract-a", "2026-09-08T00:00:00Z"); err == nil {
		t.Fatal("stale assignment replacement was accepted")
	}
	replaced, err := assignment.Replace(assignment.AssignmentVersion, "recipe-2", 3, "contract-a", "2026-09-08T00:00:00Z")
	if err != nil || replaced.AssignmentVersion != 2 || replaced.RecipeVersion != 3 {
		t.Fatalf("assignment replacement = %+v %v", replaced, err)
	}
}

func TestListingRecipeChangeForcesSourceRecalibration(t *testing.T) {
	_, source := validatedSource(t)
	current := *source.ListingAssignment
	replacement, err := current.Replace(current.AssignmentVersion, "recipe-2", current.RecipeVersion+1,
		current.ContractHash, "2026-09-08T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	changed, err := source.AssignRecipe(source.Version, replacement, true)
	if err != nil {
		t.Fatal(err)
	}
	if changed.ReadinessStatus != SourceRepairing || changed.ContractAssessment != nil || changed.CandidateEndpoint == nil {
		t.Fatalf("listing implementation changed without explicit recalibration state: %+v", changed)
	}
}

func TestListingAssignmentContractChangeRequiresCompatibilityProof(t *testing.T) {
	_, source := validatedSource(t)
	next, err := NewSourceRecipeAssignment(source.SourceID, RecipeListing, "recipe-2", 4, "contract-b", "2026-09-08T00:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.AssignRecipe(source.Version, next, false); err == nil {
		t.Fatal("incompatible listing assignment reused checkpoint without proof")
	}
	updated, err := source.AssignRecipe(source.Version, next, true)
	if err != nil || updated.ListingAssignment.RecipeVersion != 4 {
		t.Fatalf("compatible rollout = %+v %v", updated, err)
	}
	detail, _ := NewSourceRecipeAssignment(source.SourceID, RecipeDetail, "detail-1", 2, "detail-contract", "2026-09-08T00:00:00Z")
	updated, err = updated.AssignRecipe(updated.Version, detail, false)
	if err != nil || updated.DetailAssignment == nil || updated.ListingAssignment == nil {
		t.Fatalf("orthogonal detail assignment = %+v %v", updated, err)
	}
}
