package recruiting

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
)

type workCreatePayload struct {
	CommandID  string `json:"command_id"`
	WorkID     string `json:"work_id"`
	Target     Target `json:"target"`
	Purpose    string `json:"purpose"`
	Capability string `json:"capability"`
	Origin     string `json:"origin,omitempty"`
	ProfileID  string `json:"profile_id,omitempty"`
	Priority   int    `json:"priority,omitempty"`
	NotBefore  string `json:"not_before,omitempty"`
	DeadlineAt string `json:"deadline_at,omitempty"`
	Reason     string `json:"reason"`
}

type workResolvePayload struct {
	MutationCommand
	Resolution model.WorkResolution `json:"resolution"`
}

type workRetryPayload struct {
	MutationCommand
	NewWorkID  string `json:"new_work_id"`
	NotBefore  string `json:"not_before,omitempty"`
	DeadlineAt string `json:"deadline_at,omitempty"`
}

type workCommandResponse struct {
	ContractVersion string            `json:"contract_version"`
	CorrelationID   string            `json:"correlation_id"`
	RequestedBy     string            `json:"requested_by"`
	Work            model.Work        `json:"work"`
	Target          Target            `json:"target"`
	NextAction      string            `json:"next_action"`
	RunMode         RunMode           `json:"run_mode,omitempty"`
	OccurrenceID    string            `json:"occurrence_id,omitempty"`
	ListingRun      *model.ListingRun `json:"listing_run,omitempty"`
}

type listingRunPayload struct {
	MutationCommand
	RunID      string `json:"run_id"`
	WorkID     string `json:"work_id"`
	ProfileID  string `json:"profile_id,omitempty"`
	Priority   int    `json:"priority,omitempty"`
	DeadlineAt string `json:"deadline_at,omitempty"`
}

func handleWorkMessage(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	switch msg.Type {
	case TypeWorkCreate:
		handleWorkCreate(sys, cfg, repository, msg)
	case TypeWorkRetry:
		handleWorkRetry(sys, cfg, repository, msg)
	default:
		handleWorkMutation(sys, repository, msg)
	}
}

func handleRunJoinOccurrence(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	var command MutationCommand
	if !decode(sys, msg, &command) {
		return
	}
	context, err := NewCommandContext(command, string(msg.Sender.ID))
	if err != nil || command.Target.Type != "source_occurrence" {
		if err == nil {
			err = fmt.Errorf("target_type must be source_occurrence")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	if replay, found, lookupErr := repository.LookupCommand(msg.Ctx(), command.CommandID, commandRequestHash(msg)); lookupErr != nil {
		failStoreError(sys, msg, lookupErr)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	occurrence, err := repository.GetOccurrence(msg.Ctx(), command.Target.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if occurrence.Version != command.ExpectedVersion {
		failStoreError(sys, msg, &model.VersionConflictError{Expected: command.ExpectedVersion, Actual: occurrence.Version})
		return
	}
	run, err := repository.GetDailyRun(msg.Ctx(), occurrence.DailyRunID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	windowEnd, err := time.Parse(time.RFC3339, run.WindowEndAt)
	if err != nil {
		failStoreError(sys, msg, fmt.Errorf("daily run has invalid window end: %w", err))
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	work, err := model.NewWork("work-listing-"+occurrence.OccurrenceID, "source", occurrence.SourceID, "listing_sync", "manual")
	if err == nil {
		work, err = work.WithCausality(context.RequestedBy, string(msg.ID), "")
	}
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	placement := store.WorkPlacement{
		BusinessKey: "daily-listing|" + occurrence.OccurrenceID,
		Priority:    200,
		Capability:  occurrence.ListingExecution.Execution.RequiredCapability,
		Origin:      occurrence.ListingExecution.Origin,
		NotBefore:   businessAt,
		DeadlineAt:  &windowEnd,
	}
	response := makeWorkResponse(msg, work)
	response.RunMode, response.OccurrenceID = RunJoinOccurrence, occurrence.OccurrenceID
	dispatch, err := workCommandDispatch(cfg, work, placement, command.CommandID, "run_join_occurrence")
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	receipt, event, _, err := workCommandFacts(msg, command.CommandID, command.Reason, response, "work.created")
	var result store.CommandResult
	if err == nil {
		result, err = repository.ApplyJoinOccurrenceCommand(msg.Ctx(), command.ExpectedVersion, occurrence.OccurrenceID,
			work, placement, receipt, event, dispatch, businessAt)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleStandaloneListingRun(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg, mode RunMode) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	var payload listingRunPayload
	if !decode(sys, msg, &payload) {
		return
	}
	context, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	if err != nil || payload.Target.Type != "source" || strings.TrimSpace(payload.RunID) == "" || strings.TrimSpace(payload.WorkID) == "" {
		if err == nil {
			err = fmt.Errorf("source target, run_id, and work_id are required")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	if replay, found, lookupErr := repository.LookupCommand(msg.Ctx(), payload.CommandID, commandRequestHash(msg)); lookupErr != nil {
		failStoreError(sys, msg, lookupErr)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	preparation, err := repository.PrepareListingRun(msg.Ctx(), payload.Target.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if mode == RunProduction && (preparation.Checkpoint == nil || !preparation.Source.EligibleForDailyRun(preparation.Company)) {
		_, _ = sys.Fail(msg, ErrorExecutionRejected, "production run requires a daily-eligible source with an established checkpoint")
		return
	}
	if mode != RunDiagnostic && mode != RunProduction {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "standalone listing run mode must be diagnostic or production")
		return
	}
	runMode := model.ListingRunDiagnostic
	if mode == RunProduction {
		runMode = model.ListingRunProduction
	}
	run, err := preparation.NewRun(strings.TrimSpace(payload.RunID), strings.TrimSpace(payload.WorkID), runMode)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	placement, err := standaloneListingPlacement(run, payload.ProfileID, payload.Priority, payload.DeadlineAt, businessAt)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	placement.BusinessKey = "manual-listing|" + run.ListingRunID
	work, err := model.NewWork(run.WorkID, "source", run.SourceID, "listing_sync", "manual")
	if err == nil {
		work, err = work.WithCausality(context.RequestedBy, string(msg.ID), "")
	}
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	response := makeWorkResponse(msg, work)
	response.RunMode, response.ListingRun = mode, &run
	dispatch, err := workCommandDispatch(cfg, work, placement, payload.CommandID, "run_"+string(mode))
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	receipt, event, _, err := workCommandFacts(msg, payload.CommandID, payload.Reason, response, "work.created")
	var result store.CommandResult
	if err == nil {
		result, err = repository.ApplyListingRunCommand(msg.Ctx(), payload.ExpectedVersion, run, work, placement, receipt, event, dispatch, businessAt)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func standaloneListingPlacement(run model.ListingRun, profileID string, priority int, deadline string,
	businessAt time.Time) (store.WorkPlacement, error) {
	profileID = strings.TrimSpace(profileID)
	if priority < -1000 || priority > 1000 || len(profileID) > 191 || strings.ContainsAny(profileID, "\r\n\t ") {
		return store.WorkPlacement{}, fmt.Errorf("priority must be in [-1000,1000] and profile_id must be normalized")
	}
	notBefore, deadlineAt, err := retrySchedule("", deadline, businessAt)
	if err != nil {
		return store.WorkPlacement{}, err
	}
	return store.WorkPlacement{Priority: priority, Capability: run.ListingExecution.Execution.RequiredCapability,
		Origin: run.ListingExecution.Origin, ProfileID: profileID, NotBefore: notBefore, DeadlineAt: deadlineAt}, nil
}

func handleWorkCreate(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	var payload workCreatePayload
	if !decode(sys, msg, &payload) {
		return
	}
	if strings.TrimSpace(payload.CommandID) == "" || strings.TrimSpace(payload.WorkID) == "" ||
		strings.TrimSpace(payload.Reason) == "" || strings.TrimSpace(string(msg.Sender.ID)) == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "command_id, work_id, reason, and authenticated sender are required")
		return
	}
	if replay, found, lookupErr := repository.LookupCommand(msg.Ctx(), payload.CommandID, commandRequestHash(msg)); lookupErr != nil {
		failStoreError(sys, msg, lookupErr)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	if err := validateManualWorkPurpose(payload.Target, payload.Purpose); err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	if err := ensureManualWorkTarget(msg, repository, payload.Target); err != nil {
		failStoreError(sys, msg, err)
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	placement, err := manualWorkPlacement(payload.Capability, payload.Origin, payload.ProfileID, payload.Priority, payload.NotBefore, payload.DeadlineAt, businessAt)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	work, err := model.NewWork(payload.WorkID, payload.Target.Type, payload.Target.ID, strings.TrimSpace(payload.Purpose), "manual")
	if err == nil {
		work, err = work.WithCausality(string(msg.Sender.ID), string(msg.ID), "")
	}
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	placement.BusinessKey = "manual|" + work.WorkID
	response := makeWorkResponse(msg, work)
	dispatch, err := workCommandDispatch(cfg, work, placement, payload.CommandID, "work_created")
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	result, err := applyWorkCreateFacts(repository, msg, payload.CommandID, payload.Reason, response, work, placement, dispatch)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleWorkMutation(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var command MutationCommand
	var resolve workResolvePayload
	if msg.Type == TypeWorkResolve {
		if !decode(sys, msg, &resolve) {
			return
		}
		command = resolve.MutationCommand
	} else if !decode(sys, msg, &command) {
		return
	}
	context, err := NewCommandContext(command, string(msg.Sender.ID))
	if err != nil || command.Target.Type != "work" {
		if err == nil {
			err = fmt.Errorf("target_type must be work")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	if replay, found, lookupErr := repository.LookupCommand(msg.Ctx(), command.CommandID, commandRequestHash(msg)); lookupErr != nil {
		failStoreError(sys, msg, lookupErr)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	current, err := repository.GetWork(msg.Ctx(), command.Target.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	var next model.Work
	switch msg.Type {
	case TypeWorkPause:
		next, err = current.Pause(command.ExpectedVersion)
	case TypeWorkResume:
		next, err = current.Resume(command.ExpectedVersion)
	case TypeWorkCancel:
		next, err = current.Cancel(command.ExpectedVersion)
	case TypeWorkResolve:
		next, err = resolveWorkHuman(current, command.ExpectedVersion, resolve.Resolution, context.RequestedBy, command.Reason)
	default:
		err = fmt.Errorf("unsupported work mutation")
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := makeWorkResponse(msg, next)
	result, err := applyWorkMutationFacts(repository, msg, command.CommandID, command.Reason, response, next, command.ExpectedVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleWorkRetry(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	var payload workRetryPayload
	if !decode(sys, msg, &payload) {
		return
	}
	context, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	if err != nil || payload.Target.Type != "work" || strings.TrimSpace(payload.NewWorkID) == "" {
		if err == nil {
			err = fmt.Errorf("target_type work and new_work_id are required")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	if replay, found, lookupErr := repository.LookupCommand(msg.Ctx(), payload.CommandID, commandRequestHash(msg)); lookupErr != nil {
		failStoreError(sys, msg, lookupErr)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	record, err := repository.GetWorkRecord(msg.Ctx(), payload.Target.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if record.Work.Purpose == "source_discovery" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "source discovery retries require an explicit new discovery generation")
		return
	}
	if payload.ExpectedVersion != record.Work.Version {
		failStoreError(sys, msg, &model.VersionConflictError{Expected: payload.ExpectedVersion, Actual: record.Work.Version})
		return
	}
	retry, err := model.NewRetryWork(record.Work, payload.NewWorkID, context.RequestedBy, string(msg.ID))
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	placement := record.Placement
	placement.BusinessKey = "retry|" + record.Work.WorkID + "|" + retry.WorkID
	placement.NotBefore, placement.DeadlineAt, err = retrySchedule(payload.NotBefore, payload.DeadlineAt, businessAt)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	response := makeWorkResponse(msg, retry)
	dispatch, err := workCommandDispatch(cfg, retry, placement, payload.CommandID, "work_retry_created")
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	result, err := applyWorkRetryFacts(repository, msg, payload.CommandID, payload.Reason, response, record.Work, retry, placement, dispatch)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func validateManualWorkPurpose(target Target, purpose string) error {
	if err := target.Validate(); err != nil {
		return err
	}
	purpose = strings.TrimSpace(purpose)
	if purpose == "listing_sync" || purpose == "detail_sync" {
		return fmt.Errorf("purpose %q requires a dedicated run command with explicit execution semantics", purpose)
	}
	allowed := map[string]map[string]bool{
		"company_discovery": {"company": true},
		"baseline":          {"source": true},
		"reconcile":         {"company": true, "source": true},
		"repair":            {"company": true, "source": true, "job": true, "profile": true, "origin": true, "recipe": true},
		"data_maintenance":  {"company": true, "source": true, "job": true},
	}
	targets, exists := allowed[purpose]
	if !exists || !targets[target.Type] {
		return fmt.Errorf("purpose %q is not valid for target type %q", purpose, target.Type)
	}
	return nil
}

func ensureManualWorkTarget(msg actorbase.Msg, repository *store.Repository, target Target) error {
	switch target.Type {
	case "company":
		company, err := repository.GetCompany(msg.Ctx(), target.ID)
		if err != nil {
			return err
		}
		if company.ControlStatus == model.ControlArchived {
			return &model.InvalidTransitionError{Entity: "company", From: string(company.ControlStatus), Action: "create work"}
		}
	case "source":
		source, err := repository.GetSource(msg.Ctx(), target.ID)
		if err != nil {
			return err
		}
		if source.ControlStatus == model.ControlArchived {
			return &model.InvalidTransitionError{Entity: "source", From: string(source.ControlStatus), Action: "create work"}
		}
	case "job":
		if _, err := repository.GetJob(msg.Ctx(), target.ID); err != nil {
			return err
		}
	case "profile":
		if _, err := repository.GetProfile(msg.Ctx(), target.ID); err != nil {
			return err
		}
	case "origin", "recipe":
		// Origins and versioned Recipe selectors are validated by their
		// dedicated repair handler before an executable Attempt is offered.
	}
	return nil
}

func resolveWorkHuman(current model.Work, expected uint64, resolution model.WorkResolution, actorID, reason string) (model.Work, error) {
	if resolution == model.ResolutionSucceeded {
		return model.Work{}, &model.InvalidTransitionError{Entity: "work", From: string(current.Status), Action: "assert executor success from human resolution"}
	}
	return current.Complete(expected, resolution, actorID, reason)
}

func manualWorkPlacement(capability, origin, profileID string, priority int, notBefore, deadline string, businessAt time.Time) (store.WorkPlacement, error) {
	capability, origin, profileID = strings.TrimSpace(capability), strings.TrimSpace(origin), strings.TrimSpace(profileID)
	if capability == "" || len(capability) > 128 || strings.ContainsAny(capability, "\r\n\t ") || priority < -1000 || priority > 1000 {
		return store.WorkPlacement{}, fmt.Errorf("capability and priority in [-1000,1000] are required")
	}
	if origin != "" {
		parsed, err := url.Parse("https://" + origin)
		if err != nil || parsed.Hostname() != origin || parsed.Port() != "" || parsed.User != nil || parsed.Path != "" {
			return store.WorkPlacement{}, fmt.Errorf("origin must be a hostname without credentials, port, or path")
		}
	}
	notBeforeAt, deadlineAt, err := retrySchedule(notBefore, deadline, businessAt)
	if err != nil {
		return store.WorkPlacement{}, err
	}
	return store.WorkPlacement{Priority: priority, Capability: capability, Origin: origin, ProfileID: profileID, NotBefore: notBeforeAt, DeadlineAt: deadlineAt}, nil
}

func retrySchedule(notBefore, deadline string, businessAt time.Time) (time.Time, *time.Time, error) {
	notBeforeAt := businessAt
	var err error
	if strings.TrimSpace(notBefore) != "" {
		notBeforeAt, err = time.Parse(time.RFC3339, notBefore)
		if err != nil {
			return time.Time{}, nil, fmt.Errorf("not_before must be RFC3339")
		}
	}
	var deadlineAt *time.Time
	if strings.TrimSpace(deadline) != "" {
		value, parseErr := time.Parse(time.RFC3339, deadline)
		if parseErr != nil || !value.After(notBeforeAt) {
			return time.Time{}, nil, fmt.Errorf("deadline_at must be RFC3339 and later than not_before")
		}
		value = value.UTC()
		deadlineAt = &value
	}
	return notBeforeAt.UTC(), deadlineAt, nil
}

func applyWorkCreateFacts(repository *store.Repository, msg actorbase.Msg, commandID, reason string, response workCommandResponse,
	work model.Work, placement store.WorkPlacement, dispatch *store.ExecutionDispatchIntent) (store.CommandResult, error) {
	receipt, event, businessAt, err := workCommandFacts(msg, commandID, reason, response, "work.created")
	if err != nil {
		return store.CommandResult{}, err
	}
	return repository.ApplyCreateWorkCommandWithDispatch(msg.Ctx(), work, placement, receipt, event, dispatch, businessAt)
}

func applyWorkMutationFacts(repository *store.Repository, msg actorbase.Msg, commandID, reason string, response workCommandResponse, work model.Work, expected uint64) (store.CommandResult, error) {
	receipt, event, businessAt, err := workCommandFacts(msg, commandID, reason, response, workEventKind(msg.Type))
	if err != nil {
		return store.CommandResult{}, err
	}
	return repository.ApplyWorkCommand(msg.Ctx(), expected, work, receipt, event, businessAt)
}

func applyWorkRetryFacts(repository *store.Repository, msg actorbase.Msg, commandID, reason string, response workCommandResponse,
	previous, retry model.Work, placement store.WorkPlacement, dispatch *store.ExecutionDispatchIntent) (store.CommandResult, error) {
	receipt, event, businessAt, err := workCommandFacts(msg, commandID, reason, response, "work.retry_created")
	if err != nil {
		return store.CommandResult{}, err
	}
	return repository.ApplyRetryWorkCommandWithDispatch(msg.Ctx(), previous.Version, previous.WorkID, retry, placement, receipt, event, dispatch, businessAt)
}

func workCommandDispatch(cfg Config, work model.Work, placement store.WorkPlacement, commandID, causeKind string) (*store.ExecutionDispatchIntent, error) {
	if work.Purpose != "listing_sync" && work.Purpose != "source_validation" && work.Purpose != "detail_sync" && work.Purpose != "company_import" &&
		work.Purpose != "company_import_apply" && work.Purpose != "source_discovery" {
		return nil, nil
	}
	target, found := cfg.executionDispatchTarget(placement.Capability, commandID+"\n"+work.WorkID)
	if !found {
		return nil, nil
	}
	dispatch, err := store.NewExecutionDispatchIntent("dispatch-work-"+stableDigest(commandID+"|"+target.ActorID), target.ActorID,
		placement.Capability, placement.Origin, placement.ProfileID, causeKind, commandID, placement.NotBefore)
	if err != nil {
		return nil, err
	}
	return &dispatch, nil
}

func workCommandFacts(msg actorbase.Msg, commandID, reason string, response workCommandResponse, eventKind string) (model.CommandReceipt, model.EventIntent, time.Time, error) {
	businessAt := time.UnixMilli(msg.TS).UTC()
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(commandID, msg.Type, commandRequestHash(msg), responseBytes)
	if err != nil {
		return model.CommandReceipt{}, model.EventIntent{}, time.Time{}, err
	}
	auditPayload, _ := json.Marshal(map[string]any{"requested_by": string(msg.Sender.ID), "reason": reason})
	event, err := model.NewEventIntent("event-"+stableDigest(commandID+"|"+eventKind), eventKind, "work", response.Work.WorkID,
		response.Work.Version, businessAt.Format(time.RFC3339Nano), commandID, auditPayload)
	return receipt, event, businessAt, err
}

func makeWorkResponse(msg actorbase.Msg, work model.Work) workCommandResponse {
	next := "inspect_work"
	switch work.Status {
	case model.WorkOpen, model.WorkWaitingRetry:
		next = "await_execution"
	case model.WorkRunning:
		next = "monitor_work"
	case model.WorkWaitingHuman:
		next = "resolve_or_retry_work"
	case model.WorkPaused:
		next = "resume_or_cancel_work"
	case model.WorkFailed, model.WorkCanceled:
		next = "retry_or_inspect_work"
	case model.WorkCompleted:
		next = "inspect_work"
	}
	return workCommandResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID), RequestedBy: string(msg.Sender.ID),
		Work: work, Target: Target{Type: "work", ID: work.WorkID}, NextAction: next}
}

func workEventKind(word string) string {
	switch word {
	case TypeWorkPause:
		return "work.paused"
	case TypeWorkResume:
		return "work.resumed"
	case TypeWorkCancel:
		return "work.canceled"
	case TypeWorkResolve:
		return "work.completed"
	default:
		return "work.changed"
	}
}
