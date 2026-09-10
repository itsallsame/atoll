package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

const ProfileRepairCapability = "browser.profile.repair"

func (r *Repository) ApplyProfileRepairBeginCommand(ctx context.Context, expectedProfileVersion uint64,
	session model.ProfileRepairSession, work model.Work, placement WorkPlacement, receipt model.CommandReceipt,
	event model.EventIntent, dispatch ExecutionDispatchIntent, businessAt time.Time) (CommandResult, error) {
	expiresAt, expiryErr := session.Expiry()
	if expiryErr != nil || expectedProfileVersion == 0 || session.ProfileVersion != expectedProfileVersion ||
		session.Status != model.ProfileRepairAwaitingDevice || session.Version != 1 ||
		work.WorkID != session.WorkID || work.TargetType != "profile" || work.TargetID != session.ProfileID ||
		work.Purpose != "profile_repair" || work.Trigger != "human" || work.Status != model.WorkOpen ||
		work.CauseWorkID == "" || work.InitiatorActorID != session.RequestedBy ||
		placement.BusinessKey != "profile-repair|"+session.ProfileID+"|"+strconv.FormatUint(session.ProfileVersion, 10)+"|"+session.SessionID ||
		placement.Capability != ProfileRepairCapability || placement.Origin != "" || placement.ProfileID != session.ProfileID ||
		placement.DeadlineAt == nil || !placement.DeadlineAt.Equal(expiresAt) || !placement.NotBefore.Equal(businessAt) ||
		receipt.CommandID == "" || event.AggregateType != "profile_repair_session" || event.AggregateID != session.SessionID ||
		event.AggregateVersion != session.Version || event.CauseCommandID != receipt.CommandID ||
		dispatch.TargetActorID != session.DeviceActorID || dispatch.Capability != placement.Capability ||
		dispatch.Origin != "" || dispatch.ProfileID != session.ProfileID || dispatch.CauseKind != "profile_repair_created" ||
		dispatch.CauseID != receipt.CommandID || !dispatch.NextAttemptAt.Equal(businessAt) ||
		!executioncontract.ValidToolTarget(session.DeviceActorID) || expiresAt.Sub(businessAt) < time.Minute ||
		expiresAt.Sub(businessAt) > time.Hour {
		return CommandResult{}, fmt.Errorf("Profile repair begin facts are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil || !eventAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("Profile repair begin business time is inconsistent")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	var profileState []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_profiles WHERE profile_id = ? FOR UPDATE`,
		session.ProfileID).Scan(&profileState); errors.Is(err, sql.ErrNoRows) {
		return CommandResult{}, ErrNotFound
	} else if err != nil {
		return CommandResult{}, err
	}
	var profile model.BrowserProfile
	if err := json.Unmarshal(profileState, &profile); err != nil {
		return CommandResult{}, err
	}
	if profile.Version != expectedProfileVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedProfileVersion, Actual: profile.Version}
	}
	if profile.AuthStatus != model.ProfileRepairing || profile.DeviceID != session.DeviceActorID {
		return CommandResult{}, &model.InvalidTransitionError{Entity: "profile", From: string(profile.AuthStatus), Action: "begin secure repair session"}
	}
	for _, publicPayload := range [][]byte{receipt.Response, event.Payload} {
		if jsonPayloadContainsString(publicPayload, profile.SecretRef) || jsonPayloadContainsString(publicPayload, profile.DeviceID) {
			return CommandResult{}, fmt.Errorf("Profile repair public facts contain private Profile references")
		}
	}
	incident, err := getRepairIncidentForUpdate(ctx, tx, session.IncidentID)
	if err != nil {
		return CommandResult{}, err
	}
	failingVersion, failingVersionErr := strconv.ParseUint(strings.TrimPrefix(incident.FailingVersion, "profile:"), 10, 64)
	if incident.Domain != model.FailureProfile || incident.DomainKey != profile.ProfileID ||
		!strings.HasPrefix(incident.FailingVersion, "profile:") || failingVersionErr != nil || failingVersion >= profile.Version ||
		incident.Status != model.RepairOpen || incident.RepairWorkID != work.CauseWorkID {
		return CommandResult{}, ErrRepairEvidenceRejected
	}
	if err := expireStaleProfileRepairSessionTx(ctx, tx, profile.ProfileID, profile.Version,
		receipt.CommandID, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	if err := insertWork(ctx, tx, work, placement, businessAt); err != nil {
		return CommandResult{}, err
	}
	state, _ := json.Marshal(session)
	_, err = tx.ExecContext(ctx, `INSERT INTO recruiting_profile_repair_sessions(
  session_id, active_session_key, profile_id, profile_version, incident_id, work_id, requested_by,
  device_actor_id, attempt_id, validation_work_id, session_status, expires_at, version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, NULL, ?, ?, ?, ?, ?, ?)`, session.SessionID,
		session.ProfileID+"|"+strconv.FormatUint(session.ProfileVersion, 10), session.ProfileID, session.ProfileVersion,
		session.IncidentID, session.WorkID, session.RequestedBy, session.DeviceActorID, session.Status,
		expiresAt, session.Version, state, businessAt.UTC(), businessAt.UTC())
	if err != nil {
		return CommandResult{}, fmt.Errorf("create Profile repair session: %w", err)
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendExecutionDispatch(ctx, tx, dispatch, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, err
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func jsonPayloadContainsString(payload []byte, forbidden string) bool {
	var value any
	if forbidden == "" || json.Unmarshal(payload, &value) != nil {
		return false
	}
	var contains func(any) bool
	contains = func(current any) bool {
		switch typed := current.(type) {
		case string:
			return typed == forbidden
		case []any:
			for _, item := range typed {
				if contains(item) {
					return true
				}
			}
		case map[string]any:
			for _, item := range typed {
				if contains(item) {
					return true
				}
			}
		}
		return false
	}
	return contains(value)
}

func returnProfileToRepairingTx(ctx context.Context, tx *sql.Tx, session model.ProfileRepairSession,
	causeCommandID string, at time.Time) error {
	var state []byte
	err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_profiles WHERE profile_id = ? FOR UPDATE`,
		session.ProfileID).Scan(&state)
	if err != nil {
		return err
	}
	var profile model.BrowserProfile
	if err := json.Unmarshal(state, &profile); err != nil {
		return err
	}
	if profile.AuthStatus != model.ProfileVerifying || profile.Version != session.ProfileVersion+1 ||
		profile.DeviceID != session.DeviceActorID {
		return ErrResultFenced
	}
	next, err := profile.BeginRepair(profile.Version)
	if err != nil {
		return err
	}
	nextState, _ := json.Marshal(next)
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_profiles
SET auth_status = ?, version = ?, state_json = ?, updated_at = ?
WHERE profile_id = ? AND version = ? AND auth_status = ?`, next.AuthStatus, next.Version,
		nextState, at.UTC(), profile.ProfileID, profile.Version, profile.AuthStatus)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrResultFenced
	}
	payload, _ := json.Marshal(map[string]any{"profile_id": profile.ProfileID, "profile_version": next.Version,
		"session_id": session.SessionID, "validation_work_id": session.ValidationWorkID})
	event, err := model.NewEventIntent("profile-verification-failed-"+session.ValidationWorkID,
		"profile.verification_failed", "profile", profile.ProfileID, next.Version,
		at.UTC().Format(time.RFC3339Nano), causeCommandID, payload)
	if err != nil {
		return err
	}
	return appendEventIntent(ctx, tx, event, at, at)
}

// expireStaleProfileRepairSessionTx makes expiry operational, rather than only
// an offer-time check. Starting a replacement session retires an expired Work
// and Attempt in the same transaction, so the unique active-session fence can
// never strand a Profile indefinitely.
func expireStaleProfileRepairSessionTx(ctx context.Context, tx *sql.Tx, profileID string,
	profileVersion uint64, causeCommandID string, at time.Time) error {
	var state []byte
	err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_profile_repair_sessions
WHERE active_session_key = ? FOR UPDATE`, profileID+"|"+strconv.FormatUint(profileVersion, 10)).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var current model.ProfileRepairSession
	if err := json.Unmarshal(state, &current); err != nil {
		return err
	}
	expiresAt, err := current.Expiry()
	if err != nil {
		return err
	}
	if at.Before(expiresAt) {
		return fmt.Errorf("%w: Profile already has an active repair session", ErrAttemptConflict)
	}
	next, err := current.Expire(current.Version, at)
	if err != nil {
		return err
	}
	work, err := getWorkWith(ctx, tx, current.WorkID, true)
	if err != nil {
		return err
	}
	if !work.Terminal() {
		previous := work.Version
		work, err = work.Cancel(work.Version)
		if err != nil {
			return err
		}
		if err := updateWorkTx(ctx, tx, previous, work, at); err != nil {
			return err
		}
	}
	if current.AttemptID != "" {
		attempt, err := getAttemptWith(ctx, tx, current.AttemptID, true)
		if err != nil {
			return err
		}
		if attempt.Status == model.AttemptOffered || attempt.Status == model.AttemptAccepted || attempt.Status == model.AttemptRunning {
			expired, err := attempt.Expire()
			if err != nil {
				return err
			}
			if err := updateAttemptStatusTx(ctx, tx, attempt.Status, expired, nil, at); err != nil {
				return err
			}
		}
	}
	if err := updateProfileRepairSessionTx(ctx, tx, current.Version, next, at); err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]any{"profile_id": current.ProfileID, "work_id": current.WorkID,
		"attempt_id": current.AttemptID, "expired_at": current.ExpiresAt})
	event, err := model.NewEventIntent("profile-repair-session-expired-"+current.SessionID,
		"profile.repair_session.expired", "profile_repair_session", current.SessionID, next.Version,
		at.UTC().Format(time.RFC3339Nano), causeCommandID, payload)
	if err != nil {
		return err
	}
	return appendEventIntent(ctx, tx, event, at, at)
}

func (r *Repository) GetProfileRepairSession(ctx context.Context, sessionID string) (model.ProfileRepairSession, error) {
	var state []byte
	err := r.db.QueryRowContext(ctx, `SELECT state_json FROM recruiting_profile_repair_sessions WHERE session_id = ?`,
		sessionID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ProfileRepairSession{}, ErrNotFound
	}
	if err != nil {
		return model.ProfileRepairSession{}, err
	}
	var session model.ProfileRepairSession
	if err := json.Unmarshal(state, &session); err != nil {
		return model.ProfileRepairSession{}, err
	}
	return session, nil
}

func (r *Repository) GetActiveProfileRepairSession(ctx context.Context, profileID string,
	profileVersion uint64) (model.ProfileRepairSession, error) {
	if profileID == "" || profileVersion == 0 {
		return model.ProfileRepairSession{}, fmt.Errorf("Profile identity and version are required")
	}
	var state []byte
	err := r.db.QueryRowContext(ctx, `SELECT state_json FROM recruiting_profile_repair_sessions
WHERE active_session_key = ?`, profileID+"|"+strconv.FormatUint(profileVersion, 10)).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ProfileRepairSession{}, ErrNotFound
	}
	if err != nil {
		return model.ProfileRepairSession{}, err
	}
	var session model.ProfileRepairSession
	if err := json.Unmarshal(state, &session); err != nil {
		return model.ProfileRepairSession{}, err
	}
	return session, nil
}

func (r *Repository) GetLatestProfileRepairSession(ctx context.Context, profileID string) (model.ProfileRepairSession, error) {
	if strings.TrimSpace(profileID) == "" {
		return model.ProfileRepairSession{}, fmt.Errorf("Profile identity is required")
	}
	var state []byte
	err := r.db.QueryRowContext(ctx, `SELECT state_json FROM recruiting_profile_repair_sessions
WHERE profile_id = ? ORDER BY created_at DESC, session_id DESC LIMIT 1`, profileID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ProfileRepairSession{}, ErrNotFound
	}
	if err != nil {
		return model.ProfileRepairSession{}, err
	}
	var session model.ProfileRepairSession
	if err := json.Unmarshal(state, &session); err != nil {
		return model.ProfileRepairSession{}, err
	}
	return session, nil
}

func loadProfileRepairOfferFence(ctx context.Context, tx *sql.Tx, work model.Work, placement WorkPlacement,
	executorActorID string, at time.Time) (model.ProfileRepairSession, model.AttemptFence, error) {
	if work.Purpose != "profile_repair" || work.TargetType != "profile" || work.TargetID == "" ||
		placement.Capability != ProfileRepairCapability || placement.ProfileID != work.TargetID || at.IsZero() {
		return model.ProfileRepairSession{}, model.AttemptFence{}, fmt.Errorf("Profile repair Work is inconsistent")
	}
	var sessionState []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_profile_repair_sessions
WHERE work_id = ? FOR UPDATE`, work.WorkID).Scan(&sessionState); errors.Is(err, sql.ErrNoRows) {
		return model.ProfileRepairSession{}, model.AttemptFence{}, ErrNotFound
	} else if err != nil {
		return model.ProfileRepairSession{}, model.AttemptFence{}, err
	}
	var session model.ProfileRepairSession
	if err := json.Unmarshal(sessionState, &session); err != nil {
		return model.ProfileRepairSession{}, model.AttemptFence{}, err
	}
	expiresAt, err := session.Expiry()
	if err != nil || session.WorkID != work.WorkID || session.ProfileID != work.TargetID ||
		session.Status != model.ProfileRepairAwaitingDevice || !at.Before(expiresAt) {
		return model.ProfileRepairSession{}, model.AttemptFence{}, fmt.Errorf("Profile repair session is unavailable to this device")
	}
	if !executioncontract.TargetMatchesAuthenticatedActor(session.DeviceActorID, executorActorID) {
		return model.ProfileRepairSession{}, model.AttemptFence{}, ErrNotFound
	}
	var profileState []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_profiles WHERE profile_id = ? FOR UPDATE`,
		session.ProfileID).Scan(&profileState); err != nil {
		return model.ProfileRepairSession{}, model.AttemptFence{}, err
	}
	var profile model.BrowserProfile
	if err := json.Unmarshal(profileState, &profile); err != nil {
		return model.ProfileRepairSession{}, model.AttemptFence{}, err
	}
	if profile.AuthStatus != model.ProfileRepairing || profile.Version != session.ProfileVersion ||
		profile.DeviceID != session.DeviceActorID {
		return model.ProfileRepairSession{}, model.AttemptFence{}, fmt.Errorf("Profile repair session is fenced by changed Profile")
	}
	return session, model.AttemptFence{ProfileID: profile.ProfileID, ProfileVersion: profile.Version}, nil
}

func loadProfileVerificationOfferFence(ctx context.Context, tx *sql.Tx, work model.Work, placement WorkPlacement,
	executorActorID string, at time.Time) (model.ProfileRepairSession, model.AttemptFence, error) {
	if work.Purpose != "profile_verify" || work.TargetType != "profile" || work.TargetID == "" ||
		placement.Capability != ProfileRepairCapability || placement.ProfileID != work.TargetID || at.IsZero() {
		return model.ProfileRepairSession{}, model.AttemptFence{}, fmt.Errorf("Profile verification Work is inconsistent")
	}
	var sessionState []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_profile_repair_sessions
WHERE validation_work_id = ? FOR UPDATE`, work.WorkID).Scan(&sessionState); errors.Is(err, sql.ErrNoRows) {
		return model.ProfileRepairSession{}, model.AttemptFence{}, ErrNotFound
	} else if err != nil {
		return model.ProfileRepairSession{}, model.AttemptFence{}, err
	}
	var session model.ProfileRepairSession
	if err := json.Unmarshal(sessionState, &session); err != nil {
		return model.ProfileRepairSession{}, model.AttemptFence{}, err
	}
	if session.Status != model.ProfileRepairSubmitted || session.ValidationWorkID != work.WorkID ||
		!executioncontract.TargetMatchesAuthenticatedActor(session.DeviceActorID, executorActorID) {
		return model.ProfileRepairSession{}, model.AttemptFence{}, ErrNotFound
	}
	var profileState []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_profiles WHERE profile_id = ? FOR UPDATE`,
		session.ProfileID).Scan(&profileState); err != nil {
		return model.ProfileRepairSession{}, model.AttemptFence{}, err
	}
	var profile model.BrowserProfile
	if err := json.Unmarshal(profileState, &profile); err != nil {
		return model.ProfileRepairSession{}, model.AttemptFence{}, err
	}
	if profile.AuthStatus != model.ProfileVerifying || profile.Version != session.ProfileVersion+1 ||
		profile.DeviceID != session.DeviceActorID {
		return model.ProfileRepairSession{}, model.AttemptFence{}, fmt.Errorf("Profile verification is fenced by changed Profile")
	}
	return session, model.AttemptFence{ProfileID: profile.ProfileID, ProfileVersion: profile.Version}, nil
}

func loadActiveProfileRepairFailureFence(ctx context.Context, tx *sql.Tx, work model.Work, placement WorkPlacement,
	attempt model.Attempt, at time.Time) (model.ProfileRepairSession, model.AttemptFence, error) {
	if work.Purpose != "profile_repair" || work.TargetType != "profile" || placement.ProfileID != work.TargetID || at.IsZero() {
		return model.ProfileRepairSession{}, model.AttemptFence{}, fmt.Errorf("Profile repair failure Work is inconsistent")
	}
	var sessionState []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_profile_repair_sessions
WHERE work_id = ? FOR UPDATE`, work.WorkID).Scan(&sessionState); err != nil {
		return model.ProfileRepairSession{}, model.AttemptFence{}, err
	}
	var session model.ProfileRepairSession
	if err := json.Unmarshal(sessionState, &session); err != nil {
		return model.ProfileRepairSession{}, model.AttemptFence{}, err
	}
	expiresAt, err := session.Expiry()
	if err != nil || session.Status != model.ProfileRepairActive || session.AttemptID != attempt.AttemptID ||
		!at.Before(expiresAt) || !executioncontract.TargetMatchesAuthenticatedActor(session.DeviceActorID, attempt.ExecutorActorID) {
		return model.ProfileRepairSession{}, model.AttemptFence{}, ErrResultFenced
	}
	var profileState []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_profiles WHERE profile_id = ? FOR UPDATE`,
		session.ProfileID).Scan(&profileState); err != nil {
		return model.ProfileRepairSession{}, model.AttemptFence{}, err
	}
	var profile model.BrowserProfile
	if err := json.Unmarshal(profileState, &profile); err != nil {
		return model.ProfileRepairSession{}, model.AttemptFence{}, err
	}
	if profile.AuthStatus != model.ProfileRepairing || profile.Version != session.ProfileVersion ||
		profile.DeviceID != session.DeviceActorID {
		return model.ProfileRepairSession{}, model.AttemptFence{}, ErrResultFenced
	}
	return session, model.AttemptFence{ProfileID: profile.ProfileID, ProfileVersion: profile.Version}, nil
}

func updateProfileRepairSessionTx(ctx context.Context, tx *sql.Tx, expected uint64,
	session model.ProfileRepairSession, at time.Time) error {
	if session.Version != expected+1 || at.IsZero() {
		return fmt.Errorf("Profile repair session update must advance exactly one version")
	}
	state, _ := json.Marshal(session)
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_profile_repair_sessions
SET active_session_key = CASE WHEN ? IN ('awaiting_device', 'active') THEN active_session_key ELSE NULL END,
    attempt_id = ?, validation_work_id = ?, session_status = ?, version = ?, state_json = ?, updated_at = ?
WHERE session_id = ? AND version = ?`, session.Status, nullableString(session.AttemptID),
		nullableString(session.ValidationWorkID), session.Status, session.Version, state, at.UTC(), session.SessionID, expected)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrAttemptConflict
	}
	return nil
}
