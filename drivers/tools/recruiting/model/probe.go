// Package model contains the deterministic recruiting domain model. It must
// remain independent of clocks, storage, networks, and the Atoll runtime.
package model

import "fmt"

// ProbeStatus is the deliberately small P0 lifecycle. Production Work states
// will grow from the same transition style rather than from executor roles.
type ProbeStatus string

const (
	ProbeOpen      ProbeStatus = "open"
	ProbeOffered   ProbeStatus = "offered"
	ProbeCompleted ProbeStatus = "completed"
	ProbeFailed    ProbeStatus = "failed"
)

// ProbeWork proves the P0 control-plane boundary: the Recruiting Actor owns
// the work and attempt identity; an executor can only report an outcome.
type ProbeWork struct {
	WorkID      string      `json:"work_id"`
	AttemptID   string      `json:"attempt_id"`
	CommandID   string      `json:"command_id"`
	ExecutorID  string      `json:"executor_id"`
	Note        string      `json:"note,omitempty"`
	Status      ProbeStatus `json:"status"`
	Result      string      `json:"result,omitempty"`
	FailureCode string      `json:"failure_code,omitempty"`
	Version     uint64      `json:"version"`
}

// NewProbeWork creates the actor-owned fact before any execution request is
// emitted. IDs are supplied by the application layer so replay can be stable.
func NewProbeWork(workID, attemptID, commandID, executorID, note string) (ProbeWork, error) {
	if workID == "" || attemptID == "" || commandID == "" || executorID == "" {
		return ProbeWork{}, fmt.Errorf("probe work requires work_id, attempt_id, command_id, and executor_id")
	}
	return ProbeWork{
		WorkID: workID, AttemptID: attemptID, CommandID: commandID,
		ExecutorID: executorID, Note: note, Status: ProbeOpen, Version: 1,
	}, nil
}

// MarkOffered records that the deterministic execution request was accepted
// for publication. Replaying the same transition is harmless.
func (w ProbeWork) MarkOffered() (ProbeWork, error) {
	switch w.Status {
	case ProbeOpen:
		w.Status = ProbeOffered
		w.Version++
		return w, nil
	case ProbeOffered:
		return w, nil
	default:
		return ProbeWork{}, fmt.Errorf("cannot offer probe work in %s", w.Status)
	}
}

// AcceptResult is fenced by both attempt identity and expected work version.
// Duplicate equal results converge; stale or conflicting results are rejected.
func (w ProbeWork) AcceptResult(attemptID string, expectedVersion uint64, result string) (ProbeWork, error) {
	if attemptID != w.AttemptID {
		return ProbeWork{}, fmt.Errorf("attempt mismatch")
	}
	if w.Status == ProbeCompleted && result == w.Result {
		return w, nil
	}
	if expectedVersion != w.Version {
		return ProbeWork{}, fmt.Errorf("version conflict: got %d want %d", expectedVersion, w.Version)
	}
	if w.Status != ProbeOffered {
		return ProbeWork{}, fmt.Errorf("cannot accept result in %s", w.Status)
	}
	w.Status = ProbeCompleted
	w.Result = result
	w.Version++
	return w, nil
}
