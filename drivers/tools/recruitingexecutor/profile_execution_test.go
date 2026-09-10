package recruitingexecutor

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

type browserBrokerStub struct {
	result browserProfileResult
	task   browserProfileTask
	err    error
}

func (b *browserBrokerStub) Execute(_ context.Context, task browserProfileTask) (browserProfileResult, error) {
	b.task = task
	return b.result, b.err
}

func TestExecuteProfileRepairUsesBrokerAndToolControlLifecycle(t *testing.T) {
	now := time.Date(2026, 9, 10, 14, 0, 0, 0, time.UTC)
	offer, recipe := profileExecutionOffer(t, now, "profile_repair")
	broker := &browserBrokerStub{result: browserProfileResult{Version: browserBrokerProtocolVersion,
		RequestID: offer.Attempt.AttemptID, Status: "completed", NextSecretRef: "secret://browser/profile-a/session-a",
		Evidence: browserProfileEvidence{Version: browserBrokerProtocolVersion, TaskKind: "profile_repair",
			SecurityDomain: "jobs.example.test", ObservedAt: now.Format(time.RFC3339Nano), Probe: "interactive_login_completed",
			CanaryRecipeID: offer.ProfileVerification.RecipeID, CanaryRecipeVersion: offer.ProfileVerification.RecipeVersion,
			CanaryContentHash: offer.ProfileVerification.ContentHash, CanaryContractHash: offer.ProfileVerification.ContractHash,
			RecordCount: 1}}}
	resources := &executeResourceStub{artifactCreatorStub: artifactCreatorStub{writer: &writeHandleStub{}}, recipe: recipe}
	control := &executeControlStub{}
	if err := executeProfileOffer(context.Background(), control, resources, offer, profileTestOptions(now, broker)); err != nil {
		t.Fatal(err)
	}
	if len(control.calls) != 3 || control.calls[0] != "accept" || control.calls[1] != "started" ||
		control.calls[2] != "submit:profile_repair_submission" {
		t.Fatalf("Profile repair lifecycle=%v", control.calls)
	}
	submission, ok := control.submissions[0].(executioncontract.ProfileRepairSubmission)
	if !ok || submission.SessionID != offer.ProfileRepair.SessionID || submission.NextSecretRef != broker.result.NextSecretRef ||
		submission.Artifact.Kind != model.ArtifactValidation || !submission.Artifact.Redacted {
		t.Fatalf("Profile repair submission=%#v", control.submissions)
	}
	if broker.task.ProfileID != offer.ProfileRepair.ProfileID || broker.task.SecurityDomain != offer.ProfileSecurityDomain {
		t.Fatalf("browser task=%+v", broker.task)
	}
}

func TestExecuteProfileVerificationRequiresAuthenticatedCanary(t *testing.T) {
	now := time.Date(2026, 9, 10, 14, 30, 0, 0, time.UTC)
	offer, recipe := profileExecutionOffer(t, now, "profile_verification")
	broker := &browserBrokerStub{result: browserProfileResult{Version: browserBrokerProtocolVersion,
		RequestID: offer.Attempt.AttemptID, Status: "completed", Authenticated: true,
		Evidence: browserProfileEvidence{Version: browserBrokerProtocolVersion, TaskKind: "profile_verification",
			SecurityDomain: "jobs.example.test", ObservedAt: now.Format(time.RFC3339Nano), Probe: "authenticated_canary",
			CanaryRecipeID: offer.ProfileVerification.RecipeID, CanaryRecipeVersion: offer.ProfileVerification.RecipeVersion,
			CanaryContentHash: offer.ProfileVerification.ContentHash, CanaryContractHash: offer.ProfileVerification.ContractHash,
			RecordCount: 1}}}
	resources := &executeResourceStub{artifactCreatorStub: artifactCreatorStub{writer: &writeHandleStub{}}, recipe: recipe}
	control := &executeControlStub{}
	if err := executeProfileOffer(context.Background(), control, resources, offer, profileTestOptions(now, broker)); err != nil {
		t.Fatal(err)
	}
	verification, ok := control.submissions[0].(executioncontract.ProfileVerificationResult)
	if !ok || !verification.Authenticated || verification.SecurityDomain != offer.ProfileSecurityDomain ||
		verification.Artifact.Kind != model.ArtifactValidation || !verification.Artifact.Redacted {
		t.Fatalf("Profile verification submission=%#v", control.submissions)
	}

	bad := *broker
	bad.result.Authenticated = false
	badControl := &executeControlStub{}
	badResources := &executeResourceStub{artifactCreatorStub: artifactCreatorStub{writer: &writeHandleStub{}}, recipe: recipe}
	if err := executeProfileOffer(context.Background(), badControl, badResources, offer, profileTestOptions(now, &bad)); err != nil {
		t.Fatal(err)
	}
	if len(badControl.calls) != 3 || badControl.calls[2] != "failed" || badControl.failed.Class != "contract_violated" {
		t.Fatalf("unauthenticated canary lifecycle=%v failure=%+v", badControl.calls, badControl.failed)
	}
}

func profileExecutionOffer(t *testing.T, now time.Time, kind string) (executioncontract.Offer, []byte) {
	t.Helper()
	purpose := "profile_repair"
	if kind == "profile_verification" {
		purpose = "profile_verify"
	}
	work, err := model.NewWork("profile-work-"+kind, "profile", "profile-a", purpose, "human")
	if err != nil {
		t.Fatal(err)
	}
	attempt, _ := model.NewAttempt("profile-attempt-"+kind, work)
	attempt, _ = attempt.BindExecutor("tool:profile-device:1", "boot-a", "browser.profile.repair")
	profileVersion := uint64(2)
	if kind == "profile_verification" {
		profileVersion = 3
	}
	attempt, _ = attempt.WithProfileRepairFence("profile-a", profileVersion)
	session, err := model.NewProfileRepairSession("session-a", "profile-a", 2, "incident-a", "profile-work-profile_repair",
		"human:operator", "tool:profile-device", now.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	spec := recipeabi.Spec{ABIVersion: recipeabi.Version, Kind: recipeabi.KindDetail,
		RequiredCapability: "browser.profile.repair", Transport: recipeabi.TransportBrowser,
		Request: recipeabi.ReadRequest{Method: "GET", TimeoutMS: 10_000, MaxResponseBytes: 1 << 20,
			MaxRedirects: 2, UserAgent: "Atoll-Profile-Canary/1"},
		Extraction: recipeabi.Extraction{Fields: map[string]string{"account": "#account"}}}
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	contentHash, _ := spec.ContentHash()
	contractHash, _ := spec.ContractHash()
	verification := model.ProfileVerificationRecipe{EndpointURL: "https://jobs.example.test/private/canary",
		RecipeID: "profile-canary", RecipeVersion: 1, ContentHash: contentHash, ContractHash: contractHash,
		Kind: model.RecipeDetail, Execution: model.RecipeExecution{ABIVersion: model.RecipeABIVersion,
			ContentRef: "recipe://profile/canary", RequiredCapability: "browser.profile.repair",
			Transport: model.RecipeTransportBrowser}, MinimumRecordCount: 1}
	if kind == "profile_verification" {
		session.ValidationWorkID = work.WorkID
		session.Status = model.ProfileRepairSubmitted
	}
	return executioncontract.Offer{Kind: kind, Attempt: attempt, Work: work, ProfileRepair: &session,
		ProfileSecurityDomain: "jobs.example.test", ProfileTaskExpiresAt: now.Add(10 * time.Minute).Format(time.RFC3339Nano),
		ProfileVerification: &verification, RequestedCapability: "browser.profile.repair", RequestedProfileID: "profile-a"}, raw
}

func profileTestOptions(now time.Time, broker browserBroker) profileExecutionOptions {
	return profileExecutionOptions{Artifact: artifactSinkConfig{DeviceName: "worker", ChannelName: "recruiting",
		Directory: "profile-artifacts", AccessScope: "operators", Retention: "30d", Redacted: true, MaxBytes: 1 << 20},
		Broker: broker, Now: func() time.Time { return now }}
}
