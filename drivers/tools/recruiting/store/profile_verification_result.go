package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type ProfileVerificationResult struct {
	CommandID           string
	RequestHash         string
	AttemptID           string
	ExecutorActorID     string
	ExecutorIncarnation string
	SessionID           string
	SecurityDomain      string
	Authenticated       bool
	Artifact            model.ArtifactMetadata
	CompletedAt         time.Time
}

type ProfileVerificationOutcome struct {
	ProfileID      string                           `json:"profile_id"`
	ProfileStatus  model.ProfileAuthStatus          `json:"profile_status"`
	ProfileVersion uint64                           `json:"profile_version"`
	SessionID      string                           `json:"session_id"`
	SessionStatus  model.ProfileRepairSessionStatus `json:"session_status"`
	SessionVersion uint64                           `json:"session_version"`
	ValidationWork model.Work                       `json:"validation_work"`
	Replayed       bool                             `json:"replayed"`
}

func (r *Repository) AcceptProfileVerificationResult(ctx context.Context,
	input ProfileVerificationResult) (ProfileVerificationOutcome, error) {
	outcome, err := r.acceptProfileVerificationResultTransaction(ctx, input)
	if errors.Is(err, ErrResultFenced) {
		if artifactErr := r.saveRejectedArtifact(ctx, input.Artifact, input.CompletedAt); artifactErr != nil {
			return ProfileVerificationOutcome{}, fmt.Errorf("%w; also failed to retain rejected Artifact: %v", err, artifactErr)
		}
	}
	return outcome, err
}

func (r *Repository) acceptProfileVerificationResultTransaction(ctx context.Context,
	input ProfileVerificationResult) (ProfileVerificationOutcome, error) {
	if input.CommandID == "" || input.RequestHash == "" || input.AttemptID == "" ||
		input.ExecutorActorID == "" || input.ExecutorIncarnation == "" || input.SessionID == "" ||
		input.SecurityDomain == "" || !input.Authenticated || input.CompletedAt.IsZero() {
		return ProfileVerificationOutcome{}, fmt.Errorf("successful Profile verification requires complete authenticated evidence")
	}
	if err := validateResultArtifact(input.Artifact, input.AttemptID, model.ArtifactValidation); err != nil ||
		!input.Artifact.Redacted {
		return ProfileVerificationOutcome{}, fmt.Errorf("Profile verification requires a redacted validation Artifact")
	}
	if err := validateOptionalResultCommand(input.CommandID, input.RequestHash); err != nil {
		return ProfileVerificationOutcome{}, err
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ProfileVerificationOutcome{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readResultReceipt[ProfileVerificationOutcome](ctx, tx, input.CommandID, input.RequestHash); err != nil {
		return ProfileVerificationOutcome{}, err
	} else if found {
		replay.Replayed = true
		return replay, nil
	}
	attempt, err := getAttemptWith(ctx, tx, input.AttemptID, true)
	if err != nil {
		return ProfileVerificationOutcome{}, err
	}
	work, err := getWorkWith(ctx, tx, attempt.WorkID, true)
	if err != nil {
		return ProfileVerificationOutcome{}, err
	}
	if work.Purpose != "profile_verify" || work.TargetType != "profile" || input.Artifact.WorkID != work.WorkID {
		return ProfileVerificationOutcome{}, fmt.Errorf("Profile verification Work is inconsistent")
	}
	var sessionState []byte
	err = tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_profile_repair_sessions
WHERE session_id = ? AND validation_work_id = ? FOR UPDATE`, input.SessionID, work.WorkID).Scan(&sessionState)
	if errors.Is(err, sql.ErrNoRows) {
		return ProfileVerificationOutcome{}, ErrNotFound
	}
	if err != nil {
		return ProfileVerificationOutcome{}, err
	}
	var session model.ProfileRepairSession
	if err := json.Unmarshal(sessionState, &session); err != nil {
		return ProfileVerificationOutcome{}, err
	}
	if !executioncontract.TargetMatchesAuthenticatedActor(session.DeviceActorID, input.ExecutorActorID) {
		return ProfileVerificationOutcome{}, ErrNotFound
	}
	var profileState []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_profiles WHERE profile_id = ? FOR UPDATE`,
		session.ProfileID).Scan(&profileState); err != nil {
		return ProfileVerificationOutcome{}, err
	}
	var profile model.BrowserProfile
	if err := json.Unmarshal(profileState, &profile); err != nil {
		return ProfileVerificationOutcome{}, err
	}
	currentFence := model.AttemptFence{ProfileID: profile.ProfileID, ProfileVersion: profile.Version}
	if profile.AuthStatus != model.ProfileVerifying || profile.Version != session.ProfileVersion+1 ||
		profile.SecurityDomain != input.SecurityDomain || profile.DeviceID != session.DeviceActorID {
		return ProfileVerificationOutcome{}, fmt.Errorf("%w: Profile changed before verification", ErrResultFenced)
	}
	if err := canAcceptResultTx(ctx, tx, attempt, work, currentFence, input.ExecutorActorID, input.ExecutorIncarnation); err != nil {
		return ProfileVerificationOutcome{}, fmt.Errorf("%w: %v", ErrResultFenced, err)
	}
	nextSession, err := session.Verify(session.Version, work.WorkID, session.DeviceActorID)
	if err != nil {
		return ProfileVerificationOutcome{}, err
	}
	succeededAttempt, _ := attempt.Succeed()
	completedWork, _ := work.Complete(work.Version, model.ResolutionSucceeded, "", "")
	// A successful canary is evidence for resolving the repair incident. Keep
	// the Profile fenced in verifying until repair.resolve commits the incident,
	// Repair Work, and Profile transition to ready in one transaction.
	outcome := ProfileVerificationOutcome{ProfileID: profile.ProfileID, ProfileStatus: profile.AuthStatus,
		ProfileVersion: profile.Version, SessionID: nextSession.SessionID, SessionStatus: nextSession.Status,
		SessionVersion: nextSession.Version, ValidationWork: completedWork}
	resultState, _ := json.Marshal(outcome)
	if err := insertArtifact(ctx, tx, input.Artifact, false, input.CompletedAt); err != nil {
		return ProfileVerificationOutcome{}, err
	}
	if err := updateProfileRepairSessionTx(ctx, tx, session.Version, nextSession, input.CompletedAt); err != nil {
		return ProfileVerificationOutcome{}, err
	}
	if err := updateAttemptStatusTx(ctx, tx, attempt.Status, succeededAttempt, resultState, input.CompletedAt); err != nil {
		return ProfileVerificationOutcome{}, err
	}
	if err := updateWorkTx(ctx, tx, work.Version, completedWork, input.CompletedAt); err != nil {
		return ProfileVerificationOutcome{}, err
	}
	payload, _ := json.Marshal(map[string]any{"profile_id": profile.ProfileID, "profile_version": profile.Version,
		"session_id": nextSession.SessionID, "session_version": nextSession.Version,
		"validation_work_id": completedWork.WorkID, "attempt_id": succeededAttempt.AttemptID,
		"artifact_id": input.Artifact.ArtifactID, "security_domain": input.SecurityDomain})
	sessionEvent, err := model.NewEventIntent("profile-repair-session-verified-"+attempt.AttemptID,
		"profile.repair_session.verified", "profile_repair_session", nextSession.SessionID, nextSession.Version,
		input.CompletedAt.UTC().Format(time.RFC3339Nano), input.CommandID, payload)
	if err != nil {
		return ProfileVerificationOutcome{}, err
	}
	if err := appendEventIntent(ctx, tx, sessionEvent, input.CompletedAt, input.CompletedAt); err != nil {
		return ProfileVerificationOutcome{}, err
	}
	if err := reserveResultReceipt(ctx, tx, input.CommandID, executioncontract.TypeResult,
		input.RequestHash, outcome, input.CompletedAt); err != nil {
		return ProfileVerificationOutcome{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProfileVerificationOutcome{}, err
	}
	return outcome, nil
}
