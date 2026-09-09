package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

// ApplySourceDiscoveryCandidateDecisionCommand makes the independently
// versioned candidate decision, optional Source creation, receipt, and audit
// event one fact. Decisions never update sibling candidates.
func (r *Repository) ApplySourceDiscoveryCandidateDecisionCommand(ctx context.Context, discoveryID string,
	expectedVersion uint64, next model.SourceDiscoveryCandidate, source *model.RecruitmentSource,
	receipt model.CommandReceipt, event model.EventIntent, businessAt time.Time) (CommandResult, error) {
	aggregateID, err := model.SourceDiscoveryCandidateAggregateID(discoveryID, next.CandidateID)
	if err != nil || expectedVersion == 0 || next.Version != expectedVersion+1 || receipt.CommandID == "" || businessAt.IsZero() ||
		event.AggregateType != "source_discovery_candidate" || event.AggregateID != aggregateID ||
		event.AggregateVersion != next.Version || event.CauseCommandID != receipt.CommandID {
		return CommandResult{}, fmt.Errorf("candidate decision, receipt, and event are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil {
		return CommandResult{}, fmt.Errorf("event business time: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin candidate decision: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	discovery, err := getSourceDiscoveryWith(ctx, tx, discoveryID, true)
	if err != nil {
		return CommandResult{}, err
	}
	if discovery.Status != model.SourceDiscoveryCompleted {
		return CommandResult{}, &model.InvalidTransitionError{Entity: "source_discovery", From: string(discovery.Status), Action: "decide candidate"}
	}
	current, err := getSourceDiscoveryCandidateWith(ctx, tx, discoveryID, next.CandidateID, true)
	if err != nil {
		return CommandResult{}, err
	}
	var derived model.SourceDiscoveryCandidate
	switch next.Disposition {
	case model.SourceCandidateAccepted:
		derived, err = current.Accept(expectedVersion, next.SourceID, next.DecisionActorID, next.DecisionReason)
	case model.SourceCandidateRejected:
		derived, err = current.Reject(expectedVersion, next.DecisionActorID, next.DecisionReason)
	default:
		err = fmt.Errorf("candidate decision must accept or reject")
	}
	if err != nil {
		return CommandResult{}, err
	}
	if derived != next {
		return CommandResult{}, fmt.Errorf("candidate decision does not match locked state")
	}
	if next.Disposition == model.SourceCandidateAccepted {
		if source == nil || source.SourceID != next.SourceID || source.CompanyID != discovery.CompanyID ||
			source.DiscoveryGeneration != discovery.Generation || source.Version != 1 || source.CandidateEndpoint == nil ||
			source.CandidateEndpoint.URL != current.FinalURL || source.CandidateEndpoint.Category != current.Category ||
			source.CandidateEndpoint.CanonicalKey != current.CandidateKey {
			return CommandResult{}, fmt.Errorf("accepted candidate must create its exact Company Source")
		}
		var conflictingCompany string
		err := tx.QueryRowContext(ctx, `SELECT company_id FROM recruiting_sources
WHERE canonical_source_key = ? AND company_id <> ? LIMIT 1 FOR UPDATE`, current.CandidateKey, discovery.CompanyID).Scan(&conflictingCompany)
		if err == nil {
			return CommandResult{}, fmt.Errorf("%w: canonical source key belongs to company %s and requires human adjudication", ErrBusinessKeyExists, conflictingCompany)
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return CommandResult{}, fmt.Errorf("check cross-company source ownership: %w", err)
		}
		endpoint, origin, err := sourceStorageIdentity(*source)
		if err != nil {
			return CommandResult{}, err
		}
		if err := insertSourceWith(ctx, tx, *source, endpoint, origin, businessAt); err != nil {
			return CommandResult{}, err
		}
	} else if source != nil {
		return CommandResult{}, fmt.Errorf("rejected candidate cannot create a Source")
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	state, err := json.Marshal(next)
	if err != nil {
		return CommandResult{}, fmt.Errorf("encode candidate decision: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_source_discovery_candidates
SET disposition = ?, source_id = ?, version = ?, state_json = ?, updated_at = ?
WHERE discovery_id = ? AND candidate_id = ? AND version = ?`, next.Disposition, nullableString(next.SourceID),
		next.Version, state, businessAt.UTC(), discoveryID, next.CandidateID, expectedVersion)
	if err != nil {
		return CommandResult{}, fmt.Errorf("update candidate decision: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedVersion, Actual: current.Version}
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit candidate decision: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}
