package model

import (
	"fmt"
	"strings"
)

type JobStatus string

const (
	JobDiscovered    JobStatus = "discovered"
	JobDetailPending JobStatus = "detail_pending"
	JobAvailable     JobStatus = "available"
	JobUpdatePending JobStatus = "update_pending"
)

type SourceJob struct {
	JobID             string    `json:"job_id"`
	SourceID          string    `json:"source_id"`
	SourceJobKey      string    `json:"source_job_key"`
	DetailURL         string    `json:"detail_url"`
	Status            JobStatus `json:"job_status"`
	RefreshGeneration uint64    `json:"refresh_generation"`
	DetailContentHash string    `json:"detail_content_hash,omitempty"`
	DetailVersion     uint64    `json:"detail_version"`
	Version           uint64    `json:"version"`
}

type DetailAcceptance struct {
	Job            SourceJob `json:"job"`
	ContentChanged bool      `json:"content_changed"`
}

func NewSourceJob(id, sourceID, sourceJobKey, detailURL string) (SourceJob, error) {
	if strings.TrimSpace(id) == "" || strings.TrimSpace(sourceID) == "" || strings.TrimSpace(sourceJobKey) == "" {
		return SourceJob{}, fmt.Errorf("job, source, and source job key are required")
	}
	canonical, err := CanonicalHTTPURL(detailURL)
	if err != nil || canonical == "" {
		return SourceJob{}, fmt.Errorf("valid detail URL is required")
	}
	return SourceJob{JobID: id, SourceID: sourceID, SourceJobKey: sourceJobKey, DetailURL: canonical, Status: JobDetailPending, RefreshGeneration: 1, Version: 1}, nil
}

func (j SourceJob) ObserveUpdate(expected uint64, detailURL string) (SourceJob, error) {
	if err := requireVersion(expected, j.Version); err != nil {
		return SourceJob{}, err
	}
	canonical, err := CanonicalHTTPURL(detailURL)
	if err != nil || canonical == "" {
		return SourceJob{}, fmt.Errorf("valid detail URL is required")
	}
	j.DetailURL = canonical
	j.RefreshGeneration++
	j.Status = JobUpdatePending
	j.Version++
	return j, nil
}

func (j SourceJob) AcceptDetail(expectedVersion, generation uint64, contentHash string) (DetailAcceptance, error) {
	contentHash = strings.TrimSpace(contentHash)
	if generation == j.RefreshGeneration && j.Status == JobAvailable && contentHash != "" && contentHash == j.DetailContentHash {
		return DetailAcceptance{Job: j, ContentChanged: false}, nil
	}
	if err := requireVersion(expectedVersion, j.Version); err != nil {
		return DetailAcceptance{}, err
	}
	if generation != j.RefreshGeneration {
		return DetailAcceptance{}, fmt.Errorf("refresh generation conflict: got %d want %d", generation, j.RefreshGeneration)
	}
	if j.Status != JobDetailPending && j.Status != JobUpdatePending {
		return DetailAcceptance{}, &InvalidTransitionError{Entity: "job", From: string(j.Status), Action: "accept detail"}
	}
	if contentHash == "" {
		return DetailAcceptance{}, fmt.Errorf("detail content hash is required")
	}
	changed := contentHash != j.DetailContentHash
	j.DetailContentHash = contentHash
	if changed {
		j.DetailVersion++
	}
	j.Status = JobAvailable
	j.Version++
	return DetailAcceptance{Job: j, ContentChanged: changed}, nil
}
