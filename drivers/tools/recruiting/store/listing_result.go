package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/executioncontract"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type ListingPageResult struct {
	CommandID           string
	RequestHash         string
	AttemptID           string
	ExecutorActorID     string
	ExecutorIncarnation string
	PageSequence        uint64
	ResumeCursor        string
	Terminal            bool
	Artifact            model.ArtifactMetadata
	Observations        []model.ListingObservation
	ObservedAt          time.Time
}

type ListingPageOutcome struct {
	Items    []ListingIngestResult     `json:"items,omitempty"`
	Progress model.ListingPageProgress `json:"progress"`
	Replayed bool                      `json:"replayed"`
}

// listingResultExecution is the domain provenance behind one listing Work.
// A scheduled execution owns a SourceOccurrence; an independent production
// run owns a ListingRun. Diagnostic ListingRuns never enter page ingestion.
type listingResultExecution struct {
	Occurrence *model.SourceOccurrence
	ListingRun *model.ListingRun
	Baseline   *model.BaselineGeneration
}

func loadListingResultExecution(ctx context.Context, tx *sql.Tx, workID string) (listingResultExecution, error) {
	occurrence, err := getOccurrenceByWorkWith(ctx, tx, workID, true)
	if err == nil {
		return listingResultExecution{Occurrence: &occurrence}, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return listingResultExecution{}, err
	}
	run, err := getListingRunByWorkWith(ctx, tx, workID, true)
	if err == nil {
		if run.Mode != model.ListingRunProduction {
			return listingResultExecution{}, fmt.Errorf("diagnostic listing run cannot publish listing pages")
		}
		return listingResultExecution{ListingRun: &run}, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return listingResultExecution{}, err
	}
	var baselineState []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_baseline_generations WHERE work_id = ? FOR UPDATE`, workID).Scan(&baselineState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return listingResultExecution{}, ErrNotFound
		}
		return listingResultExecution{}, err
	}
	var baseline model.BaselineGeneration
	if err := json.Unmarshal(baselineState, &baseline); err != nil {
		return listingResultExecution{}, err
	}
	return listingResultExecution{Baseline: &baseline}, nil
}

func (e listingResultExecution) sourceID() string {
	if e.Occurrence != nil {
		return e.Occurrence.SourceID
	}
	if e.ListingRun != nil {
		return e.ListingRun.SourceID
	}
	if e.Baseline != nil {
		return e.Baseline.SourceID
	}
	return ""
}

// evidenceID is stored in the historical occurrence_id field. That field has
// always represented a concrete listing execution (baseline IDs included),
// and does not imply that a SourceOccurrence row exists.
func (e listingResultExecution) evidenceID() string {
	if e.Occurrence != nil {
		return e.Occurrence.OccurrenceID
	}
	if e.ListingRun != nil {
		return e.ListingRun.ListingRunID
	}
	if e.Baseline != nil {
		return e.Baseline.WorkID
	}
	return ""
}

func (e listingResultExecution) acceptsResults() bool {
	return e.Occurrence != nil && e.Occurrence.Status == model.OccurrenceRunning ||
		e.ListingRun != nil && e.ListingRun.Status == model.ListingRunRunning ||
		e.Baseline != nil && e.Baseline.Status == model.BaselineListing && !e.Baseline.ListingFinalized
}

func (e listingResultExecution) currentFence(ctx context.Context, tx *sql.Tx, profileID string) (model.AttemptFence, error) {
	if e.Occurrence != nil {
		_, fence, err := loadListingOfferFence(ctx, tx, *e.Occurrence, profileID)
		return fence, err
	}
	if e.ListingRun != nil {
		_, fence, err := loadStandaloneListingOfferFence(ctx, tx, *e.ListingRun, profileID)
		return fence, err
	}
	if e.Baseline != nil {
		work, err := getWorkWith(ctx, tx, e.Baseline.WorkID, false)
		if err != nil {
			return model.AttemptFence{}, err
		}
		placement, err := getWorkPlacementWith(ctx, tx, work.WorkID)
		if err != nil {
			return model.AttemptFence{}, err
		}
		_, fence, err := loadBaselineOfferFence(ctx, tx, work, placement)
		return fence, err
	}
	return model.AttemptFence{}, ErrNotFound
}

// AcceptListingPage authenticates one bounded page before making its
// observations and derived detail Work visible. Checkpoint advancement is
// deliberately reserved for AcceptListingCompletion.
func (r *Repository) AcceptListingPage(ctx context.Context, input ListingPageResult) (ListingPageOutcome, error) {
	if input.AttemptID == "" || input.ExecutorActorID == "" || input.ExecutorIncarnation == "" ||
		input.PageSequence == 0 || len(input.Observations) > listingPageMaxItems || input.ObservedAt.IsZero() {
		return ListingPageOutcome{}, fmt.Errorf("listing page result requires execution identity, sequence, bounded observations, and time")
	}
	if err := validateOptionalResultCommand(input.CommandID, input.RequestHash); err != nil {
		return ListingPageOutcome{}, err
	}
	if err := validateResultArtifact(input.Artifact, input.AttemptID, model.ArtifactPage); err != nil {
		return ListingPageOutcome{}, err
	}
	for attempt := 0; attempt < 3; attempt++ {
		outcome, fenceErr, err := r.acceptListingPageOnce(ctx, input)
		if isRetryableTransactionError(err) {
			continue
		}
		if err != nil {
			return ListingPageOutcome{}, err
		}
		if fenceErr != nil {
			if err := r.saveRejectedArtifact(ctx, input.Artifact, input.ObservedAt); err != nil {
				return ListingPageOutcome{}, fmt.Errorf("%w; also failed to retain rejected page artifact: %v", fenceErr, err)
			}
			return ListingPageOutcome{}, fmt.Errorf("%w: %v", ErrResultFenced, fenceErr)
		}
		return outcome, nil
	}
	return ListingPageOutcome{}, fmt.Errorf("listing page acceptance exhausted transaction retries")
}

func (r *Repository) acceptListingPageOnce(ctx context.Context, input ListingPageResult) (ListingPageOutcome, error, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ListingPageOutcome{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	attempt, err := getAttemptWith(ctx, tx, input.AttemptID, true)
	if err != nil {
		return ListingPageOutcome{}, nil, err
	}
	work, err := getWorkWith(ctx, tx, attempt.WorkID, true)
	if err != nil {
		return ListingPageOutcome{}, nil, err
	}
	execution, err := loadListingResultExecution(ctx, tx, work.WorkID)
	if err != nil {
		return ListingPageOutcome{}, nil, err
	}
	if input.Artifact.WorkID != work.WorkID {
		return ListingPageOutcome{}, nil, fmt.Errorf("listing page artifact belongs to another work")
	}
	if replay, found, err := readResultReceipt[ListingPageOutcome](ctx, tx, input.CommandID, input.RequestHash); err != nil {
		return ListingPageOutcome{}, nil, err
	} else if found {
		replay.Replayed = true
		return replay, nil, nil
	}
	if replay, found, err := replayListingPage(ctx, tx, input); err != nil {
		return ListingPageOutcome{}, nil, err
	} else if found {
		if err := reserveResultReceipt(ctx, tx, input.CommandID, executioncontract.TypeResult, input.RequestHash, replay, input.ObservedAt); err != nil {
			return ListingPageOutcome{}, nil, err
		}
		if err := tx.Commit(); err != nil {
			return ListingPageOutcome{}, nil, err
		}
		return replay, nil, nil
	}
	if !execution.acceptsResults() {
		return ListingPageOutcome{}, fmt.Errorf("listing execution is no longer accepting results"), nil
	}
	if err := ensureBudgetPermitActiveTx(ctx, tx, attempt.AttemptID, input.ObservedAt); err != nil {
		return ListingPageOutcome{}, err, nil
	}
	currentFence, err := execution.currentFence(ctx, tx, attempt.ProfileID)
	if err != nil {
		return ListingPageOutcome{}, err, nil
	}
	if err := attempt.CanAcceptResult(work, currentFence, input.ExecutorActorID, input.ExecutorIncarnation); err != nil {
		return ListingPageOutcome{}, err, nil
	}
	latest, found, err := getLatestListingProgressTx(ctx, tx, work.WorkID, attempt.AttemptID)
	if err != nil {
		return ListingPageOutcome{}, nil, err
	}
	if (!found && input.PageSequence != 1) || (found && (latest.EndOfInput || input.PageSequence != latest.PageSequence+1)) {
		return ListingPageOutcome{}, nil, ErrProgressConflict
	}
	var current *model.ListingPageProgress
	if found {
		current = &latest
	}
	progress, err := model.AdvanceAttemptListingPageProgress(current, work, attempt.AttemptID, input.ResumeCursor, input.Artifact.ArtifactID, len(input.Observations), input.Terminal)
	if err != nil {
		return ListingPageOutcome{}, nil, err
	}
	if progress.PageSequence != input.PageSequence {
		return ListingPageOutcome{}, nil, ErrProgressConflict
	}
	capability, detailProfileID := "", ""
	if execution.Baseline == nil {
		capability, detailProfileID, err = loadDetailPlacement(ctx, tx, execution.sourceID())
		if err != nil && len(input.Observations) != 0 {
			return ListingPageOutcome{}, err, nil
		}
	}
	if err := insertArtifact(ctx, tx, input.Artifact, false, input.ObservedAt); err != nil {
		return ListingPageOutcome{}, nil, err
	}
	items := make([]ListingIngestResult, 0, len(input.Observations))
	profileDetailWorks := 0
	for _, observation := range input.Observations {
		validatedObservation, err := model.NewListingObservation(observation)
		if err != nil {
			return ListingPageOutcome{}, nil, err
		}
		observation = validatedObservation
		if observation.OccurrenceID != execution.evidenceID() || observation.SourceID != execution.sourceID() ||
			observation.RecipeID != attempt.RecipeID || observation.RecipeVersion != attempt.RecipeVersion ||
			observation.ArtifactID != input.Artifact.ArtifactID {
			return ListingPageOutcome{}, nil, fmt.Errorf("listing observation is not bound to the accepted attempt page")
		}
		if execution.Baseline != nil {
			value, _ := json.Marshal(observation)
			_, err = tx.ExecContext(ctx, `INSERT INTO recruiting_baseline_staging(
  source_id, baseline_generation, attempt_id, source_job_key, observation_id, row_json, staged_at
) VALUES (?, ?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE attempt_id = VALUES(attempt_id), observation_id = VALUES(observation_id),
  row_json = VALUES(row_json), staged_at = VALUES(staged_at)`,
				execution.Baseline.SourceID, execution.Baseline.Generation, attempt.AttemptID, observation.SourceJobKey,
				observation.ObservationID, value, input.ObservedAt.UTC())
			if err != nil {
				return ListingPageOutcome{}, nil, fmt.Errorf("stage baseline observation: %w", err)
			}
		} else {
			origin, err := canonicalOrigin(observation.DetailURL)
			if err != nil {
				return ListingPageOutcome{}, nil, err
			}
			result, err := applyListingObservationTx(ctx, tx, ListingIngest{Observation: observation,
				ObservedAt: input.ObservedAt, Origin: origin, Capability: capability, Priority: 200,
				ProfileID: detailProfileID, NotBefore: input.ObservedAt, ParentWorkID: work.WorkID})
			if err != nil {
				return ListingPageOutcome{}, nil, err
			}
			items = append(items, result)
			if result.DetailWork != nil && detailProfileID != "" {
				profileDetailWorks++
			}
		}
	}
	state, _ := json.Marshal(progress)
	outcome := ListingPageOutcome{Items: items, Progress: progress}
	outcomeState, err := json.Marshal(outcome)
	if err != nil {
		return ListingPageOutcome{}, nil, err
	}
	_, err = tx.ExecContext(ctx, `
INSERT INTO recruiting_listing_page_progress(
  work_id, attempt_id, page_sequence, resume_cursor, artifact_id, item_count, end_of_input,
  work_version, work_acceptance_version, state_json, outcome_json, committed_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, progress.WorkID, progress.AttemptID, progress.PageSequence,
		nullableString(progress.ResumeCursor), progress.ArtifactID, progress.ItemCount, progress.EndOfInput,
		progress.WorkVersion, progress.WorkAcceptanceVersion, state, outcomeState, input.ObservedAt.UTC())
	if err != nil {
		return ListingPageOutcome{}, nil, fmt.Errorf("append accepted listing page progress: %w", err)
	}
	if err := reserveResultReceipt(ctx, tx, input.CommandID, executioncontract.TypeResult, input.RequestHash, outcome, input.ObservedAt); err != nil {
		return ListingPageOutcome{}, nil, err
	}
	if profileDetailWorks != 0 {
		causeID := "listing-page-" + fmt.Sprintf("%x", dispatchDigest(fmt.Sprintf("%s\n%d", attempt.AttemptID, input.PageSequence))[:16])
		if _, err := appendProfileDispatches(ctx, tx, []profileDispatchDemand{{
			Capability: capability, ProfileID: detailProfileID, Count: profileDetailWorks,
		}}, causeID, input.ObservedAt); err != nil {
			return ListingPageOutcome{}, nil, fmt.Errorf("dispatch Profile detail work: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return ListingPageOutcome{}, nil, err
	}
	return outcome, nil, nil
}

type ListingCompletion struct {
	RequestHash         string
	AttemptID           string
	ExecutorActorID     string
	ExecutorIncarnation string
	Artifact            model.ArtifactMetadata
	Progress            model.ListingProgress
	ItemCount           int
	CompletedAt         time.Time
	CauseCommandID      string
}

type ListingCompletionOutcome struct {
	Checkpoint model.IncrementalCheckpoint `json:"checkpoint"`
	Work       model.Work                  `json:"work"`
	Occurrence *model.SourceOccurrence     `json:"occurrence,omitempty"`
	ListingRun *model.ListingRun           `json:"listing_run,omitempty"`
	Baseline   *model.BaselineGeneration   `json:"baseline,omitempty"`
	Replayed   bool                        `json:"replayed"`
}

// AcceptListingCompletion is the sole daily-listing success commit. It
// advances Checkpoint, Attempt, Work, Occurrence, and outbox atomically.
func (r *Repository) AcceptListingCompletion(ctx context.Context, input ListingCompletion) (ListingCompletionOutcome, error) {
	if input.AttemptID == "" || input.ExecutorActorID == "" || input.ExecutorIncarnation == "" ||
		input.CauseCommandID == "" || input.CompletedAt.IsZero() || input.ItemCount < 0 {
		return ListingCompletionOutcome{}, fmt.Errorf("listing completion requires execution identity, command cause, and time")
	}
	if err := validateOptionalResultCommand(input.CauseCommandID, input.RequestHash); err != nil {
		return ListingCompletionOutcome{}, err
	}
	if err := validateResultArtifact(input.Artifact, input.AttemptID, model.ArtifactListingDelta); err != nil {
		return ListingCompletionOutcome{}, err
	}
	outcome, fenceErr, err := r.acceptListingCompletionOnce(ctx, input)
	if err != nil {
		return ListingCompletionOutcome{}, err
	}
	if fenceErr != nil {
		if err := r.saveRejectedArtifact(ctx, input.Artifact, input.CompletedAt); err != nil {
			return ListingCompletionOutcome{}, fmt.Errorf("%w; also failed to retain rejected completion artifact: %v", fenceErr, err)
		}
		return ListingCompletionOutcome{}, fmt.Errorf("%w: %v", ErrResultFenced, fenceErr)
	}
	return outcome, nil
}

func (r *Repository) acceptListingCompletionOnce(ctx context.Context, input ListingCompletion) (ListingCompletionOutcome, error, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return ListingCompletionOutcome{}, nil, err
	}
	defer func() { _ = tx.Rollback() }()
	attempt, err := getAttemptWith(ctx, tx, input.AttemptID, true)
	if err != nil {
		return ListingCompletionOutcome{}, nil, err
	}
	work, err := getWorkWith(ctx, tx, attempt.WorkID, true)
	if err != nil {
		return ListingCompletionOutcome{}, nil, err
	}
	execution, err := loadListingResultExecution(ctx, tx, work.WorkID)
	if err != nil {
		return ListingCompletionOutcome{}, nil, err
	}
	if input.Artifact.WorkID != work.WorkID {
		return ListingCompletionOutcome{}, nil, fmt.Errorf("listing completion artifact belongs to another work")
	}
	if replay, found, err := readResultReceipt[ListingCompletionOutcome](ctx, tx, input.CauseCommandID, input.RequestHash); err != nil {
		return ListingCompletionOutcome{}, nil, err
	} else if found {
		replay.Replayed = true
		return replay, nil, nil
	}
	if replay, found, err := replayListingCompletion(ctx, tx, input, attempt, work, execution); err != nil {
		return ListingCompletionOutcome{}, nil, err
	} else if found {
		if err := reserveResultReceipt(ctx, tx, input.CauseCommandID, executioncontract.TypeResult, input.RequestHash, replay, input.CompletedAt); err != nil {
			return ListingCompletionOutcome{}, nil, err
		}
		if err := tx.Commit(); err != nil {
			return ListingCompletionOutcome{}, nil, err
		}
		return replay, nil, nil
	}
	if !execution.acceptsResults() {
		return ListingCompletionOutcome{}, fmt.Errorf("listing execution is no longer accepting results"), nil
	}
	if err := ensureBudgetPermitActiveTx(ctx, tx, attempt.AttemptID, input.CompletedAt); err != nil {
		return ListingCompletionOutcome{}, err, nil
	}
	currentFence, err := execution.currentFence(ctx, tx, attempt.ProfileID)
	if err != nil {
		return ListingCompletionOutcome{}, err, nil
	}
	if err := attempt.CanAcceptResult(work, currentFence, input.ExecutorActorID, input.ExecutorIncarnation); err != nil {
		return ListingCompletionOutcome{}, err, nil
	}
	latest, found, err := getLatestListingProgressTx(ctx, tx, work.WorkID, attempt.AttemptID)
	if err != nil {
		return ListingCompletionOutcome{}, nil, err
	}
	if !found || !latest.EndOfInput {
		return ListingCompletionOutcome{}, fmt.Errorf("listing completion requires an accepted terminal page"), nil
	}
	var acceptedItemCount int
	if err := tx.QueryRowContext(ctx, `
SELECT COALESCE(SUM(item_count), 0) FROM recruiting_listing_page_progress WHERE attempt_id = ?`, attempt.AttemptID).Scan(&acceptedItemCount); err != nil {
		return ListingCompletionOutcome{}, nil, err
	}
	if acceptedItemCount != input.ItemCount {
		return ListingCompletionOutcome{}, fmt.Errorf("listing quality item count does not match accepted pages"), nil
	}
	var checkpoint model.IncrementalCheckpoint
	var committedCheckpoint model.IncrementalCheckpoint
	var finalizedBaseline *model.BaselineGeneration
	if execution.Baseline != nil {
		if attempt.CheckpointVersion != 0 || !input.Progress.IdentityComplete || !input.Progress.PaginationStable ||
			!input.Progress.OrderingContractHeld || !input.Progress.PreviousFrontierReached || !input.Progress.OverlapCompleted {
			return ListingCompletionOutcome{}, fmt.Errorf("baseline completion proof is incomplete"), nil
		}
		var staged int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_baseline_staging
WHERE source_id = ? AND baseline_generation = ? AND attempt_id = ?`, execution.Baseline.SourceID,
			execution.Baseline.Generation, attempt.AttemptID).Scan(&staged); err != nil {
			return ListingCompletionOutcome{}, nil, err
		}
		if staged != input.ItemCount {
			return ListingCompletionOutcome{}, fmt.Errorf("baseline staged rows do not match accepted listing items"), nil
		}
		candidate := model.IncrementalCheckpoint{SourceID: execution.Baseline.SourceID,
			RecipeID: execution.Baseline.ListingExecution.RecipeID, RecipeVersion: execution.Baseline.ListingExecution.RecipeVersion,
			ContractHash: execution.Baseline.ListingExecution.ContractHash, Strategy: execution.Baseline.CheckpointStrategy,
			FrontierActivityAt: input.Progress.Candidate.FrontierActivityAt,
			FrontierJobKeys:    append([]string(nil), input.Progress.Candidate.FrontierJobKeys...),
			OverlapPages:       execution.Baseline.OverlapPages, LastOccurrenceID: execution.Baseline.WorkID}
		var establishErr error
		committedCheckpoint, establishErr = model.EstablishCheckpoint(candidate)
		if establishErr != nil {
			return ListingCompletionOutcome{}, establishErr, nil
		}
		finalized, finalizeErr := execution.Baseline.FinalizeListingAttempt(execution.Baseline.Version, uint64(staged), attempt.AttemptID)
		if finalizeErr != nil {
			return ListingCompletionOutcome{}, finalizeErr, nil
		}
		finalizedBaseline = &finalized
	} else {
		var checkpointErr error
		checkpoint, checkpointErr = getCheckpointForUpdate(ctx, tx, execution.sourceID())
		if checkpointErr != nil {
			return ListingCompletionOutcome{}, checkpointErr, nil
		}
		if checkpoint.Version != attempt.CheckpointVersion {
			return ListingCompletionOutcome{}, fmt.Errorf("listing checkpoint changed after attempt offer"), nil
		}
		candidate := checkpoint
		candidate.FrontierActivityAt = input.Progress.Candidate.FrontierActivityAt
		candidate.FrontierJobKeys = append([]string(nil), input.Progress.Candidate.FrontierJobKeys...)
		candidate.LastOccurrenceID = execution.evidenceID()
		proof := input.Progress
		proof.Candidate = candidate
		committedCheckpoint, checkpointErr = checkpoint.Commit(checkpoint.Version, proof)
		if checkpointErr != nil {
			return ListingCompletionOutcome{}, checkpointErr, nil
		}
	}
	succeededAttempt, err := attempt.Succeed()
	if err != nil {
		return ListingCompletionOutcome{}, err, nil
	}
	completedWork, err := work.Complete(work.Version, model.ResolutionSucceeded, "", "")
	if err != nil {
		return ListingCompletionOutcome{}, err, nil
	}
	outcome := ListingCompletionOutcome{Checkpoint: committedCheckpoint, Work: completedWork, Baseline: finalizedBaseline}
	if execution.Occurrence != nil {
		completed, err := execution.Occurrence.Finish(execution.Occurrence.Version, true, "checkpoint_committed")
		if err != nil {
			return ListingCompletionOutcome{}, err, nil
		}
		outcome.Occurrence = &completed
	} else if execution.ListingRun != nil {
		completed, err := execution.ListingRun.Complete(execution.ListingRun.Version)
		if err != nil {
			return ListingCompletionOutcome{}, err, nil
		}
		outcome.ListingRun = &completed
	}
	outcomeState, err := json.Marshal(outcome)
	if err != nil {
		return ListingCompletionOutcome{}, nil, err
	}
	if err := insertArtifact(ctx, tx, input.Artifact, false, input.CompletedAt); err != nil {
		return ListingCompletionOutcome{}, nil, err
	}
	checkpointState, _ := json.Marshal(committedCheckpoint)
	var result sql.Result
	if execution.Baseline != nil {
		_, err = tx.ExecContext(ctx, `INSERT INTO recruiting_checkpoints(
  source_id, checkpoint_version, recipe_id, recipe_version, contract_hash,
  frontier_activity_at, frontier_keys_json, last_occurrence_id, state_json, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, committedCheckpoint.SourceID, committedCheckpoint.Version,
			committedCheckpoint.RecipeID, committedCheckpoint.RecipeVersion, committedCheckpoint.ContractHash,
			nullableTime(committedCheckpoint.FrontierActivityAt), nullableJSONStrings(committedCheckpoint.FrontierJobKeys),
			committedCheckpoint.LastOccurrenceID, checkpointState, input.CompletedAt.UTC())
		if err == nil {
			baselineState, _ := json.Marshal(finalizedBaseline)
			result, err = tx.ExecContext(ctx, `UPDATE recruiting_baseline_generations
SET generation_status = ?, listing_finalized = ?, details_expected = ?, details_accounted = ?, listing_attempt_id = ?,
    materialization_cursor = ?, materialized_count = ?, materialization_completed = ?,
    detail_exceptions = ?, version = ?, state_json = ?, updated_at = ?
WHERE source_id = ? AND baseline_generation = ? AND version = ?`, finalizedBaseline.Status,
				finalizedBaseline.ListingFinalized, finalizedBaseline.DetailsExpected, finalizedBaseline.DetailsAccounted,
				nullableString(finalizedBaseline.ListingAttemptID),
				nullableString(finalizedBaseline.MaterializationCursor), finalizedBaseline.MaterializedCount,
				finalizedBaseline.MaterializationCompleted, finalizedBaseline.DetailExceptions, finalizedBaseline.Version,
				baselineState, input.CompletedAt.UTC(),
				finalizedBaseline.SourceID, finalizedBaseline.Generation, execution.Baseline.Version)
			if err == nil {
				if changed, _ := result.RowsAffected(); changed != 1 {
					err = ErrProgressConflict
				}
			}
		}
	} else {
		result, err = tx.ExecContext(ctx, `
UPDATE recruiting_checkpoints
SET checkpoint_version = ?, recipe_id = ?, recipe_version = ?, contract_hash = ?,
    frontier_activity_at = ?, frontier_keys_json = ?, last_occurrence_id = ?, state_json = ?, updated_at = ?
WHERE source_id = ? AND checkpoint_version = ?`, committedCheckpoint.Version, committedCheckpoint.RecipeID,
			committedCheckpoint.RecipeVersion, committedCheckpoint.ContractHash, nullableTime(committedCheckpoint.FrontierActivityAt),
			nullableJSONStrings(committedCheckpoint.FrontierJobKeys), committedCheckpoint.LastOccurrenceID, checkpointState,
			input.CompletedAt.UTC(), committedCheckpoint.SourceID, checkpoint.Version)
	}
	if err != nil {
		return ListingCompletionOutcome{}, nil, err
	}
	if execution.Baseline == nil {
		if changed, _ := result.RowsAffected(); changed != 1 {
			return ListingCompletionOutcome{}, ErrProgressConflict, nil
		}
	}
	if err := updateAttemptStatusTx(ctx, tx, attempt.Status, succeededAttempt, outcomeState, input.CompletedAt); err != nil {
		return ListingCompletionOutcome{}, nil, err
	}
	if err := updateWorkTx(ctx, tx, work.Version, completedWork, input.CompletedAt); err != nil {
		return ListingCompletionOutcome{}, nil, err
	}
	if outcome.Occurrence != nil {
		if err := updateOccurrenceInTx(ctx, tx, execution.Occurrence.Version, *outcome.Occurrence, input.CompletedAt); err != nil {
			return ListingCompletionOutcome{}, nil, err
		}
	} else if outcome.ListingRun != nil {
		if err := updateListingRunInTx(ctx, tx, execution.ListingRun.Version, *outcome.ListingRun, input.CompletedAt); err != nil {
			return ListingCompletionOutcome{}, nil, err
		}
	}
	if err := releaseBudgetPermitTx(ctx, tx, attempt.AttemptID, model.PermitReleased, input.CompletedAt); err != nil {
		return ListingCompletionOutcome{}, nil, err
	}
	payload, _ := json.Marshal(map[string]any{"attempt_id": succeededAttempt.AttemptID, "work_id": completedWork.WorkID,
		"occurrence_id": nullableListingOccurrenceID(outcome.Occurrence), "listing_run_id": nullableListingRunID(outcome.ListingRun),
		"checkpoint_version": committedCheckpoint.Version})
	event, err := model.NewEventIntent("listing-completed-"+succeededAttempt.AttemptID, "listing.completed", "work",
		completedWork.WorkID, completedWork.Version, input.CompletedAt.UTC().Format(time.RFC3339Nano), input.CauseCommandID, payload)
	if err != nil {
		return ListingCompletionOutcome{}, nil, err
	}
	if err := appendEventIntent(ctx, tx, event, input.CompletedAt, input.CompletedAt); err != nil {
		return ListingCompletionOutcome{}, nil, err
	}
	if outcome.ListingRun != nil && outcome.ListingRun.RecoveryOfOccurrenceID != "" {
		recoveryPayload, _ := json.Marshal(map[string]any{
			"occurrence_id":  outcome.ListingRun.RecoveryOfOccurrenceID,
			"listing_run_id": outcome.ListingRun.ListingRunID,
			"work_id":        completedWork.WorkID, "checkpoint_version": committedCheckpoint.Version,
		})
		recoveryEvent, err := model.NewEventIntent("daily-occurrence-recovered-"+outcome.ListingRun.ListingRunID,
			"daily_occurrence.recovered", "listing_run", outcome.ListingRun.ListingRunID,
			outcome.ListingRun.Version, input.CompletedAt.UTC().Format(time.RFC3339Nano), input.CauseCommandID, recoveryPayload)
		if err != nil {
			return ListingCompletionOutcome{}, nil, err
		}
		if err := appendEventIntent(ctx, tx, recoveryEvent, input.CompletedAt, input.CompletedAt); err != nil {
			return ListingCompletionOutcome{}, nil, err
		}
	}
	if err := appendAttemptDispatch(ctx, tx, succeededAttempt.AttemptID, succeededAttempt.ExecutorActorID, succeededAttempt.Capability, "",
		"capacity_released", input.CauseCommandID, input.CompletedAt, input.CompletedAt); err != nil {
		return ListingCompletionOutcome{}, nil, err
	}
	if err := reserveResultReceipt(ctx, tx, input.CauseCommandID, executioncontract.TypeResult, input.RequestHash, outcome, input.CompletedAt); err != nil {
		return ListingCompletionOutcome{}, nil, err
	}
	if err := tx.Commit(); err != nil {
		return ListingCompletionOutcome{}, nil, err
	}
	return outcome, nil, nil
}

func validateResultArtifact(artifact model.ArtifactMetadata, attemptID string, kind model.ArtifactKind) error {
	validated, err := model.NewArtifactMetadata(artifact.ArtifactID, artifact.Kind, artifact.ContentHash, artifact.ObjectRef,
		artifact.WorkID, artifact.AttemptID, artifact.AccessScope, artifact.Retention, artifact.Redacted)
	if err != nil {
		return err
	}
	if validated != artifact || artifact.Kind != kind || artifact.AttemptID != attemptID {
		return fmt.Errorf("result artifact kind, attempt, or normalized metadata is invalid")
	}
	return nil
}

func replayListingPage(ctx context.Context, tx *sql.Tx, input ListingPageResult) (ListingPageOutcome, bool, error) {
	existing, rejected, found, err := getArtifactRecord(ctx, tx, input.Artifact.ArtifactID)
	if err != nil || !found {
		return ListingPageOutcome{}, false, err
	}
	if rejected || existing != input.Artifact {
		return ListingPageOutcome{}, false, fmt.Errorf("artifact ID already belongs to another result")
	}
	var state, outcomeState []byte
	err = tx.QueryRowContext(ctx, `
SELECT state_json, outcome_json FROM recruiting_listing_page_progress
WHERE attempt_id = ? AND page_sequence = ?`, input.AttemptID, input.PageSequence).Scan(&state, &outcomeState)
	if err != nil {
		return ListingPageOutcome{}, false, fmt.Errorf("accepted page artifact has no matching progress: %w", err)
	}
	var progress model.ListingPageProgress
	if err := json.Unmarshal(state, &progress); err != nil {
		return ListingPageOutcome{}, false, err
	}
	if progress.WorkID != input.Artifact.WorkID || progress.AttemptID != input.AttemptID ||
		progress.ArtifactID != input.Artifact.ArtifactID || progress.ItemCount != len(input.Observations) ||
		progress.ResumeCursor != input.ResumeCursor || progress.EndOfInput != input.Terminal {
		return ListingPageOutcome{}, false, ErrProgressConflict
	}
	var outcome ListingPageOutcome
	if len(outcomeState) == 0 || json.Unmarshal(outcomeState, &outcome) != nil || outcome.Progress != progress {
		return ListingPageOutcome{}, false, fmt.Errorf("accepted listing page has no stable outcome snapshot")
	}
	outcome.Replayed = true
	return outcome, true, nil
}

func replayListingCompletion(ctx context.Context, tx *sql.Tx, input ListingCompletion, attempt model.Attempt, work model.Work,
	execution listingResultExecution) (ListingCompletionOutcome, bool, error) {
	existing, rejected, found, err := getArtifactRecord(ctx, tx, input.Artifact.ArtifactID)
	if err != nil || !found {
		return ListingCompletionOutcome{}, false, err
	}
	contextCompleted := execution.Occurrence != nil && execution.Occurrence.Status == model.OccurrenceCompleted ||
		execution.ListingRun != nil && execution.ListingRun.Status == model.ListingRunCompleted ||
		execution.Baseline != nil && execution.Baseline.ListingFinalized
	if rejected || existing != input.Artifact || attempt.Status != model.AttemptSucceeded ||
		work.Status != model.WorkCompleted || !contextCompleted {
		return ListingCompletionOutcome{}, false, fmt.Errorf("completion artifact does not identify an accepted terminal result")
	}
	var outcomeState []byte
	if err := tx.QueryRowContext(ctx, "SELECT execution_result_json FROM recruiting_attempts WHERE attempt_id = ?", attempt.AttemptID).Scan(&outcomeState); err != nil {
		return ListingCompletionOutcome{}, false, err
	}
	var outcome ListingCompletionOutcome
	if len(outcomeState) == 0 || json.Unmarshal(outcomeState, &outcome) != nil {
		return ListingCompletionOutcome{}, false, fmt.Errorf("accepted listing completion has no stable result snapshot")
	}
	outcome.Replayed = true
	return outcome, true, nil
}

func nullableListingOccurrenceID(value *model.SourceOccurrence) any {
	if value == nil {
		return nil
	}
	return value.OccurrenceID
}

func nullableListingRunID(value *model.ListingRun) any {
	if value == nil {
		return nil
	}
	return value.ListingRunID
}

func getArtifactRecord(ctx context.Context, tx *sql.Tx, artifactID string) (model.ArtifactMetadata, bool, bool, error) {
	var artifact model.ArtifactMetadata
	var rejected bool
	var attemptID sql.NullString
	err := tx.QueryRowContext(ctx, `
SELECT artifact_kind, content_hash, object_ref, work_id, attempt_id, access_scope, retention_policy, redacted, rejected
FROM recruiting_artifacts WHERE artifact_id = ?`, artifactID).Scan(&artifact.Kind, &artifact.ContentHash, &artifact.ObjectRef,
		&artifact.WorkID, &attemptID, &artifact.AccessScope, &artifact.Retention, &artifact.Redacted, &rejected)
	if errors.Is(err, sql.ErrNoRows) {
		return model.ArtifactMetadata{}, false, false, nil
	}
	if err != nil {
		return model.ArtifactMetadata{}, false, false, err
	}
	artifact.ArtifactID, artifact.AttemptID = artifactID, attemptID.String
	return artifact, rejected, true, nil
}

func loadDetailPlacement(ctx context.Context, tx *sql.Tx, sourceID string) (string, string, error) {
	var sourceState, recipeState []byte
	err := tx.QueryRowContext(ctx, `
SELECT s.state_json, r.state_json
FROM recruiting_sources s
JOIN recruiting_source_assignments a ON a.source_id = s.source_id AND a.recipe_kind = 'detail'
JOIN recruiting_recipes r ON r.recipe_id = a.recipe_id AND r.recipe_version = a.recipe_version
WHERE s.source_id = ?`, sourceID).Scan(&sourceState, &recipeState)
	if err != nil {
		return "", "", fmt.Errorf("load detail execution placement: %w", err)
	}
	var source model.RecruitmentSource
	var recipe model.Recipe
	if err := json.Unmarshal(sourceState, &source); err != nil {
		return "", "", err
	}
	if err := json.Unmarshal(recipeState, &recipe); err != nil {
		return "", "", err
	}
	if source.DetailAssignment == nil || recipe.Status != model.RecipeActive || recipe.Kind != model.RecipeDetail ||
		recipe.RecipeID != source.DetailAssignment.RecipeID || recipe.Version != source.DetailAssignment.RecipeVersion ||
		recipe.ContractHash != source.DetailAssignment.ContractHash {
		return "", "", fmt.Errorf("source has no active matching detail recipe")
	}
	if recipe.Execution.RequiredCapability == "browser.recipe" && source.DetailProfileID == "" {
		return "", "", fmt.Errorf("browser.recipe detail Assignment has no Source Profile binding")
	}
	if recipe.Execution.RequiredCapability != "browser.recipe" && source.DetailProfileID != "" {
		return "", "", fmt.Errorf("non-Profile detail Assignment has a stale Source Profile binding")
	}
	return recipe.Execution.RequiredCapability, source.DetailProfileID, nil
}

func canonicalOrigin(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "", fmt.Errorf("detail URL has no canonical origin")
	}
	return parsed.Scheme + "://" + parsed.Host, nil
}

func getCheckpointForUpdate(ctx context.Context, tx *sql.Tx, sourceID string) (model.IncrementalCheckpoint, error) {
	var state []byte
	if err := tx.QueryRowContext(ctx, "SELECT state_json FROM recruiting_checkpoints WHERE source_id = ? FOR UPDATE", sourceID).Scan(&state); err != nil {
		return model.IncrementalCheckpoint{}, err
	}
	var checkpoint model.IncrementalCheckpoint
	if err := json.Unmarshal(state, &checkpoint); err != nil {
		return model.IncrementalCheckpoint{}, err
	}
	return checkpoint, nil
}

func updateAttemptStatusTx(ctx context.Context, tx *sql.Tx, expected model.AttemptStatus, attempt model.Attempt, resultState json.RawMessage, at time.Time) error {
	state, _ := json.Marshal(attempt)
	result, err := tx.ExecContext(ctx, `
UPDATE recruiting_attempts SET attempt_status = ?, state_json = ?, execution_result_json = ?, updated_at = ?
WHERE attempt_id = ? AND attempt_status = ?`, attempt.Status, state, nullableJSON(resultState), at.UTC(), attempt.AttemptID, expected)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return ErrAttemptConflict
	}
	return nil
}
