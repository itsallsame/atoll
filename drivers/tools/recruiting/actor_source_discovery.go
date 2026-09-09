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

type sourceDiscoverPayload struct {
	MutationCommand
	DiscoveryID   string `json:"discovery_id"`
	WorkID        string `json:"work_id"`
	Generation    uint64 `json:"discovery_generation"`
	RecipeID      string `json:"recipe_id"`
	RecipeVersion uint64 `json:"recipe_version"`
	ProfileID     string `json:"profile_id,omitempty"`
	Priority      int    `json:"priority,omitempty"`
	DeadlineAt    string `json:"deadline_at,omitempty"`
}

type sourceDiscoveryCommandResponse struct {
	ContractVersion string                `json:"contract_version"`
	CorrelationID   string                `json:"correlation_id"`
	RequestedBy     string                `json:"requested_by"`
	Discovery       model.SourceDiscovery `json:"discovery"`
	Work            model.Work            `json:"work"`
	Target          Target                `json:"target"`
	NextAction      string                `json:"next_action"`
}

func handleSourceDiscover(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	var payload sourceDiscoverPayload
	if !decode(sys, msg, &payload) {
		return
	}
	commandContext, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	if err != nil || payload.Target.Type != "company" || strings.TrimSpace(payload.DiscoveryID) == "" ||
		strings.TrimSpace(payload.WorkID) == "" || payload.Generation == 0 || strings.TrimSpace(payload.RecipeID) == "" ||
		payload.RecipeVersion == 0 {
		if err == nil {
			err = fmt.Errorf("company target, discovery_id, work_id, discovery_generation, recipe_id, and recipe_version are required")
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
	company, err := repository.GetCompany(msg.Ctx(), payload.Target.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if company.Version != payload.ExpectedVersion {
		failStoreError(sys, msg, &model.VersionConflictError{Expected: payload.ExpectedVersion, Actual: company.Version})
		return
	}
	recipe, err := repository.GetRecipe(msg.Ctx(), strings.TrimSpace(payload.RecipeID), payload.RecipeVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	work, err := model.NewWork(strings.TrimSpace(payload.WorkID), "company", company.CompanyID, "source_discovery", "human")
	if err == nil {
		work, err = work.WithCausality(commandContext.RequestedBy, string(msg.ID), "")
	}
	discovery := model.SourceDiscovery{}
	if err == nil {
		discovery, err = model.NewSourceDiscovery(strings.TrimSpace(payload.DiscoveryID), work.WorkID, company,
			payload.Generation, company.Website, recipe)
	}
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	placement, err := sourceDiscoveryPlacement(company.Website, recipe.Execution.RequiredCapability, payload.ProfileID,
		payload.Priority, payload.DeadlineAt, businessAt)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	placement.BusinessKey = fmt.Sprintf("source-discovery|%s|%d", company.CompanyID, payload.Generation)
	response := sourceDiscoveryCommandResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: commandContext.RequestedBy, Discovery: discovery, Work: work,
		Target: Target{Type: "source_discovery", ID: discovery.DiscoveryID}, NextAction: "await_discovery_execution"}
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, commandRequestHash(msg), responseBytes)
	auditPayload, _ := json.Marshal(map[string]any{"requested_by": commandContext.RequestedBy, "reason": payload.Reason,
		"company_id": company.CompanyID, "discovery_generation": payload.Generation})
	var event model.EventIntent
	if err == nil {
		event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|source.discovery.created"),
			"source.discovery.created", "source_discovery", discovery.DiscoveryID, discovery.Version,
			businessAt.Format(time.RFC3339Nano), payload.CommandID, auditPayload)
	}
	dispatch, dispatchErr := workCommandDispatch(cfg, work, placement, payload.CommandID, "source_discovery_created")
	if err == nil {
		err = dispatchErr
	}
	var result store.CommandResult
	if err == nil {
		result, err = repository.ApplyCreateSourceDiscoveryCommand(msg.Ctx(), discovery, work, placement, receipt, event, dispatch, businessAt)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func sourceDiscoveryPlacement(seedURL, capability, profileID string, priority int, deadline string, businessAt time.Time) (store.WorkPlacement, error) {
	parsed, err := url.Parse(seedURL)
	profileID, capability = strings.TrimSpace(profileID), strings.TrimSpace(capability)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" ||
		priority < -1000 || priority > 1000 || len(profileID) > 191 || strings.ContainsAny(profileID, "\r\n\t ") {
		return store.WorkPlacement{}, fmt.Errorf("source discovery requires canonical seed origin, bounded priority, and normalized profile")
	}
	notBefore, deadlineAt, err := retrySchedule("", deadline, businessAt)
	if err != nil {
		return store.WorkPlacement{}, err
	}
	return store.WorkPlacement{Priority: priority, Capability: capability, Origin: parsed.Scheme + "://" + parsed.Host,
		ProfileID: profileID, NotBefore: notBefore, DeadlineAt: deadlineAt}, nil
}
