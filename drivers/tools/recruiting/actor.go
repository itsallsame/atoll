package recruiting

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/lib/behavior"
	"github.com/wanpengxie/atoll/protocol/actor"
	"github.com/wanpengxie/atoll/protocol/message"
	"github.com/wanpengxie/atoll/protocol/resource"
	"github.com/wanpengxie/atoll/runtime/schedule"
)

const stateKey resource.ResourceID = "recruiting.p0.state"

type storedState struct {
	Works               map[string]model.ProbeWork             `json:"works"`
	CommandWorks        map[string]string                      `json:"command_works"`
	Receipts            map[string]json.RawMessage             `json:"receipts"`
	ReconcileTimerID    string                                 `json:"reconcile_timer_id,omitempty"`
	DailyTimerID        string                                 `json:"daily_timer_id,omitempty"`
	DailyWorkTimerID    string                                 `json:"daily_work_timer_id,omitempty"`
	DailyCloseTimerID   string                                 `json:"daily_close_timer_id,omitempty"`
	ExecutorPresence    map[string]executorPresenceObservation `json:"executor_presence,omitempty"`
	ExecutorSweepCursor string                                 `json:"executor_sweep_cursor,omitempty"`
}

type probeStartPayload struct {
	CommandID string `json:"command_id"`
	Note      string `json:"note,omitempty"`
}

type probeSchedulePayload struct {
	CommandID string `json:"command_id"`
	Note      string `json:"note,omitempty"`
	DelayMS   int    `json:"delay_ms"`
}

type probeDuePayload struct {
	CommandID string `json:"command_id"`
	Note      string `json:"note,omitempty"`
}

type probeStatusPayload struct {
	WorkID    string `json:"work_id,omitempty"`
	CommandID string `json:"command_id,omitempty"`
}

type executionResultPayload struct {
	WorkID              string `json:"work_id"`
	AttemptID           string `json:"attempt_id"`
	ExpectedWorkVersion uint64 `json:"expected_work_version"`
	Result              string `json:"result"`
}

type executionOfferPayload struct {
	WorkID              string        `json:"work_id"`
	AttemptID           string        `json:"attempt_id"`
	ExpectedWorkVersion uint64        `json:"expected_work_version"`
	ReplyTo             actor.ActorID `json:"reply_to"`
	Note                string        `json:"note,omitempty"`
}

type probeResponse struct {
	WorkID     string            `json:"work_id"`
	AttemptID  string            `json:"attempt_id"`
	CommandID  string            `json:"command_id"`
	ExecutorID string            `json:"executor_id"`
	WorkStatus model.ProbeStatus `json:"work_status"`
	Version    uint64            `json:"version"`
	Result     string            `json:"result,omitempty"`
}

type scheduleResponse struct {
	CommandID     string `json:"command_id"`
	TimerID       string `json:"timer_id"`
	ScheduleState string `json:"schedule_state"`
}

func Def(cfg Config) actorbase.Def {
	return actorbase.Def{Manifest: manifest(), New: func() (actorbase.Proc, error) {
		return func(sys actorbase.Sys) error { return run(sys, cfg) }, nil
	}}
}

func run(sys actorbase.Sys, cfg Config) error {
	var repository *store.Repository
	if cfg.DatabaseDSNEnv != "" {
		if dsn := strings.TrimSpace(os.Getenv(cfg.DatabaseDSNEnv)); dsn != "" {
			db, err := store.Open(dsn)
			if err != nil {
				return fmt.Errorf("recruiting: open database: %w", err)
			}
			defer db.Close()
			repository, err = store.NewRepository(db)
			if err != nil {
				return err
			}
		}
	}
	state, err := loadState(sys)
	if err != nil {
		return err
	}
	if repository != nil {
		if err := bootstrapExecutorPresence(sys, state, repository); err != nil {
			return fmt.Errorf("recruiting: bootstrap executor presence: %w", err)
		}
		if err := rearmReconcileTimerOnStartup(sys, cfg, state); err != nil {
			return fmt.Errorf("recruiting: rearm startup reconcile timer: %w", err)
		}
	}
	if repository != nil && cfg.DailyScheduleEnabled && state.DailyTimerID == "" {
		if err := armDailyTimer(sys, cfg, state, time.Now().UTC()); err != nil {
			return err
		}
	}
	if repository != nil && cfg.DailyScheduleEnabled && state.DailyWorkTimerID == "" {
		if err := armNextDailyWorkTimer(sys, cfg, state, repository, time.Now().UTC()); err != nil {
			return err
		}
	}
	if repository != nil && cfg.DailyScheduleEnabled && state.DailyCloseTimerID == "" {
		if err := armNextDailyCloseTimer(sys, cfg, state, repository, time.Now().UTC()); err != nil {
			return err
		}
	}
	for {
		msg, err := sys.Recv()
		if err != nil {
			return err
		}
		if msg.Kind == message.KindEvent {
			switch msg.Type {
			case typeProbeDue:
				handleDue(sys, cfg, state, msg)
			case typeOutboxReconcileDue:
				if err := handleOutboxReconcileDue(sys, cfg, state, repository, msg); err != nil {
					return err
				}
			case typeDailyCutoffDue:
				if err := handleDailyCutoffDue(sys, cfg, state, repository, msg); err != nil {
					return err
				}
			case typeDailyWorkDue:
				if err := handleDailyWorkDue(sys, cfg, state, repository, msg); err != nil {
					return err
				}
			case typeDailyCloseDue:
				if err := handleDailyCloseDue(sys, cfg, state, repository, msg); err != nil {
					return err
				}
			}
			continue
		}
		switch msg.Type {
		case TypeCompanyMergePreview, TypeCompanyMergeConfirm:
			handleCompanyMerge(sys, repository, msg)
		case TypeCompanyImport, TypeCompanyImportGet, TypeCompanyImportItems, TypeCompanyImportConfirm, TypeCompanyImportCancel,
			TypeCompanyImportItemResolve:
			handleCompanyImport(sys, cfg, repository, msg)
		case TypeCompanyAdd, TypeCompanyUpdate, TypeCompanyWebsiteRollback, TypeCompanyPause, TypeCompanyResume, TypeCompanyArchive, TypeCompanyRestore,
			TypeCompanyGet, TypeCompanyList:
			handleCompanyMessage(sys, repository, msg)
		case TypeCompanyErasurePreview, TypeCompanyErasureGet, TypeCompanyErasureApprove,
			TypeCompanyErasureResources, TypeCompanyErasureVerifyAbsent:
			handleCompanyErasure(sys, repository, msg)
		case TypeSourceAdd, TypeSourceUpdate, TypeSourceValidate, TypeSourceValidationPublish, TypeSourceValidationReject, TypeSourcePause, TypeSourceResume, TypeSourceArchive, TypeSourceRestore:
			handleSourceMessage(sys, cfg, repository, msg)
		case TypeSourceReassignPreview, TypeSourceReassignConfirm:
			handleSourceReassignment(sys, repository, msg)
		case TypeSourceProfileBind, TypeSourceProfileUnbind:
			handleSourceProfileBinding(sys, repository, msg)
		case TypeRecipePrepare, TypeRecipePropose, TypeRecipeValidate, TypeRecipeApprove, TypeRecipeReject, TypeRecipeAssign, TypeRecipeRollout,
			TypeRecipeRolloutBatch, TypeRecipeRolloutBatchConfirm, TypeRecipeRolloutBatchResume,
			TypeRecipeRolloutBatchRollback, TypeRecipeRolloutBatchCancel,
			TypeRecipeQuarantine, TypeRecipeRollback:
			handleRecipeMessage(sys, cfg, repository, msg)
		case TypeRepairValidate, TypeRepairResolve, TypeRepairRecover:
			handleRepairMessage(sys, cfg, repository, msg)
		case TypeSourceDiscover, TypeSourceDiscoveryCandidateAccept, TypeSourceDiscoveryCandidateReject:
			handleSourceDiscoveryMessage(sys, cfg, repository, msg)
		case TypeDeepDiscoveryStart, TypeDeepDiscoveryCheckpoint, TypeDeepDiscoveryWait,
			TypeDeepDiscoveryResume, TypeDeepDiscoveryComplete, TypeDeepDiscoveryCancel, TypeDeepDiscoveryBrowserObserve,
			TypeDeepDiscoveryPublicQueryVerify:
			handleDeepDiscoveryMessage(sys, cfg, repository, msg)
		case TypeOnboardingBegin, TypeOnboardingStatus, TypeOnboardingAdvance, TypeOnboardingRepairValidate, TypeOnboardingMaterialize:
			handleOnboardingMessage(sys, cfg, repository, msg)
		case TypeBaselineStart:
			handleBaselineStart(sys, cfg, repository, msg)
		case TypeJobCorrect:
			handleJobCorrect(sys, repository, msg)
		case TypeBackfillCreate, TypeBackfillGet, TypeBackfillItems, TypeBackfillOutputs, TypeBackfillOutputGet,
			TypeBackfillGaps, TypeBackfillConfirm, TypeBackfillPause, TypeBackfillResume, TypeBackfillCancel,
			TypeBackfillItemResolve:
			handleBackfillMessage(sys, cfg, repository, msg)
		case TypeProfileRegister:
			handleProfileRegister(sys, repository, msg)
		case TypeProfileRepairBegin:
			handleProfileRepairBegin(sys, cfg, repository, msg)
		case TypeWorkCreate, TypeWorkPause, TypeWorkResume, TypeWorkCorrect, TypeWorkRetry, TypeWorkCancel, TypeWorkResolve:
			handleWorkMessage(sys, cfg, repository, msg)
		case TypeRunJoinOccurrence:
			handleRunJoinOccurrence(sys, cfg, repository, msg)
		case TypeRunDiagnostic:
			handleStandaloneListingRun(sys, cfg, repository, msg, RunDiagnostic)
		case TypeRunProduction:
			handleStandaloneListingRun(sys, cfg, repository, msg, RunProduction)
		case TypeDailyRunOccurrenceExclude:
			handleDailyRunOccurrenceExclude(sys, repository, msg)
		case TypeExecutionOffer, TypeExecutionOfferBatch, TypeExecutionClaimBatch,
			TypeExecutionAccept, TypeExecutionStarted, TypeExecutionFailed, TypeExecutionWakeCompleted:
			handleExecutionControlMessage(sys, cfg, repository, state, msg)
		case TypeSourceGet, TypeSourceList, TypeSourceEndpointHistory, TypeSourceDiscoveryGet, TypeSourceDiscoveryCandidates,
			TypeDeepDiscoveryGet, TypeDeepDiscoveryGraph, TypeDeepDiscoveryGuide, TypeDeepDiscoveryBrowserGet,
			TypeDeepDiscoveryPublicQueryGet, TypePublicQueryInspect,
			TypeJobGet, TypeJobList, TypeJobCorrectionGet, TypeProfileGet, TypeWorkGet, TypeWorkList,
			TypeDailyRunGet, TypeDailyRunList, TypeDailyRunSummary, TypeRepairGet, TypeRepairList, TypeRecipeInspect,
			TypeRecipeRolloutBatchGet, TypeRecipeRolloutBatchItems,
			TypeSystemStatus, TypeScopeControlGet, TypeCapacityStatus, TypeConsoleSnapshot:
			handleResourceQuery(sys, cfg, repository, msg)
		case TypeSystemReconcile:
			handleOutboxReconcile(sys, cfg, repository, msg)
		case TypeProbeStart:
			handleStart(sys, cfg, state, msg)
		case TypeProbeSchedule:
			handleSchedule(sys, state, msg)
		case TypeProbeStatus:
			handleStatus(sys, state, msg)
		case TypeExecutionResult:
			handleAnyExecutionResult(sys, cfg, repository, state, msg)
		case TypeExecutionResultBatch:
			handleExecutionResultBatch(sys, repository, msg)
		default:
			_, _ = sys.Fail(msg, "type_unsupported", fmt.Sprintf("recruiting actor does not answer %q", msg.Type))
		}
	}
}

// rearmReconcileTimerOnStartup replaces the periodic reconciliation chain
// owned by the previous Actor body. A Server crash can happen after its
// one-shot fire is dequeued but before the handler persists a successor ID;
// retaining that old ID would leave durable database work without another
// wake. A late fire is harmless because the handler accepts only the currently
// persisted ID. Daily cutoff/work/close timers are not replaced here: unlike a
// periodic sweep, their exact persisted deadline and payload must survive a
// Server outage that crosses the business cutoff.
func rearmReconcileTimerOnStartup(sys actorbase.Sys, cfg Config, state *storedState) error {
	previous := state.ReconcileTimerID
	if err := armReconcileTimer(sys, cfg, state); err != nil {
		return err
	}
	cancelSupersededTimer(sys, previous, state.ReconcileTimerID)
	return nil
}

func cancelSupersededTimer(sys actorbase.Sys, previous, current string) {
	if previous != "" && previous != current {
		_ = sys.CancelTimer(schedule.TimerID(previous))
	}
}

func loadState(sys actorbase.Sys) (*storedState, error) {
	state := &storedState{}
	out, err := sys.State().Get(stateKey)
	if err != nil {
		return nil, fmt.Errorf("recruiting: load state: %w", err)
	}
	if out.Found {
		if err := json.Unmarshal(out.Value, state); err != nil {
			return nil, fmt.Errorf("recruiting: decode state: %w", err)
		}
	}
	if state.Works == nil {
		state.Works = map[string]model.ProbeWork{}
	}
	if state.CommandWorks == nil {
		state.CommandWorks = map[string]string{}
	}
	if state.Receipts == nil {
		state.Receipts = map[string]json.RawMessage{}
	}
	if state.ExecutorPresence == nil {
		state.ExecutorPresence = map[string]executorPresenceObservation{}
	}
	return state, nil
}

func bootstrapExecutorPresence(sys actorbase.Sys, state *storedState, repository *store.Repository) error {
	ids, _, err := repository.ListActiveExecutorActors(sys.Life(), 10_000)
	if err != nil {
		return err
	}
	changed := false
	for _, id := range ids {
		if _, tracked := state.ExecutorPresence[id]; tracked {
			continue
		}
		state.ExecutorPresence[id] = executorPresenceObservation{}
		changed = true
	}
	if changed {
		return persist(sys, state)
	}
	return nil
}

func persist(sys actorbase.Sys, state *storedState) error {
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	out, err := sys.State().Put(stateKey, raw)
	if err != nil {
		return err
	}
	if !out.Accepted() {
		return fmt.Errorf("state rejected: %s", out.RejectReason)
	}
	return nil
}

func handleStart(sys actorbase.Sys, cfg Config, state *storedState, msg actorbase.Msg) {
	var p probeStartPayload
	if !decode(sys, msg, &p) {
		return
	}
	p.CommandID = strings.TrimSpace(p.CommandID)
	if p.CommandID == "" {
		_, _ = sys.Fail(msg, "payload_invalid", "command_id is required")
		return
	}
	if receipt, ok := state.Receipts[p.CommandID]; ok {
		_, _ = sys.Reply(msg, receipt)
		return
	}
	work, err := ensureProbeWork(state, p.CommandID, string(cfg.ExecutorID), p.Note)
	if err != nil {
		_, _ = sys.Fail(msg, "state_error", err.Error())
		return
	}
	if work.Status == model.ProbeOpen {
		work, err = work.MarkOffered()
		if err != nil {
			_, _ = sys.Fail(msg, "state_error", err.Error())
			return
		}
		state.Works[work.WorkID] = work
		if err := persist(sys, state); err != nil {
			_, _ = sys.Fail(msg, "state_error", err.Error())
			return
		}
	}
	if err := dispatch(sys, msg.Cause(), work); err != nil {
		_, _ = sys.Fail(msg, ErrorExecutionUnavailable, "executor dispatch failed: "+err.Error())
		return
	}
	response := responseFor(work)
	raw, _ := json.Marshal(response)
	state.Receipts[p.CommandID] = raw
	if err := persist(sys, state); err != nil {
		_, _ = sys.Fail(msg, "state_error", err.Error())
		return
	}
	_, _ = sys.Reply(msg, response)
}

func handleSchedule(sys actorbase.Sys, state *storedState, msg actorbase.Msg) {
	var p probeSchedulePayload
	if !decode(sys, msg, &p) {
		return
	}
	p.CommandID = strings.TrimSpace(p.CommandID)
	if p.CommandID == "" || p.DelayMS < 1 || p.DelayMS > 60_000 {
		_, _ = sys.Fail(msg, "payload_invalid", "command_id is required and delay_ms must be in [1,60000]")
		return
	}
	receiptKey := "schedule:" + p.CommandID
	if receipt, ok := state.Receipts[receiptKey]; ok {
		_, _ = sys.Reply(msg, receipt)
		return
	}
	tid, err := sys.After(time.Duration(p.DelayMS)*time.Millisecond, typeProbeDue,
		probeDuePayload{CommandID: "timer:" + p.CommandID, Note: p.Note}, schedule.TimerHomeDurable)
	if err != nil {
		_, _ = sys.Fail(msg, "schedule_failed", err.Error())
		return
	}
	response := scheduleResponse{CommandID: p.CommandID, TimerID: string(tid), ScheduleState: "scheduled"}
	raw, _ := json.Marshal(response)
	state.Receipts[receiptKey] = raw
	if err := persist(sys, state); err != nil {
		_ = sys.CancelTimer(tid)
		_, _ = sys.Fail(msg, "state_error", err.Error())
		return
	}
	_, _ = sys.Reply(msg, response)
}

func handleDue(sys actorbase.Sys, cfg Config, state *storedState, msg actorbase.Msg) {
	var p probeDuePayload
	if err := json.Unmarshal(msg.Payload, &p); err != nil || p.CommandID == "" {
		return
	}
	work, err := ensureProbeWork(state, p.CommandID, string(cfg.ExecutorID), p.Note)
	if err != nil {
		return
	}
	if work.Status == model.ProbeCompleted || work.Status == model.ProbeFailed {
		return
	}
	if work.Status == model.ProbeOpen {
		work, err = work.MarkOffered()
		if err != nil {
			return
		}
		state.Works[work.WorkID] = work
		if err := persist(sys, state); err != nil {
			return
		}
	}
	_ = dispatch(sys, msg.Cause(), work)
}

func handleStatus(sys actorbase.Sys, state *storedState, msg actorbase.Msg) {
	var p probeStatusPayload
	if !decode(sys, msg, &p) {
		return
	}
	if p.CommandID != "" && p.WorkID == "" {
		p.WorkID = state.CommandWorks[p.CommandID]
	}
	if p.WorkID != "" {
		work, ok := state.Works[p.WorkID]
		if !ok {
			_, _ = sys.Fail(msg, "not_found", "probe work not found")
			return
		}
		_, _ = sys.Reply(msg, responseFor(work))
		return
	}
	ids := make([]string, 0, len(state.Works))
	for id := range state.Works {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	works := make([]probeResponse, 0, len(ids))
	for _, id := range ids {
		works = append(works, responseFor(state.Works[id]))
	}
	_, _ = sys.Reply(msg, map[string]any{"works": works})
}

func handleProbeExecutionResult(sys actorbase.Sys, state *storedState, msg actorbase.Msg) {
	var p executionResultPayload
	if !decode(sys, msg, &p) {
		return
	}
	work, ok := state.Works[p.WorkID]
	if !ok {
		_, _ = sys.Fail(msg, "not_found", "probe work not found")
		return
	}
	if msg.Sender.ID != actor.ActorID(work.ExecutorID) {
		_, _ = sys.Fail(msg, ErrorUnauthorizedExecutor, "result sender does not own this attempt")
		return
	}
	updated, err := work.AcceptResult(p.AttemptID, p.ExpectedWorkVersion, p.Result)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorExecutionRejected, "execution result rejected: "+err.Error())
		return
	}
	state.Works[p.WorkID] = updated
	if err := persist(sys, state); err != nil {
		_, _ = sys.Fail(msg, "state_error", err.Error())
		return
	}
	response := responseFor(updated)
	_, _ = sys.Reply(msg, response)
	raw, _ := json.Marshal(response)
	_, _ = sys.Emit(behavior.EventSpec{Type: "recruiting.probe.completed", Payload: raw, Cause: msg.Cause()})
}

func ensureProbeWork(state *storedState, commandID, executorID, note string) (model.ProbeWork, error) {
	if workID, ok := state.CommandWorks[commandID]; ok {
		return state.Works[workID], nil
	}
	digest := stableDigest(commandID)
	work, err := model.NewProbeWork("probe-work-"+digest, "probe-attempt-"+digest, commandID, executorID, note)
	if err != nil {
		return model.ProbeWork{}, err
	}
	state.Works[work.WorkID] = work
	state.CommandWorks[commandID] = work.WorkID
	return work, nil
}

func dispatch(sys actorbase.Sys, cause message.Cause, work model.ProbeWork) error {
	raw, err := json.Marshal(executionOfferPayload{
		WorkID: work.WorkID, AttemptID: work.AttemptID, ExpectedWorkVersion: work.Version,
		ReplyTo: sys.Self(), Note: work.Note,
	})
	if err != nil {
		return err
	}
	_, err = sys.Post(behavior.RequestSpec{
		ID: message.ID("probe-offer-" + stableDigest(work.AttemptID)), Type: "recruiting.execution.probe",
		Payload: raw, Audience: message.Audience{actor.ActorID(work.ExecutorID)}, Cause: cause,
	})
	return err
}

func responseFor(work model.ProbeWork) probeResponse {
	return probeResponse{
		WorkID: work.WorkID, AttemptID: work.AttemptID, CommandID: work.CommandID,
		ExecutorID: work.ExecutorID, WorkStatus: work.Status, Version: work.Version, Result: work.Result,
	}
}

func stableDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:8])
}

func decode(sys actorbase.Sys, msg actorbase.Msg, dst any) bool {
	dec := json.NewDecoder(strings.NewReader(string(msg.Payload)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		_, _ = sys.Fail(msg, "payload_invalid", err.Error())
		return false
	}
	var trailing any
	if err := dec.Decode(&trailing); !errors.Is(err, io.EOF) {
		// Accepting a second JSON value would make command hashing and
		// validation disagree.
		if err == nil {
			_, _ = sys.Fail(msg, "payload_invalid", "multiple JSON values")
		} else {
			_, _ = sys.Fail(msg, "payload_invalid", err.Error())
		}
		return false
	}
	return true
}
