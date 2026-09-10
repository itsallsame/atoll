package model

import (
	"fmt"
	"net/url"
	"strings"
)

type RecipeSampleValidationStatus string

const (
	RecipeSampleValidationQueued    RecipeSampleValidationStatus = "queued"
	RecipeSampleValidationRunning   RecipeSampleValidationStatus = "running"
	RecipeSampleValidationCompleted RecipeSampleValidationStatus = "completed"
)

// RecipeSampleValidation is an evidence-only execution of a candidate Recipe
// against one frozen real business sample. It is deliberately separate from
// ListingRun and production Detail Work so validation cannot publish data.
type RecipeSampleValidation struct {
	ValidationRunID    string                       `json:"validation_run_id"`
	WorkID             string                       `json:"work_id"`
	RecipeKind         RecipeKind                   `json:"recipe_kind"`
	SourceID           string                       `json:"source_id"`
	CompanyVersion     uint64                       `json:"company_version"`
	SourceVersion      uint64                       `json:"source_version"`
	Candidate          Recipe                       `json:"candidate"`
	ProposedAssignment SourceRecipeAssignment       `json:"proposed_assignment"`
	SampleJobID        string                       `json:"sample_job_id"`
	SampleJobVersion   uint64                       `json:"sample_job_version"`
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
		SourceID: source.SourceID, CompanyVersion: company.Version, SourceVersion: source.Version,
		Candidate: candidate, ProposedAssignment: proposed, SampleJobID: job.JobID, SampleJobVersion: job.Version,
		ExpectedFieldCount: expectedFieldCount,
		EndpointURL:        job.DetailURL, EndpointVersion: job.Version, Origin: endpoint.Scheme + "://" + endpoint.Host,
		Status: RecipeSampleValidationQueued, Version: 1}
	return run, run.Validate()
}

func (r RecipeSampleValidation) Validate() error {
	if r.ValidationRunID == "" || r.WorkID == "" || r.RecipeKind != RecipeDetail || r.SourceID == "" ||
		r.CompanyVersion == 0 || r.SourceVersion == 0 || r.SampleJobID == "" || r.SampleJobVersion == 0 ||
		r.ExpectedFieldCount < 1 ||
		r.EndpointVersion != r.SampleJobVersion || r.Version == 0 || r.Candidate.Kind != r.RecipeKind ||
		r.Candidate.Status != RecipeValidating || r.ProposedAssignment.SourceID != r.SourceID ||
		r.ProposedAssignment.Kind != r.RecipeKind || r.ProposedAssignment.RecipeID != r.Candidate.RecipeID ||
		r.ProposedAssignment.RecipeVersion != r.Candidate.Version ||
		r.ProposedAssignment.ContractHash != r.Candidate.ContractHash {
		return fmt.Errorf("Recipe sample validation is incomplete or inconsistent")
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
	case RecipeSampleValidationQueued, RecipeSampleValidationRunning, RecipeSampleValidationCompleted:
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
