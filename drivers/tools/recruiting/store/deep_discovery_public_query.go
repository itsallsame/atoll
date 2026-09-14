package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

type PublicQueryVerificationResultOutcome struct {
	Verification model.DeepDiscoveryPublicQueryVerification `json:"verification"`
	Work         model.Work                                 `json:"work"`
	Replayed     bool                                       `json:"replayed"`
}

func (r *Repository) ApplyCreatePublicQueryVerificationCommand(ctx context.Context,
	current, next model.DeepDiscoveryMission, verification model.DeepDiscoveryPublicQueryVerification, work model.Work,
	placement WorkPlacement, receipt model.CommandReceipt, event model.EventIntent,
	dispatch *ExecutionDispatchIntent, at time.Time) (CommandResult, error) {
	if current.MissionID == "" || next.MissionID != current.MissionID || next.Version != current.Version+1 ||
		next.Budget.OperationsUsed != current.Budget.OperationsUsed+1 || verification.MissionID != current.MissionID ||
		verification.MissionVersion != next.Version || verification.WorkID != work.WorkID ||
		verification.Status != model.DeepDiscoveryPublicQueryQueued || verification.Version != 1 ||
		work.TargetType != "deep_discovery_public_query" || work.TargetID != verification.VerificationID ||
		work.Purpose != "deep_discovery_public_query" || work.Status != model.WorkOpen || work.Version != 1 ||
		placement.CompanyID != current.CompanyID || placement.Capability != "http.fetch" ||
		receipt.CommandID == "" || event.AggregateType != "deep_discovery" || event.AggregateID != current.MissionID ||
		event.AggregateVersion != next.Version || event.CauseCommandID != receipt.CommandID || at.IsZero() {
		return CommandResult{}, fmt.Errorf("public query verification command is inconsistent")
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
	lockedMission, err := getDeepDiscoveryMissionWith(ctx, tx, current.MissionID, true)
	if err != nil {
		return CommandResult{}, err
	}
	if lockedMission.Version != current.Version {
		return CommandResult{}, &model.VersionConflictError{Expected: current.Version, Actual: lockedMission.Version}
	}
	calculated, err := lockedMission.ConsumeOperations(current.Version, 1)
	if err != nil || calculated != next {
		return CommandResult{}, fmt.Errorf("public query verification budget transition does not match locked Mission")
	}
	// Completed Probe evidence is immutable. Read it without a row lock so command
	// creation keeps Mission as its only aggregate lock and cannot invert the
	// Probe -> Mission order used while accepting browser results.
	probe, err := getDeepDiscoveryBrowserProbeWith(ctx, tx, verification.ProbeID, false)
	if err != nil || probe.MissionID != current.MissionID || probe.Status != model.DeepDiscoveryProbeCompleted {
		return CommandResult{}, fmt.Errorf("public query verification requires a completed Probe in the same Mission")
	}
	var probeResultRaw []byte
	if err := tx.QueryRowContext(ctx, `SELECT result_json FROM recruiting_deep_discovery_browser_probes WHERE probe_id=?`, probe.ProbeID).
		Scan(&probeResultRaw); err != nil {
		return CommandResult{}, err
	}
	var probeResult DeepDiscoveryBrowserResult
	if err := json.Unmarshal(probeResultRaw, &probeResult); err != nil {
		return CommandResult{}, err
	}
	if err := canonicalizeBrowserResultEvidence(&probeResult); err != nil {
		return CommandResult{}, err
	}
	matched := false
	for _, observation := range probeResult.PublicQueryEvidence {
		matched = matched || verification.Request.Matches(model.PublicQueryRequestEvidence{EndpointURL: observation.EndpointURL,
			Method: observation.Method, Headers: observation.Headers, JSONBody: observation.JSONBody, BodyHash: observation.BodyHash})
	}
	if !matched {
		return CommandResult{}, fmt.Errorf("public query verification request is not frozen Probe evidence")
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, at); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	if err := insertWork(ctx, tx, work, placement, at); err != nil {
		return CommandResult{}, err
	}
	state, _ := json.Marshal(verification)
	requestKeySum := sha256.Sum256([]byte(verification.Request.EndpointURL + "\n" + verification.Request.BodyHash))
	requestKey := "sha256:" + hex.EncodeToString(requestKeySum[:])
	if _, err := tx.ExecContext(ctx, `INSERT INTO recruiting_deep_discovery_public_query_verifications(
verification_id,mission_id,probe_id,work_id,mission_version,request_key,verification_status,version,state_json,result_json,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,?,NULL,?,?)`, verification.VerificationID, verification.MissionID, verification.ProbeID,
		verification.WorkID, verification.MissionVersion, requestKey, verification.Status,
		verification.Version, state, at.UTC(), at.UTC()); err != nil {
		if isDuplicateKey(err) {
			return CommandResult{}, fmt.Errorf("%w: public query verification", ErrBusinessKeyExists)
		}
		return CommandResult{}, err
	}
	missionState, _ := json.Marshal(next)
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_deep_discovery_missions SET version=?,state_json=?,updated_at=?
WHERE mission_id=? AND version=?`, next.Version, missionState, at.UTC(), current.MissionID, current.Version)
	if err != nil {
		return CommandResult{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return CommandResult{}, ErrProgressConflict
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, at); err != nil {
		return CommandResult{}, err
	}
	if err := appendWorkCommandDispatch(ctx, tx, dispatch, placement, receipt.CommandID, "deep_discovery_public_query_queued", at); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, err
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func getPublicQueryVerificationWith(ctx context.Context, executor interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string, lock bool) (model.DeepDiscoveryPublicQueryVerification, error) {
	query := `SELECT state_json FROM recruiting_deep_discovery_public_query_verifications WHERE verification_id=?`
	if lock {
		query += " FOR UPDATE"
	}
	var raw []byte
	if err := executor.QueryRowContext(ctx, query, strings.TrimSpace(id)).Scan(&raw); errors.Is(err, sql.ErrNoRows) {
		return model.DeepDiscoveryPublicQueryVerification{}, ErrNotFound
	} else if err != nil {
		return model.DeepDiscoveryPublicQueryVerification{}, err
	}
	var value model.DeepDiscoveryPublicQueryVerification
	if err := json.Unmarshal(raw, &value); err != nil {
		return model.DeepDiscoveryPublicQueryVerification{}, err
	}
	return value, nil
}

func (r *Repository) GetPublicQueryVerification(ctx context.Context, id string) (model.DeepDiscoveryPublicQueryVerification, model.Work, error) {
	verification, err := getPublicQueryVerificationWith(ctx, r.db, id, false)
	if err != nil {
		return model.DeepDiscoveryPublicQueryVerification{}, model.Work{}, err
	}
	work, err := r.GetWork(ctx, verification.WorkID)
	return verification, work, err
}

func loadVerifiedPublicQueryStubRefsTx(ctx context.Context, tx *sql.Tx,
	probe model.DeepDiscoveryBrowserProbe) ([]executioncontract.VerifiedPublicQueryStubRef, error) {
	refs := make([]executioncontract.VerifiedPublicQueryStubRef, 0, len(probe.StubVerificationIDs))
	for _, verificationID := range probe.StubVerificationIDs {
		verification, err := getPublicQueryVerificationWith(ctx, tx, verificationID, true)
		if err != nil || verification.MissionID != probe.MissionID ||
			verification.Status != model.DeepDiscoveryPublicQueryCompleted || verification.Artifact == nil {
			return nil, fmt.Errorf("browser Probe references unavailable verified public query %q", verificationID)
		}
		refs = append(refs, executioncontract.VerifiedPublicQueryStubRef{VerificationID: verification.VerificationID,
			Request: recipeabi.PublicQueryObservation{EndpointURL: verification.Request.EndpointURL, Method: verification.Request.Method,
				Headers: verification.Request.Headers, JSONBody: verification.Request.JSONBody, BodyHash: verification.Request.BodyHash},
			Artifact: *verification.Artifact, StatusCode: verification.StatusCode,
			ContentType: verification.ContentType})
	}
	return refs, nil
}

type PublicQueryVerificationResult struct {
	CommandID                string
	RequestHash              string
	AttemptID                string
	ExecutorActorID          string
	ExecutorIncarnation      string
	Artifact                 model.ArtifactMetadata
	StatusCode               int
	ContentType              string
	ContentHash              string
	RequestEndpointURL       string
	RequestBodyHash          string
	ResponsePreview          json.RawMessage
	ResponsePreviewTruncated bool
	ObservedAt               time.Time
}

func (r *Repository) AcceptPublicQueryVerificationResult(ctx context.Context,
	input PublicQueryVerificationResult) (PublicQueryVerificationResultOutcome, error) {
	outcome, err := r.acceptPublicQueryVerificationResultTx(ctx, input)
	if errors.Is(err, ErrResultFenced) {
		if artifactErr := r.saveRejectedArtifacts(ctx, []model.ArtifactMetadata{input.Artifact}, input.ObservedAt); artifactErr != nil {
			return PublicQueryVerificationResultOutcome{}, fmt.Errorf("%w: %v; retain rejected verification evidence: %v",
				ErrResultFenced, err, artifactErr)
		}
	}
	return outcome, err
}

func (r *Repository) acceptPublicQueryVerificationResultTx(ctx context.Context,
	input PublicQueryVerificationResult) (PublicQueryVerificationResultOutcome, error) {
	if input.CommandID == "" || input.RequestHash == "" || input.AttemptID == "" || input.ExecutorActorID == "" ||
		input.ExecutorIncarnation == "" || input.ObservedAt.IsZero() || input.ContentHash != input.Artifact.ContentHash {
		return PublicQueryVerificationResultOutcome{}, fmt.Errorf("public query verification result requires complete bound evidence")
	}
	if err := validateResultArtifact(input.Artifact, input.AttemptID, model.ArtifactResponse); err != nil {
		return PublicQueryVerificationResultOutcome{}, err
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return PublicQueryVerificationResultOutcome{}, err
	}
	defer func() { _ = tx.Rollback() }()
	attempt, err := getAttemptWith(ctx, tx, input.AttemptID, true)
	if err != nil {
		return PublicQueryVerificationResultOutcome{}, err
	}
	work, err := getWorkWith(ctx, tx, attempt.WorkID, true)
	if err != nil {
		return PublicQueryVerificationResultOutcome{}, err
	}
	if replay, found, err := readResultReceipt[PublicQueryVerificationResultOutcome](ctx, tx, input.CommandID, input.RequestHash); err != nil {
		return PublicQueryVerificationResultOutcome{}, err
	} else if found {
		replay.Replayed = true
		return replay, nil
	}
	if work.Purpose != "deep_discovery_public_query" || work.TargetType != "deep_discovery_public_query" ||
		input.Artifact.WorkID != work.WorkID {
		return PublicQueryVerificationResultOutcome{}, fmt.Errorf("public query verification Work is inconsistent")
	}
	if err := ensureBudgetPermitActiveTx(ctx, tx, attempt.AttemptID, input.ObservedAt); err != nil {
		return PublicQueryVerificationResultOutcome{}, fmt.Errorf("%w: %v", ErrResultFenced, err)
	}
	verification, err := getPublicQueryVerificationWith(ctx, tx, work.TargetID, true)
	if err != nil {
		return PublicQueryVerificationResultOutcome{}, err
	}
	if input.RequestEndpointURL != verification.Request.EndpointURL || input.RequestBodyHash != verification.Request.BodyHash {
		return PublicQueryVerificationResultOutcome{}, fmt.Errorf("public query verification result request fence changed")
	}
	mission, err := getDeepDiscoveryMissionWith(ctx, tx, verification.MissionID, true)
	if err != nil || mission.Status == model.DeepDiscoveryDone || mission.Status == model.DeepDiscoveryCanceled {
		return PublicQueryVerificationResultOutcome{}, fmt.Errorf("%w: public query verification Mission is closed", ErrResultFenced)
	}
	var fence model.AttemptFence
	if err := attachCurrentScopeFencesTx(ctx, tx, work.WorkID, &fence); err != nil {
		return PublicQueryVerificationResultOutcome{}, err
	}
	if err := canAcceptResultTx(ctx, tx, attempt, work, fence, input.ExecutorActorID, input.ExecutorIncarnation); err != nil {
		return PublicQueryVerificationResultOutcome{}, fmt.Errorf("%w: %v", ErrResultFenced, err)
	}
	next, err := verification.Complete(verification.Version, input.Artifact, input.StatusCode, input.ContentType,
		input.ResponsePreview, input.ResponsePreviewTruncated)
	if err != nil {
		return PublicQueryVerificationResultOutcome{}, err
	}
	if err := insertArtifact(ctx, tx, input.Artifact, false, input.ObservedAt); err != nil {
		return PublicQueryVerificationResultOutcome{}, err
	}
	nextState, _ := json.Marshal(next)
	resultJSON, _ := json.Marshal(input)
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_deep_discovery_public_query_verifications
SET verification_status=?,version=?,state_json=?,result_json=?,updated_at=? WHERE verification_id=? AND version=?`,
		next.Status, next.Version, nextState, resultJSON, input.ObservedAt.UTC(), next.VerificationID, verification.Version)
	if err != nil {
		return PublicQueryVerificationResultOutcome{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return PublicQueryVerificationResultOutcome{}, ErrProgressConflict
	}
	succeededAttempt, err := attempt.Succeed()
	if err != nil {
		return PublicQueryVerificationResultOutcome{}, err
	}
	completedWork, err := work.Complete(work.Version, model.ResolutionSucceeded, "", "")
	if err != nil {
		return PublicQueryVerificationResultOutcome{}, err
	}
	attemptResult, _ := json.Marshal(map[string]any{"verification_id": next.VerificationID, "artifact_id": input.Artifact.ArtifactID})
	if err := updateAttemptStatusTx(ctx, tx, attempt.Status, succeededAttempt, attemptResult, input.ObservedAt); err != nil {
		return PublicQueryVerificationResultOutcome{}, err
	}
	if err := updateWorkTx(ctx, tx, work.Version, completedWork, input.ObservedAt); err != nil {
		return PublicQueryVerificationResultOutcome{}, err
	}
	if err := releaseBudgetPermitTx(ctx, tx, attempt.AttemptID, model.PermitReleased, input.ObservedAt); err != nil {
		return PublicQueryVerificationResultOutcome{}, err
	}
	eventPayload, _ := json.Marshal(map[string]any{"verification_id": next.VerificationID, "artifact_id": input.Artifact.ArtifactID,
		"request_body_hash": next.Request.BodyHash, "response_content_hash": input.ContentHash})
	event, err := model.NewEventIntent("deep-discovery-public-query-completed-"+attempt.AttemptID,
		"deep.discovery.public_query.completed", "deep_discovery_public_query", next.VerificationID, next.Version,
		input.ObservedAt.Format(time.RFC3339Nano), input.CommandID, eventPayload)
	if err != nil {
		return PublicQueryVerificationResultOutcome{}, err
	}
	if err := appendEventIntent(ctx, tx, event, input.ObservedAt, input.ObservedAt); err != nil {
		return PublicQueryVerificationResultOutcome{}, fmt.Errorf("append public query verification event: %w", err)
	}
	if err := appendAttemptDispatch(ctx, tx, succeededAttempt.AttemptID, succeededAttempt.ExecutorActorID,
		succeededAttempt.Capability, "", "capacity_released", input.CommandID, input.ObservedAt, input.ObservedAt); err != nil {
		return PublicQueryVerificationResultOutcome{}, err
	}
	outcome := PublicQueryVerificationResultOutcome{Verification: next, Work: completedWork}
	if err := reserveResultReceipt(ctx, tx, input.CommandID, executioncontract.TypeResult, input.RequestHash, outcome, input.ObservedAt); err != nil {
		return PublicQueryVerificationResultOutcome{}, err
	}
	if err := tx.Commit(); err != nil {
		return PublicQueryVerificationResultOutcome{}, err
	}
	return outcome, nil
}
