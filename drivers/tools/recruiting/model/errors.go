package model

import "fmt"

type VersionConflictError struct {
	Expected uint64
	Actual   uint64
}

func (e *VersionConflictError) Error() string {
	return fmt.Sprintf("version conflict: expected %d, actual %d", e.Expected, e.Actual)
}

type InvalidTransitionError struct {
	Entity string
	From   string
	Action string
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("%s cannot %s from %s", e.Entity, e.Action, e.From)
}

func requireVersion(expected, actual uint64) error {
	if expected != actual {
		return &VersionConflictError{Expected: expected, Actual: actual}
	}
	return nil
}
