package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type ProfileRepairSubmission struct {
	CommandID            string
	RequestHash          string
	AttemptID            string
	ExecutorActorID      string
	ExecutorIncarnation  string
	SessionID            string
	NextSecretRef        string
	Artifact             model.ArtifactMetadata
	VerificationWork     model.Work
	VerificationPlace    WorkPlacement
	VerificationDispatch ExecutionDispatchIntent
	CompletedAt          time.Time
}

type ProfileRepairSubmissionOutcome struct {
	ProfileID         string                           `json:"profile_id"`
	ProfileStatus     model.ProfileAuthStatus          `json:"profile_status"`
	ProfileVersion    uint64                           `json:"profile_version"`
	SessionID         string                           `json:"session_id"`
	SessionStatus     model.ProfileRepairSessionStatus `json:"session_status"`
	SessionVersion    uint64                           `json:"session_version"`
	InteractionWorkID string                           `json:"interaction_work_id"`
	VerificationWork  model.Work                       `json:"verification_work"`
	Replayed          bool                             `json:"replayed"`
}

func (r *Repository) AcceptProfileRepairSubmission(ctx context.Context,
	input ProfileRepairSubmission) (ProfileRepairSubmissionOutcome, error) {
	outcome, err := r.acceptProfileRepairSubmissionTransaction(ctx, input)
	if errors.Is(err, ErrResultFenced) {
		if artifactErr := r.saveRejectedArtifact(ctx, input.Artifact, input.CompletedAt); artifactErr != nil {
			return ProfileRepairSubmissionOutcome{}, fmt.Errorf("%w; also failed to retain rejected Artifact: %v", err, artifactErr)
		}
	}
	return outcome, err
}

func (r *Repository) acceptProfileRepairSubmissionTransaction(ctx context.Context,
	input ProfileRepairSubmission) (ProfileRepairSubmissionOutcome, error) {
	if input.CommandID == "" || input.RequestHash == "" || input.AttemptID == "" ||
		input.ExecutorActorID == "" || input.ExecutorIncarnation == "" || input.SessionID == "" ||
		!validOpaqueSecretRef(input.NextSecretRef) || input.CompletedAt.IsZero() ||
		input.VerificationWork.WorkID == "" || input.VerificationPlace.DeadlineAt == nil {
		return ProfileRepairSubmissionOutcome{}, fmt.Errorf("Profile repair submission requires complete bounded evidence")
	}
	if err := validateResultArtifact(input.Artifact, input.AttemptID, model.ArtifactValidation); err != nil ||
		!input.Artifact.Redacted {
		return ProfileRepairSubmissionOutcome{}, fmt.Errorf("Profile repair submission requires a redacted validation Artifact")
	}
	if err := validateOptionalResultCommand(input.CommandID, input.RequestHash); err != nil {
		return ProfileRepairSubmissionOutcome{}, err
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ProfileRepairSubmissionOutcome{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readResultReceipt[ProfileRepairSubmissionOutcome](ctx, tx, input.CommandID, input.RequestHash); err != nil {
		return ProfileRepairSubmissionOutcome{}, err
	} else if found {
		replay.Replayed = true
		return replay, nil
	}
	attempt, err := getAttemptWith(ctx, tx, input.AttemptID, true)
	if err != nil {
		return ProfileRepairSubmissionOutcome{}, err
	}
	work, err := getWorkWith(ctx, tx, attempt.WorkID, true)
	if err != nil {
		return ProfileRepairSubmissionOutcome{}, err
	}
	if work.Purpose != "profile_repair" || work.TargetType != "profile" || input.Artifact.WorkID != work.WorkID {
		return ProfileRepairSubmissionOutcome{}, fmt.Errorf("Profile repair result Work is inconsistent")
	}
	var sessionState []byte
	err = tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_profile_repair_sessions
WHERE session_id = ? AND work_id = ? FOR UPDATE`, input.SessionID, work.WorkID).Scan(&sessionState)
	if errors.Is(err, sql.ErrNoRows) {
		return ProfileRepairSubmissionOutcome{}, ErrNotFound
	}
	if err != nil {
		return ProfileRepairSubmissionOutcome{}, err
	}
	var session model.ProfileRepairSession
	if err := json.Unmarshal(sessionState, &session); err != nil {
		return ProfileRepairSubmissionOutcome{}, err
	}
	if !executioncontract.TargetMatchesAuthenticatedActor(session.DeviceActorID, input.ExecutorActorID) {
		return ProfileRepairSubmissionOutcome{}, ErrNotFound
	}
	var profileState []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_profiles WHERE profile_id = ? FOR UPDATE`,
		session.ProfileID).Scan(&profileState); err != nil {
		return ProfileRepairSubmissionOutcome{}, err
	}
	var profile model.BrowserProfile
	if err := json.Unmarshal(profileState, &profile); err != nil {
		return ProfileRepairSubmissionOutcome{}, err
	}
	fence := model.AttemptFence{ProfileID: profile.ProfileID, ProfileVersion: profile.Version}
	if profile.AuthStatus != model.ProfileRepairing || profile.Version != session.ProfileVersion ||
		profile.DeviceID != session.DeviceActorID || input.NextSecretRef == profile.SecretRef {
		return ProfileRepairSubmissionOutcome{}, fmt.Errorf("%w: Profile changed before repair submission", ErrResultFenced)
	}
	if err := canAcceptResultTx(ctx, tx, attempt, work, fence, input.ExecutorActorID, input.ExecutorIncarnation); err != nil {
		return ProfileRepairSubmissionOutcome{}, fmt.Errorf("%w: %v", ErrResultFenced, err)
	}
	nextProfile, err := profile.BeginVerification(profile.Version, input.NextSecretRef)
	if err != nil {
		return ProfileRepairSubmissionOutcome{}, err
	}
	nextSession, err := session.Submit(session.Version, attempt.AttemptID, session.DeviceActorID,
		input.VerificationWork.WorkID, input.CompletedAt)
	if err != nil {
		return ProfileRepairSubmissionOutcome{}, err
	}
	if input.VerificationWork.TargetType != "profile" || input.VerificationWork.TargetID != profile.ProfileID ||
		input.VerificationWork.Purpose != "profile_verify" || input.VerificationWork.Trigger != "automatic" ||
		input.VerificationWork.Status != model.WorkOpen || input.VerificationWork.CauseWorkID != work.WorkID ||
		input.VerificationWork.InitiatorActorID != input.ExecutorActorID ||
		input.VerificationPlace.BusinessKey != "profile-verify|"+profile.ProfileID+fmt.Sprintf("|%d", nextProfile.Version) ||
		input.VerificationPlace.Capability != ProfileRepairCapability || input.VerificationPlace.Origin != "" ||
		input.VerificationPlace.ProfileID != profile.ProfileID || !input.VerificationPlace.NotBefore.Equal(input.CompletedAt) ||
		!input.VerificationPlace.DeadlineAt.After(input.CompletedAt) ||
		input.VerificationDispatch.TargetActorID != session.DeviceActorID ||
		input.VerificationDispatch.Capability != ProfileRepairCapability || input.VerificationDispatch.Origin != "" ||
		input.VerificationDispatch.ProfileID != profile.ProfileID ||
		input.VerificationDispatch.CauseKind != "profile_verification_created" ||
		input.VerificationDispatch.CauseID != input.CommandID ||
		!input.VerificationDispatch.NextAttemptAt.Equal(input.CompletedAt) {
		return ProfileRepairSubmissionOutcome{}, fmt.Errorf("Profile verification Work and dispatch are inconsistent")
	}
	succeededAttempt, _ := attempt.Succeed()
	completedWork, _ := work.Complete(work.Version, model.ResolutionSucceeded, "", "")
	outcome := ProfileRepairSubmissionOutcome{ProfileID: nextProfile.ProfileID, ProfileStatus: nextProfile.AuthStatus,
		ProfileVersion: nextProfile.Version, SessionID: nextSession.SessionID, SessionStatus: nextSession.Status,
		SessionVersion: nextSession.Version, InteractionWorkID: completedWork.WorkID,
		VerificationWork: input.VerificationWork}
	resultState, _ := json.Marshal(outcome)
	if err := insertArtifact(ctx, tx, input.Artifact, false, input.CompletedAt); err != nil {
		return ProfileRepairSubmissionOutcome{}, err
	}
	if err := insertWork(ctx, tx, input.VerificationWork, input.VerificationPlace, input.CompletedAt); err != nil {
		return ProfileRepairSubmissionOutcome{}, err
	}
	profileJSON, _ := json.Marshal(nextProfile)
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_profiles
SET secret_ref = ?, auth_status = ?, version = ?, state_json = ?, updated_at = ?
WHERE profile_id = ? AND version = ? AND auth_status = ?`, nextProfile.SecretRef, nextProfile.AuthStatus,
		nextProfile.Version, profileJSON, input.CompletedAt.UTC(), profile.ProfileID, profile.Version, profile.AuthStatus)
	if err != nil {
		return ProfileRepairSubmissionOutcome{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ProfileRepairSubmissionOutcome{}, ErrResultFenced
	}
	if err := updateProfileRepairSessionTx(ctx, tx, session.Version, nextSession, input.CompletedAt); err != nil {
		return ProfileRepairSubmissionOutcome{}, err
	}
	if err := updateAttemptStatusTx(ctx, tx, attempt.Status, succeededAttempt, resultState, input.CompletedAt); err != nil {
		return ProfileRepairSubmissionOutcome{}, err
	}
	if err := updateWorkTx(ctx, tx, work.Version, completedWork, input.CompletedAt); err != nil {
		return ProfileRepairSubmissionOutcome{}, err
	}
	if err := appendExecutionDispatch(ctx, tx, input.VerificationDispatch, input.CompletedAt); err != nil {
		return ProfileRepairSubmissionOutcome{}, err
	}
	payload, _ := json.Marshal(map[string]any{"profile_id": nextProfile.ProfileID, "profile_version": nextProfile.Version,
		"session_id": nextSession.SessionID, "session_version": nextSession.Version,
		"interaction_work_id": completedWork.WorkID, "verification_work_id": input.VerificationWork.WorkID,
		"attempt_id": succeededAttempt.AttemptID, "artifact_id": input.Artifact.ArtifactID})
	profileEvent, err := model.NewEventIntent("profile-repair-submitted-"+attempt.AttemptID,
		"profile.repair_submitted", "profile", nextProfile.ProfileID, nextProfile.Version,
		input.CompletedAt.UTC().Format(time.RFC3339Nano), input.CommandID, payload)
	if err != nil {
		return ProfileRepairSubmissionOutcome{}, err
	}
	if err := appendEventIntent(ctx, tx, profileEvent, input.CompletedAt, input.CompletedAt); err != nil {
		return ProfileRepairSubmissionOutcome{}, err
	}
	sessionEvent, err := model.NewEventIntent("profile-repair-session-submitted-"+attempt.AttemptID,
		"profile.repair_session.submitted", "profile_repair_session", nextSession.SessionID, nextSession.Version,
		input.CompletedAt.UTC().Format(time.RFC3339Nano), input.CommandID, payload)
	if err != nil {
		return ProfileRepairSubmissionOutcome{}, err
	}
	if err := appendEventIntent(ctx, tx, sessionEvent, input.CompletedAt, input.CompletedAt); err != nil {
		return ProfileRepairSubmissionOutcome{}, err
	}
	if err := reserveResultReceipt(ctx, tx, input.CommandID, executioncontract.TypeResult,
		input.RequestHash, outcome, input.CompletedAt); err != nil {
		return ProfileRepairSubmissionOutcome{}, err
	}
	if err := tx.Commit(); err != nil {
		return ProfileRepairSubmissionOutcome{}, err
	}
	return outcome, nil
}

func validOpaqueSecretRef(value string) bool {
	if !strings.HasPrefix(value, "secret://") || len(value) > 512 || strings.TrimSpace(value) != value {
		return false
	}
	for _, character := range value {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return false
		}
	}
	return len(value) > len("secret://")
}
