package store

import "errors"

var (
	ErrNotFound           = errors.New("recruiting entity not found")
	ErrBusinessKeyExists  = errors.New("recruiting business key already exists")
	ErrCommandConflict    = errors.New("command ID reused with different request")
	ErrAttemptConflict    = errors.New("attempt state changed")
	ErrAssignmentConflict = errors.New("source recipe assignment changed")
	ErrResultFenced       = errors.New("execution result rejected by current domain fence")
	ErrOverrideConflict   = errors.New("recruiting override head conflict")
	ErrOutboxConflict     = errors.New("recruiting outbox delivery state changed")
	ErrProgressConflict   = errors.New("recruiting listing progress changed")
	ErrInvalidCursor      = errors.New("invalid recruiting page cursor")
)
