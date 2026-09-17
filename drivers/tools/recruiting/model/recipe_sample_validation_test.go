package model

import "testing"

func TestDetailRecipeSampleValidationFreezesCandidateAndJob(t *testing.T) {
	company, _ := NewCompany("company-1", "Company", "https://company.example")
	source, _ := NewRecruitmentSource("source-1", company.CompanyID, "https://jobs.example/openings", "all", 1)
	company.OnboardingStatus = CompanyReady
	source.ReadinessStatus = SourceReady
	current, _ := NewSourceRecipeAssignment(source.SourceID, RecipeDetail, "detail-old", 1, "contract",
		"2026-09-10T00:00:00Z")
	candidate, _ := NewRecipe("detail-new", RecipeDetail, "apply.example", 2, "content", "contract",
		RecipeExecution{ABIVersion: RecipeABIVersion, ContentRef: "recipe://detail/new",
			RequiredCapability: "http.fetch", Transport: RecipeTransportHTTPHTML})
	candidate, _ = candidate.BeginValidation(candidate.StateVersion)
	job, _ := NewSourceJob("job-1", source.SourceID, "external-1", "https://apply.example/jobs/1")
	run, err := NewDetailRecipeSampleValidation("validation-1", "work-1", company, source, current,
		candidate, job, 3, "2026-09-10T01:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if run.EndpointURL != job.DetailURL || run.EndpointVersion != job.Version ||
		run.ProposedAssignment.AssignmentVersion != current.AssignmentVersion+1 || run.ExpectedFieldCount != 3 {
		t.Fatalf("run did not freeze Detail candidate and Job: %+v", run)
	}
	running, err := run.Start(run.Version)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := running.Complete(running.Version); err != nil {
		t.Fatal(err)
	}
	canceled, err := running.Cancel(running.Version)
	if err != nil || canceled.Status != RecipeSampleValidationCanceled || canceled.Version != running.Version+1 {
		t.Fatalf("canceled validation=%+v err=%v", canceled, err)
	}
	if err := canceled.Validate(); err != nil {
		t.Fatalf("canceled validation is invalid: %v", err)
	}
}

func TestFirstDetailRecipeValidationUsesPendingBaselineJob(t *testing.T) {
	company, _ := NewCompany("bootstrap-detail-company", "Company", "https://company.example")
	company.OnboardingStatus = CompanyInitializing
	source, _ := NewRecruitmentSource("bootstrap-detail-source", company.CompanyID,
		"https://jobs.example/openings", "all", 1)
	source.ReadinessStatus = SourceReady
	candidate, _ := NewRecipe("bootstrap-detail", RecipeDetail, "jobs.example", 1, "content", "contract",
		RecipeExecution{ABIVersion: RecipeABIVersion, ContentRef: "recipe://detail/bootstrap",
			RequiredCapability: "http.fetch", Transport: RecipeTransportHTTPHTML})
	candidate, _ = candidate.BeginValidation(candidate.StateVersion)
	job, _ := NewSourceJob("bootstrap-detail-job", source.SourceID, "external-1", "https://jobs.example/1")
	run, err := NewBootstrapDetailRecipeSampleValidation("bootstrap-detail-validation", "bootstrap-detail-work",
		company, source, candidate, job, 4, "2026-09-10T01:00:00Z")
	if err != nil || run.ProposedAssignment.AssignmentVersion != 1 || run.SampleJobID != job.JobID {
		t.Fatalf("bootstrap Detail validation=%+v err=%v", run, err)
	}
	company.OnboardingStatus = CompanyReady
	readyRun, err := NewBootstrapDetailRecipeSampleValidation("ready-bootstrap-detail-validation",
		"ready-bootstrap-detail-work", company, source, candidate, job, 4, "2026-09-10T01:00:00Z")
	if err != nil || readyRun.ProposedAssignment.AssignmentVersion != 1 || readyRun.SampleJobID != job.JobID {
		t.Fatalf("ready-company bootstrap Detail validation=%+v err=%v", readyRun, err)
	}
	source.DetailAssignment = &run.ProposedAssignment
	if _, err := NewBootstrapDetailRecipeSampleValidation("invalid", "invalid-work", company, source,
		candidate, job, 4, "2026-09-10T01:00:00Z"); err == nil {
		t.Fatal("bootstrap Detail validation accepted a Source with an existing assignment")
	}
}

func TestDiscoveryRecipeSampleValidationHasNoFakeSourceFence(t *testing.T) {
	company, _ := NewCompany("company-discovery", "Company", "https://company.example/careers")
	candidate, _ := NewRecipe("discovery-new", RecipeDiscovery, "company.example", 2, "content", "contract",
		RecipeExecution{ABIVersion: RecipeABIVersion, ContentRef: "recipe://discovery/new",
			RequiredCapability: "http.fetch", Transport: RecipeTransportHTTPHTML})
	candidate, _ = candidate.BeginValidation(candidate.StateVersion)
	run, err := NewDiscoveryRecipeSampleValidation("validation-discovery", "work-discovery", company, candidate, 2)
	if err != nil {
		t.Fatal(err)
	}
	if run.SourceID != "" || run.SourceVersion != 0 || run.SampleJobID != "" ||
		run.ProposedAssignment != (SourceRecipeAssignment{}) || run.EndpointVersion != company.Version {
		t.Fatalf("Discovery validation invented Source facts: %+v", run)
	}
	running, _ := run.Start(run.Version)
	rebound, err := running.RebindWork(running.Version, "work-discovery", "work-discovery-retry")
	if err != nil || rebound.WorkID != "work-discovery-retry" || rebound.Version != running.Version+1 ||
		rebound.ValidationRunID != running.ValidationRunID || rebound.Candidate != running.Candidate {
		t.Fatalf("Discovery validation retry did not preserve its frozen input: run=%+v err=%v", rebound, err)
	}
	if _, err := rebound.RebindWork(rebound.Version, "work-discovery", "another-retry"); err == nil {
		t.Fatal("Discovery validation accepted a retry from the stale Work")
	}
}

func TestDetailRolloutValidationUsesPublishedAssignmentWithoutMutatingJob(t *testing.T) {
	company, _ := NewCompany("rollout-company", "Company", "https://rollout.example")
	company.OnboardingStatus = CompanyReady
	source, _ := NewRecruitmentSource("rollout-source", company.CompanyID,
		"https://rollout.example/jobs", "all", 1)
	source.ReadinessStatus = SourceReady
	recipe, _ := NewRecipe("rollout-detail", RecipeDetail, "rollout.example", 2, "content", "contract",
		RecipeExecution{ABIVersion: RecipeABIVersion, ContentRef: "recipe://rollout/detail",
			RequiredCapability: "http.fetch", Transport: RecipeTransportHTTPHTML})
	recipe, _ = recipe.BeginValidation(recipe.StateVersion)
	recipe, _ = recipe.Publish(recipe.StateVersion)
	assignment, _ := NewSourceRecipeAssignment(source.SourceID, RecipeDetail, recipe.RecipeID,
		recipe.Version, recipe.ContractHash, "2026-09-10T00:00:00Z")
	source, _ = source.AssignRecipe(source.Version, assignment, false)
	job, _ := NewSourceJob("rollout-job", source.SourceID, "external-1",
		"https://rollout.example/jobs/1")
	accepted, _ := job.AcceptDetail(job.Version, job.RefreshGeneration, "sha256:sample")
	job = accepted.Job
	before := job
	run, err := NewDetailRecipeRolloutValidation("rollout-validation", "rollout-work", company,
		source, assignment, recipe, job)
	if err != nil {
		t.Fatal(err)
	}
	if run.Mode != RecipeSampleValidationRollout || run.ProposedAssignment != assignment ||
		run.ExpectedFieldCount != 1 || job != before {
		t.Fatalf("rollout validation run=%+v job before=%+v after=%+v", run, before, job)
	}
	if _, err := NewDetailRecipeRolloutValidation("invalid", "invalid-work", company,
		source, assignment, recipe, SourceJob{}); err == nil {
		t.Fatal("Detail rollout validation accepted a missing stable sample Job")
	}
}
