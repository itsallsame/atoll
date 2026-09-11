package model

import (
	"fmt"
	"net/url"
	"strings"
	"time"
)

type OccurrenceStatus string

const (
	OccurrencePlanned   OccurrenceStatus = "planned"
	OccurrenceQueued    OccurrenceStatus = "queued"
	OccurrenceRunning   OccurrenceStatus = "running"
	OccurrenceCompleted OccurrenceStatus = "completed"
	OccurrenceException OccurrenceStatus = "completed_with_exceptions"
	OccurrenceExcluded  OccurrenceStatus = "excluded"
)

type SourceOccurrence struct {
	OccurrenceID          string                   `json:"occurrence_id"`
	DailyRunID            string                   `json:"daily_run_id"`
	SourceID              string                   `json:"source_id"`
	ScheduleDate          string                   `json:"schedule_date"`
	SchedulePolicyVersion uint64                   `json:"schedule_policy_version"`
	CompanyVersion        uint64                   `json:"company_version"`
	SourceVersion         uint64                   `json:"source_version"`
	DueAt                 string                   `json:"due_at"`
	ListingExecution      ListingExecutionSnapshot `json:"listing_execution"`
	ProfileID             string                   `json:"profile_id,omitempty"`
	WorkID                string                   `json:"work_id,omitempty"`
	Status                OccurrenceStatus         `json:"occurrence_status"`
	Outcome               string                   `json:"outcome,omitempty"`
	Version               uint64                   `json:"version"`
}

func (o SourceOccurrence) WithProfile(profileID string) (SourceOccurrence, error) {
	profileID = strings.TrimSpace(profileID)
	if profileID == "" || o.Version == 0 || o.Status != OccurrencePlanned || o.WorkID != "" {
		return SourceOccurrence{}, fmt.Errorf("only a new planned occurrence can freeze a Profile identity")
	}
	o.ProfileID = profileID
	return o, nil
}

type ListingExecutionSnapshot struct {
	Endpoint      SourceEndpoint         `json:"endpoint"`
	Assignment    SourceRecipeAssignment `json:"assignment"`
	RecipeID      string                 `json:"recipe_id"`
	RecipeVersion uint64                 `json:"recipe_version"`
	ContentHash   string                 `json:"content_hash"`
	ContractHash  string                 `json:"contract_hash"`
	Execution     RecipeExecution        `json:"execution"`
	Origin        string                 `json:"origin"`
}

func NewListingExecutionSnapshot(source RecruitmentSource, recipe Recipe) (ListingExecutionSnapshot, error) {
	if source.ActiveEndpoint == nil || source.ListingAssignment == nil || recipe.Status != RecipeActive || recipe.Kind != RecipeListing ||
		recipe.RecipeID != source.ListingAssignment.RecipeID || recipe.Version != source.ListingAssignment.RecipeVersion ||
		recipe.ContractHash != source.ListingAssignment.ContractHash {
		return ListingExecutionSnapshot{}, fmt.Errorf("listing snapshot requires matching active endpoint, assignment, and recipe")
	}
	endpoint, err := url.Parse(source.ActiveEndpoint.URL)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return ListingExecutionSnapshot{}, fmt.Errorf("listing snapshot endpoint is invalid")
	}
	return ListingExecutionSnapshot{
		Endpoint: *source.ActiveEndpoint, Assignment: *source.ListingAssignment,
		RecipeID: recipe.RecipeID, RecipeVersion: recipe.Version, ContentHash: recipe.ContentHash,
		ContractHash: recipe.ContractHash, Execution: recipe.Execution,
		Origin: endpoint.Scheme + "://" + endpoint.Host,
	}, nil
}

// NewCandidateListingExecutionSnapshot freezes the exact staged endpoint and
// proposed listing Recipe used by Source validation. The assignment is an
// execution fence only; publishing it remains a separate, evidence-gated
// operator command.
func NewCandidateListingExecutionSnapshot(source RecruitmentSource, recipe Recipe, assignment SourceRecipeAssignment) (ListingExecutionSnapshot, error) {
	if source.CandidateEndpoint == nil || recipe.Status != RecipeActive || recipe.Kind != RecipeListing ||
		assignment.SourceID != source.SourceID || assignment.Kind != RecipeListing || assignment.RecipeID != recipe.RecipeID ||
		assignment.RecipeVersion != recipe.Version || assignment.ContractHash != recipe.ContractHash || assignment.AssignmentVersion == 0 {
		return ListingExecutionSnapshot{}, fmt.Errorf("candidate listing snapshot requires a staged endpoint, active listing recipe, and matching proposed assignment")
	}
	endpoint, err := url.Parse(source.CandidateEndpoint.URL)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return ListingExecutionSnapshot{}, fmt.Errorf("candidate listing snapshot endpoint is invalid")
	}
	return ListingExecutionSnapshot{
		Endpoint: *source.CandidateEndpoint, Assignment: assignment,
		RecipeID: recipe.RecipeID, RecipeVersion: recipe.Version, ContentHash: recipe.ContentHash,
		ContractHash: recipe.ContractHash, Execution: recipe.Execution,
		Origin: endpoint.Scheme + "://" + endpoint.Host,
	}, nil
}

// NewRecipeValidationListingExecutionSnapshot freezes a candidate Recipe and
// one known production endpoint. The proposed Assignment is an execution
// fence only and is not published to the Source by validation.
func NewRecipeValidationListingExecutionSnapshot(source RecruitmentSource, recipe Recipe,
	assignment SourceRecipeAssignment) (ListingExecutionSnapshot, error) {
	if source.ActiveEndpoint == nil || recipe.Status != RecipeValidating || recipe.Kind != RecipeListing ||
		assignment.SourceID != source.SourceID || assignment.Kind != RecipeListing || assignment.RecipeID != recipe.RecipeID ||
		assignment.RecipeVersion != recipe.Version || assignment.ContractHash != recipe.ContractHash || assignment.AssignmentVersion == 0 {
		return ListingExecutionSnapshot{}, fmt.Errorf("Recipe validation snapshot requires an active endpoint, validating listing Recipe, and proposed assignment")
	}
	endpoint, err := url.Parse(source.ActiveEndpoint.URL)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" {
		return ListingExecutionSnapshot{}, fmt.Errorf("Recipe validation endpoint is invalid")
	}
	return ListingExecutionSnapshot{Endpoint: *source.ActiveEndpoint, Assignment: assignment,
		RecipeID: recipe.RecipeID, RecipeVersion: recipe.Version, ContentHash: recipe.ContentHash,
		ContractHash: recipe.ContractHash, Execution: recipe.Execution, Origin: endpoint.Scheme + "://" + endpoint.Host}, nil
}

func (s ListingExecutionSnapshot) Validate(sourceID string) error {
	endpoint, err := url.Parse(s.Endpoint.URL)
	if err != nil || endpoint.Scheme == "" || endpoint.Host == "" || s.Endpoint.Revision == 0 ||
		s.Origin != endpoint.Scheme+"://"+endpoint.Host || s.Assignment.SourceID != sourceID ||
		s.Assignment.Kind != RecipeListing || s.Assignment.AssignmentVersion == 0 ||
		s.RecipeID != s.Assignment.RecipeID || s.RecipeVersion != s.Assignment.RecipeVersion ||
		s.ContractHash != s.Assignment.ContractHash || strings.TrimSpace(s.ContentHash) == "" {
		return fmt.Errorf("listing execution snapshot is incomplete or inconsistent")
	}
	return s.Execution.Validate()
}

func OccurrenceKey(sourceID, scheduleDate string, policyVersion uint64) (string, error) {
	sourceID, scheduleDate = strings.TrimSpace(sourceID), strings.TrimSpace(scheduleDate)
	if _, err := time.Parse("2006-01-02", scheduleDate); sourceID == "" || err != nil || policyVersion == 0 {
		return "", fmt.Errorf("source, YYYY-MM-DD schedule date, and policy version are required")
	}
	return sourceID + "|" + scheduleDate + fmt.Sprintf("|%d", policyVersion), nil
}

type DailyRunStatus string

const (
	DailyRunPlanned                 DailyRunStatus = "planned"
	DailyRunRunning                 DailyRunStatus = "running"
	DailyRunCompleted               DailyRunStatus = "completed"
	DailyRunCompletedWithExceptions DailyRunStatus = "completed_with_exceptions"
)

type DailyRun struct {
	DailyRunID            string          `json:"daily_run_id"`
	ScheduleDate          string          `json:"schedule_date"`
	SchedulePolicyVersion uint64          `json:"schedule_policy_version"`
	CutoffAt              string          `json:"cutoff_at"`
	WindowStartAt         string          `json:"window_start_at"`
	WindowEndAt           string          `json:"window_end_at"`
	ExpectedSources       int             `json:"expected_sources"`
	Status                DailyRunStatus  `json:"daily_run_status"`
	Version               uint64          `json:"version"`
	Summary               CoverageSummary `json:"summary"`
}

type DailySchedule struct {
	PolicyVersion uint64 `json:"policy_version"`
	CutoffAt      string `json:"cutoff_at"`
	WindowStartAt string `json:"window_start_at"`
	WindowEndAt   string `json:"window_end_at"`
}

type CoverageSummary struct {
	ListingSucceeded  int `json:"listing_succeeded"`
	ListingExceptions int `json:"listing_exceptions"`
	Excluded          int `json:"excluded"`
	DetailExpected    int `json:"detail_expected"`
	DetailSucceeded   int `json:"detail_succeeded"`
	DetailAcceptedGap int `json:"detail_accepted_gap"`
	DetailExceptions  int `json:"detail_exceptions"`
}

func NewDailyRun(id, scheduleDate string, expectedSources int, schedule DailySchedule) (DailyRun, error) {
	if strings.TrimSpace(id) == "" || expectedSources < 0 || schedule.PolicyVersion == 0 {
		return DailyRun{}, fmt.Errorf("daily run identity, schedule policy, and non-negative expected sources are required")
	}
	if _, err := time.Parse("2006-01-02", scheduleDate); err != nil {
		return DailyRun{}, fmt.Errorf("invalid schedule date: %w", err)
	}
	cutoffAt, err := time.Parse(time.RFC3339, schedule.CutoffAt)
	if err != nil {
		return DailyRun{}, fmt.Errorf("daily cutoff must be RFC3339")
	}
	windowStart, err := time.Parse(time.RFC3339, schedule.WindowStartAt)
	if err != nil {
		return DailyRun{}, fmt.Errorf("daily window start must be RFC3339")
	}
	windowEnd, err := time.Parse(time.RFC3339, schedule.WindowEndAt)
	if err != nil || windowStart.Before(cutoffAt) || windowEnd.Sub(windowStart) < time.Microsecond {
		return DailyRun{}, fmt.Errorf("daily schedule requires cutoff <= window start < window end")
	}
	cutoffAt = cutoffAt.UTC().Truncate(time.Microsecond)
	windowStart = windowStart.UTC().Truncate(time.Microsecond)
	windowEnd = windowEnd.UTC().Truncate(time.Microsecond)
	return DailyRun{
		DailyRunID: id, ScheduleDate: scheduleDate, SchedulePolicyVersion: schedule.PolicyVersion,
		CutoffAt: cutoffAt.Format(time.RFC3339Nano), WindowStartAt: windowStart.Format(time.RFC3339Nano),
		WindowEndAt: windowEnd.Format(time.RFC3339Nano), ExpectedSources: expectedSources,
		Status: DailyRunPlanned, Version: 1,
	}, nil
}

func (d DailyRun) Start(expected uint64) (DailyRun, error) {
	if err := requireVersion(expected, d.Version); err != nil {
		return DailyRun{}, err
	}
	if d.Status != DailyRunPlanned {
		return DailyRun{}, &InvalidTransitionError{Entity: "daily run", From: string(d.Status), Action: "start"}
	}
	d.Status, d.Version = DailyRunRunning, d.Version+1
	return d, nil
}

func (d DailyRun) Close(expected uint64, summary CoverageSummary) (DailyRun, error) {
	if err := requireVersion(expected, d.Version); err != nil {
		return DailyRun{}, err
	}
	if d.Status != DailyRunRunning {
		return DailyRun{}, &InvalidTransitionError{Entity: "daily run", From: string(d.Status), Action: "close"}
	}
	for _, count := range []int{summary.ListingSucceeded, summary.ListingExceptions, summary.Excluded, summary.DetailExpected, summary.DetailSucceeded, summary.DetailAcceptedGap, summary.DetailExceptions} {
		if count < 0 {
			return DailyRun{}, fmt.Errorf("coverage counts cannot be negative")
		}
	}
	if summary.ListingSucceeded+summary.ListingExceptions+summary.Excluded != d.ExpectedSources {
		return DailyRun{}, fmt.Errorf("not all expected source occurrences are accounted for")
	}
	if summary.DetailSucceeded+summary.DetailAcceptedGap+summary.DetailExceptions != summary.DetailExpected {
		return DailyRun{}, fmt.Errorf("not all detail work is accounted for")
	}
	if summary.ListingExceptions == 0 && summary.Excluded == 0 && summary.DetailAcceptedGap == 0 && summary.DetailExceptions == 0 {
		d.Status = DailyRunCompleted
	} else {
		d.Status = DailyRunCompletedWithExceptions
	}
	d.Summary, d.Version = summary, d.Version+1
	return d, nil
}

func NewSourceOccurrence(id, dailyRunID, sourceID, scheduleDate string, policyVersion, companyVersion, sourceVersion uint64, dueAt string, listingExecution ListingExecutionSnapshot) (SourceOccurrence, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(dailyRunID) == "" || companyVersion == 0 || sourceVersion == 0 {
		return SourceOccurrence{}, fmt.Errorf("occurrence identity and snapshot versions are required")
	}
	if _, err := OccurrenceKey(sourceID, scheduleDate, policyVersion); err != nil {
		return SourceOccurrence{}, err
	}
	due, err := time.Parse(time.RFC3339, dueAt)
	if err != nil {
		return SourceOccurrence{}, fmt.Errorf("occurrence due_at must be RFC3339")
	}
	if err := listingExecution.Validate(sourceID); err != nil {
		return SourceOccurrence{}, err
	}
	return SourceOccurrence{
		OccurrenceID: id, DailyRunID: dailyRunID, SourceID: sourceID, ScheduleDate: scheduleDate,
		SchedulePolicyVersion: policyVersion, CompanyVersion: companyVersion, SourceVersion: sourceVersion,
		DueAt: due.UTC().Truncate(time.Microsecond).Format(time.RFC3339Nano), ListingExecution: listingExecution,
		Status: OccurrencePlanned, Version: 1,
	}, nil
}

func (o SourceOccurrence) Start(expected uint64) (SourceOccurrence, error) {
	if err := requireVersion(expected, o.Version); err != nil {
		return SourceOccurrence{}, err
	}
	if o.Status != OccurrenceQueued || strings.TrimSpace(o.WorkID) == "" {
		return SourceOccurrence{}, &InvalidTransitionError{Entity: "source occurrence", From: string(o.Status), Action: "start"}
	}
	o.Status, o.Version = OccurrenceRunning, o.Version+1
	return o, nil
}

// RebindWork moves an unfinished daily occurrence to a distinct causal Work
// created by an operator retry. The old Work remains immutable history; the
// occurrence points at the one current Work whose Attempt may finish it.
func (o SourceOccurrence) RebindWork(expected uint64, previousWorkID, retryWorkID string) (SourceOccurrence, error) {
	if err := requireVersion(expected, o.Version); err != nil {
		return SourceOccurrence{}, err
	}
	previousWorkID, retryWorkID = strings.TrimSpace(previousWorkID), strings.TrimSpace(retryWorkID)
	if (o.Status != OccurrenceQueued && o.Status != OccurrenceRunning) || previousWorkID == "" || retryWorkID == "" ||
		previousWorkID == retryWorkID || o.WorkID != previousWorkID {
		return SourceOccurrence{}, &InvalidTransitionError{Entity: "source occurrence", From: string(o.Status), Action: "rebind retry work"}
	}
	o.WorkID, o.Version = retryWorkID, o.Version+1
	return o, nil
}

func (o SourceOccurrence) Queue(expected uint64, workID string) (SourceOccurrence, error) {
	if err := requireVersion(expected, o.Version); err != nil {
		return SourceOccurrence{}, err
	}
	if o.Status != OccurrencePlanned || o.WorkID != "" || strings.TrimSpace(workID) == "" {
		return SourceOccurrence{}, &InvalidTransitionError{Entity: "source occurrence", From: string(o.Status), Action: "queue"}
	}
	o.Status, o.WorkID, o.Version = OccurrenceQueued, strings.TrimSpace(workID), o.Version+1
	return o, nil
}

func (o SourceOccurrence) ExpireBeforeQueue(expected uint64, reason string) (SourceOccurrence, error) {
	if err := requireVersion(expected, o.Version); err != nil {
		return SourceOccurrence{}, err
	}
	if o.Status != OccurrencePlanned || o.WorkID != "" || strings.TrimSpace(reason) == "" {
		return SourceOccurrence{}, &InvalidTransitionError{Entity: "source occurrence", From: string(o.Status), Action: "expire before queue"}
	}
	o.Status, o.Outcome, o.Version = OccurrenceException, strings.TrimSpace(reason), o.Version+1
	return o, nil
}

// CloseWithException accounts for work that did not reach a successful
// checkpoint before its immutable daily execution window ended. Unlike
// ExpireBeforeQueue it also covers queued and running occurrences, retaining
// the Work link as evidence for operations and late-result fencing.
func (o SourceOccurrence) CloseWithException(expected uint64, reason string) (SourceOccurrence, error) {
	if err := requireVersion(expected, o.Version); err != nil {
		return SourceOccurrence{}, err
	}
	if (o.Status != OccurrencePlanned && o.Status != OccurrenceQueued && o.Status != OccurrenceRunning) || strings.TrimSpace(reason) == "" {
		return SourceOccurrence{}, &InvalidTransitionError{Entity: "source occurrence", From: string(o.Status), Action: "close with exception"}
	}
	o.Status, o.Outcome, o.Version = OccurrenceException, strings.TrimSpace(reason), o.Version+1
	return o, nil
}

func (o SourceOccurrence) Finish(expected uint64, checkpointCommitted bool, outcome string) (SourceOccurrence, error) {
	if err := requireVersion(expected, o.Version); err != nil {
		return SourceOccurrence{}, err
	}
	if o.Status != OccurrenceRunning || strings.TrimSpace(outcome) == "" {
		return SourceOccurrence{}, &InvalidTransitionError{Entity: "source occurrence", From: string(o.Status), Action: "finish"}
	}
	if checkpointCommitted {
		o.Status = OccurrenceCompleted
	} else {
		o.Status = OccurrenceException
	}
	o.Outcome, o.Version = strings.TrimSpace(outcome), o.Version+1
	return o, nil
}

func (o SourceOccurrence) Exclude(expected uint64, reason string) (SourceOccurrence, error) {
	if err := requireVersion(expected, o.Version); err != nil {
		return SourceOccurrence{}, err
	}
	if o.Status != OccurrencePlanned || o.WorkID != "" || strings.TrimSpace(reason) == "" {
		return SourceOccurrence{}, &InvalidTransitionError{Entity: "source occurrence", From: string(o.Status), Action: "exclude"}
	}
	o.Status, o.Outcome, o.Version = OccurrenceExcluded, strings.TrimSpace(reason), o.Version+1
	return o, nil
}
