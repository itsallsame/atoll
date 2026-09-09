package model

import (
	"fmt"
	"strings"
)

type ListingRunMode string

const (
	ListingRunDiagnostic ListingRunMode = "diagnostic"
	ListingRunProduction ListingRunMode = "production"
	// ListingRunValidation executes a staged Source endpoint and records
	// evidence without publishing either jobs or an incremental checkpoint.
	ListingRunValidation ListingRunMode = "source_validation"
)

type ListingRunStatus string

const (
	ListingRunQueued    ListingRunStatus = "queued"
	ListingRunRunning   ListingRunStatus = "running"
	ListingRunCompleted ListingRunStatus = "completed"
)

// ListingRun is the immutable execution context for a standalone manual
// listing run. Daily coverage continues to use SourceOccurrence; keeping the
// two facts separate prevents diagnostic work from entering a DailyRun.
type ListingRun struct {
	ListingRunID      string                   `json:"listing_run_id"`
	WorkID            string                   `json:"work_id"`
	Mode              ListingRunMode           `json:"run_mode"`
	SourceID          string                   `json:"source_id"`
	CompanyVersion    uint64                   `json:"company_version"`
	SourceVersion     uint64                   `json:"source_version"`
	Checkpoint        *IncrementalCheckpoint   `json:"checkpoint,omitempty"`
	CheckpointVersion uint64                   `json:"checkpoint_version,omitempty"`
	ListingExecution  ListingExecutionSnapshot `json:"listing_execution"`
	Status            ListingRunStatus         `json:"listing_run_status"`
	Version           uint64                   `json:"version"`
}

func NewListingRun(id, workID string, mode ListingRunMode, sourceID string, companyVersion, sourceVersion uint64,
	checkpoint *IncrementalCheckpoint, execution ListingExecutionSnapshot) (ListingRun, error) {
	id, workID, sourceID = strings.TrimSpace(id), strings.TrimSpace(workID), strings.TrimSpace(sourceID)
	if id == "" || workID == "" || sourceID == "" || companyVersion == 0 || sourceVersion == 0 ||
		(mode != ListingRunDiagnostic && mode != ListingRunProduction && mode != ListingRunValidation) {
		return ListingRun{}, fmt.Errorf("listing run identity, mode, and aggregate versions are required")
	}
	if err := execution.Validate(sourceID); err != nil {
		return ListingRun{}, err
	}
	checkpointVersion := uint64(0)
	var frozenCheckpoint *IncrementalCheckpoint
	if checkpoint != nil {
		copy := *checkpoint
		copy.FrontierJobKeys = append([]string(nil), checkpoint.FrontierJobKeys...)
		frozenCheckpoint, checkpointVersion = &copy, copy.Version
	}
	return ListingRun{ListingRunID: id, WorkID: workID, Mode: mode, SourceID: sourceID,
		CompanyVersion: companyVersion, SourceVersion: sourceVersion, Checkpoint: frozenCheckpoint, CheckpointVersion: checkpointVersion,
		ListingExecution: execution, Status: ListingRunQueued, Version: 1}, nil
}

func (r ListingRun) Start(expected uint64) (ListingRun, error) {
	if err := requireVersion(expected, r.Version); err != nil {
		return ListingRun{}, err
	}
	if r.Status == ListingRunRunning {
		return r, nil
	}
	if r.Status != ListingRunQueued {
		return ListingRun{}, &InvalidTransitionError{Entity: "listing run", From: string(r.Status), Action: "start"}
	}
	r.Status, r.Version = ListingRunRunning, r.Version+1
	return r, nil
}

func (r ListingRun) Complete(expected uint64) (ListingRun, error) {
	if err := requireVersion(expected, r.Version); err != nil {
		return ListingRun{}, err
	}
	if r.Status != ListingRunRunning {
		return ListingRun{}, &InvalidTransitionError{Entity: "listing run", From: string(r.Status), Action: "complete"}
	}
	r.Status, r.Version = ListingRunCompleted, r.Version+1
	return r, nil
}

func (r ListingRun) RebindWork(expected uint64, previousWorkID, retryWorkID string) (ListingRun, error) {
	if err := requireVersion(expected, r.Version); err != nil {
		return ListingRun{}, err
	}
	previousWorkID, retryWorkID = strings.TrimSpace(previousWorkID), strings.TrimSpace(retryWorkID)
	if r.Status == ListingRunCompleted || r.WorkID != previousWorkID || previousWorkID == "" || retryWorkID == "" || previousWorkID == retryWorkID {
		return ListingRun{}, &InvalidTransitionError{Entity: "listing run", From: string(r.Status), Action: "rebind retry work"}
	}
	r.WorkID, r.Version = retryWorkID, r.Version+1
	return r, nil
}
