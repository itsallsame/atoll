package recruitingexecutor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/httpdriver"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

type profileExecutionOptions struct {
	Artifact artifactSinkConfig
	Broker   browserBroker
	Now      func() time.Time
}

func executeProfileOffer(ctx context.Context, control executionControl, resources executionResourceAccess,
	offer executioncontract.Offer, options profileExecutionOptions) error {
	if ctx == nil || control == nil || resources == nil || options.Broker == nil || options.Now == nil {
		return errors.New("Profile execution requires context, control, Resource access, browser broker, and clock")
	}
	if err := validateProfileOffer(offer, options.Now().UTC()); err != nil {
		return fmt.Errorf("validate immutable Profile offer: %w", err)
	}
	canary := offer.ProfileVerification
	spec, err := resolveRecipe(resources, canary.Execution.ContentRef, recipeExpectation{ContentHash: canary.ContentHash,
		Kind: recipeabi.Kind(canary.Kind), Capability: canary.Execution.RequiredCapability,
		Transport: recipeabi.Transport(canary.Execution.Transport)})
	if err != nil {
		return fmt.Errorf("resolve Profile authentication canary Recipe: %w", err)
	}
	contractHash, err := spec.ContractHash()
	if err != nil || contractHash != canary.ContractHash {
		return fmt.Errorf("Profile authentication canary contract hash mismatch")
	}
	sinkConfig := options.Artifact
	sinkConfig.WorkID, sinkConfig.AttemptID = offer.Work.WorkID, offer.Attempt.AttemptID
	if !sinkConfig.Redacted {
		return errors.New("Profile execution requires redacted Artifact storage")
	}
	sink, err := newAtollArtifactSink(resources, sinkConfig)
	if err != nil {
		return fmt.Errorf("prepare Profile Artifact sink: %w", err)
	}
	if err := control.Accept(ctx, offer); err != nil {
		return fmt.Errorf("accept Profile execution offer: %w", err)
	}
	if err := control.Started(ctx, offer); err != nil {
		return fmt.Errorf("start Profile execution offer: %w", err)
	}
	task := browserProfileTask{Version: browserBrokerProtocolVersion, RequestID: offer.Attempt.AttemptID,
		Kind: offer.Kind, SessionID: offer.ProfileRepair.SessionID, ProfileID: offer.ProfileRepair.ProfileID,
		SecurityDomain: offer.ProfileSecurityDomain, ExpiresAt: offer.ProfileTaskExpiresAt,
		CanaryURL: canary.EndpointURL, CanaryMinimum: canary.MinimumRecordCount, CanaryRecipe: spec,
		CanaryRecipeID: canary.RecipeID, CanaryRecipeVersion: canary.RecipeVersion,
		CanaryContentHash: canary.ContentHash, CanaryContractHash: canary.ContractHash}
	result, err := options.Broker.Execute(ctx, task)
	if err != nil {
		return failLocalExecution(ctx, control, sink, offer, "unexpected_status", "browser_broker", err)
	}
	if err := validateBrowserProfileResult(task, result, options.Now().UTC()); err != nil {
		return failLocalExecution(ctx, control, sink, offer, "contract_violated", "browser_broker_result", err)
	}
	evidence, _ := json.Marshal(result.Evidence)
	ref, err := sink.Put(ctx, httpdriver.ArtifactWrite{Kind: "validation", AttemptID: offer.Attempt.AttemptID,
		PageSequence: 1, URL: "https://" + offer.ProfileSecurityDomain, ContentType: "application/json", Body: evidence})
	if err != nil {
		return fmt.Errorf("save Profile validation evidence: %w", err)
	}
	artifact, err := sink.metadata(ref, model.ArtifactValidation)
	if err != nil {
		return fmt.Errorf("bind Profile validation evidence: %w", err)
	}
	if offer.Kind == "profile_repair" {
		submission := executioncontract.ProfileRepairSubmission{CommandID: "profile-repair-result-" + offer.Attempt.AttemptID,
			ResultKind: "profile_repair_submission", AttemptID: offer.Attempt.AttemptID,
			ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, SessionID: offer.ProfileRepair.SessionID,
			NextSecretRef: result.NextSecretRef, Artifact: artifact}
		if err := control.Submit(ctx, submission.ResultKind, submission); err != nil {
			return fmt.Errorf("submit Profile repair result: %w", err)
		}
		return nil
	}
	verification := executioncontract.ProfileVerificationResult{CommandID: "profile-verification-result-" + offer.Attempt.AttemptID,
		ResultKind: "profile_verification", AttemptID: offer.Attempt.AttemptID,
		ExecutorIncarnation: offer.Attempt.ExecutorIncarnation, SessionID: offer.ProfileRepair.SessionID,
		SecurityDomain: offer.ProfileSecurityDomain, Authenticated: result.Authenticated, Artifact: artifact}
	if err := control.Submit(ctx, verification.ResultKind, verification); err != nil {
		return fmt.Errorf("submit Profile verification result: %w", err)
	}
	return nil
}

func validateProfileOffer(offer executioncontract.Offer, now time.Time) error {
	if offer.ProfileRepair == nil || offer.ProfileVerification == nil || offer.RequestedCapability != "browser.profile.repair" ||
		offer.RequestedProfileID != offer.ProfileRepair.ProfileID || offer.Attempt.ProfileID != offer.ProfileRepair.ProfileID ||
		offer.Attempt.ProfileVersion == 0 || offer.Budget.PermitID != "" || offer.BudgetExpiresAt != "" {
		return errors.New("Profile offer identity, capability, fence, or budget is inconsistent")
	}
	if offer.Occurrence != nil || offer.ListingRun != nil || offer.Baseline != nil || offer.CompanyImport != nil ||
		len(offer.CompanyImportItems) != 0 || offer.Checkpoint != nil || offer.Detail != nil || offer.Discovery != nil ||
		offer.Recipe != nil || offer.RecipeValidation != nil {
		return errors.New("Profile offer contains an unrelated execution payload")
	}
	if offer.Kind != "profile_repair" && offer.Kind != "profile_verification" {
		return fmt.Errorf("unsupported Profile offer kind %q", offer.Kind)
	}
	wantPurpose := "profile_repair"
	if offer.Kind == "profile_verification" {
		wantPurpose = "profile_verify"
	}
	if offer.Work.Purpose != wantPurpose || offer.Work.WorkID != offer.Attempt.WorkID ||
		offer.ProfileRepair.ProfileID != offer.Work.TargetID || strings.TrimSpace(offer.ProfileSecurityDomain) == "" {
		return errors.New("Profile offer Work or security domain is inconsistent")
	}
	if offer.Kind == "profile_repair" {
		if offer.ProfileRepair.WorkID != offer.Work.WorkID || offer.ProfileRepair.Status != model.ProfileRepairAwaitingDevice ||
			offer.Attempt.ProfileVersion != offer.ProfileRepair.ProfileVersion {
			return errors.New("Profile repair session does not match its Work and version fence")
		}
	} else if offer.ProfileRepair.ValidationWorkID != offer.Work.WorkID ||
		offer.ProfileRepair.Status != model.ProfileRepairSubmitted ||
		offer.Attempt.ProfileVersion != offer.ProfileRepair.ProfileVersion+1 {
		return errors.New("Profile verification session does not match its Work and version fence")
	}
	if err := offer.ProfileVerification.Validate(offer.ProfileSecurityDomain); err != nil {
		return fmt.Errorf("Profile verification Recipe is invalid: %w", err)
	}
	domainURL, err := url.Parse("https://" + offer.ProfileSecurityDomain)
	if err != nil || domainURL.Hostname() != offer.ProfileSecurityDomain || domainURL.Port() != "" {
		return errors.New("Profile security domain is invalid")
	}
	expiresAt, err := time.Parse(time.RFC3339Nano, offer.ProfileTaskExpiresAt)
	if err != nil || now.IsZero() || !now.Before(expiresAt) {
		return errors.New("Profile browser task deadline is invalid or expired")
	}
	if offer.Kind == "profile_repair" && offer.ProfileTaskExpiresAt != offer.ProfileRepair.ExpiresAt {
		return errors.New("Profile repair task deadline differs from its one-time session expiry")
	}
	return nil
}

func validateBrowserProfileResult(task browserProfileTask, result browserProfileResult, now time.Time) error {
	if result.Version != browserBrokerProtocolVersion || result.RequestID != task.RequestID || result.Status != "completed" ||
		result.FailureCode != "" || result.Evidence.Version != browserBrokerProtocolVersion ||
		result.Evidence.TaskKind != task.Kind || result.Evidence.SecurityDomain != task.SecurityDomain {
		return errors.New("browser broker result identity or status is inconsistent")
	}
	observedAt, err := time.Parse(time.RFC3339Nano, result.Evidence.ObservedAt)
	if err != nil || now.IsZero() || observedAt.Before(now.Add(-5*time.Minute)) || observedAt.After(now.Add(time.Minute)) {
		return errors.New("browser broker evidence time is invalid")
	}
	if result.Evidence.CanaryRecipeID != task.CanaryRecipeID ||
		result.Evidence.CanaryRecipeVersion != task.CanaryRecipeVersion ||
		result.Evidence.CanaryContentHash != task.CanaryContentHash ||
		result.Evidence.CanaryContractHash != task.CanaryContractHash || result.Evidence.RecordCount < task.CanaryMinimum {
		return errors.New("browser broker evidence does not prove the frozen canary Recipe")
	}
	if task.Kind == "profile_repair" {
		secretRef, secretErr := url.Parse(result.NextSecretRef)
		if secretErr != nil || secretRef.Scheme != "secret" || secretRef.Host == "" || secretRef.User != nil ||
			secretRef.RawQuery != "" || secretRef.Fragment != "" || len(result.NextSecretRef) > 1024 ||
			result.Authenticated || result.Evidence.Probe != "interactive_login_completed" {
			return errors.New("browser repair result lacks an opaque local Profile reference or completion proof")
		}
		return nil
	}
	if result.NextSecretRef != "" || !result.Authenticated || result.Evidence.Probe != "authenticated_canary" {
		return errors.New("browser verification result lacks an authenticated canary proof")
	}
	return nil
}
