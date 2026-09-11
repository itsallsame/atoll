package model

import (
	"fmt"
	"strings"
	"time"
)

type ScopeControlStatus string
type ScopeControlAction string
type ScopeCatchUpDisposition string

const (
	ScopeControlApplying  ScopeControlStatus      = "applying"
	ScopeControlCompleted ScopeControlStatus      = "completed"
	ScopeControlPause     ScopeControlAction      = "pause"
	ScopeControlResume    ScopeControlAction      = "resume"
	ScopeCatchUpQueued    ScopeCatchUpDisposition = "queued"
	ScopeCatchUpSkipped   ScopeCatchUpDisposition = "skipped"
	scopeControlBatchMax                          = 500
)

// ScopeControlOperation is the durable, bounded projection of one Company or
// Source pause command over its Work set. It is data owned by the Recruiting
// extension, not a new Actor or Worker type.
type ScopeControlOperation struct {
	OperationID          string             `json:"operation_id"`
	Action               ScopeControlAction `json:"action"`
	ScopeType            string             `json:"scope_type"`
	ScopeID              string             `json:"scope_id"`
	Mode                 PauseMode          `json:"pause_mode"`
	ReversesOperationID  string             `json:"reverses_operation_id,omitempty"`
	PausedEntityVersion  uint64             `json:"paused_entity_version"`
	ConfigurationVersion uint64             `json:"configuration_version"`
	ControlEpoch         uint64             `json:"control_epoch"`
	ExecutionFence       uint64             `json:"execution_fence"`
	Status               ScopeControlStatus `json:"status"`
	ProjectionCompleted  bool               `json:"projection_completed"`
	WorkCursor           string             `json:"work_cursor,omitempty"`
	WorksScanned         uint64             `json:"works_scanned"`
	WorksPaused          uint64             `json:"works_paused"`
	WorksCanceled        uint64             `json:"works_canceled"`
	WorksResumed         uint64             `json:"works_resumed"`
	AttemptsExpired      uint64             `json:"attempts_expired"`
	ActiveRoots          uint64             `json:"active_roots"`
	CatchUpCompleted     bool               `json:"catch_up_completed"`
	CatchUpCursor        string             `json:"catch_up_cursor,omitempty"`
	CatchUpUpperSourceID string             `json:"catch_up_upper_source_id,omitempty"`
	SourcesScanned       uint64             `json:"sources_scanned"`
	CatchUpsQueued       uint64             `json:"catch_ups_queued"`
	CatchUpsSkipped      uint64             `json:"catch_ups_skipped"`
	StartedAt            string             `json:"started_at"`
	CompletedAt          string             `json:"completed_at,omitempty"`
	Version              uint64             `json:"version"`
}

type ScopeControlBatch struct {
	Cursor          string
	Scanned         int
	Paused          int
	Canceled        int
	Resumed         int
	ExpiredAttempts int
	HasMore         bool
	AppliedAt       time.Time
}

func NewScopeControlOperation(id, scopeType, scopeID string, mode PauseMode, entityVersion,
	configurationVersion, controlEpoch, executionFence, activeRoots uint64, at time.Time) (ScopeControlOperation, error) {
	id, scopeType, scopeID = strings.TrimSpace(id), strings.TrimSpace(scopeType), strings.TrimSpace(scopeID)
	if id == "" || scopeID == "" || at.IsZero() || entityVersion < 2 || configurationVersion == 0 ||
		controlEpoch == 0 || executionFence == 0 {
		return ScopeControlOperation{}, fmt.Errorf("scope control identity, versions, fences, and time are required")
	}
	if scopeType != "company" && scopeType != "source" {
		return ScopeControlOperation{}, fmt.Errorf("scope control type must be company or source")
	}
	if err := mode.Validate(); err != nil {
		return ScopeControlOperation{}, err
	}
	if mode != PauseFinishCausalChain && activeRoots != 0 {
		return ScopeControlOperation{}, fmt.Errorf("only finish_causal_chain may register active roots")
	}
	return ScopeControlOperation{OperationID: id, Action: ScopeControlPause, ScopeType: scopeType, ScopeID: scopeID, Mode: mode,
		PausedEntityVersion: entityVersion, ConfigurationVersion: configurationVersion,
		ControlEpoch: controlEpoch, ExecutionFence: executionFence, Status: ScopeControlApplying,
		ActiveRoots: activeRoots, CatchUpCompleted: true, StartedAt: at.UTC().Format(time.RFC3339Nano), Version: 1}, nil
}

func NewScopeResumeOperation(id, scopeType, scopeID, reversesOperationID, catchUpUpperSourceID string, entityVersion,
	configurationVersion, controlEpoch, executionFence uint64, at time.Time) (ScopeControlOperation, error) {
	id, scopeType, scopeID = strings.TrimSpace(id), strings.TrimSpace(scopeType), strings.TrimSpace(scopeID)
	reversesOperationID = strings.TrimSpace(reversesOperationID)
	catchUpUpperSourceID = strings.TrimSpace(catchUpUpperSourceID)
	if id == "" || scopeID == "" || reversesOperationID == "" || at.IsZero() || entityVersion < 2 ||
		configurationVersion == 0 || controlEpoch == 0 || executionFence == 0 {
		return ScopeControlOperation{}, fmt.Errorf("scope resume identity, versions, fences, reverse operation, and time are required")
	}
	if scopeType != "company" && scopeType != "source" {
		return ScopeControlOperation{}, fmt.Errorf("scope control type must be company or source")
	}
	if scopeType == "source" && catchUpUpperSourceID != scopeID {
		return ScopeControlOperation{}, fmt.Errorf("Source resume catch-up bound must equal its Source")
	}
	return ScopeControlOperation{OperationID: id, Action: ScopeControlResume, ScopeType: scopeType, ScopeID: scopeID,
		ReversesOperationID: reversesOperationID, PausedEntityVersion: entityVersion,
		ConfigurationVersion: configurationVersion, ControlEpoch: controlEpoch, ExecutionFence: executionFence,
		Status: ScopeControlApplying, CatchUpUpperSourceID: catchUpUpperSourceID,
		StartedAt: at.UTC().Format(time.RFC3339Nano), Version: 1}, nil
}

func (o ScopeControlOperation) RecordBatch(expected uint64, batch ScopeControlBatch) (ScopeControlOperation, error) {
	if err := requireVersion(expected, o.Version); err != nil {
		return ScopeControlOperation{}, err
	}
	if o.Status != ScopeControlApplying || batch.AppliedAt.IsZero() || batch.Scanned < 0 ||
		batch.Scanned > scopeControlBatchMax || batch.Paused < 0 || batch.Canceled < 0 ||
		batch.ExpiredAttempts < 0 || batch.Resumed < 0 || batch.Paused+batch.Canceled+batch.Resumed > batch.Scanned {
		return ScopeControlOperation{}, fmt.Errorf("scope control batch is invalid or operation is terminal")
	}
	batch.Cursor = strings.TrimSpace(batch.Cursor)
	if batch.Scanned > 0 && (batch.Cursor == "" || batch.Cursor <= o.WorkCursor) {
		return ScopeControlOperation{}, fmt.Errorf("scope control batch cursor must advance")
	}
	if batch.Scanned == 0 && batch.Cursor != "" {
		return ScopeControlOperation{}, fmt.Errorf("empty scope control batch cannot advance cursor")
	}
	if batch.HasMore && batch.Scanned == 0 {
		return ScopeControlOperation{}, fmt.Errorf("scope control continuation requires progress")
	}
	if o.Action == ScopeControlResume {
		if o.Mode != "" || o.ReversesOperationID == "" || batch.Paused != 0 || batch.Canceled != 0 || batch.ExpiredAttempts != 0 {
			return ScopeControlOperation{}, fmt.Errorf("resume operation can only report resumed Work")
		}
	} else {
		if o.Action != ScopeControlPause || batch.Resumed != 0 {
			return ScopeControlOperation{}, fmt.Errorf("pause operation cannot report resumed Work")
		}
		if o.Mode == PauseCancel && batch.Paused != 0 {
			return ScopeControlOperation{}, fmt.Errorf("cancel operation cannot report paused Work")
		}
		if o.Mode != PauseCancel && (batch.Canceled != 0 || batch.ExpiredAttempts != 0) {
			return ScopeControlOperation{}, fmt.Errorf("non-cancel operation cannot cancel Work or expire Attempts")
		}
	}
	o.WorksScanned += uint64(batch.Scanned)
	o.WorksPaused += uint64(batch.Paused)
	o.WorksCanceled += uint64(batch.Canceled)
	o.WorksResumed += uint64(batch.Resumed)
	o.AttemptsExpired += uint64(batch.ExpiredAttempts)
	if batch.Scanned > 0 {
		o.WorkCursor = batch.Cursor
	}
	if !batch.HasMore {
		o.ProjectionCompleted = true
		if o.Action == ScopeControlPause && (o.Mode != PauseFinishCausalChain || o.ActiveRoots == 0) {
			o.Status = ScopeControlCompleted
			o.CompletedAt = batch.AppliedAt.UTC().Format(time.RFC3339Nano)
		}
	}
	o.Version++
	return o, nil
}

func (o ScopeControlOperation) RecordCatchUpSource(expected uint64, sourceID string,
	disposition ScopeCatchUpDisposition, at time.Time) (ScopeControlOperation, error) {
	if err := requireVersion(expected, o.Version); err != nil {
		return ScopeControlOperation{}, err
	}
	sourceID = strings.TrimSpace(sourceID)
	if o.Action != ScopeControlResume || o.Status != ScopeControlApplying || !o.ProjectionCompleted ||
		o.CatchUpCompleted || sourceID == "" || sourceID <= o.CatchUpCursor || at.IsZero() {
		return ScopeControlOperation{}, fmt.Errorf("next resumptive catch-up Source is required")
	}
	switch disposition {
	case ScopeCatchUpQueued:
		o.CatchUpsQueued++
	case ScopeCatchUpSkipped:
		o.CatchUpsSkipped++
	default:
		return ScopeControlOperation{}, fmt.Errorf("catch-up disposition must be queued or skipped")
	}
	o.CatchUpCursor = sourceID
	o.SourcesScanned++
	o.Version++
	return o, nil
}

func (o ScopeControlOperation) CompleteCatchUp(expected uint64, at time.Time) (ScopeControlOperation, error) {
	if err := requireVersion(expected, o.Version); err != nil {
		return ScopeControlOperation{}, err
	}
	if o.Action != ScopeControlResume || o.Status != ScopeControlApplying || !o.ProjectionCompleted ||
		o.CatchUpCompleted || at.IsZero() {
		return ScopeControlOperation{}, fmt.Errorf("projected resume operation is required to complete catch-up")
	}
	o.CatchUpCompleted = true
	o.Status = ScopeControlCompleted
	o.CompletedAt = at.UTC().Format(time.RFC3339Nano)
	o.Version++
	return o, nil
}

func (o ScopeControlOperation) SettleRoot(expected uint64, at time.Time) (ScopeControlOperation, error) {
	if err := requireVersion(expected, o.Version); err != nil {
		return ScopeControlOperation{}, err
	}
	if o.Status != ScopeControlApplying || o.Action != ScopeControlPause || o.Mode != PauseFinishCausalChain || o.ActiveRoots == 0 || at.IsZero() {
		return ScopeControlOperation{}, fmt.Errorf("active finish_causal_chain root is required")
	}
	o.ActiveRoots--
	if o.ActiveRoots == 0 && o.ProjectionCompleted {
		o.Status = ScopeControlCompleted
		o.CompletedAt = at.UTC().Format(time.RFC3339Nano)
	}
	o.Version++
	return o, nil
}

func (o ScopeControlOperation) NeedsProjection() bool {
	return o.Status == ScopeControlApplying && !o.ProjectionCompleted
}

func (o ScopeControlOperation) AcceptsExistingAttempt() bool {
	return o.Action == ScopeControlPause && (o.Mode == PauseDrain || o.Mode == PauseFinishCausalChain)
}

func (o ScopeControlOperation) AllowsCausalDescendant(rootWorkID string) bool {
	return o.Status == ScopeControlApplying && o.Action == ScopeControlPause && o.Mode == PauseFinishCausalChain &&
		o.ActiveRoots > 0 && strings.TrimSpace(rootWorkID) != ""
}
