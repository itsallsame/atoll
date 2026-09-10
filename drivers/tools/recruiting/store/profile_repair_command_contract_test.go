package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestProfileRepairBeginIsAtomicDeviceBoundAndSecretFree(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	now := time.Date(2026, 9, 10, 9, 0, 0, 0, time.UTC)
	profile, _ := model.NewBrowserProfileWithVerification("profile-repair-begin-profile", "jobs.example.test",
		"tool:authorized-profile-device", "secret://profiles/repair-begin/v1",
		testProfileVerificationRecipe("jobs.example.test", "repair-begin"))
	if err := repository.CreateProfile(ctx, profile, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	repairing, _ := profile.BeginRepair(profile.Version)
	if err := repository.UpdateProfileCAS(ctx, profile.Version, repairing, now); err != nil {
		t.Fatal(err)
	}
	affected, _ := model.NewWork("profile-repair-begin-affected", "source", "source-a", "listing_sync", "timer")
	if err := repository.CreateWork(ctx, affected, WorkPlacement{BusinessKey: "profile-repair-begin-affected",
		Capability: "browser.recipe", ProfileID: profile.ProfileID, NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}
	repairWork, _ := model.NewWork("profile-repair-begin-incident-work", "repair_incident",
		"profile-repair-begin-incident", "repair", "automatic")
	if err := repository.CreateWork(ctx, repairWork, WorkPlacement{BusinessKey: "profile-repair-begin-incident-work",
		Priority: 500, NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}
	incident, _ := model.NewRepairIncident("profile-repair-begin-incident", model.FailureProfile, profile.ProfileID,
		"browser.profile.expired", "profile:1", affected.WorkID)
	incident, _ = incident.WithRepairWork(repairWork.WorkID)
	if _, joined, err := repository.OpenOrJoinRepair(ctx, incident, now); err != nil || joined {
		t.Fatalf("open Profile repair incident joined=%v err=%v", joined, err)
	}
	expiresAt := now.Add(10 * time.Minute)
	session, _ := model.NewProfileRepairSession("profile-repair-begin-session", profile.ProfileID, repairing.Version,
		incident.IncidentID, "profile-repair-begin-session-work", "human:operator", profile.DeviceID, expiresAt)
	work, _ := model.NewWork(session.WorkID, "profile", profile.ProfileID, "profile_repair", "human")
	work, _ = work.WithCausality(session.RequestedBy, "message-profile-repair-begin", repairWork.WorkID)
	placement := WorkPlacement{BusinessKey: "profile-repair|" + profile.ProfileID + "|2|" + session.SessionID, Priority: 600,
		Capability: ProfileRepairCapability, ProfileID: profile.ProfileID, NotBefore: now, DeadlineAt: &expiresAt}
	response, _ := json.Marshal(map[string]any{"session_id": session.SessionID, "work_id": work.WorkID,
		"status": session.Status, "expires_at": session.ExpiresAt})
	receipt, _ := model.NewCommandReceipt("profile-repair-begin-command", "recruiting.profile.repair.begin",
		"sha256:profile-repair-begin", response)
	audit, _ := json.Marshal(map[string]any{"profile_id": profile.ProfileID, "repair_incident_id": incident.IncidentID,
		"requested_by": session.RequestedBy})
	event, _ := model.NewEventIntent("profile-repair-begin-event", "profile.repair_session.created",
		"profile_repair_session", session.SessionID, session.Version, now.Format(time.RFC3339Nano), receipt.CommandID, audit)
	dispatch, _ := NewExecutionDispatchIntent("profile-repair-begin-dispatch", session.DeviceActorID,
		ProfileRepairCapability, "", profile.ProfileID, "profile_repair_created", receipt.CommandID, now)
	result, err := repository.ApplyProfileRepairBeginCommand(ctx, repairing.Version, session, work, placement,
		receipt, event, dispatch, now)
	if err != nil || result.Replayed {
		t.Fatalf("begin Profile repair=%+v err=%v", result, err)
	}
	replay, err := repository.ApplyProfileRepairBeginCommand(ctx, repairing.Version, session, work, placement,
		receipt, event, dispatch, now)
	if err != nil || !replay.Replayed || string(replay.Response) != string(result.Response) {
		t.Fatalf("replay Profile repair=%+v err=%v", replay, err)
	}
	stored, err := repository.GetProfileRepairSession(ctx, session.SessionID)
	if err != nil || stored != session {
		t.Fatalf("stored Profile repair session=%+v err=%v", stored, err)
	}
	storedProfile, _ := repository.GetProfile(ctx, profile.ProfileID)
	if !reflect.DeepEqual(storedProfile, repairing) {
		t.Fatalf("begin session changed Profile=%+v want=%+v", storedProfile, repairing)
	}
	pending, err := repository.ListPendingExecutionDispatches(ctx, now, 10)
	var deviceDispatch *PendingExecutionDispatch
	for index := range pending {
		if pending[index].Intent.DispatchID == dispatch.DispatchID {
			deviceDispatch = &pending[index]
			break
		}
	}
	if err != nil || deviceDispatch == nil || deviceDispatch.Intent.TargetActorID != profile.DeviceID ||
		deviceDispatch.Intent.ProfileID != profile.ProfileID || deviceDispatch.Intent.Capability != ProfileRepairCapability {
		t.Fatalf("device-bound dispatch=%+v err=%v", pending, err)
	}
	var sessionState, eventPayload []byte
	if err := db.QueryRowContext(ctx, `SELECT state_json FROM recruiting_profile_repair_sessions WHERE session_id = ?`,
		session.SessionID).Scan(&sessionState); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT payload_json FROM recruiting_event_outbox WHERE event_id = ?`,
		event.EventID).Scan(&eventPayload); err != nil {
		t.Fatal(err)
	}
	for label, raw := range map[string][]byte{"event": eventPayload, "response": result.Response} {
		if strings.Contains(string(raw), profile.SecretRef) {
			t.Fatalf("%s leaked opaque Profile secret reference", label)
		}
		if strings.Contains(string(raw), profile.DeviceID) {
			t.Fatalf("%s leaked private Profile device identity", label)
		}
	}
	if !strings.Contains(string(sessionState), profile.DeviceID) {
		t.Fatal("durable private session lost its authorized device identity")
	}
	if _, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "profile-repair-wrong-device-attempt",
		ExecutorActorID: "tool:other-device:1", ExecutorIncarnation: "wrong-device-boot",
		Capability: ProfileRepairCapability, ProfileID: profile.ProfileID, OfferedAt: now.Add(time.Second),
		BudgetPolicy: testExecutionBudgetPolicy()}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unauthorized device saw Profile repair Work: %v", err)
	}
	offer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "profile-repair-device-attempt",
		ExecutorActorID: profile.DeviceID + ":1", ExecutorIncarnation: "authorized-device-boot",
		Capability: ProfileRepairCapability, ProfileID: profile.ProfileID, OfferedAt: now.Add(time.Second),
		BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || offer.Kind != "profile_repair" || offer.ProfileRepair == nil ||
		offer.ProfileRepair.SessionID != session.SessionID || offer.Attempt.ProfileID != profile.ProfileID ||
		offer.Attempt.ProfileVersion != profile.Version+1 || offer.Budget.PermitID != "" ||
		offer.ProfileSecurityDomain != profile.SecurityDomain || offer.ProfileTaskExpiresAt != session.ExpiresAt ||
		offer.ProfileVerification == nil || *offer.ProfileVerification != *profile.Verification {
		t.Fatalf("authorized Profile repair offer=%+v err=%v", offer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, offer.Attempt.AttemptID, offer.Attempt.ExecutorActorID,
		offer.Attempt.ExecutorIncarnation, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	activeSession, err := repository.GetProfileRepairSession(ctx, session.SessionID)
	if err != nil || activeSession.Status != model.ProfileRepairActive ||
		activeSession.AttemptID != offer.Attempt.AttemptID || activeSession.Version != session.Version+1 {
		t.Fatalf("activated Profile repair session=%+v err=%v", activeSession, err)
	}
	var permits int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_budget_permits WHERE attempt_id = ?`,
		offer.Attempt.AttemptID).Scan(&permits); err != nil || permits != 0 {
		t.Fatalf("interactive Profile repair acquired website permit count=%d err=%v", permits, err)
	}

	originalExpiry := activeSession.ExpiresAt
	recovery, err := repository.RecoverStaleAttempts(ctx, now.Add(4*time.Second), 500, now.Add(5*time.Second))
	if err != nil || recovery.Expired < 1 || recovery.RetryQueued < 1 {
		t.Fatalf("recover crashed Profile repair execution=%+v err=%v", recovery, err)
	}
	expiredAttempt, _ := repository.GetAttempt(ctx, offer.Attempt.AttemptID)
	retryableWork, _ := repository.GetWork(ctx, work.WorkID)
	retryableSession, err := repository.GetProfileRepairSession(ctx, session.SessionID)
	if err != nil || expiredAttempt.Status != model.AttemptExpired ||
		retryableWork.Status != model.WorkWaitingRetry ||
		retryableSession.Status != model.ProfileRepairAwaitingDevice || retryableSession.AttemptID != "" ||
		retryableSession.ExpiresAt != originalExpiry {
		t.Fatalf("recovered Profile repair attempt=%+v work=%+v session=%+v err=%v",
			expiredAttempt, retryableWork, retryableSession, err)
	}
	retryOffer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "profile-repair-retry-attempt",
		ExecutorActorID: profile.DeviceID + ":1", ExecutorIncarnation: "authorized-device-retry-boot",
		Capability: ProfileRepairCapability, ProfileID: profile.ProfileID, OfferedAt: now.Add(6 * time.Second),
		BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || retryOffer.Kind != "profile_repair" || retryOffer.ProfileRepair == nil ||
		retryOffer.ProfileRepair.SessionID != session.SessionID {
		t.Fatalf("retried Profile repair offer=%+v err=%v", retryOffer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, retryOffer.Attempt.AttemptID,
		retryOffer.Attempt.ExecutorActorID, retryOffer.Attempt.ExecutorIncarnation, now.Add(7*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, retryOffer.Attempt.AttemptID,
		retryOffer.Attempt.ExecutorActorID, retryOffer.Attempt.ExecutorIncarnation, now.Add(8*time.Second)); err != nil {
		t.Fatal(err)
	}
	offer = retryOffer

	secondSession, _ := model.NewProfileRepairSession("profile-repair-begin-session-2", profile.ProfileID, repairing.Version,
		incident.IncidentID, "profile-repair-begin-session-work-2", "human:operator", profile.DeviceID, expiresAt)
	secondWork, _ := model.NewWork(secondSession.WorkID, "profile", profile.ProfileID, "profile_repair", "human")
	secondWork, _ = secondWork.WithCausality(secondSession.RequestedBy, "message-profile-repair-begin-2", repairWork.WorkID)
	secondPlacement := placement
	secondPlacement.BusinessKey = "profile-repair|" + profile.ProfileID + "|2|" + secondSession.SessionID
	secondResponse, _ := json.Marshal(map[string]any{"session_id": secondSession.SessionID, "status": secondSession.Status})
	secondReceipt, _ := model.NewCommandReceipt("profile-repair-begin-command-2", receipt.Word,
		"sha256:profile-repair-begin-2", secondResponse)
	secondEvent, _ := model.NewEventIntent("profile-repair-begin-event-2", event.Kind, event.AggregateType,
		secondSession.SessionID, secondSession.Version, now.Format(time.RFC3339Nano), secondReceipt.CommandID, audit)
	secondDispatch, _ := NewExecutionDispatchIntent("profile-repair-begin-dispatch-2", secondSession.DeviceActorID,
		ProfileRepairCapability, "", profile.ProfileID, "profile_repair_created", secondReceipt.CommandID, now)
	if _, err := repository.ApplyProfileRepairBeginCommand(ctx, repairing.Version, secondSession, secondWork,
		secondPlacement, secondReceipt, secondEvent, secondDispatch, now); err == nil {
		t.Fatal("second active Profile repair session was accepted")
	}
	var secondWorks, secondReceipts int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_works WHERE work_id = ?`, secondWork.WorkID).Scan(&secondWorks)
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_command_receipts WHERE command_id = ?`,
		secondReceipt.CommandID).Scan(&secondReceipts)
	if secondWorks != 0 || secondReceipts != 0 {
		t.Fatalf("failed duplicate session left work=%d receipt=%d", secondWorks, secondReceipts)
	}

	completedAt := now.Add(9 * time.Second)
	verificationDeadline := completedAt.Add(10 * time.Minute)
	verificationWork, _ := model.NewWork("profile-repair-verification-work", "profile", profile.ProfileID,
		"profile_verify", "automatic")
	verificationWork, _ = verificationWork.WithCausality(offer.Attempt.ExecutorActorID,
		"message-profile-repair-submitted", work.WorkID)
	verificationPlacement := WorkPlacement{BusinessKey: "profile-verify|" + profile.ProfileID + "|3",
		Priority: 700, Capability: ProfileRepairCapability, ProfileID: profile.ProfileID,
		NotBefore: completedAt, DeadlineAt: &verificationDeadline}
	verificationDispatch, _ := NewExecutionDispatchIntent("profile-repair-verification-dispatch", profile.DeviceID,
		ProfileRepairCapability, "", profile.ProfileID, "profile_verification_created",
		"profile-repair-submission-command", completedAt)
	submissionArtifact := mustResultArtifact(t, "profile-repair-submission-artifact", model.ArtifactValidation,
		work.WorkID, offer.Attempt.AttemptID)
	const nextSecretRef = "secret://profiles/repair-begin/v2"
	submission := ProfileRepairSubmission{CommandID: "profile-repair-submission-command",
		RequestHash: "sha256:profile-repair-submission", AttemptID: offer.Attempt.AttemptID,
		ExecutorActorID: offer.Attempt.ExecutorActorID, ExecutorIncarnation: offer.Attempt.ExecutorIncarnation,
		SessionID: session.SessionID, NextSecretRef: nextSecretRef, Artifact: submissionArtifact,
		VerificationWork: verificationWork, VerificationPlace: verificationPlacement,
		VerificationDispatch: verificationDispatch, CompletedAt: completedAt}
	submissionOutcome, err := repository.AcceptProfileRepairSubmission(ctx, submission)
	if err != nil || submissionOutcome.Replayed || submissionOutcome.ProfileStatus != model.ProfileVerifying ||
		submissionOutcome.ProfileVersion != 3 || submissionOutcome.VerificationWork.WorkID != verificationWork.WorkID {
		t.Fatalf("Profile repair submission=%+v err=%v", submissionOutcome, err)
	}
	submissionReplay, err := repository.AcceptProfileRepairSubmission(ctx, submission)
	if err != nil || !submissionReplay.Replayed || submissionReplay.VerificationWork.WorkID != verificationWork.WorkID {
		t.Fatalf("Profile repair submission replay=%+v err=%v", submissionReplay, err)
	}
	if encoded, _ := json.Marshal(submissionOutcome); strings.Contains(string(encoded), nextSecretRef) {
		t.Fatal("Profile repair result response leaked opaque secret reference")
	}
	rotatedProfile, _ := repository.GetProfile(ctx, profile.ProfileID)
	if rotatedProfile.AuthStatus != model.ProfileVerifying || rotatedProfile.Version != 3 ||
		rotatedProfile.SecretRef != nextSecretRef || rotatedProfile.DeviceID != profile.DeviceID {
		t.Fatalf("rotated Profile=%+v", rotatedProfile)
	}
	submittedSession, _ := repository.GetProfileRepairSession(ctx, session.SessionID)
	if submittedSession.Status != model.ProfileRepairSubmitted || submittedSession.Version != retryableSession.Version+2 ||
		submittedSession.ValidationWorkID != verificationWork.WorkID {
		t.Fatalf("submitted Profile repair session=%+v", submittedSession)
	}
	if _, err := repository.GetActiveProfileRepairSession(ctx, profile.ProfileID, repairing.Version); !errors.Is(err, ErrNotFound) {
		t.Fatalf("submitted session retained active key: %v", err)
	}
	completedInteraction, _ := repository.GetWork(ctx, work.WorkID)
	storedVerification, _ := repository.GetWork(ctx, verificationWork.WorkID)
	storedAttempt, _ := repository.GetAttempt(ctx, offer.Attempt.AttemptID)
	if completedInteraction.Status != model.WorkCompleted || completedInteraction.Resolution != model.ResolutionSucceeded ||
		storedVerification.Status != model.WorkOpen || storedAttempt.Status != model.AttemptSucceeded {
		t.Fatalf("submission lifecycle interaction=%+v verification=%+v attempt=%+v",
			completedInteraction, storedVerification, storedAttempt)
	}
	var submissionEvents, submissionReceipts int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_event_outbox
WHERE cause_command_id = ? AND event_kind IN ('profile.repair_submitted','profile.repair_session.submitted')`,
		submission.CommandID).Scan(&submissionEvents)
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_command_receipts WHERE command_id = ?`,
		submission.CommandID).Scan(&submissionReceipts)
	if submissionEvents != 2 || submissionReceipts != 1 {
		t.Fatalf("Profile repair submission events=%d receipts=%d", submissionEvents, submissionReceipts)
	}

	verificationOffer, err := repository.OfferExecution(ctx, ListingOfferRequest{
		AttemptID: "profile-repair-verification-attempt", ExecutorActorID: profile.DeviceID + ":1",
		ExecutorIncarnation: "authorized-device-verify-boot", Capability: ProfileRepairCapability,
		ProfileID: profile.ProfileID, OfferedAt: completedAt.Add(time.Second), BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil || verificationOffer.Kind != "profile_verification" || verificationOffer.ProfileRepair == nil ||
		verificationOffer.ProfileRepair.SessionID != session.SessionID || verificationOffer.Attempt.ProfileVersion != 3 ||
		verificationOffer.Budget.PermitID != "" || verificationOffer.ProfileSecurityDomain != profile.SecurityDomain ||
		verificationOffer.ProfileTaskExpiresAt != verificationDeadline.Format(time.RFC3339Nano) ||
		verificationOffer.ProfileVerification == nil || *verificationOffer.ProfileVerification != *profile.Verification {
		t.Fatalf("Profile verification offer=%+v err=%v", verificationOffer, err)
	}
	if _, err := repository.AcceptListingExecution(ctx, verificationOffer.Attempt.AttemptID,
		verificationOffer.Attempt.ExecutorActorID, verificationOffer.Attempt.ExecutorIncarnation,
		completedAt.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, verificationOffer.Attempt.AttemptID,
		verificationOffer.Attempt.ExecutorActorID, verificationOffer.Attempt.ExecutorIncarnation,
		completedAt.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	verificationArtifact := mustResultArtifact(t, "profile-repair-verification-artifact", model.ArtifactValidation,
		verificationWork.WorkID, verificationOffer.Attempt.AttemptID)
	verification := ProfileVerificationResult{CommandID: "profile-repair-verification-command",
		RequestHash: "sha256:profile-repair-verification", AttemptID: verificationOffer.Attempt.AttemptID,
		ExecutorActorID:     verificationOffer.Attempt.ExecutorActorID,
		ExecutorIncarnation: verificationOffer.Attempt.ExecutorIncarnation, SessionID: session.SessionID,
		SecurityDomain: profile.SecurityDomain, Authenticated: true, Artifact: verificationArtifact,
		CompletedAt: completedAt.Add(4 * time.Second)}
	wrongDomain := verification
	wrongDomain.CommandID, wrongDomain.RequestHash, wrongDomain.SecurityDomain =
		"profile-repair-verification-wrong-domain", "sha256:profile-repair-verification-wrong-domain", "other.example.test"
	wrongDomain.Artifact = mustResultArtifact(t, "profile-repair-verification-wrong-domain-artifact",
		model.ArtifactValidation, verificationWork.WorkID, verificationOffer.Attempt.AttemptID)
	if _, err := repository.AcceptProfileVerificationResult(ctx, wrongDomain); !errors.Is(err, ErrResultFenced) {
		t.Fatalf("wrong security domain verification was not fenced: %v", err)
	}
	var rejectedArtifacts int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_artifacts WHERE artifact_id = ?`,
		wrongDomain.Artifact.ArtifactID).Scan(&rejectedArtifacts)
	if rejectedArtifacts != 1 {
		t.Fatalf("fenced Profile verification persisted Artifact count=%d", rejectedArtifacts)
	}
	verificationOutcome, err := repository.AcceptProfileVerificationResult(ctx, verification)
	if err != nil || verificationOutcome.Replayed || verificationOutcome.ProfileStatus != model.ProfileVerifying ||
		verificationOutcome.ProfileVersion != 3 || verificationOutcome.SessionStatus != model.ProfileRepairVerified ||
		verificationOutcome.ValidationWork.WorkID != verificationWork.WorkID {
		t.Fatalf("Profile verification=%+v err=%v", verificationOutcome, err)
	}
	verificationReplay, err := repository.AcceptProfileVerificationResult(ctx, verification)
	if err != nil || !verificationReplay.Replayed || verificationReplay.ProfileVersion != 3 {
		t.Fatalf("Profile verification replay=%+v err=%v", verificationReplay, err)
	}
	verifyingProfile, _ := repository.GetProfile(ctx, profile.ProfileID)
	verifiedSession, _ := repository.GetProfileRepairSession(ctx, session.SessionID)
	verifiedWork, _ := repository.GetWork(ctx, verificationWork.WorkID)
	verifiedAttempt, _ := repository.GetAttempt(ctx, verificationOffer.Attempt.AttemptID)
	if verifyingProfile.AuthStatus != model.ProfileVerifying || verifyingProfile.Version != 3 || verifyingProfile.SecretRef != nextSecretRef ||
		verifiedSession.Status != model.ProfileRepairVerified || verifiedSession.Version != retryableSession.Version+3 ||
		verifiedWork.Status != model.WorkCompleted || verifiedAttempt.Status != model.AttemptSucceeded {
		t.Fatalf("verified lifecycle profile=%+v session=%+v work=%+v attempt=%+v",
			verifyingProfile, verifiedSession, verifiedWork, verifiedAttempt)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if err := verifyRepairValidationWork(ctx, tx, incident, verificationWork.WorkID); err != nil {
		_ = tx.Rollback()
		t.Fatalf("verified Profile Work was rejected as Incident evidence: %v", err)
	}
	_ = tx.Rollback()

	validating, _ := incident.BeginValidationWithWork(incident.Version, verificationWork.WorkID)
	runningRepairWork, _ := repairWork.Start(repairWork.Version)
	validationReceipt, validationEvent, validationWorkEvent := repairLifecycleFacts(t,
		"profile-repair-validation-command", "repair.validation_started", validating, runningRepairWork,
		completedAt.Add(5*time.Second))
	if _, err := repository.ApplyRepairValidationCommand(ctx, incident.Version, validating, runningRepairWork,
		validationReceipt, validationEvent, validationWorkEvent, completedAt.Add(5*time.Second)); err != nil {
		t.Fatalf("begin Profile repair validation: %v", err)
	}
	stillVerifying, _ := repository.GetProfile(ctx, profile.ProfileID)
	if stillVerifying.AuthStatus != model.ProfileVerifying || stillVerifying.Version != 3 {
		t.Fatalf("validation begin prematurely reopened Profile=%+v", stillVerifying)
	}
	resolved, _ := validating.Resolve(validating.Version, "credentials rotated and authenticated canary passed")
	completedRepairWork, _ := runningRepairWork.Complete(runningRepairWork.Version, model.ResolutionSucceeded, "", "")
	resolveReceipt, resolveEvent, resolveWorkEvent := repairLifecycleFacts(t,
		"profile-repair-resolve-command", "repair.resolved", resolved, completedRepairWork,
		completedAt.Add(6*time.Second))
	if _, err := repository.ApplyRepairResolveCommand(ctx, validating.Version, resolved, completedRepairWork,
		resolveReceipt, resolveEvent, resolveWorkEvent, completedAt.Add(6*time.Second)); err != nil {
		t.Fatalf("resolve Profile repair: %v", err)
	}
	readyProfile, _ := repository.GetProfile(ctx, profile.ProfileID)
	if readyProfile.AuthStatus != model.ProfileReady || readyProfile.Version != 4 || readyProfile.SecretRef != nextSecretRef {
		t.Fatalf("resolved repair did not reopen Profile=%+v", readyProfile)
	}
	var verificationEvents, wrongArtifacts int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_event_outbox
WHERE cause_command_id = ? AND event_kind = 'profile.repair_session.verified'`,
		verification.CommandID).Scan(&verificationEvents)
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_artifacts WHERE artifact_id = ?`,
		wrongDomain.Artifact.ArtifactID).Scan(&wrongArtifacts)
	if verificationEvents != 1 || wrongArtifacts != 1 {
		t.Fatalf("Profile verification events=%d final artifacts=%d", verificationEvents, wrongArtifacts)
	}

	// Expiry is reconciled durably even when no replacement command arrives.
	expiryProfile, _ := model.NewBrowserProfileWithVerification("profile-repair-expiry-profile", "expiry.example.test",
		profile.DeviceID, "secret://profiles/expiry/v1", testProfileVerificationRecipe("expiry.example.test", "expiry"))
	if err := repository.CreateProfile(ctx, expiryProfile, now); err != nil {
		t.Fatal(err)
	}
	expiryRepairing, _ := expiryProfile.BeginRepair(expiryProfile.Version)
	if err := repository.UpdateProfileCAS(ctx, expiryProfile.Version, expiryRepairing, now); err != nil {
		t.Fatal(err)
	}
	expiryAffected, _ := model.NewWork("profile-repair-expiry-affected", "source", "expiry-source", "listing_sync", "timer")
	if err := repository.CreateWork(ctx, expiryAffected, WorkPlacement{BusinessKey: expiryAffected.WorkID,
		Capability: "browser.recipe", ProfileID: expiryProfile.ProfileID, NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}
	expiryRepairWork, _ := model.NewWork("profile-repair-expiry-incident-work", "repair_incident",
		"profile-repair-expiry-incident", "repair", "automatic")
	if err := repository.CreateWork(ctx, expiryRepairWork, WorkPlacement{BusinessKey: expiryRepairWork.WorkID, NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}
	expiryIncident, _ := model.NewRepairIncident("profile-repair-expiry-incident", model.FailureProfile,
		expiryProfile.ProfileID, "browser.profile.expired", "profile:1", expiryAffected.WorkID)
	expiryIncident, _ = expiryIncident.WithRepairWork(expiryRepairWork.WorkID)
	if _, _, err := repository.OpenOrJoinRepair(ctx, expiryIncident, now); err != nil {
		t.Fatal(err)
	}
	expiryAt := now.Add(time.Minute)
	expirySession, _ := model.NewProfileRepairSession("profile-repair-expiry-session", expiryProfile.ProfileID,
		expiryRepairing.Version, expiryIncident.IncidentID, "profile-repair-expiry-work", "human:operator",
		expiryProfile.DeviceID, expiryAt)
	expiryWork, _ := model.NewWork(expirySession.WorkID, "profile", expiryProfile.ProfileID, "profile_repair", "human")
	expiryWork, _ = expiryWork.WithCausality(expirySession.RequestedBy, "message-profile-expiry", expiryRepairWork.WorkID)
	expiryPlacement := WorkPlacement{BusinessKey: "profile-repair|" + expiryProfile.ProfileID + "|2|" + expirySession.SessionID,
		Capability: ProfileRepairCapability, ProfileID: expiryProfile.ProfileID, NotBefore: now, DeadlineAt: &expiryAt}
	expiryResponse, _ := json.Marshal(map[string]any{"session_id": expirySession.SessionID})
	expiryReceipt, _ := model.NewCommandReceipt("profile-repair-expiry-command", receipt.Word,
		"sha256:profile-repair-expiry", expiryResponse)
	expiryEvent, _ := model.NewEventIntent("profile-repair-expiry-created", event.Kind, "profile_repair_session",
		expirySession.SessionID, expirySession.Version, now.Format(time.RFC3339Nano), expiryReceipt.CommandID, audit)
	expiryDispatch, _ := NewExecutionDispatchIntent("profile-repair-expiry-dispatch", expiryProfile.DeviceID,
		ProfileRepairCapability, "", expiryProfile.ProfileID, "profile_repair_created", expiryReceipt.CommandID, now)
	if _, err := repository.ApplyProfileRepairBeginCommand(ctx, expiryRepairing.Version, expirySession, expiryWork,
		expiryPlacement, expiryReceipt, expiryEvent, expiryDispatch, now); err != nil {
		t.Fatalf("create expiring Profile session: %v", err)
	}
	expiryOffer, err := repository.OfferExecution(ctx, ListingOfferRequest{AttemptID: "profile-repair-failure-attempt",
		ExecutorActorID: expiryProfile.DeviceID + ":1", ExecutorIncarnation: "profile-repair-failure-boot",
		Capability: ProfileRepairCapability, ProfileID: expiryProfile.ProfileID, OfferedAt: now.Add(time.Second),
		BudgetPolicy: testExecutionBudgetPolicy()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repository.AcceptListingExecution(ctx, expiryOffer.Attempt.AttemptID, expiryOffer.Attempt.ExecutorActorID,
		expiryOffer.Attempt.ExecutorIncarnation, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := repository.StartListingExecution(ctx, expiryOffer.Attempt.AttemptID, expiryOffer.Attempt.ExecutorActorID,
		expiryOffer.Attempt.ExecutorIncarnation, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	failureArtifact := mustResultArtifact(t, "profile-repair-control-failure-artifact", model.ArtifactFailure,
		expiryWork.WorkID, expiryOffer.Attempt.AttemptID)
	failure := executioncontract.FailureReport{Class: "transport_timeout", Retryable: true, Artifact: failureArtifact}
	failedCommand := ExecutionTransitionCommand{CommandID: "profile-repair-control-failure-command",
		Word: executioncontract.TypeFailed, RequestHash: "sha256:profile-repair-control-failure",
		CorrelationID: "profile-repair-control-failure-correlation", RequestedBy: expiryOffer.Attempt.ExecutorActorID,
		AttemptID: expiryOffer.Attempt.AttemptID, ExecutorIncarnation: expiryOffer.Attempt.ExecutorIncarnation,
		Action: "fail", Reason: failure.Class, Failure: &failure, FailurePolicy: testExecutionFailurePolicy()}
	if _, err := repository.ApplyExecutionTransitionCommand(ctx, failedCommand, now.Add(4*time.Second)); err != nil {
		t.Fatalf("fail Profile repair control Work: %v", err)
	}
	failedSession, _ := repository.GetProfileRepairSession(ctx, expirySession.SessionID)
	failedWork, _ := repository.GetWork(ctx, expiryWork.WorkID)
	stillRepairing, _ := repository.GetProfile(ctx, expiryProfile.ProfileID)
	var incidentCount int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_repair_incidents WHERE failure_domain = 'profile' AND domain_key = ?`,
		expiryProfile.ProfileID).Scan(&incidentCount)
	if failedSession.Status != model.ProfileRepairFailed || failedWork.Status != model.WorkWaitingHuman ||
		failedWork.BlockedByRepairWorkID != expiryRepairWork.WorkID || stillRepairing.AuthStatus != model.ProfileRepairing ||
		incidentCount != 1 {
		t.Fatalf("Profile repair failure nested or escaped incident: session=%+v work=%+v profile=%+v incidents=%d",
			failedSession, failedWork, stillRepairing, incidentCount)
	}

	replacementStart := now.Add(5 * time.Second)
	replacementExpiry := replacementStart.Add(time.Minute)
	replacementSession, _ := model.NewProfileRepairSession("profile-repair-expiry-replacement-session",
		expiryProfile.ProfileID, expiryRepairing.Version, expiryIncident.IncidentID,
		"profile-repair-expiry-replacement-work", "human:operator", expiryProfile.DeviceID, replacementExpiry)
	replacementWork, _ := model.NewWork(replacementSession.WorkID, "profile", expiryProfile.ProfileID, "profile_repair", "human")
	replacementWork, _ = replacementWork.WithCausality(replacementSession.RequestedBy,
		"message-profile-expiry-replacement", expiryRepairWork.WorkID)
	replacementPlacement := WorkPlacement{BusinessKey: "profile-repair|" + expiryProfile.ProfileID + "|2|" + replacementSession.SessionID,
		Capability: ProfileRepairCapability, ProfileID: expiryProfile.ProfileID,
		NotBefore: replacementStart, DeadlineAt: &replacementExpiry}
	replacementResponse, _ := json.Marshal(map[string]any{"session_id": replacementSession.SessionID})
	replacementReceipt, _ := model.NewCommandReceipt("profile-repair-expiry-replacement-command", receipt.Word,
		"sha256:profile-repair-expiry-replacement", replacementResponse)
	replacementEvent, _ := model.NewEventIntent("profile-repair-expiry-replacement-created", event.Kind,
		"profile_repair_session", replacementSession.SessionID, replacementSession.Version,
		replacementStart.Format(time.RFC3339Nano), replacementReceipt.CommandID, audit)
	replacementDispatch, _ := NewExecutionDispatchIntent("profile-repair-expiry-replacement-dispatch",
		expiryProfile.DeviceID, ProfileRepairCapability, "", expiryProfile.ProfileID, "profile_repair_created",
		replacementReceipt.CommandID, replacementStart)
	if _, err := repository.ApplyProfileRepairBeginCommand(ctx, expiryRepairing.Version, replacementSession,
		replacementWork, replacementPlacement, replacementReceipt, replacementEvent, replacementDispatch,
		replacementStart); err != nil {
		t.Fatalf("replace failed Profile repair session: %v", err)
	}
	expiredResult, err := repository.ExpireProfileRepairSessions(ctx, replacementExpiry, 10)
	if err != nil || expiredResult.Expired != 1 {
		t.Fatalf("expire Profile sessions=%+v err=%v", expiredResult, err)
	}
	expiredSession, _ := repository.GetProfileRepairSession(ctx, replacementSession.SessionID)
	expiredWork, _ := repository.GetWork(ctx, replacementWork.WorkID)
	if expiredSession.Status != model.ProfileRepairExpired || expiredWork.Status != model.WorkCanceled {
		t.Fatalf("expired lifecycle session=%+v work=%+v", expiredSession, expiredWork)
	}
}

func testProfileVerificationRecipe(domain, suffix string) model.ProfileVerificationRecipe {
	return model.ProfileVerificationRecipe{EndpointURL: "https://" + domain + "/private/canary",
		RecipeID: "profile-canary-" + suffix, RecipeVersion: 1, ContentHash: "sha256:" + strings.Repeat("a", 64),
		ContractHash: "sha256:" + strings.Repeat("b", 64), Kind: model.RecipeDetail,
		Execution: model.RecipeExecution{ABIVersion: model.RecipeABIVersion,
			ContentRef: "recipe://profile-canary/" + suffix, RequiredCapability: ProfileRepairCapability,
			Transport: model.RecipeTransportBrowser}, MinimumRecordCount: 1}
}
