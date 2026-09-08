package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

// ExecutionOffer is the immutable input accepted by one executor. Kind
// selects the domain payload while Work/Attempt and routing stay uniform, so
// listing and detail steps do not become separate worker types.
// Large recipe bodies remain behind RecipeExecution.ContentRef; this value is
// safe to carry in an Atoll control message.
type ExecutionOffer = executioncontract.Offer

// ListingExecutionOffer remains an alias for source compatibility with the
// first vertical slice. New callers should use ExecutionOffer.
type ListingExecutionOffer = ExecutionOffer

type DetailExecutionInput = executioncontract.DetailInput

type ListingOfferRequest struct {
	AttemptID           string
	ExecutorActorID     string
	ExecutorIncarnation string
	Capability          string
	Origin              string
	ProfileID           string
	OfferedAt           time.Time
	BudgetPolicy        ExecutionBudgetPolicy
}

// OfferListingExecution claims one runnable listing Work and creates its
// active Attempt in the same transaction. The public protocol remains
// offer/accept; SKIP LOCKED is only the repository's scale-out mechanism.
func (r *Repository) OfferListingExecution(ctx context.Context, request ListingOfferRequest) (ListingExecutionOffer, error) {
	return r.offerExecution(ctx, request, "listing_sync")
}

// OfferExecution lets one capability-bearing executor claim either listing
// or detail work in the control plane's global priority order.
func (r *Repository) OfferExecution(ctx context.Context, request ListingOfferRequest) (ExecutionOffer, error) {
	return r.offerExecution(ctx, request, "")
}

func (r *Repository) offerExecution(ctx context.Context, request ListingOfferRequest, requiredPurpose string) (ExecutionOffer, error) {
	if strings.TrimSpace(request.AttemptID) == "" || strings.TrimSpace(request.ExecutorActorID) == "" ||
		strings.TrimSpace(request.ExecutorIncarnation) == "" || strings.TrimSpace(request.Capability) == "" || request.OfferedAt.IsZero() ||
		request.BudgetPolicy.validate() != nil {
		return ExecutionOffer{}, fmt.Errorf("execution offer requires attempt, executor identity, incarnation, capability, and time")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ExecutionOffer{}, fmt.Errorf("begin execution offer: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := getExecutionOfferReplay(ctx, tx, request, requiredPurpose); err != nil {
		return ExecutionOffer{}, err
	} else if found {
		if err := tx.Commit(); err != nil {
			return ExecutionOffer{}, fmt.Errorf("commit execution offer replay: %w", err)
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
	  AND (? = '' OR w.purpose = ?)
	  AND w.not_before <= ? AND (w.deadline_at IS NULL OR w.deadline_at > ?)
  AND (? = '' OR w.origin = ?) AND (? = '' OR w.profile_id = ?)
	  AND ((w.purpose = 'listing_sync' AND EXISTS (
	    SELECT 1 FROM recruiting_source_occurrences o
	    WHERE o.listing_work_id = w.work_id AND o.status IN ('queued', 'running')
	  )) OR (w.purpose = 'detail_sync' AND EXISTS (
	    SELECT 1 FROM recruiting_source_jobs j
	    WHERE j.job_id = w.target_id AND j.job_status IN ('detail_pending', 'update_pending')
	  )))
  AND NOT EXISTS (
    SELECT 1 FROM recruiting_attempts a
    WHERE a.work_id = w.work_id AND a.attempt_status IN ('offered', 'accepted', 'running')
  )
ORDER BY w.priority DESC, w.not_before, w.work_id
LIMIT 100`, request.Capability, requiredPurpose, requiredPurpose, request.OfferedAt.UTC(), request.OfferedAt.UTC(),
		request.Origin, request.Origin, request.ProfileID, request.ProfileID)
	if err != nil {
		return ExecutionOffer{}, fmt.Errorf("discover runnable execution work: %w", err)
	}
	var candidateIDs []string
	for rows.Next() {
		var workID string
		if err := rows.Scan(&workID); err != nil {
			_ = rows.Close()
			return ExecutionOffer{}, err
		}
		candidateIDs = append(candidateIDs, workID)
	}
	if err := rows.Close(); err != nil {
		return ExecutionOffer{}, err
	}
	if err := rows.Err(); err != nil {
		return ExecutionOffer{}, err
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
			return ExecutionOffer{}, fmt.Errorf("lock runnable execution work: %w", err)
		}
		var active int
		if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM recruiting_attempts
WHERE work_id = ? AND attempt_status IN ('offered', 'accepted', 'running')`, candidateID).Scan(&active); err != nil {
			return ExecutionOffer{}, fmt.Errorf("check active execution attempt: %w", err)
		}
		if active != 0 {
			continue
		}
		claimed = true
		break
	}
	if !claimed {
		return ExecutionOffer{}, ErrNotFound
	}
	var work model.Work
	if err := json.Unmarshal(workState, &work); err != nil {
		return ExecutionOffer{}, fmt.Errorf("decode claimed execution work: %w", err)
	}
	placement.BusinessKey, placement.Origin, placement.ProfileID = businessKey.String, origin.String, profileID.String
	if deadline.Valid {
		value := deadline.Time.UTC()
		placement.DeadlineAt = &value
	}

	var occurrence model.SourceOccurrence
	var checkpoint *model.IncrementalCheckpoint
	var detail *DetailExecutionInput
	var fence model.AttemptFence
	switch work.Purpose {
	case "listing_sync":
		occurrence, err = getOccurrenceByWorkWith(ctx, tx, work.WorkID, true)
		if err == nil {
			checkpoint, fence, err = loadListingOfferFence(ctx, tx, occurrence, placement.ProfileID)
		}
	case "detail_sync":
		detail, fence, err = loadDetailOfferFence(ctx, tx, work, placement)
	default:
		err = fmt.Errorf("unsupported executable work purpose %q", work.Purpose)
	}
	if err != nil {
		return ExecutionOffer{}, err
	}
	sourceID := occurrence.SourceID
	if detail != nil {
		sourceID = detail.Job.SourceID
	}
	var companyID string
	if err := tx.QueryRowContext(ctx, "SELECT company_id FROM recruiting_sources WHERE source_id = ?", sourceID).Scan(&companyID); err != nil {
		return ExecutionOffer{}, fmt.Errorf("load execution company: %w", err)
	}
	attempt, err := model.NewAttempt(request.AttemptID, work)
	if err == nil {
		attempt, err = attempt.BindExecutor(strings.TrimSpace(request.ExecutorActorID), strings.TrimSpace(request.ExecutorIncarnation), request.Capability)
	}
	if err == nil {
		attempt, err = attempt.WithFence(fence)
	}
	if err != nil {
		return ExecutionOffer{}, err
	}
	permit, permitExpiresAt, err := acquireBudgetPermitTx(ctx, tx, attempt.AttemptID, placement.Origin, placement.ProfileID,
		placement.Capability, companyID, request.BudgetPolicy, request.OfferedAt)
	if err != nil {
		return ExecutionOffer{}, err
	}
	offer := ExecutionOffer{
		Kind: strings.TrimSuffix(work.Purpose, "_sync"), Attempt: attempt, Work: work, Checkpoint: checkpoint,
		Detail: detail, Budget: permit, BudgetExpiresAt: permitExpiresAt.Format(time.RFC3339Nano),
		RequestedCapability: strings.TrimSpace(request.Capability), RequestedOrigin: strings.TrimSpace(request.Origin),
		RequestedProfileID: strings.TrimSpace(request.ProfileID),
	}
	if work.Purpose == "listing_sync" {
		offer.Occurrence = &occurrence
	}
	offerState, err := json.Marshal(offer)
	if err != nil {
		return ExecutionOffer{}, err
	}
	if err := insertAttempt(ctx, tx, attempt, offerState, request.OfferedAt); err != nil {
		return ExecutionOffer{}, err
	}
	if err := tx.Commit(); err != nil {
		return ExecutionOffer{}, fmt.Errorf("commit execution offer: %w", err)
	}
	return offer, nil
}

func getExecutionOfferReplay(ctx context.Context, tx *sql.Tx, request ListingOfferRequest, requiredPurpose string) (ExecutionOffer, bool, error) {
	var attemptState, offerState []byte
	err := tx.QueryRowContext(ctx, `
SELECT state_json, execution_offer_json
FROM recruiting_attempts WHERE attempt_id = ? FOR UPDATE`, request.AttemptID).Scan(&attemptState, &offerState)
	if errors.Is(err, sql.ErrNoRows) {
		return ExecutionOffer{}, false, nil
	}
	if err != nil {
		return ExecutionOffer{}, false, fmt.Errorf("read execution offer replay: %w", err)
	}
	var attempt model.Attempt
	if err := json.Unmarshal(attemptState, &attempt); err != nil {
		return ExecutionOffer{}, false, err
	}
	if attempt.ExecutorActorID != strings.TrimSpace(request.ExecutorActorID) ||
		attempt.ExecutorIncarnation != strings.TrimSpace(request.ExecutorIncarnation) ||
		attempt.Capability != strings.TrimSpace(request.Capability) || len(offerState) == 0 {
		return ExecutionOffer{}, false, fmt.Errorf("%w: attempt ID reused with different execution request", ErrAttemptConflict)
	}
	var offer ExecutionOffer
	if err := json.Unmarshal(offerState, &offer); err != nil {
		return ExecutionOffer{}, false, fmt.Errorf("decode execution offer replay: %w", err)
	}
	if offer.Attempt.AttemptID != attempt.AttemptID || offer.Work.WorkID != attempt.WorkID ||
		offer.RequestedCapability != strings.TrimSpace(request.Capability) ||
		offer.RequestedOrigin != strings.TrimSpace(request.Origin) || offer.RequestedProfileID != strings.TrimSpace(request.ProfileID) ||
		(requiredPurpose != "" && offer.Work.Purpose != requiredPurpose) {
		return ExecutionOffer{}, false, fmt.Errorf("%w: persisted execution offer does not match request", ErrAttemptConflict)
	}
	return offer, true, nil
}

func getOccurrenceByWorkWith(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, workID string, lock bool) (model.SourceOccurrence, error) {
	query := "SELECT state_json FROM recruiting_source_occurrences WHERE listing_work_id = ?"
	if lock {
		query += " FOR UPDATE"
	}
	var state []byte
	if err := queryer.QueryRowContext(ctx, query, workID).Scan(&state); err != nil {
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

func loadDetailOfferFence(ctx context.Context, tx *sql.Tx, work model.Work, placement WorkPlacement) (*DetailExecutionInput, model.AttemptFence, error) {
	if work.TargetType != "job" || work.Purpose != "detail_sync" {
		return nil, model.AttemptFence{}, fmt.Errorf("detail execution requires a detail job work")
	}
	job, err := getJobWithLock(ctx, tx, work.TargetID)
	if err != nil {
		return nil, model.AttemptFence{}, fmt.Errorf("load detail execution job: %w", err)
	}
	var companyState, sourceState, assignmentState, recipeState []byte
	err = tx.QueryRowContext(ctx, `
SELECT c.state_json, s.state_json, a.state_json, r.state_json
FROM recruiting_sources s
JOIN recruiting_companies c ON c.company_id = s.company_id
JOIN recruiting_source_assignments a ON a.source_id = s.source_id AND a.recipe_kind = 'detail'
JOIN recruiting_recipes r ON r.recipe_id = a.recipe_id AND r.recipe_version = a.recipe_version
WHERE s.source_id = ?`, job.SourceID).Scan(&companyState, &sourceState, &assignmentState, &recipeState)
	if err != nil {
		return nil, model.AttemptFence{}, fmt.Errorf("load detail execution fence: %w", err)
	}
	var company model.Company
	var source model.RecruitmentSource
	var assignment model.SourceRecipeAssignment
	var recipe model.Recipe
	for _, item := range []struct {
		data  []byte
		value any
	}{{companyState, &company}, {sourceState, &source}, {assignmentState, &assignment}, {recipeState, &recipe}} {
		if err := json.Unmarshal(item.data, item.value); err != nil {
			return nil, model.AttemptFence{}, err
		}
	}
	origin, err := canonicalOrigin(job.DetailURL)
	if err != nil {
		return nil, model.AttemptFence{}, err
	}
	if company.OnboardingStatus != model.CompanyReady || company.ControlStatus != model.ControlActive ||
		source.ReadinessStatus != model.SourceReady || source.ControlStatus != model.ControlActive || source.HealthStatus != model.HealthHealthy ||
		source.DetailAssignment == nil || *source.DetailAssignment != assignment || assignment.Kind != model.RecipeDetail ||
		recipe.Status != model.RecipeActive || recipe.Kind != model.RecipeDetail || recipe.RecipeID != assignment.RecipeID ||
		recipe.Version != assignment.RecipeVersion || recipe.ContractHash != assignment.ContractHash ||
		recipe.Execution.RequiredCapability != placement.Capability || origin != placement.Origin ||
		(job.Status != model.JobDetailPending && job.Status != model.JobUpdatePending) {
		return nil, model.AttemptFence{}, fmt.Errorf("detail work is fenced by changed or unavailable source, recipe, job, or placement")
	}
	fence := model.AttemptFence{
		CompanyVersion: company.Version, SourceVersion: source.Version, AssignmentVersion: assignment.AssignmentVersion,
		RecipeID: recipe.RecipeID, RecipeVersion: recipe.Version, RefreshGeneration: job.RefreshGeneration,
	}
	if placement.ProfileID != "" {
		var profileState []byte
		if err := tx.QueryRowContext(ctx, "SELECT state_json FROM recruiting_profiles WHERE profile_id = ?", placement.ProfileID).Scan(&profileState); err != nil {
			return nil, model.AttemptFence{}, fmt.Errorf("load detail profile: %w", err)
		}
		var profile model.BrowserProfile
		if err := json.Unmarshal(profileState, &profile); err != nil {
			return nil, model.AttemptFence{}, err
		}
		if profile.AuthStatus != model.ProfileReady {
			return nil, model.AttemptFence{}, fmt.Errorf("detail profile is not ready")
		}
		fence.ProfileID, fence.ProfileVersion = profile.ProfileID, profile.Version
	}
	return &DetailExecutionInput{Job: job, Assignment: assignment, Recipe: recipe}, fence, nil
}

// AcceptListingExecution records that the bound executor accepted an offer.
// The name is retained for API compatibility; it accepts both executable
// purposes selected by OfferExecution.
func (r *Repository) AcceptListingExecution(ctx context.Context, attemptID, executorActorID, executorIncarnation string, businessAt time.Time) (model.Attempt, error) {
	return r.transitionListingExecution(ctx, attemptID, executorActorID, executorIncarnation, "accept", "", nil, nil, businessAt, nil)
}

// StartListingExecution atomically starts Attempt, Work, and the first
// occurrence execution. A retry starts a new Attempt while retaining the
// already-running occurrence lifecycle.
func (r *Repository) StartListingExecution(ctx context.Context, attemptID, executorActorID, executorIncarnation string, businessAt time.Time) (model.Attempt, error) {
	return r.transitionListingExecution(ctx, attemptID, executorActorID, executorIncarnation, "start", "", nil, nil, businessAt, nil)
}

// FailListingExecution releases the active Attempt slot and moves Work to a
// retryable state. Retry policy and repair classification are a later control
// decision; the repository never loops automatically.
func (r *Repository) FailListingExecution(ctx context.Context, attemptID, executorActorID, executorIncarnation, reason string, businessAt time.Time) (model.Attempt, error) {
	return r.transitionListingExecution(ctx, attemptID, executorActorID, executorIncarnation, "fail", reason, nil, nil, businessAt, nil)
}

func (r *Repository) FailExecutionWithReport(ctx context.Context, attemptID, executorActorID, executorIncarnation, reason string,
	report executioncontract.FailureReport, policy ExecutionFailurePolicy, businessAt time.Time) (model.Attempt, error) {
	if err := report.Validate(attemptID); err != nil {
		return model.Attempt{}, err
	}
	if strings.TrimSpace(reason) != report.Class {
		return model.Attempt{}, fmt.Errorf("execution failure reason must match its classified report")
	}
	return r.transitionListingExecution(ctx, attemptID, executorActorID, executorIncarnation, "fail", reason, &report, &policy, businessAt, nil)
}

type ExecutionTransitionCommand struct {
	CommandID           string
	Word                string
	RequestHash         string
	CorrelationID       string
	RequestedBy         string
	AttemptID           string
	ExecutorIncarnation string
	Action              string
	Reason              string
	Failure             *executioncontract.FailureReport
	FailurePolicy       ExecutionFailurePolicy
}

type executionTransitionHooks struct {
	before func(*sql.Tx) (bool, error)
	after  func(*sql.Tx, model.Attempt, model.Work) error
}

// ApplyExecutionTransitionCommand makes the executor's command receipt and
// Attempt/Work/Permit transition one transaction. The stored response is the
// original application response, so a lost Atoll reply can be retried without
// re-running the state transition.
func (r *Repository) ApplyExecutionTransitionCommand(ctx context.Context, command ExecutionTransitionCommand,
	businessAt time.Time) (CommandResult, error) {
	if strings.TrimSpace(command.CommandID) == "" || strings.TrimSpace(command.Word) == "" ||
		strings.TrimSpace(command.RequestHash) == "" || strings.TrimSpace(command.CorrelationID) == "" ||
		strings.TrimSpace(command.RequestedBy) == "" || command.RequestedBy != strings.TrimSpace(command.RequestedBy) {
		return CommandResult{}, fmt.Errorf("execution transition command identity, hash, correlation, and requester are required")
	}
	switch command.Action {
	case "accept":
		if command.Word != executioncontract.TypeAccept || command.Failure != nil || strings.TrimSpace(command.Reason) != "" {
			return CommandResult{}, fmt.Errorf("accept execution command shape is invalid")
		}
	case "start":
		if command.Word != executioncontract.TypeStarted || command.Failure != nil || strings.TrimSpace(command.Reason) != "" {
			return CommandResult{}, fmt.Errorf("start execution command shape is invalid")
		}
	case "fail":
		if command.Word != executioncontract.TypeFailed || strings.TrimSpace(command.Reason) == "" || command.Failure == nil {
			return CommandResult{}, fmt.Errorf("failed execution command shape is invalid")
		}
		if err := command.Failure.Validate(command.AttemptID); err != nil || command.Failure.Class != strings.TrimSpace(command.Reason) {
			return CommandResult{}, fmt.Errorf("failed execution command report is invalid")
		}
		if err := command.FailurePolicy.Validate(); err != nil {
			return CommandResult{}, err
		}
	default:
		return CommandResult{}, fmt.Errorf("unsupported execution transition action %q", command.Action)
	}
	var response json.RawMessage
	replayed := false
	hooks := &executionTransitionHooks{
		before: func(tx *sql.Tx) (bool, error) {
			stored, found, err := readCommandReceipt(ctx, tx, command.CommandID, command.RequestHash)
			if err != nil || !found {
				return false, err
			}
			response, replayed = stored, true
			return true, nil
		},
		after: func(tx *sql.Tx, attempt model.Attempt, work model.Work) error {
			var err error
			response, err = json.Marshal(struct {
				ContractVersion string         `json:"contract_version"`
				CorrelationID   string         `json:"correlation_id"`
				RequestedBy     string         `json:"requested_by"`
				Attempt         *model.Attempt `json:"attempt,omitempty"`
			}{executioncontract.Version, command.CorrelationID, command.RequestedBy, &attempt})
			if err != nil {
				return err
			}
			receipt, err := model.NewCommandReceipt(command.CommandID, command.Word, command.RequestHash, response)
			if err != nil {
				return err
			}
			if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
				return err
			}
			if command.Action != "fail" {
				return nil
			}
			eventType := "work.retry_scheduled"
			if work.Status == model.WorkWaitingHuman {
				eventType = "work.waiting_human"
			}
			payload, _ := json.Marshal(map[string]any{
				"attempt_id": attempt.AttemptID, "work_id": work.WorkID, "failure_class": work.LastFailureClass,
				"retry_policy_version": work.RetryPolicyVersion, "automatic_attempts": work.AutomaticAttempts,
				"retry_not_before": work.RetryNotBefore, "failure_artifact_id": command.Failure.Artifact.ArtifactID,
			})
			event, err := model.NewEventIntent("execution-failed-"+attempt.AttemptID, eventType, "work", work.WorkID,
				work.Version, businessAt.UTC().Format(time.RFC3339Nano), command.CommandID, payload)
			if err != nil {
				return err
			}
			if err := appendEventIntent(ctx, tx, event, businessAt, businessAt); err != nil {
				return err
			}
			if err := appendAttemptDispatch(ctx, tx, attempt.AttemptID, attempt.ExecutorActorID, attempt.Capability, "",
				"capacity_released", command.CommandID, businessAt, businessAt); err != nil {
				return err
			}
			if work.Status == model.WorkWaitingRetry {
				retryAt, err := time.Parse(time.RFC3339Nano, work.RetryNotBefore)
				if err != nil {
					return err
				}
				return appendAttemptDispatch(ctx, tx, attempt.AttemptID, attempt.ExecutorActorID, attempt.Capability, attempt.ProfileID,
					"retry_due", command.CommandID, retryAt, businessAt)
			}
			return nil
		},
	}
	_, err := r.transitionListingExecution(ctx, command.AttemptID, command.RequestedBy, command.ExecutorIncarnation,
		command.Action, command.Reason, command.Failure, failurePolicyPointer(command), businessAt, hooks)
	if errors.Is(err, ErrCommandConflict) {
		return r.replayCommittedCommand(ctx, command.CommandID, command.RequestHash)
	}
	if err != nil {
		return CommandResult{}, err
	}
	return CommandResult{Response: append(json.RawMessage(nil), response...), Replayed: replayed}, nil
}

func (r *Repository) transitionListingExecution(ctx context.Context, attemptID, executorActorID, executorIncarnation, action, reason string,
	report *executioncontract.FailureReport, failurePolicy *ExecutionFailurePolicy, businessAt time.Time, hooks *executionTransitionHooks) (model.Attempt, error) {
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
	if report != nil && report.Artifact.WorkID != work.WorkID {
		return model.Attempt{}, fmt.Errorf("execution failure Artifact belongs to another Work")
	}
	// Read the receipt only after locking the Attempt. Concurrent delivery of
	// the same command then observes the winner's receipt instead of applying
	// the transition against its already-advanced state.
	if hooks != nil && hooks.before != nil {
		stop, err := hooks.before(tx)
		if err != nil {
			return model.Attempt{}, err
		}
		if stop {
			return model.Attempt{}, nil
		}
	}
	if action != "fail" {
		if err := ensureBudgetPermitActiveTx(ctx, tx, attempt.AttemptID, businessAt); err != nil {
			return model.Attempt{}, err
		}
	}
	var occurrence model.SourceOccurrence
	var currentFence model.AttemptFence
	// A failure closes execution authority but does not publish business data,
	// so it remains safe to record after a domain configuration change. Accept
	// and start do publish execution authority and therefore recheck every
	// frozen fence.
	if action != "fail" {
		var fenceErr error
		switch work.Purpose {
		case "listing_sync":
			occurrence, fenceErr = getOccurrenceByWorkWith(ctx, tx, work.WorkID, true)
			if fenceErr == nil {
				_, currentFence, fenceErr = loadListingOfferFence(ctx, tx, occurrence, attempt.ProfileID)
			}
		case "detail_sync":
			placement, placementErr := getWorkPlacementWith(ctx, tx, work.WorkID)
			if placementErr != nil {
				fenceErr = placementErr
			} else {
				_, currentFence, fenceErr = loadDetailOfferFence(ctx, tx, work, placement)
			}
		default:
			fenceErr = fmt.Errorf("unsupported executable work purpose %q", work.Purpose)
		}
		if fenceErr != nil || !sameAttemptFence(attempt, currentFence) {
			return model.Attempt{}, fmt.Errorf("%w: execution domain fence changed", ErrResultFenced)
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
		if err == nil && work.Purpose == "listing_sync" && occurrence.Status == model.OccurrenceQueued {
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
			if report == nil {
				work, err = work.WaitRetry(work.Version, strings.TrimSpace(reason))
				if err == nil {
					err = updateWorkTx(ctx, tx, previousWorkVersion, work, businessAt)
				}
			} else if failurePolicy == nil {
				err = fmt.Errorf("classified execution failure requires retry policy")
			} else {
				var decision model.ExecutionFailureDecision
				decision, err = failurePolicy.Decide(work, *report, businessAt)
				if err == nil {
					work, err = work.ApplyExecutionFailure(work.Version, decision)
				}
				if err == nil {
					err = updateFailedWorkTx(ctx, tx, previousWorkVersion, work, decision, businessAt)
				}
			}
		}
		if err == nil {
			err = releaseBudgetPermitTx(ctx, tx, attempt.AttemptID, model.PermitReleased, businessAt)
		}
		if err == nil && report != nil {
			err = insertArtifact(ctx, tx, report.Artifact, false, businessAt)
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
		return model.Attempt{}, fmt.Errorf("transition execution attempt: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return model.Attempt{}, ErrAttemptConflict
	}
	if hooks != nil && hooks.after != nil {
		if err := hooks.after(tx, attempt, work); err != nil {
			return model.Attempt{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return model.Attempt{}, fmt.Errorf("commit execution transition: %w", err)
	}
	return attempt, nil
}

func failurePolicyPointer(command ExecutionTransitionCommand) *ExecutionFailurePolicy {
	if command.Failure == nil {
		return nil
	}
	policy := command.FailurePolicy
	return &policy
}

func updateFailedWorkTx(ctx context.Context, tx *sql.Tx, expected uint64, work model.Work,
	decision model.ExecutionFailureDecision, businessAt time.Time) error {
	notBefore := businessAt.UTC()
	if decision.Route == model.FailureRetry {
		var err error
		notBefore, err = time.Parse(time.RFC3339Nano, decision.RetryNotBefore)
		if err != nil {
			return fmt.Errorf("parse retry not-before: %w", err)
		}
	}
	state, _ := json.Marshal(work)
	result, err := tx.ExecContext(ctx, `
UPDATE recruiting_works
SET status = ?, resolution = ?, acceptance_version = ?, version = ?, state_json = ?, not_before = ?, updated_at = ?
WHERE work_id = ? AND version = ?`, work.Status, nullableString(string(work.Resolution)), work.AcceptanceVersion,
		work.Version, state, notBefore.UTC(), businessAt.UTC(), work.WorkID, expected)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrAttemptConflict
	}
	return nil
}

func getWorkPlacementWith(ctx context.Context, tx *sql.Tx, workID string) (WorkPlacement, error) {
	var placement WorkPlacement
	var businessKey, capability, origin, profileID sql.NullString
	var deadline sql.NullTime
	err := tx.QueryRowContext(ctx, `
SELECT business_key, priority, capability, origin, profile_id, not_before, deadline_at
FROM recruiting_works WHERE work_id = ?`, workID).Scan(&businessKey, &placement.Priority, &capability,
		&origin, &profileID, &placement.NotBefore, &deadline)
	if err != nil {
		return WorkPlacement{}, err
	}
	placement.BusinessKey, placement.Capability = businessKey.String, capability.String
	placement.Origin, placement.ProfileID = origin.String, profileID.String
	placement.NotBefore = placement.NotBefore.UTC()
	if deadline.Valid {
		value := deadline.Time.UTC()
		placement.DeadlineAt = &value
	}
	return placement, nil
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
