package model

import (
	"fmt"
	"net/url"
	"strings"
)

type RecipeSampleValidationStatus string

type RecipeSampleValidationMode string

const (
	RecipeSampleValidationQueued    RecipeSampleValidationStatus = "queued"
	RecipeSampleValidationRunning   RecipeSampleValidationStatus = "running"
	RecipeSampleValidationCompleted RecipeSampleValidationStatus = "completed"
	RecipeSampleValidationCanceled  RecipeSampleValidationStatus = "canceled"
)

const (
	// RecipeSampleValidationCandidate is also represented by the empty value
	// when reading rows written before validation modes were introduced.
	RecipeSampleValidationCandidate RecipeSampleValidationMode = "candidate"
	RecipeSampleValidationRollout   RecipeSampleValidationMode = "rollout"
)

// RecipeSampleValidation is an evidence-only execution of a candidate Recipe
// against one frozen real business sample. It is deliberately separate from
// ListingRun and production Detail Work so validation cannot publish data.
type RecipeSampleValidation struct {
	Mode               RecipeSampleValidationMode   `json:"validation_mode,omitempty"`
	ValidationRunID    string                       `json:"validation_run_id"`
	WorkID             string                       `json:"work_id"`
	RecipeKind         RecipeKind                   `json:"recipe_kind"`
	CompanyID          string                       `json:"company_id"`
	SourceID           string                       `json:"source_id,omitempty"`
	CompanyVersion     uint64                       `json:"company_version"`
	SourceVersion      uint64                       `json:"source_version,omitempty"`
	Candidate          Recipe                       `json:"candidate"`
	ProposedAssignment SourceRecipeAssignment       `json:"proposed_assignment"`
	SampleJobID        string                       `json:"sample_job_id,omitempty"`
	SampleJobVersion   uint64                       `json:"sample_job_version,omitempty"`
	ExpectedFieldCount int                          `json:"expected_field_count"`
	EndpointURL        string                       `json:"endpoint_url"`
	EndpointVersion    uint64                       `json:"endpoint_version"`
	Origin             string                       `json:"origin"`
	Status             RecipeSampleValidationStatus `json:"validation_status"`
	Version            uint64                       `json:"version"`
}

func NewDetailRecipeSampleValidation(id, workID string, company Company, source RecruitmentSource,
	currentAssignment SourceRecipeAssignment, candidate Recipe, job SourceJob, expectedFieldCount int,
	effectiveAt string) (RecipeSampleValidation, error) {
	id, workID = strings.TrimSpace(id), strings.TrimSpace(workID)
	if id == "" || workID == "" || company.CompanyID != source.CompanyID || company.Version == 0 || source.Version == 0 ||
		company.OnboardingStatus != CompanyReady || company.ControlStatus != ControlActive ||
		source.ReadinessStatus != SourceReady || source.ControlStatus != ControlActive || source.HealthStatus != HealthHealthy ||
		source.DetailAssignment != nil ||
		candidate.Kind != RecipeDetail || candidate.Status != RecipeValidating || expectedFieldCount < 1 ||
		currentAssignment.SourceID != source.SourceID ||
		currentAssignment.Kind != RecipeDetail || job.SourceID != source.SourceID || job.JobID == "" || job.Version == 0 {
		return RecipeSampleValidation{}, fmt.Errorf("Detail Recipe validation requires a validating candidate and frozen Source Job")
	}
	proposed, err := currentAssignment.Replace(currentAssignment.AssignmentVersion, candidate.RecipeID,
		candidate.Version, candidate.ContractHash, effectiveAt)
	if err != nil {
		return RecipeSampleValidation{}, err
	}
	endpoint, err := url.Parse(job.DetailURL)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" || endpoint.User != nil || endpoint.Fragment != "" ||
		!strings.EqualFold(candidate.Scope, endpoint.Hostname()) {
		return RecipeSampleValidation{}, fmt.Errorf("Detail Recipe validation sample URL does not match candidate scope")
	}
	run := RecipeSampleValidation{ValidationRunID: id, WorkID: workID, RecipeKind: RecipeDetail,
		Mode:      RecipeSampleValidationCandidate,
		CompanyID: company.CompanyID, SourceID: source.SourceID, CompanyVersion: company.Version, SourceVersion: source.Version,
		Candidate: candidate, ProposedAssignment: proposed, SampleJobID: job.JobID, SampleJobVersion: job.Version,
		ExpectedFieldCount: expectedFieldCount,
		EndpointURL:        job.DetailURL, EndpointVersion: job.Version, Origin: endpoint.Scheme + "://" + endpoint.Host,
		Status: RecipeSampleValidationQueued, Version: 1}
	return run, run.Validate()
}

// NewBootstrapDetailRecipeSampleValidation validates the first Detail Recipe
// against a real Job identity observed by the finalized baseline. It freezes a
// proposed first Assignment but does not publish that Assignment or any detail
// data; approval and recipe.assign remain separate operator-visible steps.
func NewBootstrapDetailRecipeSampleValidation(id, workID string, company Company, source RecruitmentSource,
	candidate Recipe, job SourceJob, expectedFieldCount int, effectiveAt string) (RecipeSampleValidation, error) {
	id, workID = strings.TrimSpace(id), strings.TrimSpace(workID)
	bootstrapOnboarding := company.OnboardingStatus == CompanyDiscoveringSources ||
		company.OnboardingStatus == CompanyInitializing
	if id == "" || workID == "" || company.CompanyID != source.CompanyID || company.Version == 0 || source.Version == 0 ||
		!bootstrapOnboarding || company.ControlStatus != ControlActive ||
		source.ReadinessStatus != SourceReady || source.ControlStatus != ControlActive || source.HealthStatus != HealthHealthy ||
		source.DetailAssignment != nil || candidate.Kind != RecipeDetail || candidate.Status != RecipeValidating ||
		expectedFieldCount < 1 || job.SourceID != source.SourceID || job.JobID == "" || job.Version == 0 ||
		job.Status != JobDetailPending {
		return RecipeSampleValidation{}, fmt.Errorf("Detail Recipe bootstrap requires a ready Source, unassigned real pending Job, and validating candidate")
	}
	proposed, err := NewSourceRecipeAssignment(source.SourceID, RecipeDetail, candidate.RecipeID,
		candidate.Version, candidate.ContractHash, effectiveAt)
	if err != nil {
		return RecipeSampleValidation{}, err
	}
	endpoint, err := url.Parse(job.DetailURL)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" || endpoint.User != nil || endpoint.Fragment != "" ||
		!strings.EqualFold(candidate.Scope, endpoint.Hostname()) {
		return RecipeSampleValidation{}, fmt.Errorf("Detail Recipe bootstrap sample URL does not match candidate scope")
	}
	run := RecipeSampleValidation{ValidationRunID: id, WorkID: workID, RecipeKind: RecipeDetail,
		Mode: RecipeSampleValidationCandidate, CompanyID: company.CompanyID, SourceID: source.SourceID,
		CompanyVersion: company.Version, SourceVersion: source.Version, Candidate: candidate,
		ProposedAssignment: proposed, SampleJobID: job.JobID, SampleJobVersion: job.Version,
		ExpectedFieldCount: expectedFieldCount, EndpointURL: job.DetailURL, EndpointVersion: job.Version,
		Origin: endpoint.Scheme + "://" + endpoint.Host, Status: RecipeSampleValidationQueued, Version: 1}
	return run, run.Validate()
}

// NewDetailRecipeRolloutValidation executes an already-published Detail
// Recipe against one frozen real Job without publishing a DetailVersion. The
// Assignment has already been switched by the rollout item, so unlike a
// candidate validation there is no proposed future Assignment.
func NewDetailRecipeRolloutValidation(id, workID string, company Company, source RecruitmentSource,
	assignment SourceRecipeAssignment, recipe Recipe, job SourceJob) (RecipeSampleValidation, error) {
	id, workID = strings.TrimSpace(id), strings.TrimSpace(workID)
	if id == "" || workID == "" || company.CompanyID != source.CompanyID || company.Version == 0 ||
		company.OnboardingStatus != CompanyReady || company.ControlStatus != ControlActive ||
		source.ReadinessStatus != SourceReady || source.ControlStatus != ControlActive ||
		source.HealthStatus != HealthHealthy || source.DetailAssignment == nil || *source.DetailAssignment != assignment ||
		assignment.SourceID != source.SourceID || assignment.Kind != RecipeDetail ||
		recipe.Kind != RecipeDetail || recipe.Status != RecipeActive || recipe.RecipeID != assignment.RecipeID ||
		recipe.Version != assignment.RecipeVersion || recipe.ContractHash != assignment.ContractHash ||
		job.SourceID != source.SourceID || job.JobID == "" || job.Version == 0 || job.Status != JobAvailable {
		return RecipeSampleValidation{}, fmt.Errorf("Detail rollout validation requires an active assigned Recipe and stable Source Job")
	}
	endpoint, err := url.Parse(job.DetailURL)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" || endpoint.User != nil || endpoint.Fragment != "" ||
		!strings.EqualFold(recipe.Scope, endpoint.Hostname()) {
		return RecipeSampleValidation{}, fmt.Errorf("Detail rollout validation sample URL does not match Recipe scope")
	}
	run := RecipeSampleValidation{Mode: RecipeSampleValidationRollout, ValidationRunID: id, WorkID: workID,
		RecipeKind: RecipeDetail, CompanyID: company.CompanyID, SourceID: source.SourceID,
		CompanyVersion: company.Version, SourceVersion: source.Version, Candidate: recipe,
		ProposedAssignment: assignment, SampleJobID: job.JobID, SampleJobVersion: job.Version,
		ExpectedFieldCount: 1, EndpointURL: job.DetailURL, EndpointVersion: job.Version,
		Origin: endpoint.Scheme + "://" + endpoint.Host, Status: RecipeSampleValidationQueued, Version: 1}
	return run, run.Validate()
}

func NewDiscoveryRecipeSampleValidation(id, workID string, company Company, candidate Recipe,
	expectedFieldCount int) (RecipeSampleValidation, error) {
	id, workID = strings.TrimSpace(id), strings.TrimSpace(workID)
	if id == "" || workID == "" || company.CompanyID == "" || company.Version == 0 ||
		company.ControlStatus != ControlActive || candidate.Kind != RecipeDiscovery ||
		candidate.Status != RecipeValidating || expectedFieldCount < 1 {
		return RecipeSampleValidation{}, fmt.Errorf("Discovery Recipe validation requires an active Company and validating candidate")
	}
	endpoint, err := url.Parse(company.Website)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" || endpoint.User != nil || endpoint.Fragment != "" ||
		!strings.EqualFold(candidate.Scope, endpoint.Hostname()) {
		return RecipeSampleValidation{}, fmt.Errorf("Discovery Recipe validation sample does not match Company scope")
	}
	run := RecipeSampleValidation{ValidationRunID: id, WorkID: workID, RecipeKind: RecipeDiscovery,
		Mode:      RecipeSampleValidationCandidate,
		CompanyID: company.CompanyID, CompanyVersion: company.Version, Candidate: candidate,
		ExpectedFieldCount: expectedFieldCount, EndpointURL: company.Website, EndpointVersion: company.Version,
		Origin: endpoint.Scheme + "://" + endpoint.Host, Status: RecipeSampleValidationQueued, Version: 1}
	return run, run.Validate()
}

func (r RecipeSampleValidation) Validate() error {
	mode := r.Mode
	if mode == "" {
		mode = RecipeSampleValidationCandidate
	}
	if r.ValidationRunID == "" || r.WorkID == "" || r.CompanyID == "" || r.CompanyVersion == 0 ||
		r.ExpectedFieldCount < 1 || r.Version == 0 || r.Candidate.Kind != r.RecipeKind ||
		(mode != RecipeSampleValidationCandidate && mode != RecipeSampleValidationRollout) ||
		(mode == RecipeSampleValidationCandidate && r.Candidate.Status != RecipeValidating) ||
		(mode == RecipeSampleValidationRollout && r.Candidate.Status != RecipeActive) {
		return fmt.Errorf("Recipe sample validation is incomplete or inconsistent")
	}
	switch r.RecipeKind {
	case RecipeDetail:
		if r.SourceID == "" || r.SourceVersion == 0 || r.SampleJobID == "" || r.SampleJobVersion == 0 ||
			r.EndpointVersion != r.SampleJobVersion || r.ProposedAssignment.SourceID != r.SourceID ||
			r.ProposedAssignment.Kind != r.RecipeKind || r.ProposedAssignment.RecipeID != r.Candidate.RecipeID ||
			r.ProposedAssignment.RecipeVersion != r.Candidate.Version ||
			r.ProposedAssignment.ContractHash != r.Candidate.ContractHash {
			return fmt.Errorf("Detail Recipe sample validation is incomplete or inconsistent")
		}
		if mode == RecipeSampleValidationRollout &&
			r.ProposedAssignment.AssignmentVersion == 0 {
			return fmt.Errorf("Detail rollout validation requires the published Assignment")
		}
	case RecipeDiscovery:
		if mode != RecipeSampleValidationCandidate {
			return fmt.Errorf("Discovery Recipe rollout validation is unsupported")
		}
		if r.SourceID != "" || r.SourceVersion != 0 || r.SampleJobID != "" || r.SampleJobVersion != 0 ||
			r.EndpointVersion != r.CompanyVersion || r.ProposedAssignment != (SourceRecipeAssignment{}) {
			return fmt.Errorf("Discovery Recipe sample validation is incomplete or inconsistent")
		}
	default:
		return fmt.Errorf("Recipe sample validation kind is unsupported")
	}
	if err := r.Candidate.Validate(); err != nil {
		return err
	}
	endpoint, err := url.Parse(r.EndpointURL)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" || endpoint.User != nil || endpoint.Fragment != "" ||
		r.Origin != endpoint.Scheme+"://"+endpoint.Host || !strings.EqualFold(r.Candidate.Scope, endpoint.Hostname()) {
		return fmt.Errorf("Recipe sample validation endpoint is invalid")
	}
	switch r.Status {
	case RecipeSampleValidationQueued, RecipeSampleValidationRunning, RecipeSampleValidationCompleted,
		RecipeSampleValidationCanceled:
	default:
		return fmt.Errorf("Recipe sample validation status is invalid")
	}
	return nil
}

func (r RecipeSampleValidation) Start(expected uint64) (RecipeSampleValidation, error) {
	if err := requireVersion(expected, r.Version); err != nil {
		return RecipeSampleValidation{}, err
	}
	if r.Status == RecipeSampleValidationRunning {
		return r, nil
	}
	if r.Status != RecipeSampleValidationQueued {
		return RecipeSampleValidation{}, &InvalidTransitionError{Entity: "Recipe sample validation", From: string(r.Status), Action: "start"}
	}
	r.Status, r.Version = RecipeSampleValidationRunning, r.Version+1
	return r, nil
}

func (r RecipeSampleValidation) Complete(expected uint64) (RecipeSampleValidation, error) {
	if err := requireVersion(expected, r.Version); err != nil {
		return RecipeSampleValidation{}, err
	}
	if r.Status != RecipeSampleValidationRunning {
		return RecipeSampleValidation{}, &InvalidTransitionError{Entity: "Recipe sample validation", From: string(r.Status), Action: "complete"}
	}
	r.Status, r.Version = RecipeSampleValidationCompleted, r.Version+1
	return r, nil
}

func (r RecipeSampleValidation) Cancel(expected uint64) (RecipeSampleValidation, error) {
	if err := requireVersion(expected, r.Version); err != nil {
		return RecipeSampleValidation{}, err
	}
	if r.Status != RecipeSampleValidationQueued && r.Status != RecipeSampleValidationRunning {
		return RecipeSampleValidation{}, &InvalidTransitionError{
			Entity: "Recipe sample validation", From: string(r.Status), Action: "cancel"}
	}
	r.Status, r.Version = RecipeSampleValidationCanceled, r.Version+1
	return r, nil
}

// RebindWork preserves the frozen sample and candidate while moving an
// unfinished validation run to a distinct causal retry Work.
func (r RecipeSampleValidation) RebindWork(expected uint64, previousWorkID, retryWorkID string) (RecipeSampleValidation, error) {
	if err := requireVersion(expected, r.Version); err != nil {
		return RecipeSampleValidation{}, err
	}
	previousWorkID, retryWorkID = strings.TrimSpace(previousWorkID), strings.TrimSpace(retryWorkID)
	if (r.Status != RecipeSampleValidationQueued && r.Status != RecipeSampleValidationRunning) ||
		r.WorkID != previousWorkID || previousWorkID == "" || retryWorkID == "" || previousWorkID == retryWorkID {
		return RecipeSampleValidation{}, &InvalidTransitionError{Entity: "Recipe sample validation",
			From: string(r.Status), Action: "rebind retry work"}
	}
	r.WorkID, r.Version = retryWorkID, r.Version+1
	return r, r.Validate()
}
