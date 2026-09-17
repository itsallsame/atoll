package recruiting

import (
	"encoding/json"
	"testing"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/protocol/actor"
	"github.com/wanpengxie/atoll/protocol/message"
)

func TestAutomaticOnboardingContinuationIsNarrow(t *testing.T) {
	allowed := []onboardingAutomationAction{
		onboardingCreateSeedBrowser,
		onboardingReviewEvidence,
	}
	for _, action := range allowed {
		if !automaticOnboardingContinuation(action) {
			t.Fatalf("expected %q to be automatic", action)
		}
	}
	blocked := []onboardingAutomationAction{
		onboardingStartEvidence,
		onboardingAwaitBrowser,
		onboardingNeedsAttention,
		onboardingValidateRepair,
		onboardingResolveIdentity,
		onboardingCompleteEvidence,
		onboardingStaleContinuation,
	}
	for _, action := range blocked {
		if automaticOnboardingContinuation(action) {
			t.Fatalf("expected %q to require an external transition", action)
		}
	}
}

func TestOnboardingContinuationRequestIsRetryableAndMissionScoped(t *testing.T) {
	mission := model.DeepDiscoveryMission{
		MissionID:   "mission-auto-initialization-example",
		CompanyID:   "company-example",
		CompanyName: "Example",
		Purpose:     model.DeepDiscoveryPurposeSourceInitialization,
		Status:      model.DeepDiscoveryActive,
		Version:     7,
	}
	first, err := onboardingContinuationRequest(actor.ActorID("recruiting-control"), mission, message.Root())
	if err != nil {
		t.Fatal(err)
	}
	second, err := onboardingContinuationRequest(actor.ActorID("recruiting-control"), mission, message.Root())
	if err != nil {
		t.Fatal(err)
	}
	if first.ID == second.ID || first.ID == "" || second.ID == "" {
		t.Fatalf("recovery attempts need unique request IDs: %q, %q", first.ID, second.ID)
	}
	if len(first.Audience) != 1 || first.Audience[0] != actor.ActorID("recruiting-control") {
		t.Fatalf("continuation must be self-addressed: %#v", first.Audience)
	}
	var payload onboardingStatusPayload
	if err := json.Unmarshal(first.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.MissionID != mission.MissionID || payload.CompanyID != mission.CompanyID ||
		payload.ExpectedMissionVersion != mission.Version {
		t.Fatalf("continuation lost Mission identity: %#v", payload)
	}
	mission.Version++
	third, err := onboardingContinuationRequest(actor.ActorID("recruiting-control"), mission, message.Root())
	if err != nil {
		t.Fatal(err)
	}
	if third.ID == first.ID {
		t.Fatal("a new Mission version must produce a new continuation request")
	}
	var thirdPayload onboardingStatusPayload
	if err := json.Unmarshal(third.Payload, &thirdPayload); err != nil {
		t.Fatal(err)
	}
	if thirdPayload.ExpectedMissionVersion != mission.Version {
		t.Fatalf("continuation did not fence Mission version: %#v", thirdPayload)
	}
}
