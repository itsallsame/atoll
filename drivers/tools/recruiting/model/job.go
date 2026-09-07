package model

import (
	"fmt"
	"strings"
	"time"
)

type JobStatus string

const (
	JobDiscovered    JobStatus = "discovered"
	JobDetailPending JobStatus = "detail_pending"
	JobAvailable     JobStatus = "available"
	JobUpdatePending JobStatus = "update_pending"
)

type SourceJob struct {
	JobID              string    `json:"job_id"`
	SourceID           string    `json:"source_id"`
	SourceJobKey       string    `json:"source_job_key"`
	DetailURL          string    `json:"detail_url"`
	FirstDiscoveredAt  string    `json:"first_discovered_at,omitempty"`
	LastActivityAt     string    `json:"last_activity_at,omitempty"`
	ListingFingerprint string    `json:"listing_fingerprint,omitempty"`
	Status             JobStatus `json:"job_status"`
	RefreshGeneration  uint64    `json:"refresh_generation"`
	DetailContentHash  string    `json:"detail_content_hash,omitempty"`
	DetailVersion      uint64    `json:"detail_version"`
	Version            uint64    `json:"version"`
}

type DetailAcceptance struct {
	Job            SourceJob         `json:"job"`
	ContentChanged bool              `json:"content_changed"`
	DetailVersion  *JobDetailVersion `json:"detail_version,omitempty"`
}

type JobDetailVersion struct {
	DetailVersionID   string `json:"detail_version_id"`
	JobID             string `json:"job_id"`
	RefreshGeneration uint64 `json:"refresh_generation"`
	Version           uint64 `json:"version"`
	ContentHash       string `json:"content_hash"`
	ArtifactID        string `json:"artifact_id"`
	RecipeID          string `json:"recipe_id"`
	RecipeVersion     uint64 `json:"recipe_version"`
	ObservedAt        string `json:"observed_at"`
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

func NewSourceJobFromObservation(id string, observation ListingObservation, observedAt string) (SourceJob, error) {
	if _, err := time.Parse(time.RFC3339, observedAt); err != nil {
		return SourceJob{}, fmt.Errorf("observed_at must be RFC3339: %w", err)
	}
	job, err := NewSourceJob(id, observation.SourceID, observation.SourceJobKey, observation.DetailURL)
	if err != nil {
		return SourceJob{}, err
	}
	job.FirstDiscoveredAt = observedAt
	job.LastActivityAt = observation.ActivityAt
	job.ListingFingerprint = observation.ListingFingerprint
	return job, nil
}

// ObserveListing distinguishes overlap re-observation from a source-visible
// update. Only changed activity, fingerprint, or canonical detail URL advances
// refresh_generation and therefore permits new detail work.
func (j SourceJob) ObserveListing(expected uint64, observation ListingObservation) (SourceJob, bool, error) {
	if err := requireVersion(expected, j.Version); err != nil {
		return SourceJob{}, false, err
	}
	if observation.SourceID != j.SourceID || observation.SourceJobKey != j.SourceJobKey {
		return SourceJob{}, false, fmt.Errorf("listing observation does not identify this source job")
	}
	canonical, err := CanonicalHTTPURL(observation.DetailURL)
	if err != nil || canonical == "" {
		return SourceJob{}, false, fmt.Errorf("valid detail URL is required")
	}
	changed := canonical != j.DetailURL || observation.ActivityAt != j.LastActivityAt ||
		(observation.ListingFingerprint != "" && observation.ListingFingerprint != j.ListingFingerprint)
	if !changed {
		return j, false, nil
	}
	j.DetailURL = canonical
	j.LastActivityAt = observation.ActivityAt
	j.ListingFingerprint = observation.ListingFingerprint
	j.RefreshGeneration++
	j.Status = JobUpdatePending
	j.Version++
	return j, true, nil
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

func (j SourceJob) AcceptDetailVersion(expectedVersion, generation uint64, detailVersionID, contentHash, artifactID, recipeID string, recipeVersion uint64, observedAt string) (DetailAcceptance, error) {
	if strings.TrimSpace(detailVersionID) == "" || strings.TrimSpace(artifactID) == "" || strings.TrimSpace(recipeID) == "" || recipeVersion == 0 {
		return DetailAcceptance{}, fmt.Errorf("detail version, artifact, recipe, and recipe version are required")
	}
	if _, err := time.Parse(time.RFC3339, observedAt); err != nil {
		return DetailAcceptance{}, fmt.Errorf("observed_at must be RFC3339: %w", err)
	}
	accepted, err := j.AcceptDetail(expectedVersion, generation, contentHash)
	if err != nil || !accepted.ContentChanged {
		return accepted, err
	}
	accepted.DetailVersion = &JobDetailVersion{
		DetailVersionID: detailVersionID, JobID: j.JobID, RefreshGeneration: generation,
		Version: accepted.Job.DetailVersion, ContentHash: strings.TrimSpace(contentHash), ArtifactID: artifactID,
		RecipeID: recipeID, RecipeVersion: recipeVersion, ObservedAt: observedAt,
	}
	return accepted, nil
}
