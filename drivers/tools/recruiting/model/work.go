package model

import (
	"fmt"
	"strings"
)

type WorkStatus string

const (
	WorkOpen         WorkStatus = "open"
	WorkRunning      WorkStatus = "running"
	WorkWaitingRetry WorkStatus = "waiting_retry"
	WorkWaitingHuman WorkStatus = "waiting_human"
	WorkPaused       WorkStatus = "paused"
	WorkCompleted    WorkStatus = "completed"
	WorkFailed       WorkStatus = "failed"
	WorkCanceled     WorkStatus = "canceled"
)

type WorkResolution string

const (
	ResolutionSucceeded   WorkResolution = "succeeded"
	ResolutionAcceptedGap WorkResolution = "accepted_gap"
	ResolutionSkipped     WorkResolution = "skipped"
	ResolutionTerminated  WorkResolution = "terminated"
)

type Work struct {
	WorkID            string         `json:"work_id"`
	ParentWorkID      string         `json:"parent_work_id,omitempty"`
	TargetType        string         `json:"target_type"`
	TargetID          string         `json:"target_id"`
	Purpose           string         `json:"purpose"`
	Trigger           string         `json:"trigger"`
	Status            WorkStatus     `json:"work_status"`
	WaitingReason     string         `json:"waiting_reason,omitempty"`
	Resolution        WorkResolution `json:"resolution,omitempty"`
	ResolutionActorID string         `json:"resolution_actor_id,omitempty"`
	ResolutionReason  string         `json:"resolution_reason,omitempty"`
	AcceptanceVersion uint64         `json:"acceptance_version"`
	Version           uint64         `json:"version"`
}

// NewChildWork creates an independently versioned unit of work while retaining
// the user-visible causal link to its parent. Parent and child lifecycles are
// deliberately not coupled: repositories persist and CAS each Work separately.
func NewChildWork(parent Work, workID, targetType, targetID, purpose, trigger string) (Work, error) {
	if parent.WorkID == "" || parent.Terminal() {
		return Work{}, fmt.Errorf("child work requires a non-terminal parent")
	}
	child, err := NewWork(workID, targetType, targetID, purpose, trigger)
	if err != nil {
		return Work{}, err
	}
	child.ParentWorkID = parent.WorkID
	return child, nil
}

func NewWork(workID, targetType, targetID, purpose, trigger string) (Work, error) {
	if strings.TrimSpace(workID) == "" || strings.TrimSpace(targetType) == "" || strings.TrimSpace(targetID) == "" || strings.TrimSpace(purpose) == "" || strings.TrimSpace(trigger) == "" {
		return Work{}, fmt.Errorf("work identity, target, purpose, and trigger are required")
	}
	return Work{WorkID: workID, TargetType: targetType, TargetID: targetID, Purpose: purpose, Trigger: trigger, Status: WorkOpen, AcceptanceVersion: 1, Version: 1}, nil
}

func (w Work) Start(expected uint64) (Work, error) {
	if err := requireVersion(expected, w.Version); err != nil {
		return Work{}, err
	}
	if w.Status != WorkOpen && w.Status != WorkWaitingRetry && w.Status != WorkWaitingHuman {
		return Work{}, &InvalidTransitionError{Entity: "work", From: string(w.Status), Action: "start"}
	}
	w.Status, w.WaitingReason = WorkRunning, ""
	w.Version++
	return w, nil
}

func (w Work) WaitRetry(expected uint64, reason string) (Work, error) {
	return w.wait(expected, WorkWaitingRetry, reason)
}

func (w Work) WaitHuman(expected uint64, reason string) (Work, error) {
	return w.wait(expected, WorkWaitingHuman, reason)
}

func (w Work) wait(expected uint64, status WorkStatus, reason string) (Work, error) {
	if err := requireVersion(expected, w.Version); err != nil {
		return Work{}, err
	}
	if w.Status != WorkRunning || strings.TrimSpace(reason) == "" {
		return Work{}, &InvalidTransitionError{Entity: "work", From: string(w.Status), Action: "wait"}
	}
	w.Status, w.WaitingReason = status, strings.TrimSpace(reason)
	w.Version++
	return w, nil
}

func (w Work) Complete(expected uint64, resolution WorkResolution, actorID, reason string) (Work, error) {
	if err := requireVersion(expected, w.Version); err != nil {
		return Work{}, err
	}
	if w.Status != WorkRunning && w.Status != WorkWaitingHuman {
		return Work{}, &InvalidTransitionError{Entity: "work", From: string(w.Status), Action: "complete"}
	}
	switch resolution {
	case ResolutionSucceeded:
	case ResolutionAcceptedGap, ResolutionSkipped, ResolutionTerminated:
		if strings.TrimSpace(actorID) == "" || strings.TrimSpace(reason) == "" {
			return Work{}, fmt.Errorf("non-success resolution requires actor and reason")
		}
	default:
		return Work{}, fmt.Errorf("unknown work resolution %q", resolution)
	}
	w.Status, w.Resolution = WorkCompleted, resolution
	w.ResolutionActorID, w.ResolutionReason, w.WaitingReason = strings.TrimSpace(actorID), strings.TrimSpace(reason), ""
	w.Version++
	return w, nil
}

func (w Work) Cancel(expected uint64) (Work, error) {
	if err := requireVersion(expected, w.Version); err != nil {
		return Work{}, err
	}
	if w.Terminal() {
		return Work{}, &InvalidTransitionError{Entity: "work", From: string(w.Status), Action: "cancel"}
	}
	w.Status, w.WaitingReason = WorkCanceled, ""
	w.AcceptanceVersion++
	w.Version++
	return w, nil
}

func (w Work) Pause(expected uint64) (Work, error) {
	if err := requireVersion(expected, w.Version); err != nil {
		return Work{}, err
	}
	if w.Terminal() || w.Status == WorkPaused {
		return Work{}, &InvalidTransitionError{Entity: "work", From: string(w.Status), Action: "pause"}
	}
	w.Status, w.WaitingReason = WorkPaused, ""
	w.AcceptanceVersion++
	w.Version++
	return w, nil
}

func (w Work) Resume(expected uint64) (Work, error) {
	if err := requireVersion(expected, w.Version); err != nil {
		return Work{}, err
	}
	if w.Status != WorkPaused {
		return Work{}, &InvalidTransitionError{Entity: "work", From: string(w.Status), Action: "resume"}
	}
	w.Status = WorkOpen
	w.Version++
	return w, nil
}

func (w Work) Fail(expected uint64, reason string) (Work, error) {
	if err := requireVersion(expected, w.Version); err != nil {
		return Work{}, err
	}
	if w.Status != WorkRunning || strings.TrimSpace(reason) == "" {
		return Work{}, &InvalidTransitionError{Entity: "work", From: string(w.Status), Action: "fail"}
	}
	w.Status, w.ResolutionReason = WorkFailed, strings.TrimSpace(reason)
	w.AcceptanceVersion++
	w.Version++
	return w, nil
}

func (w Work) Terminal() bool {
	return w.Status == WorkCompleted || w.Status == WorkFailed || w.Status == WorkCanceled
}

type AttemptStatus string

const (
	AttemptOffered   AttemptStatus = "offered"
	AttemptAccepted  AttemptStatus = "accepted"
	AttemptRunning   AttemptStatus = "running"
	AttemptSucceeded AttemptStatus = "succeeded"
	AttemptFailed    AttemptStatus = "failed"
	AttemptExpired   AttemptStatus = "expired"
	AttemptRejected  AttemptStatus = "rejected"
)

type Attempt struct {
	AttemptID           string        `json:"attempt_id"`
	WorkID              string        `json:"work_id"`
	Status              AttemptStatus `json:"attempt_status"`
	AcceptanceVersion   uint64        `json:"acceptance_version"`
	ExecutorActorID     string        `json:"executor_actor_id,omitempty"`
	ExecutorIncarnation string        `json:"executor_incarnation,omitempty"`
	Capability          string        `json:"capability,omitempty"`
	CompanyVersion      uint64        `json:"company_version,omitempty"`
	SourceVersion       uint64        `json:"source_version,omitempty"`
	AssignmentVersion   uint64        `json:"assignment_version,omitempty"`
	RecipeID            string        `json:"recipe_id,omitempty"`
	RecipeVersion       uint64        `json:"recipe_version,omitempty"`
	CheckpointVersion   uint64        `json:"checkpoint_version,omitempty"`
	RefreshGeneration   uint64        `json:"refresh_generation,omitempty"`
	ProfileID           string        `json:"profile_id,omitempty"`
	ProfileVersion      uint64        `json:"profile_version,omitempty"`
}

type AttemptFence struct {
	CompanyVersion    uint64
	SourceVersion     uint64
	AssignmentVersion uint64
	RecipeID          string
	RecipeVersion     uint64
	CheckpointVersion uint64
	RefreshGeneration uint64
	ProfileID         string
	ProfileVersion    uint64
}

func NewAttempt(id string, work Work) (Attempt, error) {
	if strings.TrimSpace(id) == "" || work.WorkID == "" || work.Terminal() {
		return Attempt{}, fmt.Errorf("attempt requires a non-terminal work and identity")
	}
	return Attempt{AttemptID: id, WorkID: work.WorkID, Status: AttemptOffered, AcceptanceVersion: work.AcceptanceVersion}, nil
}

func (a Attempt) BindExecutor(actorID, incarnation, capability string) (Attempt, error) {
	if a.Status != AttemptOffered || strings.TrimSpace(actorID) == "" || strings.TrimSpace(incarnation) == "" || strings.TrimSpace(capability) == "" {
		return Attempt{}, fmt.Errorf("offered attempt and complete executor identity are required")
	}
	a.ExecutorActorID, a.ExecutorIncarnation, a.Capability = actorID, incarnation, capability
	return a, nil
}

func (a Attempt) WithFence(f AttemptFence) (Attempt, error) {
	if a.Status != AttemptOffered || f.CompanyVersion == 0 || f.SourceVersion == 0 || f.AssignmentVersion == 0 ||
		strings.TrimSpace(f.RecipeID) == "" || f.RecipeVersion == 0 {
		return Attempt{}, fmt.Errorf("offered attempt and company/source/assignment/recipe fence are required")
	}
	if (f.ProfileID == "") != (f.ProfileVersion == 0) {
		return Attempt{}, fmt.Errorf("profile identity and version must be supplied together")
	}
	a.CompanyVersion, a.SourceVersion, a.AssignmentVersion = f.CompanyVersion, f.SourceVersion, f.AssignmentVersion
	a.RecipeID, a.RecipeVersion = f.RecipeID, f.RecipeVersion
	a.CheckpointVersion, a.RefreshGeneration = f.CheckpointVersion, f.RefreshGeneration
	a.ProfileID, a.ProfileVersion = f.ProfileID, f.ProfileVersion
	return a, nil
}

func (a Attempt) CanSubmit(work Work) error {
	if a.WorkID != work.WorkID || a.AcceptanceVersion != work.AcceptanceVersion {
		return fmt.Errorf("attempt fenced by work acceptance version")
	}
	if work.Terminal() {
		return fmt.Errorf("attempt cannot submit to terminal work")
	}
	return nil
}

func (a Attempt) CanAcceptResult(work Work, current AttemptFence, executorActorID, executorIncarnation string) error {
	if err := a.CanSubmit(work); err != nil {
		return err
	}
	if a.Status != AttemptRunning || executorActorID != a.ExecutorActorID || executorIncarnation != a.ExecutorIncarnation {
		return fmt.Errorf("attempt result sender or lifecycle is not accepted")
	}
	if current.CompanyVersion != a.CompanyVersion || current.SourceVersion != a.SourceVersion ||
		current.AssignmentVersion != a.AssignmentVersion || current.RecipeID != a.RecipeID || current.RecipeVersion != a.RecipeVersion ||
		current.CheckpointVersion != a.CheckpointVersion || current.RefreshGeneration != a.RefreshGeneration ||
		current.ProfileID != a.ProfileID || current.ProfileVersion != a.ProfileVersion {
		return fmt.Errorf("attempt result fenced by changed domain version")
	}
	return nil
}

func (a Attempt) Accept() (Attempt, error) {
	return a.transition(AttemptOffered, AttemptAccepted, "accept")
}

func (a Attempt) Start() (Attempt, error) {
	return a.transition(AttemptAccepted, AttemptRunning, "start")
}

func (a Attempt) Succeed() (Attempt, error) {
	return a.transition(AttemptRunning, AttemptSucceeded, "succeed")
}

func (a Attempt) Fail() (Attempt, error) {
	return a.transition(AttemptRunning, AttemptFailed, "fail")
}

func (a Attempt) Expire() (Attempt, error) {
	if a.Status != AttemptOffered && a.Status != AttemptAccepted && a.Status != AttemptRunning {
		return Attempt{}, &InvalidTransitionError{Entity: "attempt", From: string(a.Status), Action: "expire"}
	}
	a.Status = AttemptExpired
	return a, nil
}

func (a Attempt) Reject() (Attempt, error) {
	if a.Status == AttemptSucceeded || a.Status == AttemptFailed || a.Status == AttemptExpired || a.Status == AttemptRejected {
		return Attempt{}, &InvalidTransitionError{Entity: "attempt", From: string(a.Status), Action: "reject"}
	}
	a.Status = AttemptRejected
	return a, nil
}

func (a Attempt) transition(from, to AttemptStatus, action string) (Attempt, error) {
	if a.Status != from {
		return Attempt{}, &InvalidTransitionError{Entity: "attempt", From: string(a.Status), Action: action}
	}
	a.Status = to
	return a, nil
}
