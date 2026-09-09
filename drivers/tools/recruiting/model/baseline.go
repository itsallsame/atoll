package model

import (
	"fmt"
	"strings"
)

type BaselineStatus string

const (
	BaselineListing        BaselineStatus = "listing"
	BaselineDetailsPending BaselineStatus = "details_pending"
	BaselineCompleted      BaselineStatus = "completed"
	BaselineWithExceptions BaselineStatus = "completed_with_exceptions"
)

type BaselineGeneration struct {
	SourceID                 string                   `json:"source_id"`
	WorkID                   string                   `json:"work_id,omitempty"`
	Generation               uint64                   `json:"baseline_generation"`
	CompanyVersion           uint64                   `json:"company_version,omitempty"`
	SourceVersion            uint64                   `json:"source_version,omitempty"`
	ListingExecution         ListingExecutionSnapshot `json:"listing_execution,omitempty"`
	CheckpointStrategy       CheckpointStrategy       `json:"checkpoint_strategy,omitempty"`
	OverlapPages             int                      `json:"overlap_pages,omitempty"`
	Status                   BaselineStatus           `json:"status"`
	ListingFinalized         bool                     `json:"listing_finalized"`
	DetailsExpected          uint64                   `json:"details_expected"`
	MaterializationCursor    string                   `json:"materialization_cursor,omitempty"`
	MaterializedCount        uint64                   `json:"materialized_count"`
	MaterializationCompleted bool                     `json:"materialization_completed"`
	DetailsAccounted         uint64                   `json:"details_accounted"`
	DetailExceptions         uint64                   `json:"detail_exceptions"`
	Version                  uint64                   `json:"version"`
}

func NewExecutableBaselineGeneration(workID string, company Company, source RecruitmentSource, generation uint64,
	recipe Recipe) (BaselineGeneration, error) {
	workID = strings.TrimSpace(workID)
	if workID == "" || source.CompanyID != company.CompanyID || generation == 0 ||
		company.ControlStatus != ControlActive ||
		(company.OnboardingStatus != CompanyDiscoveringSources && company.OnboardingStatus != CompanyInitializing) ||
		source.ReadinessStatus != SourceReady || source.ControlStatus != ControlActive || source.HealthStatus != HealthHealthy ||
		!source.HasVerifiedIncrementalContract() {
		return BaselineGeneration{}, fmt.Errorf("executable baseline requires active onboarding Company and verified ready Source")
	}
	execution, err := NewListingExecutionSnapshot(source, recipe)
	if err != nil {
		return BaselineGeneration{}, err
	}
	return BaselineGeneration{SourceID: source.SourceID, WorkID: workID, Generation: generation,
		CompanyVersion: company.Version, SourceVersion: source.Version, ListingExecution: execution,
		CheckpointStrategy: source.ContractAssessment.CheckpointStrategy, OverlapPages: source.ContractAssessment.OverlapPages,
		Status: BaselineListing, Version: 1}, nil
}

func NewBaselineGeneration(sourceID string, generation uint64) (BaselineGeneration, error) {
	if strings.TrimSpace(sourceID) == "" || generation == 0 {
		return BaselineGeneration{}, fmt.Errorf("source and baseline generation are required")
	}
	return BaselineGeneration{SourceID: sourceID, Generation: generation, Status: BaselineListing, Version: 1}, nil
}

func (b BaselineGeneration) FinalizeListing(expectedVersion, detailsExpected uint64) (BaselineGeneration, error) {
	if err := requireVersion(expectedVersion, b.Version); err != nil {
		return BaselineGeneration{}, err
	}
	if b.Status != BaselineListing || b.ListingFinalized {
		return BaselineGeneration{}, &InvalidTransitionError{Entity: "baseline", From: string(b.Status), Action: "finalize listing"}
	}
	b.ListingFinalized = true
	b.DetailsExpected = detailsExpected
	if detailsExpected == 0 {
		b.Status = BaselineCompleted
		b.MaterializationCompleted = true
	} else {
		b.Status = BaselineDetailsPending
	}
	b.Version++
	return b, nil
}

func (b BaselineGeneration) AdvanceMaterialization(expectedVersion uint64, cursor string, count uint64, completed bool) (BaselineGeneration, error) {
	if err := requireVersion(expectedVersion, b.Version); err != nil {
		return BaselineGeneration{}, err
	}
	if b.Status != BaselineDetailsPending || !b.ListingFinalized || b.MaterializationCompleted {
		return BaselineGeneration{}, &InvalidTransitionError{Entity: "baseline", From: string(b.Status), Action: "advance materialization"}
	}
	if count == 0 || count > 500 || strings.TrimSpace(cursor) == "" || b.MaterializedCount+count > b.DetailsExpected {
		return BaselineGeneration{}, fmt.Errorf("baseline materialization page is invalid")
	}
	nextCount := b.MaterializedCount + count
	if completed != (nextCount == b.DetailsExpected) {
		return BaselineGeneration{}, fmt.Errorf("baseline materialization completion does not match expected details")
	}
	b.MaterializationCursor = cursor
	b.MaterializedCount = nextCount
	b.MaterializationCompleted = completed
	b.Version++
	return b, nil
}

func (b BaselineGeneration) AccountDetails(expectedVersion, succeeded, acceptedGaps, failed uint64) (BaselineGeneration, error) {
	if err := requireVersion(expectedVersion, b.Version); err != nil {
		return BaselineGeneration{}, err
	}
	if b.Status != BaselineDetailsPending {
		return BaselineGeneration{}, &InvalidTransitionError{Entity: "baseline", From: string(b.Status), Action: "account details"}
	}
	accounted := succeeded + acceptedGaps + failed
	if b.DetailsAccounted+accounted > b.DetailsExpected {
		return BaselineGeneration{}, fmt.Errorf("detail accounting exceeds baseline expectation")
	}
	b.DetailsAccounted += accounted
	b.DetailExceptions += acceptedGaps + failed
	if b.DetailsAccounted == b.DetailsExpected {
		if b.DetailExceptions == 0 {
			b.Status = BaselineCompleted
		} else {
			b.Status = BaselineWithExceptions
		}
	}
	b.Version++
	return b, nil
}
