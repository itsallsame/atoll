package store

import (
	"errors"
	"fmt"
)

var (
	ErrNotFound                   = errors.New("recruiting entity not found")
	ErrBusinessKeyExists          = errors.New("recruiting business key already exists")
	ErrCommandConflict            = errors.New("command ID reused with different request")
	ErrAttemptConflict            = errors.New("attempt state changed")
	ErrAssignmentConflict         = errors.New("source recipe assignment changed")
	ErrResultFenced               = errors.New("execution result rejected by current domain fence")
	ErrOverrideConflict           = errors.New("recruiting override head conflict")
	ErrOutboxConflict             = errors.New("recruiting outbox delivery state changed")
	ErrProgressConflict           = errors.New("recruiting listing progress changed")
	ErrInvalidCursor              = errors.New("invalid recruiting page cursor")
	ErrBudgetBlocked              = errors.New("recruiting execution budget blocked")
	ErrRecipeRolloutRejected      = errors.New("Recipe rollout rejected")
	ErrRecipeValidationRejected   = errors.New("Recipe validation evidence rejected")
	ErrRecipeValidationInProgress = errors.New("Recipe validation Work is still active")
	ErrRepairEvidenceRejected     = errors.New("repair validation evidence rejected")
	ErrWorkCorrectionInProgress   = errors.New("Work correction blocked by an active Attempt")
	ErrWebsiteRevisionConflict    = errors.New("Company website revision changed")
)

// SourceRestoreRequiredError turns a canonical-key collision with an archived
// Source into an actionable public outcome without creating a duplicate or
// mutating the retained Source history.
type SourceRestoreRequiredError struct {
	SourceID string
	Version  uint64
}

func (e *SourceRestoreRequiredError) Error() string {
	return fmt.Sprintf("archived Source %s at version %d already owns this canonical endpoint; use recruiting.source.restore",
		e.SourceID, e.Version)
}
