package recruiting

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/lib/behavior"
	"github.com/wanpengxie/atoll/protocol/actor"
	"github.com/wanpengxie/atoll/protocol/message"
)

type onboardingReconcileResult struct {
	MissionsScanned     int
	ContinuationsPosted int
	Waiting             int
	Conflicts           int
	Errors              int
}

func automaticOnboardingContinuation(action onboardingAutomationAction) bool {
	switch action {
	case onboardingCreateSeedBrowser, onboardingVerifyPublicQuery, onboardingReplayVerified, onboardingReviewEvidence:
		return true
	default:
		return false
	}
}

func onboardingContinuationRequest(self actor.ActorID, mission model.DeepDiscoveryMission, cause message.Cause) (behavior.RequestSpec, error) {
	if self == "" || mission.MissionID == "" || mission.CompanyID == "" || mission.Status != model.DeepDiscoveryActive ||
		!mission.IsSourceInitializationEvidence() {
		return behavior.RequestSpec{}, fmt.Errorf("active source-initialization Mission and actor identity are required")
	}
	payload, err := json.Marshal(onboardingStatusPayload{CompanyName: mission.CompanyName, CompanyID: mission.CompanyID,
		MissionID: mission.MissionID})
	if err != nil {
		return behavior.RequestSpec{}, err
	}
	return behavior.RequestSpec{
		ID:       message.ID("onboarding-continuation-" + stableDigest(fmt.Sprintf("%s|%d", mission.MissionID, mission.Version))),
		Type:     TypeOnboardingAdvance,
		Payload:  payload,
		Audience: message.Audience{self},
		Cause:    cause,
	}, nil
}

// postOnboardingContinuation is the low-latency edge. Failure is intentionally
// best effort because the recurring durable reconciliation path posts the same
// stable request ID from committed SQL facts.
func postOnboardingContinuation(ctx context.Context, sys actorbase.Sys, repository *store.Repository, missionID string, cause message.Cause) {
	mission, err := repository.GetDeepDiscoveryMission(ctx, missionID)
	if err != nil || mission.Status != model.DeepDiscoveryActive || !mission.IsSourceInitializationEvidence() {
		return
	}
	request, err := onboardingContinuationRequest(sys.Self(), mission, cause)
	if err == nil {
		_, _ = sys.Post(request)
	}
}

func reconcileOnboardingContinuations(ctx context.Context, sys actorbase.Sys, repository *store.Repository, limit int,
	cause message.Cause) (onboardingReconcileResult, error) {
	items, err := repository.ListActiveSourceInitializationMissions(ctx, limit)
	if err != nil {
		return onboardingReconcileResult{}, err
	}
	result := onboardingReconcileResult{MissionsScanned: len(items)}
	for _, item := range items {
		sources, sourceErr := listAllCompanySourcesContext(ctx, repository, item.Mission.CompanyID)
		if sourceErr != nil {
			result.Errors++
			continue
		}
		snapshot, snapshotErr := repository.GetDeepDiscoveryAutomationSnapshot(ctx, item.Mission.MissionID)
		if snapshotErr != nil {
			result.Errors++
			continue
		}
		if !automaticOnboardingContinuation(planOnboardingAutomation(snapshot, item.Company, sources...).Action) {
			result.Waiting++
			continue
		}
		request, requestErr := onboardingContinuationRequest(sys.Self(), item.Mission, cause)
		if requestErr != nil {
			result.Errors++
			continue
		}
		if _, postErr := sys.Post(request); postErr != nil {
			result.Conflicts++
			continue
		}
		result.ContinuationsPosted++
	}
	return result, nil
}
