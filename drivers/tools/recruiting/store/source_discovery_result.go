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

type SourceDiscoveryResult struct {
	CommandID           string
	RequestHash         string
	AttemptID           string
	ExecutorActorID     string
	ExecutorIncarnation string
	Artifact            model.ArtifactMetadata
	Candidates          []model.SourceDiscoveryCandidate
	ObservedAt          time.Time
}

type SourceDiscoveryResultOutcome struct {
	Discovery model.SourceDiscovery `json:"discovery"`
	Work      model.Work            `json:"work"`
	Company   *model.Company        `json:"company,omitempty"`
	Replayed  bool                  `json:"replayed"`
}

// AcceptSourceDiscoveryResult commits one bounded seed-page result. Discovery
// is intentionally one-shot: following candidate endpoints belongs to later
// validation Work and is never hidden inside the daily schedule.
func (r *Repository) AcceptSourceDiscoveryResult(ctx context.Context, input SourceDiscoveryResult) (SourceDiscoveryResultOutcome, error) {
	if input.CommandID == "" || input.RequestHash == "" || input.AttemptID == "" || input.ExecutorActorID == "" ||
		input.ExecutorIncarnation == "" || input.ObservedAt.IsZero() || len(input.Candidates) > 500 {
		return SourceDiscoveryResultOutcome{}, fmt.Errorf("source discovery result requires command, execution identity, time, and at most 500 candidates")
	}
	if err := validateResultArtifact(input.Artifact, input.AttemptID, model.ArtifactResponse); err != nil {
		return SourceDiscoveryResultOutcome{}, err
	}
	seen := make(map[string]struct{}, len(input.Candidates))
	for _, candidate := range input.Candidates {
		validated, err := model.NewSourceDiscoveryCandidate(candidate.Endpoint, candidate.Category, candidate.FinalURL,
			candidate.ConfidenceBasis, candidate.EvidenceArtifactID)
		if err != nil || validated != candidate || candidate.EvidenceArtifactID != input.Artifact.ArtifactID {
			return SourceDiscoveryResultOutcome{}, fmt.Errorf("source discovery result contains an invalid or unbound candidate")
		}
		if _, duplicate := seen[candidate.CandidateID]; duplicate {
			return SourceDiscoveryResultOutcome{}, fmt.Errorf("source discovery result contains duplicate candidates")
		}
		seen[candidate.CandidateID] = struct{}{}
	}
	outcome, fenceErr, err := r.acceptSourceDiscoveryResultTx(ctx, input)
	if err != nil {
		return SourceDiscoveryResultOutcome{}, err
	}
	if fenceErr != nil {
		if err := r.saveRejectedArtifact(ctx, input.Artifact, input.ObservedAt); err != nil {
			return SourceDiscoveryResultOutcome{}, fmt.Errorf("%w; also failed to retain rejected discovery Artifact: %v", fenceErr, err)
		}
		return SourceDiscoveryResultOutcome{}, fmt.Errorf("%w: %v", ErrResultFenced, fenceErr)
	}
	return outcome, nil
}

func (r *Repository) acceptSourceDiscoveryResultTx(ctx context.Context, input SourceDiscoveryResult) (SourceDiscoveryResultOutcome, error, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return SourceDiscoveryResultOutcome{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	attempt, err := getAttemptWith(ctx, tx, input.AttemptID, true)
	if err != nil {
		return SourceDiscoveryResultOutcome{}, nil, err
	}
	work, err := getWorkWith(ctx, tx, attempt.WorkID, true)
	if err != nil {
		return SourceDiscoveryResultOutcome{}, nil, err
	}
	if work.Purpose != "source_discovery" || work.TargetType != "company" || input.Artifact.WorkID != work.WorkID {
		return SourceDiscoveryResultOutcome{}, nil, fmt.Errorf("source discovery result Work or Artifact is inconsistent")
	}
	if replay, found, err := readResultReceipt[SourceDiscoveryResultOutcome](ctx, tx, input.CommandID, input.RequestHash); err != nil {
		return SourceDiscoveryResultOutcome{}, nil, err
	} else if found {
		replay.Replayed = true
		return replay, nil, nil
	}
	if err := ensureBudgetPermitActiveTx(ctx, tx, attempt.AttemptID, input.ObservedAt); err != nil {
		return SourceDiscoveryResultOutcome{}, err, nil
	}
	placement, err := getWorkPlacementWith(ctx, tx, work.WorkID)
	if err != nil {
		return SourceDiscoveryResultOutcome{}, nil, err
	}
	discovery, _, currentFence, err := loadSourceDiscoveryOfferFence(ctx, tx, work, placement)
	if err != nil {
		return SourceDiscoveryResultOutcome{}, err, nil
	}
	if discovery.Status != model.SourceDiscoveryRunning || discovery.CandidateCount != 0 || discovery.NextChunkSequence != 0 {
		return SourceDiscoveryResultOutcome{}, fmt.Errorf("source discovery is not accepting its one-shot result"), nil
	}
	if err := attempt.CanAcceptResult(work, currentFence, input.ExecutorActorID, input.ExecutorIncarnation); err != nil {
		return SourceDiscoveryResultOutcome{}, err, nil
	}
	if err := insertArtifact(ctx, tx, input.Artifact, false, input.ObservedAt); err != nil {
		return SourceDiscoveryResultOutcome{}, nil, err
	}
	next := discovery
	if len(input.Candidates) > 0 {
		next, err = next.AppendCandidates(next.Version, 0, len(input.Candidates))
		if err == nil {
			err = insertSourceDiscoveryCandidates(ctx, tx, discovery.DiscoveryID, 0, input.Candidates, input.ObservedAt)
		}
	}
	if err == nil {
		next, err = next.Complete(next.Version, len(input.Candidates))
	}
	if err != nil {
		return SourceDiscoveryResultOutcome{}, nil, err
	}
	if err := updateSourceDiscoveryCAS(ctx, tx, discovery.Version, next, input.ObservedAt); err != nil {
		return SourceDiscoveryResultOutcome{}, nil, err
	}
	succeededAttempt, err := attempt.Succeed()
	if err != nil {
		return SourceDiscoveryResultOutcome{}, nil, err
	}
	completedWork, err := work.Complete(work.Version, model.ResolutionSucceeded, "", "")
	if err != nil {
		return SourceDiscoveryResultOutcome{}, nil, err
	}
	resultState, _ := json.Marshal(map[string]any{"discovery_id": next.DiscoveryID, "candidate_count": next.CandidateCount})
	if err := updateAttemptStatusTx(ctx, tx, attempt.Status, succeededAttempt, resultState, input.ObservedAt); err != nil {
		return SourceDiscoveryResultOutcome{}, nil, err
	}
	if err := updateWorkTx(ctx, tx, work.Version, completedWork, input.ObservedAt); err != nil {
		return SourceDiscoveryResultOutcome{}, nil, err
	}
	var blockedCompany *model.Company
	if len(input.Candidates) == 0 {
		blockedCompany, err = markCompanyWithoutSourcesTx(ctx, tx, discovery, input.CommandID, input.ObservedAt)
		if err != nil {
			var conflict *model.VersionConflictError
			if errors.As(err, &conflict) || errors.Is(err, ErrProgressConflict) {
				return SourceDiscoveryResultOutcome{}, err, nil
			}
			return SourceDiscoveryResultOutcome{}, nil, err
		}
	}
	if err := releaseBudgetPermitTx(ctx, tx, attempt.AttemptID, model.PermitReleased, input.ObservedAt); err != nil {
		return SourceDiscoveryResultOutcome{}, nil, err
	}
	eventPayload, _ := json.Marshal(map[string]any{"attempt_id": attempt.AttemptID, "work_id": work.WorkID,
		"candidate_count": next.CandidateCount, "artifact_id": input.Artifact.ArtifactID})
	event, err := model.NewEventIntent("source-discovery-completed-"+attempt.AttemptID, "source.discovery.completed",
		"source_discovery", next.DiscoveryID, next.Version, input.ObservedAt.Format(time.RFC3339Nano), input.CommandID, eventPayload)
	if err != nil {
		return SourceDiscoveryResultOutcome{}, nil, err
	}
	if err := appendEventIntent(ctx, tx, event, input.ObservedAt, input.ObservedAt); err != nil {
		return SourceDiscoveryResultOutcome{}, nil, err
	}
	if err := appendAttemptDispatch(ctx, tx, succeededAttempt.AttemptID, succeededAttempt.ExecutorActorID,
		succeededAttempt.Capability, "", "capacity_released", input.CommandID, input.ObservedAt, input.ObservedAt); err != nil {
		return SourceDiscoveryResultOutcome{}, nil, err
	}
	outcome := SourceDiscoveryResultOutcome{Discovery: next, Work: completedWork, Company: blockedCompany}
	if err := reserveResultReceipt(ctx, tx, input.CommandID, executioncontract.TypeResult, input.RequestHash, outcome, input.ObservedAt); err != nil {
		return SourceDiscoveryResultOutcome{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return SourceDiscoveryResultOutcome{}, nil, err
	}
	return outcome, nil, nil
}

// markCompanyWithoutSourcesTx closes the zero-candidate onboarding branch in
// the same transaction as the discovery result. Locking the Company before
// checking Sources also serializes with Source insertion through its foreign
// key, so a concurrent accepted/manual Source cannot be hidden by a false
// blocked_no_sources state.
func markCompanyWithoutSourcesTx(ctx context.Context, tx *sql.Tx, discovery model.SourceDiscovery,
	causeCommandID string, at time.Time) (*model.Company, error) {
	var state []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_companies
WHERE company_id = ? FOR UPDATE`, discovery.CompanyID).Scan(&state); err != nil {
		return nil, fmt.Errorf("lock zero-source Company: %w", err)
	}
	var company model.Company
	if err := json.Unmarshal(state, &company); err != nil {
		return nil, fmt.Errorf("decode zero-source Company: %w", err)
	}
	if company.Version != discovery.CompanyVersion {
		return nil, &model.VersionConflictError{Expected: discovery.CompanyVersion, Actual: company.Version}
	}
	if company.OnboardingStatus != model.CompanyDiscoveringSources {
		return nil, nil
	}
	var sourceCount int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_sources
WHERE company_id = ? AND control_status <> 'archived'`, company.CompanyID).Scan(&sourceCount); err != nil {
		return nil, fmt.Errorf("count zero-source Company Sources: %w", err)
	}
	if sourceCount != 0 {
		return nil, nil
	}
	blocked, err := company.MarkNoSources(company.Version)
	if err != nil {
		return nil, err
	}
	blockedState, _ := json.Marshal(blocked)
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_companies
SET onboarding_status = ?, version = ?, state_json = ?, updated_at = ?
WHERE company_id = ? AND version = ?`, blocked.OnboardingStatus, blocked.Version, blockedState,
		at.UTC(), blocked.CompanyID, company.Version)
	if err != nil {
		return nil, fmt.Errorf("mark zero-source Company: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return nil, ErrProgressConflict
	}
	payload, _ := json.Marshal(map[string]any{"discovery_id": discovery.DiscoveryID, "reason": "no_candidates"})
	event, err := model.NewEventIntent("company-no-sources-"+discovery.DiscoveryID,
		"company.onboarding.blocked_no_sources", "company", blocked.CompanyID, blocked.Version,
		at.UTC().Format(time.RFC3339Nano), causeCommandID, payload)
	if err != nil {
		return nil, err
	}
	if err := appendEventIntent(ctx, tx, event, at, at); err != nil {
		return nil, err
	}
	return &blocked, nil
}
