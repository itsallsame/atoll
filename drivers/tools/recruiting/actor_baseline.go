package recruiting

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
)

type baselineStartPayload struct {
	MutationCommand
	ExpectedCompanyVersion uint64 `json:"expected_company_version"`
	WorkID                 string `json:"work_id"`
	Generation             uint64 `json:"baseline_generation"`
	ProfileID              string `json:"profile_id,omitempty"`
	Priority               int    `json:"priority,omitempty"`
	DeadlineAt             string `json:"deadline_at,omitempty"`
}

type baselineCommandResponse struct {
	ContractVersion string                   `json:"contract_version"`
	CorrelationID   string                   `json:"correlation_id"`
	RequestedBy     string                   `json:"requested_by"`
	Company         model.Company            `json:"company"`
	Baseline        model.BaselineGeneration `json:"baseline"`
	Work            model.Work               `json:"work"`
	Target          Target                   `json:"target"`
	NextAction      string                   `json:"next_action"`
}

func handleBaselineStart(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	var payload baselineStartPayload
	if !decode(sys, msg, &payload) {
		return
	}
	commandContext, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	if err != nil || payload.Target.Type != "source" || payload.ExpectedCompanyVersion == 0 ||
		strings.TrimSpace(payload.WorkID) == "" || payload.Generation == 0 {
		if err == nil {
			err = fmt.Errorf("source target, Company version, Work ID, and baseline generation are required")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	requestHash := commandRequestHash(msg)
	if replay, found, lookupErr := repository.LookupCommand(msg.Ctx(), payload.CommandID, requestHash); lookupErr != nil {
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
	if preparation.Company.Version != payload.ExpectedCompanyVersion || preparation.Source.Version != payload.ExpectedVersion {
		_, _ = sys.Fail(msg, ErrorVersionConflict, "Company or Source version changed")
		return
	}
	nextCompany := preparation.Company
	if nextCompany.OnboardingStatus == model.CompanyDiscoveringSources {
		nextCompany, err = nextCompany.StartInitialization(payload.ExpectedCompanyVersion)
	} else if nextCompany.OnboardingStatus != model.CompanyInitializing &&
		nextCompany.OnboardingStatus != model.CompanyReady {
		err = &model.InvalidTransitionError{Entity: "company", From: string(nextCompany.OnboardingStatus), Action: "start baseline"}
	}
	work, workErr := model.NewWork(strings.TrimSpace(payload.WorkID), "source", preparation.Source.SourceID, "baseline_listing", "human")
	if err == nil {
		err = workErr
	}
	if err == nil {
		work, err = work.WithCausality(commandContext.RequestedBy, string(msg.ID), "")
	}
	var baseline model.BaselineGeneration
	if err == nil {
		baseline, err = model.NewExecutableBaselineGeneration(work.WorkID, nextCompany, preparation.Source,
			payload.Generation, preparation.Recipe)
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	placement, placementErr := sourceDiscoveryPlacement(baseline.ListingExecution.Endpoint.URL,
		baseline.ListingExecution.Execution.RequiredCapability, payload.ProfileID, payload.Priority, payload.DeadlineAt, businessAt)
	if err == nil {
		err = placementErr
	}
	placement.BusinessKey = fmt.Sprintf("baseline|%s|%d", preparation.Source.SourceID, payload.Generation)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	response := baselineCommandResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: commandContext.RequestedBy, Company: nextCompany, Baseline: baseline, Work: work,
		Target: Target{Type: "baseline", ID: work.WorkID}, NextAction: "await_baseline_listing"}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
	audit, _ := json.Marshal(map[string]any{"requested_by": commandContext.RequestedBy, "reason": payload.Reason,
		"source_id": baseline.SourceID, "baseline_generation": baseline.Generation})
	var event model.EventIntent
	if err == nil {
		event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|baseline.created"), "baseline.created",
			"baseline", work.WorkID, baseline.Version, businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
	}
	var companyEvent *model.EventIntent
	if err == nil && nextCompany.Version != preparation.Company.Version {
		created, createErr := model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|company.initialization.started"),
			"company.initialization.started", "company", nextCompany.CompanyID, nextCompany.Version,
			businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
		err, companyEvent = createErr, &created
	}
	dispatch, dispatchErr := workCommandDispatch(cfg, work, placement, payload.CommandID, "baseline_created")
	if err == nil {
		err = dispatchErr
	}
	var result store.CommandResult
	if err == nil {
		result, err = repository.ApplyCreateBaselineCommand(msg.Ctx(), payload.ExpectedCompanyVersion, payload.ExpectedVersion,
			nextCompany, baseline, work, placement, receipt, event, companyEvent, dispatch, businessAt)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}
