package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruitingexecutor/recipeabi"
)

type DeepDiscoveryBrowserResultOutcome struct {
	Probe    model.DeepDiscoveryBrowserProbe `json:"probe"`
	Work     model.Work                      `json:"work"`
	Replayed bool                            `json:"replayed"`
}

func (r *Repository) ApplyCreateDeepDiscoveryBrowserProbeCommand(ctx context.Context,
	current, next model.DeepDiscoveryMission, probe model.DeepDiscoveryBrowserProbe, work model.Work,
	placement WorkPlacement, receipt model.CommandReceipt, event model.EventIntent,
	dispatch *ExecutionDispatchIntent, at time.Time) (CommandResult, error) {
	if current.MissionID == "" || next.MissionID != current.MissionID || next.Version != current.Version+1 ||
		next.Budget.OperationsUsed != current.Budget.OperationsUsed+1 || probe.MissionID != current.MissionID ||
		probe.MissionVersion != next.Version || probe.WorkID != work.WorkID || probe.Status != model.DeepDiscoveryProbeQueued ||
		probe.Version != 1 || work.TargetType != "deep_discovery_probe" || work.TargetID != probe.ProbeID ||
		work.Purpose != "deep_discovery_browser" || work.Version != 1 || work.Status != model.WorkOpen ||
		placement.CompanyID != current.CompanyID || placement.Capability != "browser.public" ||
		receipt.CommandID == "" || event.AggregateType != "deep_discovery" || event.AggregateID != current.MissionID ||
		event.AggregateVersion != next.Version || event.CauseCommandID != receipt.CommandID || at.IsZero() {
		return CommandResult{}, fmt.Errorf("deep discovery browser probe command is inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil {
		return CommandResult{}, err
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
	locked, err := getDeepDiscoveryMissionWith(ctx, tx, current.MissionID, true)
	if err != nil {
		return CommandResult{}, err
	}
	if locked.Version != current.Version {
		return CommandResult{}, &model.VersionConflictError{Expected: current.Version, Actual: locked.Version}
	}
	calculated, err := locked.ConsumeOperations(current.Version, 1)
	if err != nil || calculated != next {
		return CommandResult{}, fmt.Errorf("deep discovery browser budget transition does not match locked state")
	}
	seenStubRequests := make(map[string]string, len(probe.StubVerificationIDs))
	for _, verificationID := range probe.StubVerificationIDs {
		// Completed verification evidence is immutable. The Mission lock fences
		// this command; taking verification locks here would invert the result
		// acceptance lock order (verification -> Mission).
		verification, verificationErr := getPublicQueryVerificationWith(ctx, tx, verificationID, false)
		if verificationErr != nil || verification.MissionID != current.MissionID ||
			verification.Status != model.DeepDiscoveryPublicQueryCompleted || verification.Artifact == nil {
			return CommandResult{}, fmt.Errorf("browser Probe Stub requires a completed public query verification in the same Mission")
		}
		canonical, canonicalErr := (recipeabi.PublicQueryObservation{EndpointURL: verification.Request.EndpointURL,
			Method: verification.Request.Method, Headers: verification.Request.Headers,
			JSONBody: verification.Request.JSONBody, BodyHash: verification.Request.BodyHash}).Canonicalized()
		if canonicalErr != nil {
			return CommandResult{}, fmt.Errorf("browser Probe Stub verification is invalid: %w", canonicalErr)
		}
		key := canonical.EndpointURL + "\n" + canonical.BodyHash
		if previous, duplicate := seenStubRequests[key]; duplicate {
			return CommandResult{}, fmt.Errorf("browser Probe Stub verifications %q and %q describe the same canonical request",
				previous, verificationID)
		}
		seenStubRequests[key] = verificationID
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
	probeState, _ := json.Marshal(probe)
	if _, err := tx.ExecContext(ctx, `INSERT INTO recruiting_deep_discovery_browser_probes(
probe_id,mission_id,work_id,mission_version,probe_status,target_url,version,state_json,result_json,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,?,NULL,?,?)`, probe.ProbeID, probe.MissionID, probe.WorkID, probe.MissionVersion,
		probe.Status, probe.URL, probe.Version, probeState, at.UTC(), at.UTC()); err != nil {
		if isDuplicateKey(err) {
			return CommandResult{}, fmt.Errorf("%w: deep discovery browser probe", ErrBusinessKeyExists)
		}
		return CommandResult{}, err
	}
	missionState, _ := json.Marshal(next)
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_deep_discovery_missions
SET version=?,state_json=?,updated_at=? WHERE mission_id=? AND version=?`, next.Version, missionState,
		at.UTC(), current.MissionID, current.Version)
	if err != nil {
		return CommandResult{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return CommandResult{}, &model.VersionConflictError{Expected: current.Version, Actual: locked.Version}
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, at); err != nil {
		return CommandResult{}, err
	}
	if err := appendWorkCommandDispatch(ctx, tx, dispatch, placement, receipt.CommandID, "deep_discovery_browser_queued", at); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, err
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func (r *Repository) GetDeepDiscoveryBrowserProbe(ctx context.Context, id string) (model.DeepDiscoveryBrowserProbe, error) {
	return getDeepDiscoveryBrowserProbeWith(ctx, r.db, strings.TrimSpace(id), false)
}

func (r *Repository) GetDeepDiscoveryBrowserResult(ctx context.Context, id string) (model.DeepDiscoveryBrowserProbe, model.Work, *DeepDiscoveryBrowserResult, error) {
	var probeRaw, workRaw, resultRaw []byte
	err := r.db.QueryRowContext(ctx, `SELECT p.state_json,w.state_json,p.result_json
FROM recruiting_deep_discovery_browser_probes p
JOIN recruiting_works w ON w.work_id=p.work_id
WHERE p.probe_id=?`, strings.TrimSpace(id)).Scan(&probeRaw, &workRaw, &resultRaw)
	if errors.Is(err, sql.ErrNoRows) {
		return model.DeepDiscoveryBrowserProbe{}, model.Work{}, nil, ErrNotFound
	}
	if err != nil {
		return model.DeepDiscoveryBrowserProbe{}, model.Work{}, nil, err
	}
	var probe model.DeepDiscoveryBrowserProbe
	if err := json.Unmarshal(probeRaw, &probe); err != nil {
		return model.DeepDiscoveryBrowserProbe{}, model.Work{}, nil, err
	}
	var work model.Work
	if err := json.Unmarshal(workRaw, &work); err != nil {
		return model.DeepDiscoveryBrowserProbe{}, model.Work{}, nil, err
	}
	if len(resultRaw) == 0 {
		return probe, work, nil, nil
	}
	var result DeepDiscoveryBrowserResult
	if err := json.Unmarshal(resultRaw, &result); err != nil {
		return model.DeepDiscoveryBrowserProbe{}, model.Work{}, nil, err
	}
	if err := canonicalizeBrowserResultEvidence(&result); err != nil {
		return model.DeepDiscoveryBrowserProbe{}, model.Work{}, nil, err
	}
	return probe, work, &result, nil
}

func getDeepDiscoveryBrowserProbeWith(ctx context.Context, executor interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, id string, lock bool) (model.DeepDiscoveryBrowserProbe, error) {
	query := "SELECT state_json FROM recruiting_deep_discovery_browser_probes WHERE probe_id = ?"
	if lock {
		query += " FOR UPDATE"
	}
	var raw []byte
	if err := executor.QueryRowContext(ctx, query, id).Scan(&raw); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.DeepDiscoveryBrowserProbe{}, ErrNotFound
		}
		return model.DeepDiscoveryBrowserProbe{}, err
	}
	var probe model.DeepDiscoveryBrowserProbe
	if err := json.Unmarshal(raw, &probe); err != nil {
		return model.DeepDiscoveryBrowserProbe{}, err
	}
	return probe, nil
}

type DeepDiscoveryBrowserResult struct {
	CommandID           string
	RequestHash         string
	AttemptID           string
	ExecutorActorID     string
	ExecutorIncarnation string
	Artifact            model.ArtifactMetadata
	SupportingArtifacts []model.ArtifactMetadata
	FinalURL            string
	ContentHash         string
	Links               []executioncontract.DeepDiscoveryLink
	DOMPreview          []executioncontract.DeepDiscoveryDOMElement
	PublicQueryEvidence []recipeabi.PublicQueryObservation
	Attestation         executioncontract.DeepDiscoveryEffectAttestation
	ObservedAt          time.Time
}

func (r *Repository) AcceptDeepDiscoveryBrowserResult(ctx context.Context, input DeepDiscoveryBrowserResult) (DeepDiscoveryBrowserResultOutcome, error) {
	if input.CommandID == "" || input.RequestHash == "" || input.AttemptID == "" || input.ExecutorActorID == "" ||
		input.ExecutorIncarnation == "" || input.ObservedAt.IsZero() || len(input.Links) > 200 || len(input.DOMPreview) > 400 ||
		len(input.PublicQueryEvidence) > 20 || len(input.SupportingArtifacts) > 9 {
		return DeepDiscoveryBrowserResultOutcome{}, fmt.Errorf("deep discovery browser result requires bounded execution evidence")
	}
	if err := validateResultArtifact(input.Artifact, input.AttemptID, model.ArtifactResponse); err != nil {
		return DeepDiscoveryBrowserResultOutcome{}, err
	}
	if !strings.HasPrefix(input.ContentHash, "sha256:") || len(input.ContentHash) != len("sha256:")+64 {
		return DeepDiscoveryBrowserResultOutcome{}, fmt.Errorf("deep discovery browser result requires SHA-256 content hash")
	}
	if input.Artifact.ContentHash != input.ContentHash {
		return DeepDiscoveryBrowserResultOutcome{}, fmt.Errorf("deep discovery browser content hash does not match its response Artifact")
	}
	canonicalFinalURL, err := model.CanonicalHTTPURL(input.FinalURL)
	if err != nil {
		return DeepDiscoveryBrowserResultOutcome{}, err
	}
	if canonicalFinalURL != input.FinalURL {
		return DeepDiscoveryBrowserResultOutcome{}, fmt.Errorf("deep discovery browser final URL is not canonical")
	}
	if err := input.Attestation.Validate(2); err != nil {
		return DeepDiscoveryBrowserResultOutcome{}, err
	}
	seen := map[string]struct{}{}
	for _, link := range input.Links {
		parsed, err := url.Parse(link.URL)
		canonical, canonicalErr := model.CanonicalHTTPURL(link.URL)
		if err != nil || canonicalErr != nil || canonical != link.URL || parsed.User != nil ||
			(parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" || len(link.Text) > 200 {
			return DeepDiscoveryBrowserResultOutcome{}, fmt.Errorf("deep discovery browser result contains invalid link")
		}
		if _, duplicate := seen[link.URL]; duplicate {
			return DeepDiscoveryBrowserResultOutcome{}, fmt.Errorf("deep discovery browser result contains duplicate link")
		}
		seen[link.URL] = struct{}{}
	}
	for _, element := range input.DOMPreview {
		if element.Tag == "" || len(element.Tag) > 32 || len(element.Text) > 200 || len(element.Attributes) > 12 {
			return DeepDiscoveryBrowserResultOutcome{}, fmt.Errorf("deep discovery browser result contains invalid DOM preview element")
		}
		for name, value := range element.Attributes {
			if name == "" || len(name) > 64 || len(value) > 256 {
				return DeepDiscoveryBrowserResultOutcome{}, fmt.Errorf("deep discovery browser result contains invalid DOM preview attribute")
			}
		}
	}
	seenQueries := map[string]struct{}{}
	queryEvidenceBytes := 0
	for index, observation := range input.PublicQueryEvidence {
		if err := observation.Validate(); err != nil {
			return DeepDiscoveryBrowserResultOutcome{}, err
		}
		canonical, err := observation.Canonicalized()
		if err != nil {
			return DeepDiscoveryBrowserResultOutcome{}, err
		}
		input.PublicQueryEvidence[index] = canonical
		observation = canonical
		key := observation.EndpointURL + "\n" + observation.BodyHash
		if _, duplicate := seenQueries[key]; duplicate {
			return DeepDiscoveryBrowserResultOutcome{}, fmt.Errorf("deep discovery browser result contains duplicate public-query evidence")
		}
		seenQueries[key] = struct{}{}
		queryEvidenceBytes += observation.EncodedSize()
		if queryEvidenceBytes > recipeabi.MaxPublicQueryEvidenceBytes {
			return DeepDiscoveryBrowserResultOutcome{}, fmt.Errorf("deep discovery browser public-query evidence exceeds its total byte bound")
		}
	}
	outcome, err := r.acceptDeepDiscoveryBrowserResultTx(ctx, input)
	if errors.Is(err, ErrResultFenced) {
		artifacts := append([]model.ArtifactMetadata{input.Artifact}, input.SupportingArtifacts...)
		if artifactErr := r.saveRejectedArtifacts(ctx, artifacts, input.ObservedAt); artifactErr != nil {
			return DeepDiscoveryBrowserResultOutcome{}, fmt.Errorf("%w: %v; retain rejected browser evidence: %v", ErrResultFenced, err, artifactErr)
		}
	}
	return outcome, err
}

func canonicalizeBrowserResultEvidence(result *DeepDiscoveryBrowserResult) error {
	// Public-query policy can become stricter after immutable browser evidence
	// has already been stored.  Historical observations remain available in
	// their Artifact, but an observation that no longer satisfies the current
	// read-only contract must not make the whole Mission projection unreadable
	// (or, worse, become executable again).  Only current-policy observations
	// enter the automation projection.
	canonicalEvidence := make([]recipeabi.PublicQueryObservation, 0, len(result.PublicQueryEvidence))
	for _, observation := range result.PublicQueryEvidence {
		canonical, err := observation.Canonicalized()
		if err != nil {
			continue
		}
		canonicalEvidence = append(canonicalEvidence, canonical)
	}
	result.PublicQueryEvidence = canonicalEvidence
	return nil
}

func (r *Repository) acceptDeepDiscoveryBrowserResultTx(ctx context.Context, input DeepDiscoveryBrowserResult) (DeepDiscoveryBrowserResultOutcome, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return DeepDiscoveryBrowserResultOutcome{}, err
	}
	defer func() { _ = tx.Rollback() }()
	attempt, err := getAttemptWith(ctx, tx, input.AttemptID, true)
	if err != nil {
		return DeepDiscoveryBrowserResultOutcome{}, err
	}
	work, err := getWorkWith(ctx, tx, attempt.WorkID, true)
	if err != nil {
		return DeepDiscoveryBrowserResultOutcome{}, err
	}
	if replay, found, err := readResultReceipt[DeepDiscoveryBrowserResultOutcome](ctx, tx, input.CommandID, input.RequestHash); err != nil {
		return DeepDiscoveryBrowserResultOutcome{}, err
	} else if found {
		replay.Replayed = true
		return replay, nil
	}
	if work.Purpose != "deep_discovery_browser" || work.TargetType != "deep_discovery_probe" || input.Artifact.WorkID != work.WorkID {
		return DeepDiscoveryBrowserResultOutcome{}, fmt.Errorf("deep discovery browser result Work is inconsistent")
	}
	if err := ensureBudgetPermitActiveTx(ctx, tx, attempt.AttemptID, input.ObservedAt); err != nil {
		return DeepDiscoveryBrowserResultOutcome{}, fmt.Errorf("%w: %v", ErrResultFenced, err)
	}
	probe, err := getDeepDiscoveryBrowserProbeWith(ctx, tx, work.TargetID, true)
	if err != nil {
		return DeepDiscoveryBrowserResultOutcome{}, err
	}
	maxNavigations := 1
	if probe.FollowLinkSelector != "" {
		maxNavigations = 2
	}
	if err := input.Attestation.Validate(maxNavigations); err != nil {
		return DeepDiscoveryBrowserResultOutcome{}, err
	}
	allowedStubHashes := make(map[string]struct{}, len(probe.StubVerificationIDs))
	for _, verificationID := range probe.StubVerificationIDs {
		verification, verificationErr := getPublicQueryVerificationWith(ctx, tx, verificationID, true)
		if verificationErr != nil || verification.MissionID != probe.MissionID ||
			verification.Status != model.DeepDiscoveryPublicQueryCompleted || verification.Artifact == nil {
			return DeepDiscoveryBrowserResultOutcome{}, fmt.Errorf("deep discovery browser Stub verification is no longer available")
		}
		request, canonicalErr := (recipeabi.PublicQueryObservation{
			EndpointURL: verification.Request.EndpointURL,
			Method:      verification.Request.Method,
			Headers:     verification.Request.Headers,
			JSONBody:    verification.Request.JSONBody,
			BodyHash:    verification.Request.BodyHash,
		}).Canonicalized()
		if canonicalErr != nil {
			return DeepDiscoveryBrowserResultOutcome{}, fmt.Errorf("deep discovery browser Stub verification is invalid: %w", canonicalErr)
		}
		bindingSum := sha256.Sum256([]byte(request.EndpointURL + "\n" + request.BodyHash + "\n" +
			verification.Artifact.ContentHash))
		allowedStubHashes["sha256:"+hex.EncodeToString(bindingSum[:])] = struct{}{}
	}
	for _, fulfilledHash := range input.Attestation.FulfilledPublicQueryHashes {
		if _, allowed := allowedStubHashes[fulfilledHash]; !allowed {
			return DeepDiscoveryBrowserResultOutcome{}, fmt.Errorf("deep discovery browser fulfilled an unbound Stub response")
		}
	}
	mission, err := getDeepDiscoveryMissionWith(ctx, tx, probe.MissionID, true)
	if err != nil {
		return DeepDiscoveryBrowserResultOutcome{}, err
	}
	if mission.Status == model.DeepDiscoveryDone || mission.Status == model.DeepDiscoveryCanceled {
		return DeepDiscoveryBrowserResultOutcome{}, fmt.Errorf("%w: deep discovery mission is closed", ErrResultFenced)
	}
	var currentFence model.AttemptFence
	if err := attachCurrentScopeFencesTx(ctx, tx, work.WorkID, &currentFence); err != nil {
		return DeepDiscoveryBrowserResultOutcome{}, err
	}
	if err := canAcceptResultTx(ctx, tx, attempt, work, currentFence, input.ExecutorActorID, input.ExecutorIncarnation); err != nil {
		return DeepDiscoveryBrowserResultOutcome{}, fmt.Errorf("%w: %v", ErrResultFenced, err)
	}
	allArtifacts := append([]model.ArtifactMetadata{input.Artifact}, input.SupportingArtifacts...)
	seenArtifacts := map[string]struct{}{}
	for index, artifact := range allArtifacts {
		if artifact.WorkID != work.WorkID || artifact.AttemptID != attempt.AttemptID {
			return DeepDiscoveryBrowserResultOutcome{}, fmt.Errorf("deep discovery browser Artifact is unbound")
		}
		wantKind := model.ArtifactResponse
		if index > 0 {
			wantKind = model.ArtifactTrace
		}
		if err := validateResultArtifact(artifact, input.AttemptID, wantKind); err != nil {
			return DeepDiscoveryBrowserResultOutcome{}, err
		}
		if _, duplicate := seenArtifacts[artifact.ArtifactID]; duplicate {
			return DeepDiscoveryBrowserResultOutcome{}, fmt.Errorf("deep discovery browser Artifacts must be unique")
		}
		seenArtifacts[artifact.ArtifactID] = struct{}{}
		if err := insertArtifact(ctx, tx, artifact, false, input.ObservedAt); err != nil {
			return DeepDiscoveryBrowserResultOutcome{}, err
		}
	}
	nextProbe, err := probe.Complete(probe.Version, input.Artifact.ArtifactID, input.FinalURL, input.ContentHash, len(input.Links))
	if err != nil {
		return DeepDiscoveryBrowserResultOutcome{}, err
	}
	probeState, _ := json.Marshal(nextProbe)
	resultJSON, _ := json.Marshal(input)
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_deep_discovery_browser_probes
SET probe_status=?,version=?,state_json=?,result_json=?,updated_at=? WHERE probe_id=? AND version=?`,
		nextProbe.Status, nextProbe.Version, probeState, resultJSON, input.ObservedAt.UTC(), probe.ProbeID, probe.Version)
	if err != nil {
		return DeepDiscoveryBrowserResultOutcome{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return DeepDiscoveryBrowserResultOutcome{}, ErrProgressConflict
	}
	succeededAttempt, err := attempt.Succeed()
	if err != nil {
		return DeepDiscoveryBrowserResultOutcome{}, err
	}
	completedWork, err := work.Complete(work.Version, model.ResolutionSucceeded, "", "")
	if err != nil {
		return DeepDiscoveryBrowserResultOutcome{}, err
	}
	attemptResult, _ := json.Marshal(map[string]any{"probe_id": probe.ProbeID, "artifact_id": input.Artifact.ArtifactID,
		"link_count": len(input.Links), "public_query_count": len(input.PublicQueryEvidence)})
	if err := updateAttemptStatusTx(ctx, tx, attempt.Status, succeededAttempt, attemptResult, input.ObservedAt); err != nil {
		return DeepDiscoveryBrowserResultOutcome{}, err
	}
	if err := updateWorkTx(ctx, tx, work.Version, completedWork, input.ObservedAt); err != nil {
		return DeepDiscoveryBrowserResultOutcome{}, err
	}
	if err := releaseBudgetPermitTx(ctx, tx, attempt.AttemptID, model.PermitReleased, input.ObservedAt); err != nil {
		return DeepDiscoveryBrowserResultOutcome{}, err
	}
	eventPayload, _ := json.Marshal(map[string]any{"attempt_id": attempt.AttemptID, "work_id": work.WorkID,
		"probe_id": probe.ProbeID, "artifact_id": input.Artifact.ArtifactID, "link_count": len(input.Links),
		"public_query_count": len(input.PublicQueryEvidence)})
	event, err := model.NewEventIntent("deep-discovery-browser-completed-"+attempt.AttemptID, "deep.discovery.browser.completed",
		"deep_discovery_probe", probe.ProbeID, nextProbe.Version, input.ObservedAt.Format(time.RFC3339Nano), input.CommandID, eventPayload)
	if err != nil {
		return DeepDiscoveryBrowserResultOutcome{}, err
	}
	if err := appendEventIntent(ctx, tx, event, input.ObservedAt, input.ObservedAt); err != nil {
		return DeepDiscoveryBrowserResultOutcome{}, fmt.Errorf("append deep discovery browser completion event: %w", err)
	}
	if err := appendAttemptDispatch(ctx, tx, succeededAttempt.AttemptID, succeededAttempt.ExecutorActorID,
		succeededAttempt.Capability, "", "capacity_released", input.CommandID, input.ObservedAt, input.ObservedAt); err != nil {
		return DeepDiscoveryBrowserResultOutcome{}, err
	}
	outcome := DeepDiscoveryBrowserResultOutcome{Probe: nextProbe, Work: completedWork}
	if err := reserveResultReceipt(ctx, tx, input.CommandID, executioncontract.TypeResult, input.RequestHash, outcome, input.ObservedAt); err != nil {
		return DeepDiscoveryBrowserResultOutcome{}, err
	}
	if err := tx.Commit(); err != nil {
		return DeepDiscoveryBrowserResultOutcome{}, err
	}
	return outcome, nil
}
