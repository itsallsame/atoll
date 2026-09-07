package model

import "testing"

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
	source, err = source.PublishValidated(source.Version, RecipeAssignment{RecipeID: "recipe-1", Version: 3, ContractHash: "contract-a"})
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
	published, err := validating.PublishValidated(validating.Version, RecipeAssignment{RecipeID: "recipe-2", Version: 1, ContractHash: "contract-b"})
	if err != nil {
		t.Fatal(err)
	}
	if published.ActiveEndpoint.URL != "https://jobs.example.com/v2" || published.CandidateEndpoint != nil {
		t.Fatalf("candidate was not atomically published: %+v", published)
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
