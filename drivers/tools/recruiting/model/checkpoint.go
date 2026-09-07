package model

import (
	"fmt"
	"strings"
	"time"
)

type CheckpointStrategy string

const (
	CheckpointActivityTime CheckpointStrategy = "activity_desc"
	CheckpointFrontierKeys CheckpointStrategy = "frontier_keys"
)

type IncrementalCheckpoint struct {
	SourceID           string             `json:"source_id"`
	RecipeID           string             `json:"recipe_id"`
	RecipeVersion      uint64             `json:"recipe_version"`
	ContractHash       string             `json:"contract_hash"`
	Strategy           CheckpointStrategy `json:"strategy"`
	FrontierActivityAt string             `json:"frontier_activity_at,omitempty"`
	FrontierJobKeys    []string           `json:"frontier_job_keys,omitempty"`
	OverlapPages       int                `json:"overlap_pages,omitempty"`
	OverlapItems       int                `json:"overlap_items,omitempty"`
	LastOccurrenceID   string             `json:"last_occurrence_id"`
	Version            uint64             `json:"version"`
}

type ListingProgress struct {
	PreviousFrontierReached bool                  `json:"previous_frontier_reached"`
	OverlapCompleted        bool                  `json:"overlap_completed"`
	OrderingContractHeld    bool                  `json:"ordering_contract_held"`
	SameTimeGroupCompleted  bool                  `json:"same_time_group_completed"`
	Candidate               IncrementalCheckpoint `json:"candidate"`
}

func EstablishCheckpoint(candidate IncrementalCheckpoint) (IncrementalCheckpoint, error) {
	if err := validateCheckpoint(candidate); err != nil {
		return IncrementalCheckpoint{}, err
	}
	if candidate.Version != 0 {
		return IncrementalCheckpoint{}, fmt.Errorf("initial checkpoint version must be zero")
	}
	candidate.Version = 1
	return candidate, nil
}

func (c IncrementalCheckpoint) Commit(expected uint64, progress ListingProgress) (IncrementalCheckpoint, error) {
	if err := requireVersion(expected, c.Version); err != nil {
		return IncrementalCheckpoint{}, err
	}
	if !progress.PreviousFrontierReached || !progress.OverlapCompleted || !progress.OrderingContractHeld || !progress.SameTimeGroupCompleted {
		return IncrementalCheckpoint{}, fmt.Errorf("checkpoint proof incomplete")
	}
	next := progress.Candidate
	if err := validateCheckpoint(next); err != nil {
		return IncrementalCheckpoint{}, err
	}
	if next.SourceID != c.SourceID {
		return IncrementalCheckpoint{}, fmt.Errorf("checkpoint source changed")
	}
	if next.ContractHash != c.ContractHash {
		return IncrementalCheckpoint{}, fmt.Errorf("checkpoint contract changed without compatibility validation")
	}
	next.Version = c.Version + 1
	return next, nil
}

func validateCheckpoint(c IncrementalCheckpoint) error {
	if strings.TrimSpace(c.SourceID) == "" || strings.TrimSpace(c.RecipeID) == "" || c.RecipeVersion == 0 || strings.TrimSpace(c.ContractHash) == "" || strings.TrimSpace(c.LastOccurrenceID) == "" {
		return fmt.Errorf("checkpoint source, recipe, contract, and occurrence are required")
	}
	if c.OverlapPages < 0 || c.OverlapItems < 0 {
		return fmt.Errorf("checkpoint overlap cannot be negative")
	}
	switch c.Strategy {
	case CheckpointActivityTime:
		if _, err := time.Parse(time.RFC3339, c.FrontierActivityAt); err != nil {
			return fmt.Errorf("activity checkpoint requires frontier_activity_at")
		}
	case CheckpointFrontierKeys:
		if len(c.FrontierJobKeys) == 0 {
			return fmt.Errorf("key checkpoint requires ordered frontier_job_keys")
		}
	default:
		return fmt.Errorf("unknown checkpoint strategy %q", c.Strategy)
	}
	return nil
}
