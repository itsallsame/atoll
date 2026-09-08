package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

// ListingExecutionOffer is the immutable input accepted by one executor.
// Large recipe bodies remain behind RecipeExecution.ContentRef; this value is
// safe to carry in an Atoll control message.
type ListingExecutionOffer struct {
	Attempt             model.Attempt                `json:"attempt"`
	Work                model.Work                   `json:"work"`
	Occurrence          model.SourceOccurrence       `json:"occurrence"`
	Checkpoint          *model.IncrementalCheckpoint `json:"checkpoint,omitempty"`
	RequestedCapability string                       `json:"requested_capability"`
	RequestedOrigin     string                       `json:"requested_origin,omitempty"`
	RequestedProfileID  string                       `json:"requested_profile_id,omitempty"`
}

type ListingOfferRequest struct {
	AttemptID           string
	ExecutorActorID     string
	ExecutorIncarnation string
	Capability          string
	Origin              string
	ProfileID           string
	OfferedAt           time.Time
}

// OfferListingExecution claims one runnable listing Work and creates its
// active Attempt in the same transaction. The public protocol remains
// offer/accept; SKIP LOCKED is only the repository's scale-out mechanism.
func (r *Repository) OfferListingExecution(ctx context.Context, request ListingOfferRequest) (ListingExecutionOffer, error) {
	if strings.TrimSpace(request.AttemptID) == "" || strings.TrimSpace(request.ExecutorActorID) == "" ||
		strings.TrimSpace(request.ExecutorIncarnation) == "" || strings.TrimSpace(request.Capability) == "" || request.OfferedAt.IsZero() {
		return ListingExecutionOffer{}, fmt.Errorf("listing offer requires attempt, executor identity, incarnation, capability, and time")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ListingExecutionOffer{}, fmt.Errorf("begin listing offer: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := getListingExecutionOfferReplay(ctx, tx, request); err != nil {
		return ListingExecutionOffer{}, err
	} else if found {
		if err := tx.Commit(); err != nil {
			return ListingExecutionOffer{}, fmt.Errorf("commit listing offer replay: %w", err)
		}
		return replay, nil
	}

	var workState []byte
	var placement WorkPlacement
	var businessKey, origin, profileID sql.NullString
	var deadline sql.NullTime
	// Discover a small candidate set without locks, then lock exact primary-key
	// rows one by one. A locking range query with ORDER BY/EXISTS can make
	// MySQL lock or skip every examined candidate rather than just LIMIT 1,
	// collapsing concurrent executors onto a false empty result.
	rows, err := tx.QueryContext(ctx, `
SELECT w.work_id
FROM recruiting_works w
WHERE w.capability = ? AND w.status IN ('open', 'waiting_retry')
  AND w.not_before <= ? AND (w.deadline_at IS NULL OR w.deadline_at > ?)
  AND (? = '' OR w.origin = ?) AND (? = '' OR w.profile_id = ?)
  AND EXISTS (
    SELECT 1 FROM recruiting_source_occurrences o
    WHERE o.listing_work_id = w.work_id AND o.status IN ('queued', 'running')
  )
  AND NOT EXISTS (
    SELECT 1 FROM recruiting_attempts a
    WHERE a.work_id = w.work_id AND a.attempt_status IN ('offered', 'accepted', 'running')
  )
ORDER BY w.priority DESC, w.not_before, w.work_id
LIMIT 100`, request.Capability, request.OfferedAt.UTC(), request.OfferedAt.UTC(),
		request.Origin, request.Origin, request.ProfileID, request.ProfileID)
	if err != nil {
		return ListingExecutionOffer{}, fmt.Errorf("discover runnable listing work: %w", err)
	}
	var candidateIDs []string
	for rows.Next() {
		var workID string
		if err := rows.Scan(&workID); err != nil {
			_ = rows.Close()
			return ListingExecutionOffer{}, err
		}
		candidateIDs = append(candidateIDs, workID)
	}
	if err := rows.Close(); err != nil {
		return ListingExecutionOffer{}, err
	}
	if err := rows.Err(); err != nil {
		return ListingExecutionOffer{}, err
	}
	claimed := false
	for _, candidateID := range candidateIDs {
		err = tx.QueryRowContext(ctx, `
SELECT state_json, business_key, priority, capability, origin, profile_id, not_before, deadline_at
FROM recruiting_works
WHERE work_id = ? AND status IN ('open', 'waiting_retry')
  AND not_before <= ? AND (deadline_at IS NULL OR deadline_at > ?)
FOR UPDATE SKIP LOCKED`, candidateID, request.OfferedAt.UTC(), request.OfferedAt.UTC()).Scan(
			&workState, &businessKey, &placement.Priority, &placement.Capability, &origin, &profileID,
			&placement.NotBefore, &deadline)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return ListingExecutionOffer{}, fmt.Errorf("lock runnable listing work: %w", err)
		}
		var active int
		if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM recruiting_attempts
WHERE work_id = ? AND attempt_status IN ('offered', 'accepted', 'running')`, candidateID).Scan(&active); err != nil {
			return ListingExecutionOffer{}, fmt.Errorf("check active listing attempt: %w", err)
		}
		if active != 0 {
			continue
		}
		claimed = true
		break
	}
	if !claimed {
		return ListingExecutionOffer{}, ErrNotFound
	}
	var work model.Work
	if err := json.Unmarshal(workState, &work); err != nil {
		return ListingExecutionOffer{}, fmt.Errorf("decode claimed listing work: %w", err)
	}
	placement.BusinessKey, placement.Origin, placement.ProfileID = businessKey.String, origin.String, profileID.String
	if deadline.Valid {
		value := deadline.Time.UTC()
		placement.DeadlineAt = &value
	}

	occurrence, err := getOccurrenceByWorkWith(ctx, tx, work.WorkID, true)
	if err != nil {
		return ListingExecutionOffer{}, err
	}
	checkpoint, fence, err := loadListingOfferFence(ctx, tx, occurrence, placement.ProfileID)
	if err != nil {
		return ListingExecutionOffer{}, err
	}
	attempt, err := model.NewAttempt(request.AttemptID, work)
	if err == nil {
		attempt, err = attempt.BindExecutor(strings.TrimSpace(request.ExecutorActorID), strings.TrimSpace(request.ExecutorIncarnation), request.Capability)
	}
	if err == nil {
		attempt, err = attempt.WithFence(fence)
	}
	if err != nil {
		return ListingExecutionOffer{}, err
	}
	offer := ListingExecutionOffer{
		Attempt: attempt, Work: work, Occurrence: occurrence, Checkpoint: checkpoint,
		RequestedCapability: strings.TrimSpace(request.Capability), RequestedOrigin: strings.TrimSpace(request.Origin),
		RequestedProfileID: strings.TrimSpace(request.ProfileID),
	}
	offerState, err := json.Marshal(offer)
	if err != nil {
		return ListingExecutionOffer{}, err
	}
	if err := insertAttempt(ctx, tx, attempt, offerState, request.OfferedAt); err != nil {
		return ListingExecutionOffer{}, err
	}
	if err := tx.Commit(); err != nil {
		return ListingExecutionOffer{}, fmt.Errorf("commit listing offer: %w", err)
	}
	return offer, nil
}

func getListingExecutionOfferReplay(ctx context.Context, tx *sql.Tx, request ListingOfferRequest) (ListingExecutionOffer, bool, error) {
	var attemptState, offerState []byte
	err := tx.QueryRowContext(ctx, `
SELECT state_json, execution_offer_json
FROM recruiting_attempts WHERE attempt_id = ? FOR UPDATE`, request.AttemptID).Scan(&attemptState, &offerState)
	if errors.Is(err, sql.ErrNoRows) {
		return ListingExecutionOffer{}, false, nil
	}
	if err != nil {
		return ListingExecutionOffer{}, false, fmt.Errorf("read listing offer replay: %w", err)
	}
	var attempt model.Attempt
	if err := json.Unmarshal(attemptState, &attempt); err != nil {
		return ListingExecutionOffer{}, false, err
	}
	if attempt.ExecutorActorID != strings.TrimSpace(request.ExecutorActorID) ||
		attempt.ExecutorIncarnation != strings.TrimSpace(request.ExecutorIncarnation) ||
		attempt.Capability != strings.TrimSpace(request.Capability) || len(offerState) == 0 {
		return ListingExecutionOffer{}, false, fmt.Errorf("%w: attempt ID reused with different execution request", ErrAttemptConflict)
	}
	var offer ListingExecutionOffer
	if err := json.Unmarshal(offerState, &offer); err != nil {
		return ListingExecutionOffer{}, false, fmt.Errorf("decode listing offer replay: %w", err)
	}
	if offer.Attempt.AttemptID != attempt.AttemptID || offer.Work.WorkID != attempt.WorkID ||
		offer.RequestedCapability != strings.TrimSpace(request.Capability) ||
		offer.RequestedOrigin != strings.TrimSpace(request.Origin) || offer.RequestedProfileID != strings.TrimSpace(request.ProfileID) {
		return ListingExecutionOffer{}, false, fmt.Errorf("%w: persisted listing offer does not match request", ErrAttemptConflict)
	}
	return offer, true, nil
}

func getOccurrenceByWorkWith(ctx context.Context, tx *sql.Tx, workID string, lock bool) (model.SourceOccurrence, error) {
	query := "SELECT state_json FROM recruiting_source_occurrences WHERE listing_work_id = ?"
	if lock {
		query += " FOR UPDATE"
	}
	var state []byte
	if err := tx.QueryRowContext(ctx, query, workID).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.SourceOccurrence{}, ErrNotFound
		}
		return model.SourceOccurrence{}, fmt.Errorf("get listing occurrence by work: %w", err)
	}
	var occurrence model.SourceOccurrence
	if err := json.Unmarshal(state, &occurrence); err != nil {
		return model.SourceOccurrence{}, fmt.Errorf("decode listing occurrence: %w", err)
	}
	return occurrence, nil
}

func loadListingOfferFence(ctx context.Context, tx *sql.Tx, occurrence model.SourceOccurrence, profileID string) (*model.IncrementalCheckpoint, model.AttemptFence, error) {
	if err := occurrence.ListingExecution.Validate(occurrence.SourceID); err != nil {
		return nil, model.AttemptFence{}, err
	}
	var companyState, sourceState, assignmentState, recipeState []byte
	err := tx.QueryRowContext(ctx, `
SELECT c.state_json, s.state_json, a.state_json, r.state_json
FROM recruiting_sources s
JOIN recruiting_companies c ON c.company_id = s.company_id
JOIN recruiting_source_assignments a ON a.source_id = s.source_id AND a.recipe_kind = 'listing'
JOIN recruiting_recipes r ON r.recipe_id = a.recipe_id AND r.recipe_version = a.recipe_version
WHERE s.source_id = ?`, occurrence.SourceID).Scan(&companyState, &sourceState, &assignmentState, &recipeState)
	if err != nil {
		return nil, model.AttemptFence{}, fmt.Errorf("load listing execution fence: %w", err)
	}
	var company model.Company
	var source model.RecruitmentSource
	var assignment model.SourceRecipeAssignment
	var recipe model.Recipe
	if err := json.Unmarshal(companyState, &company); err != nil {
		return nil, model.AttemptFence{}, err
	}
	if err := json.Unmarshal(sourceState, &source); err != nil {
		return nil, model.AttemptFence{}, err
	}
	if err := json.Unmarshal(assignmentState, &assignment); err != nil {
		return nil, model.AttemptFence{}, err
	}
	if err := json.Unmarshal(recipeState, &recipe); err != nil {
		return nil, model.AttemptFence{}, err
	}
	snapshot := occurrence.ListingExecution
	if company.Version != occurrence.CompanyVersion || company.OnboardingStatus != model.CompanyReady || company.ControlStatus != model.ControlActive ||
		source.Version != occurrence.SourceVersion || source.ReadinessStatus != model.SourceReady || source.ControlStatus != model.ControlActive || source.HealthStatus != model.HealthHealthy ||
		assignment != snapshot.Assignment || recipe.Status != model.RecipeActive || recipe.RecipeID != snapshot.RecipeID ||
		recipe.Version != snapshot.RecipeVersion || recipe.ContentHash != snapshot.ContentHash || recipe.ContractHash != snapshot.ContractHash || recipe.Execution != snapshot.Execution {
		return nil, model.AttemptFence{}, fmt.Errorf("listing work is fenced by changed or unavailable cutoff configuration")
	}
	fence := model.AttemptFence{
		CompanyVersion: company.Version, SourceVersion: source.Version, AssignmentVersion: assignment.AssignmentVersion,
		RecipeID: assignment.RecipeID, RecipeVersion: assignment.RecipeVersion,
	}
	var checkpointState []byte
	err = tx.QueryRowContext(ctx, "SELECT state_json FROM recruiting_checkpoints WHERE source_id = ?", occurrence.SourceID).Scan(&checkpointState)
	var checkpoint *model.IncrementalCheckpoint
	if err == nil {
		checkpoint = &model.IncrementalCheckpoint{}
		if err := json.Unmarshal(checkpointState, checkpoint); err != nil {
			return nil, model.AttemptFence{}, err
		}
		if checkpoint.RecipeID != snapshot.RecipeID || checkpoint.RecipeVersion != snapshot.RecipeVersion || checkpoint.ContractHash != snapshot.ContractHash {
			return nil, model.AttemptFence{}, fmt.Errorf("listing checkpoint is incompatible with cutoff recipe")
		}
		fence.CheckpointVersion = checkpoint.Version
	} else if !errors.Is(err, sql.ErrNoRows) {
		return nil, model.AttemptFence{}, fmt.Errorf("load listing checkpoint: %w", err)
	}
	if profileID != "" {
		var profileState []byte
		if err := tx.QueryRowContext(ctx, "SELECT state_json FROM recruiting_profiles WHERE profile_id = ?", profileID).Scan(&profileState); err != nil {
			return nil, model.AttemptFence{}, fmt.Errorf("load listing profile: %w", err)
		}
		var profile model.BrowserProfile
		if err := json.Unmarshal(profileState, &profile); err != nil {
			return nil, model.AttemptFence{}, err
		}
		if profile.AuthStatus != model.ProfileReady {
			return nil, model.AttemptFence{}, fmt.Errorf("listing profile is not ready")
		}
		fence.ProfileID, fence.ProfileVersion = profile.ProfileID, profile.Version
	}
	return checkpoint, fence, nil
}

// AcceptListingExecution records that the bound executor accepted an offer.
func (r *Repository) AcceptListingExecution(ctx context.Context, attemptID, executorActorID, executorIncarnation string, businessAt time.Time) (model.Attempt, error) {
	return r.transitionListingExecution(ctx, attemptID, executorActorID, executorIncarnation, "accept", "", businessAt)
}

// StartListingExecution atomically starts Attempt, Work, and the first
// occurrence execution. A retry starts a new Attempt while retaining the
// already-running occurrence lifecycle.
func (r *Repository) StartListingExecution(ctx context.Context, attemptID, executorActorID, executorIncarnation string, businessAt time.Time) (model.Attempt, error) {
	return r.transitionListingExecution(ctx, attemptID, executorActorID, executorIncarnation, "start", "", businessAt)
}

// FailListingExecution releases the active Attempt slot and moves Work to a
// retryable state. Retry policy and repair classification are a later control
// decision; the repository never loops automatically.
func (r *Repository) FailListingExecution(ctx context.Context, attemptID, executorActorID, executorIncarnation, reason string, businessAt time.Time) (model.Attempt, error) {
	return r.transitionListingExecution(ctx, attemptID, executorActorID, executorIncarnation, "fail", reason, businessAt)
}

func (r *Repository) transitionListingExecution(ctx context.Context, attemptID, executorActorID, executorIncarnation, action, reason string, businessAt time.Time) (model.Attempt, error) {
	if strings.TrimSpace(attemptID) == "" || strings.TrimSpace(executorActorID) == "" || strings.TrimSpace(executorIncarnation) == "" || businessAt.IsZero() ||
		(action == "fail" && strings.TrimSpace(reason) == "") {
		return model.Attempt{}, fmt.Errorf("execution transition requires attempt, executor identity, incarnation, time, and failure reason when applicable")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return model.Attempt{}, err
	}
	defer func() { _ = tx.Rollback() }()
	attempt, err := getAttemptWith(ctx, tx, attemptID, true)
	if err != nil {
		return model.Attempt{}, err
	}
	work, err := getWorkWith(ctx, tx, attempt.WorkID, true)
	if err != nil {
		return model.Attempt{}, err
	}
	if attempt.ExecutorActorID != executorActorID || attempt.ExecutorIncarnation != executorIncarnation || attempt.AcceptanceVersion != work.AcceptanceVersion {
		return model.Attempt{}, ErrAttemptConflict
	}
	occurrence, err := getOccurrenceByWorkWith(ctx, tx, work.WorkID, true)
	if err != nil {
		return model.Attempt{}, err
	}
	// A failure closes execution authority but does not publish business data,
	// so it remains safe to record after a domain configuration change. Accept
	// and start do publish execution authority and therefore recheck every
	// frozen fence.
	if action != "fail" {
		_, currentFence, fenceErr := loadListingOfferFence(ctx, tx, occurrence, attempt.ProfileID)
		if fenceErr != nil || !sameAttemptFence(attempt, currentFence) {
			return model.Attempt{}, fmt.Errorf("%w: listing domain fence changed", ErrResultFenced)
		}
	}

	previousAttemptStatus := attempt.Status
	switch action {
	case "accept":
		attempt, err = attempt.Accept()
	case "start":
		attempt, err = attempt.Start()
		if err == nil {
			previousWorkVersion := work.Version
			work, err = work.Start(work.Version)
			if err == nil {
				err = updateWorkTx(ctx, tx, previousWorkVersion, work, businessAt)
			}
		}
		if err == nil && occurrence.Status == model.OccurrenceQueued {
			previousOccurrenceVersion := occurrence.Version
			occurrence, err = occurrence.Start(occurrence.Version)
			if err == nil {
				err = updateOccurrenceInTx(ctx, tx, previousOccurrenceVersion, occurrence, businessAt)
			}
		}
	case "fail":
		attempt, err = attempt.Fail()
		if err == nil {
			previousWorkVersion := work.Version
			work, err = work.WaitRetry(work.Version, strings.TrimSpace(reason))
			if err == nil {
				err = updateWorkTx(ctx, tx, previousWorkVersion, work, businessAt)
			}
		}
	default:
		err = fmt.Errorf("unknown execution transition %q", action)
	}
	if err != nil {
		return model.Attempt{}, err
	}
	state, _ := json.Marshal(attempt)
	result, err := tx.ExecContext(ctx, `
UPDATE recruiting_attempts
SET attempt_status = ?, state_json = ?, updated_at = ?
WHERE attempt_id = ? AND attempt_status = ?`, attempt.Status, state, businessAt.UTC(), attempt.AttemptID, previousAttemptStatus)
	if err != nil {
		return model.Attempt{}, fmt.Errorf("transition listing attempt: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return model.Attempt{}, ErrAttemptConflict
	}
	if err := tx.Commit(); err != nil {
		return model.Attempt{}, fmt.Errorf("commit listing execution transition: %w", err)
	}
	return attempt, nil
}

func sameAttemptFence(attempt model.Attempt, current model.AttemptFence) bool {
	return attempt.CompanyVersion == current.CompanyVersion && attempt.SourceVersion == current.SourceVersion &&
		attempt.AssignmentVersion == current.AssignmentVersion && attempt.RecipeID == current.RecipeID &&
		attempt.RecipeVersion == current.RecipeVersion && attempt.CheckpointVersion == current.CheckpointVersion &&
		attempt.RefreshGeneration == current.RefreshGeneration && attempt.ProfileID == current.ProfileID &&
		attempt.ProfileVersion == current.ProfileVersion
}

func updateWorkTx(ctx context.Context, tx *sql.Tx, expected uint64, work model.Work, businessAt time.Time) error {
	state, _ := json.Marshal(work)
	result, err := tx.ExecContext(ctx, `
UPDATE recruiting_works
SET status = ?, resolution = ?, acceptance_version = ?, version = ?, state_json = ?, updated_at = ?
WHERE work_id = ? AND version = ?`, work.Status, nullableString(string(work.Resolution)), work.AcceptanceVersion,
		work.Version, state, businessAt.UTC(), work.WorkID, expected)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return ErrAttemptConflict
	}
	return nil
}
