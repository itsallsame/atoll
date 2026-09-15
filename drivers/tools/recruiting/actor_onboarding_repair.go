package recruiting

import (
	"strings"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
)

type onboardingRepairValidatePayload struct {
	CompanyName string `json:"company_name"`
	Reason      string `json:"reason"`
}

// handleOnboardingRepairValidate translates an operator's repair confirmation
// into a new budgeted Probe. It never reopens or rewrites the failed Probe;
// the new Work carries explicit causality for the shared Repair state machine.
func handleOnboardingRepairValidate(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	var payload onboardingRepairValidatePayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CompanyName, payload.Reason = strings.TrimSpace(payload.CompanyName), strings.TrimSpace(payload.Reason)
	if payload.CompanyName == "" || len(payload.CompanyName) > 200 || payload.Reason == "" || len(payload.Reason) > 1000 {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "bounded company_name and operator repair reason are required")
		return
	}
	matches, err := repository.FindCompaniesByExactName(msg.Ctx(), payload.CompanyName, 3)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if len(matches) != 1 {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "repair validation requires one unambiguous Company name")
		return
	}
	company := matches[0]
	mission, err := repository.GetLatestDeepDiscoveryMissionForCompany(msg.Ctx(), company.CompanyID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if mission.Status != model.DeepDiscoveryActive {
		_, _ = sys.Fail(msg, ErrorExecutionRejected, "repair validation requires an active Deep Discovery Mission")
		return
	}
	snapshot, err := repository.GetDeepDiscoveryAutomationSnapshot(msg.Ctx(), mission.MissionID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	for index := len(snapshot.BrowserProbes) - 1; index >= 0; index-- {
		failed := snapshot.BrowserProbes[index]
		if failed.Work.Status != model.WorkWaitingHuman || failed.Work.BlockedByRepairWorkID == "" {
			continue
		}
		for candidateIndex := range snapshot.BrowserProbes {
			candidate := snapshot.BrowserProbes[candidateIndex]
			if candidate.Work.CauseWorkID != failed.Work.WorkID {
				continue
			}
			if candidate.Work.Status == model.WorkOpen || candidate.Work.Status == model.WorkRunning ||
				candidate.Work.Status == model.WorkWaitingRetry {
				_, _ = sys.Reply(msg, onboardingAdvanceResponse{ContractVersion: ContractVersion, Status: "waiting",
					Company: company, Mission: mission, Action: onboardingAwaitBrowser, Work: &candidate.Work,
					Probe: &candidate.Probe, NextAction: string(onboardingAwaitBrowser),
					AgentDirective: onboardingAdvanceDirective(onboardingAwaitBrowser, mission.IsSourceInitializationEvidence())})
				return
			}
			if candidate.Work.Status == model.WorkCompleted && candidate.Work.Resolution == model.ResolutionSucceeded {
				_, _ = sys.Reply(msg, onboardingAdvanceResponse{ContractVersion: ContractVersion, Status: "validated",
					Company: company, Mission: mission, Action: onboardingValidateRepair, Work: &candidate.Work,
					Probe: &candidate.Probe, NextAction: "finalize_shared_repair",
					AgentDirective: "Use this successful causal Work to validate and resolve its shared Repair Incident, then recover affected Work through recruiting.repair commands. Derive all IDs from persisted repair facts; do not ask the user."})
				return
			}
		}
		handleOnboardingAdvanceBrowser(sys, cfg, repository, msg, company, mission, onboardingAutomationPlan{
			Action: onboardingValidateRepair, SourceProbe: &failed.Probe,
			VerificationIDs: append([]string(nil), failed.Probe.StubVerificationIDs...), CauseWorkID: failed.Work.WorkID,
			Detail: payload.Reason,
		})
		return
	}
	_, _ = sys.Fail(msg, ErrorExecutionRejected, "no failed Deep Discovery browser Work is waiting for repair validation")
}
