package model

import (
	"fmt"
	"strings"
	"time"
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
	WorkID                string         `json:"work_id"`
	ParentWorkID          string         `json:"parent_work_id,omitempty"`
	InitiatorActorID      string         `json:"initiator_actor_id,omitempty"`
	CauseMessageID        string         `json:"cause_message_id,omitempty"`
	CauseWorkID           string         `json:"cause_work_id,omitempty"`
	TargetType            string         `json:"target_type"`
	TargetID              string         `json:"target_id"`
	Purpose               string         `json:"purpose"`
	Trigger               string         `json:"trigger"`
	Status                WorkStatus     `json:"work_status"`
	WaitingReason         string         `json:"waiting_reason,omitempty"`
	Resolution            WorkResolution `json:"resolution,omitempty"`
	ResolutionActorID     string         `json:"resolution_actor_id,omitempty"`
	ResolutionReason      string         `json:"resolution_reason,omitempty"`
	AcceptanceVersion     uint64         `json:"acceptance_version"`
	Version               uint64         `json:"version"`
	RetryPolicyVersion    uint64         `json:"retry_policy_version,omitempty"`
	AutomaticAttempts     uint64         `json:"automatic_attempts,omitempty"`
	LastFailureClass      string         `json:"last_failure_class,omitempty"`
	RetryNotBefore        string         `json:"retry_not_before,omitempty"`
	BlockedByRepairWorkID string         `json:"blocked_by_repair_work_id,omitempty"`
}

type FailureRoute string

const (
	FailureRetry FailureRoute = "retry"
	FailureHuman FailureRoute = "human"
)

type ExecutionFailureDecision struct {
	PolicyVersion  uint64
	AttemptCount   uint64
	FailureClass   string
	Route          FailureRoute
	RetryNotBefore string
	RepairWorkID   string
}

// ApplyExecutionFailure records the control plane's versioned decision on the
// Work itself. Placement persists the same retry time for indexed claiming;
// keeping it here makes the reason and policy visible to operators and audit.
func (w Work) ApplyExecutionFailure(expected uint64, decision ExecutionFailureDecision) (Work, error) {
	if err := requireVersion(expected, w.Version); err != nil {
		return Work{}, err
	}
	if w.Status != WorkRunning || decision.PolicyVersion == 0 || decision.AttemptCount != w.AutomaticAttempts+1 ||
		strings.TrimSpace(decision.FailureClass) == "" || decision.FailureClass != strings.TrimSpace(decision.FailureClass) {
		return Work{}, fmt.Errorf("running work and complete monotonic failure decision are required")
	}
	switch decision.Route {
	case FailureRetry:
		if strings.TrimSpace(decision.RetryNotBefore) == "" {
			return Work{}, fmt.Errorf("retry failure decision requires not-before time")
		}
		if decision.RepairWorkID != "" {
			return Work{}, fmt.Errorf("retry failure decision cannot carry a repair Work")
		}
		if _, err := time.Parse(time.RFC3339Nano, decision.RetryNotBefore); err != nil {
			return Work{}, fmt.Errorf("retry failure decision requires RFC3339 not-before time")
		}
		w.Status = WorkWaitingRetry
		w.RetryNotBefore = strings.TrimSpace(decision.RetryNotBefore)
	case FailureHuman:
		if strings.TrimSpace(decision.RetryNotBefore) != "" || strings.TrimSpace(decision.RepairWorkID) != decision.RepairWorkID {
			return Work{}, fmt.Errorf("human failure decision cannot carry retry time")
		}
		w.Status = WorkWaitingHuman
		w.RetryNotBefore = ""
		w.BlockedByRepairWorkID = decision.RepairWorkID
	default:
		return Work{}, fmt.Errorf("unknown failure route %q", decision.Route)
	}
	w.WaitingReason = decision.FailureClass
	w.RetryPolicyVersion = decision.PolicyVersion
	w.AutomaticAttempts = decision.AttemptCount
	w.LastFailureClass = decision.FailureClass
	w.Version++
	return w, nil
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

// WithCausality binds the identity that initiated a Work and its optional
// message/work causes. It is separate from NewWork so scheduler-created work
// and older callers can migrate without inventing a human identity.
func (w Work) WithCausality(initiatorActorID, causeMessageID, causeWorkID string) (Work, error) {
	if w.Version != 1 || w.Status != WorkOpen || strings.TrimSpace(initiatorActorID) == "" {
		return Work{}, fmt.Errorf("new open work and initiator identity are required")
	}
	w.InitiatorActorID = strings.TrimSpace(initiatorActorID)
	w.CauseMessageID = strings.TrimSpace(causeMessageID)
	w.CauseWorkID = strings.TrimSpace(causeWorkID)
	if w.CauseWorkID == w.WorkID {
		return Work{}, fmt.Errorf("work cannot cause itself")
	}
	return w, nil
}

// NewRetryWork preserves the original business target while creating a new
// lifecycle and acceptance fence. Terminal facts are never reopened.
func NewRetryWork(previous Work, workID, initiatorActorID, causeMessageID string) (Work, error) {
	if !previous.Terminal() {
		return Work{}, fmt.Errorf("retry requires a terminal previous work")
	}
	if previous.Status == WorkCompleted && previous.Resolution == ResolutionSucceeded {
		return Work{}, fmt.Errorf("succeeded work cannot be retried")
	}
	retry, err := NewWork(workID, previous.TargetType, previous.TargetID, previous.Purpose, "manual")
	if err != nil {
		return Work{}, err
	}
	retry.ParentWorkID = previous.ParentWorkID
	return retry.WithCausality(initiatorActorID, causeMessageID, previous.WorkID)
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

// RecoverFromRepair reopens a non-terminal Work after its shared Repair Work
// has been validated and resolved. Failed Attempts and failure metadata remain
// immutable; only the current scheduling gate is cleared.
func (w Work) RecoverFromRepair(expected uint64, repairWorkID string) (Work, error) {
	if err := requireVersion(expected, w.Version); err != nil {
		return Work{}, err
	}
	repairWorkID = strings.TrimSpace(repairWorkID)
	if w.Status != WorkWaitingHuman || repairWorkID == "" || w.BlockedByRepairWorkID != repairWorkID {
		return Work{}, &InvalidTransitionError{Entity: "work", From: string(w.Status), Action: "recover from repair"}
	}
	w.Status, w.WaitingReason, w.BlockedByRepairWorkID, w.RetryNotBefore = WorkOpen, "", "", ""
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
	SampleVersion       uint64        `json:"sample_version,omitempty"`
	ProfileID           string        `json:"profile_id,omitempty"`
	ProfileVersion      uint64        `json:"profile_version,omitempty"`
	BatchVersion        uint64        `json:"batch_version,omitempty"`
	DiscoveryGeneration uint64        `json:"discovery_generation,omitempty"`
}

type AttemptFence struct {
	CompanyVersion      uint64
	SourceVersion       uint64
	AssignmentVersion   uint64
	RecipeID            string
	RecipeVersion       uint64
	CheckpointVersion   uint64
	RefreshGeneration   uint64
	SampleVersion       uint64
	ProfileID           string
	ProfileVersion      uint64
	BatchVersion        uint64
	DiscoveryGeneration uint64
}

// WithBatchFence binds an Attempt to a version of an extension-owned batch
// aggregate. Batch execution has no website, Recipe, profile, or origin fence,
// and therefore must not invent fake source-domain identities.
func (a Attempt) WithBatchFence(batchVersion uint64) (Attempt, error) {
	if a.Status != AttemptOffered || batchVersion == 0 || a.CompanyVersion != 0 || a.SourceVersion != 0 ||
		a.AssignmentVersion != 0 || a.RecipeID != "" || a.RecipeVersion != 0 || a.ProfileID != "" || a.ProfileVersion != 0 {
		return Attempt{}, fmt.Errorf("offered unfenced attempt and batch version are required")
	}
	a.BatchVersion = batchVersion
	return a, nil
}

// WithDiscoveryFence binds source discovery to its Company, immutable Recipe
// version, aggregate generation state, and optional authenticated profile. It
// deliberately leaves Source and Assignment versions empty: neither exists
// before a candidate is accepted.
func (a Attempt) WithDiscoveryFence(f AttemptFence) (Attempt, error) {
	if a.Status != AttemptOffered || f.CompanyVersion == 0 || f.DiscoveryGeneration == 0 ||
		strings.TrimSpace(f.RecipeID) == "" || f.RecipeVersion == 0 || f.SourceVersion != 0 ||
		f.AssignmentVersion != 0 || f.CheckpointVersion != 0 || f.RefreshGeneration != 0 || f.SampleVersion != 0 || f.BatchVersion != 0 {
		return Attempt{}, fmt.Errorf("offered attempt and company/discovery/recipe fence are required")
	}
	if (f.ProfileID == "") != (f.ProfileVersion == 0) {
		return Attempt{}, fmt.Errorf("profile identity and version must be supplied together")
	}
	a.CompanyVersion, a.DiscoveryGeneration = f.CompanyVersion, f.DiscoveryGeneration
	a.RecipeID, a.RecipeVersion = f.RecipeID, f.RecipeVersion
	a.ProfileID, a.ProfileVersion = f.ProfileID, f.ProfileVersion
	return a, nil
}

// WithCompanyRecipeFence binds evidence-only validation of a company-scoped
// Discovery Recipe. It has neither a Source/Assignment nor a production
// discovery generation and must not invent either identity.
func (a Attempt) WithCompanyRecipeFence(f AttemptFence) (Attempt, error) {
	if a.Status != AttemptOffered || f.CompanyVersion == 0 || strings.TrimSpace(f.RecipeID) == "" ||
		f.RecipeVersion == 0 || f.SourceVersion != 0 || f.AssignmentVersion != 0 || f.CheckpointVersion != 0 ||
		f.RefreshGeneration != 0 || f.SampleVersion != 0 || f.BatchVersion != 0 || f.DiscoveryGeneration != 0 {
		return Attempt{}, fmt.Errorf("offered attempt and company/recipe validation fence are required")
	}
	if (f.ProfileID == "") != (f.ProfileVersion == 0) {
		return Attempt{}, fmt.Errorf("profile identity and version must be supplied together")
	}
	a.CompanyVersion, a.RecipeID, a.RecipeVersion = f.CompanyVersion, f.RecipeID, f.RecipeVersion
	a.ProfileID, a.ProfileVersion = f.ProfileID, f.ProfileVersion
	return a, nil
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
	a.CheckpointVersion, a.RefreshGeneration, a.SampleVersion = f.CheckpointVersion, f.RefreshGeneration, f.SampleVersion
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
		current.SampleVersion != a.SampleVersion ||
		current.ProfileID != a.ProfileID || current.ProfileVersion != a.ProfileVersion ||
		current.DiscoveryGeneration != a.DiscoveryGeneration {
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
