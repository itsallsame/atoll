package model

import (
	"fmt"
	"strings"
	"time"
)

type OccurrenceStatus string

const (
	OccurrencePlanned   OccurrenceStatus = "planned"
	OccurrenceRunning   OccurrenceStatus = "running"
	OccurrenceCompleted OccurrenceStatus = "completed"
	OccurrenceException OccurrenceStatus = "completed_with_exceptions"
	OccurrenceExcluded  OccurrenceStatus = "excluded"
)

type SourceOccurrence struct {
	OccurrenceID          string           `json:"occurrence_id"`
	DailyRunID            string           `json:"daily_run_id"`
	SourceID              string           `json:"source_id"`
	ScheduleDate          string           `json:"schedule_date"`
	SchedulePolicyVersion uint64           `json:"schedule_policy_version"`
	CompanyVersion        uint64           `json:"company_version"`
	SourceVersion         uint64           `json:"source_version"`
	Status                OccurrenceStatus `json:"occurrence_status"`
	Outcome               string           `json:"outcome,omitempty"`
	Version               uint64           `json:"version"`
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
	DailyRunID      string          `json:"daily_run_id"`
	ScheduleDate    string          `json:"schedule_date"`
	ExpectedSources int             `json:"expected_sources"`
	Status          DailyRunStatus  `json:"daily_run_status"`
	Version         uint64          `json:"version"`
	Summary         CoverageSummary `json:"summary"`
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

func NewDailyRun(id, scheduleDate string, expectedSources int) (DailyRun, error) {
	if strings.TrimSpace(id) == "" || expectedSources < 0 {
		return DailyRun{}, fmt.Errorf("daily run identity is required and expected sources cannot be negative")
	}
	if _, err := time.Parse("2006-01-02", scheduleDate); err != nil {
		return DailyRun{}, fmt.Errorf("invalid schedule date: %w", err)
	}
	return DailyRun{DailyRunID: id, ScheduleDate: scheduleDate, ExpectedSources: expectedSources, Status: DailyRunPlanned, Version: 1}, nil
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
	if summary.ListingExceptions == 0 && summary.DetailAcceptedGap == 0 && summary.DetailExceptions == 0 {
		d.Status = DailyRunCompleted
	} else {
		d.Status = DailyRunCompletedWithExceptions
	}
	d.Summary, d.Version = summary, d.Version+1
	return d, nil
}

func NewSourceOccurrence(id, dailyRunID, sourceID, scheduleDate string, policyVersion, companyVersion, sourceVersion uint64) (SourceOccurrence, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(dailyRunID) == "" || companyVersion == 0 || sourceVersion == 0 {
		return SourceOccurrence{}, fmt.Errorf("occurrence identity and snapshot versions are required")
	}
	if _, err := OccurrenceKey(sourceID, scheduleDate, policyVersion); err != nil {
		return SourceOccurrence{}, err
	}
	return SourceOccurrence{
		OccurrenceID: id, DailyRunID: dailyRunID, SourceID: sourceID, ScheduleDate: scheduleDate,
		SchedulePolicyVersion: policyVersion, CompanyVersion: companyVersion, SourceVersion: sourceVersion,
		Status: OccurrencePlanned, Version: 1,
	}, nil
}

func (o SourceOccurrence) Start(expected uint64) (SourceOccurrence, error) {
	if err := requireVersion(expected, o.Version); err != nil {
		return SourceOccurrence{}, err
	}
	if o.Status != OccurrencePlanned {
		return SourceOccurrence{}, &InvalidTransitionError{Entity: "source occurrence", From: string(o.Status), Action: "start"}
	}
	o.Status, o.Version = OccurrenceRunning, o.Version+1
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
	if o.Status != OccurrencePlanned || strings.TrimSpace(reason) == "" {
		return SourceOccurrence{}, &InvalidTransitionError{Entity: "source occurrence", From: string(o.Status), Action: "exclude"}
	}
	o.Status, o.Outcome, o.Version = OccurrenceExcluded, strings.TrimSpace(reason), o.Version+1
	return o, nil
}
