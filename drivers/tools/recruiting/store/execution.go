package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
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

func isCompanyImportPurpose(purpose string) bool {
	return purpose == "company_import" || purpose == "company_import_apply"
}

func isBudgetlessPurpose(purpose string) bool {
	return isCompanyImportPurpose(purpose) || purpose == "profile_repair" || purpose == "profile_verify"
}

type ListingOfferRequest struct {
	AttemptID           string
	ExecutorActorID     string
	ExecutorIncarnation string
	Capability          string
	Origin              string
	ProfileID           string
	OfferedAt           time.Time
	BudgetPolicy        ExecutionBudgetPolicy
	CompanyImportLimit  int
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
		request.BudgetPolicy.validate() != nil || request.CompanyImportLimit < 0 || request.CompanyImportLimit > 500 {
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
	  AND (w.profile_id IS NULL OR EXISTS (
	    SELECT 1 FROM recruiting_profiles eligible_profile
	    WHERE eligible_profile.profile_id = w.profile_id AND
	      ((w.purpose = 'profile_repair' AND eligible_profile.auth_status = 'repairing') OR
	       (w.purpose = 'profile_verify' AND eligible_profile.auth_status = 'verifying') OR
	       (w.purpose NOT IN ('profile_repair', 'profile_verify') AND eligible_profile.auth_status = 'ready'))
	  ))
	  AND ((w.purpose = 'listing_sync' AND (EXISTS (
	    SELECT 1 FROM recruiting_source_occurrences o
	    WHERE o.listing_work_id = w.work_id AND o.status IN ('queued', 'running')
	  ) OR EXISTS (
	    SELECT 1 FROM recruiting_listing_runs lr
	    WHERE lr.work_id = w.work_id AND lr.run_status IN ('queued', 'running')
	  ))) OR (w.purpose = 'source_validation' AND EXISTS (
	    SELECT 1 FROM recruiting_listing_runs lr
	    JOIN recruiting_sources validation_source ON validation_source.source_id = lr.source_id
	    JOIN recruiting_recipes validation_recipe
	      ON validation_recipe.recipe_id = JSON_UNQUOTE(JSON_EXTRACT(lr.state_json, '$.listing_execution.recipe_id'))
	     AND validation_recipe.recipe_version = CAST(JSON_UNQUOTE(JSON_EXTRACT(lr.state_json, '$.listing_execution.recipe_version')) AS UNSIGNED)
	    WHERE lr.work_id = w.work_id AND lr.run_mode = 'source_validation' AND lr.run_status IN ('queued', 'running')
	      AND validation_source.readiness_status = 'validating'
	      AND CAST(JSON_UNQUOTE(JSON_EXTRACT(lr.state_json, '$.source_version')) AS UNSIGNED) = validation_source.version
	      AND validation_recipe.status = 'active'
	      AND validation_recipe.content_hash = JSON_UNQUOTE(JSON_EXTRACT(lr.state_json, '$.listing_execution.content_hash'))
	      AND validation_recipe.contract_hash = JSON_UNQUOTE(JSON_EXTRACT(lr.state_json, '$.listing_execution.contract_hash'))
	  )) OR (w.purpose = 'recipe_validation' AND (EXISTS (
	    SELECT 1 FROM recruiting_listing_runs lr
	    JOIN recruiting_sources sample_source ON sample_source.source_id = lr.source_id
	    JOIN recruiting_recipes candidate_recipe
	      ON candidate_recipe.recipe_id = JSON_UNQUOTE(JSON_EXTRACT(lr.state_json, '$.listing_execution.recipe_id'))
	     AND candidate_recipe.recipe_version = CAST(JSON_UNQUOTE(JSON_EXTRACT(lr.state_json, '$.listing_execution.recipe_version')) AS UNSIGNED)
	    WHERE lr.work_id = w.work_id AND lr.run_mode = 'recipe_validation' AND lr.run_status IN ('queued', 'running')
	      AND sample_source.readiness_status = 'ready'
	      AND CAST(JSON_UNQUOTE(JSON_EXTRACT(lr.state_json, '$.source_version')) AS UNSIGNED) = sample_source.version
	      AND candidate_recipe.status = 'validating'
	      AND candidate_recipe.content_hash = JSON_UNQUOTE(JSON_EXTRACT(lr.state_json, '$.listing_execution.content_hash'))
	      AND candidate_recipe.contract_hash = JSON_UNQUOTE(JSON_EXTRACT(lr.state_json, '$.listing_execution.contract_hash'))
	  ) OR EXISTS (
	    SELECT 1 FROM recruiting_recipe_validation_runs rv
	    JOIN recruiting_sources sample_source ON sample_source.source_id = rv.source_id
	    JOIN recruiting_companies sample_company ON sample_company.company_id = sample_source.company_id
	    JOIN recruiting_recipes candidate_recipe
	      ON candidate_recipe.recipe_id = rv.recipe_id AND candidate_recipe.recipe_version = rv.recipe_version
	    JOIN recruiting_source_jobs sample_job ON sample_job.job_id = rv.sample_job_id AND sample_job.source_id = rv.source_id
	    WHERE rv.work_id = w.work_id AND rv.recipe_kind = 'detail' AND rv.run_status IN ('queued', 'running')
	      AND sample_company.onboarding_status = 'ready' AND sample_company.control_status = 'active'
	      AND sample_source.readiness_status = 'ready' AND sample_source.control_status = 'active'
	      AND sample_source.health_status = 'healthy' AND candidate_recipe.status = 'validating'
	      AND CAST(JSON_UNQUOTE(JSON_EXTRACT(rv.state_json, '$.company_version')) AS UNSIGNED) = sample_company.version
	      AND CAST(JSON_UNQUOTE(JSON_EXTRACT(rv.state_json, '$.source_version')) AS UNSIGNED) = sample_source.version
	      AND CAST(JSON_UNQUOTE(JSON_EXTRACT(rv.state_json, '$.sample_job_version')) AS UNSIGNED) = sample_job.version
	      AND candidate_recipe.content_hash = JSON_UNQUOTE(JSON_EXTRACT(rv.state_json, '$.candidate.content_hash'))
	      AND candidate_recipe.contract_hash = JSON_UNQUOTE(JSON_EXTRACT(rv.state_json, '$.candidate.contract_hash'))
	  ) OR EXISTS (
	    SELECT 1 FROM recruiting_recipe_validation_runs rv
	    JOIN recruiting_companies sample_company ON sample_company.company_id = rv.company_id
	    JOIN recruiting_recipes candidate_recipe
	      ON candidate_recipe.recipe_id = rv.recipe_id AND candidate_recipe.recipe_version = rv.recipe_version
	    WHERE rv.work_id = w.work_id AND rv.recipe_kind = 'discovery' AND rv.run_status IN ('queued', 'running')
	      AND sample_company.control_status = 'active' AND candidate_recipe.status = 'validating'
	      AND CAST(JSON_UNQUOTE(JSON_EXTRACT(rv.state_json, '$.company_version')) AS UNSIGNED) = sample_company.version
	      AND candidate_recipe.content_hash = JSON_UNQUOTE(JSON_EXTRACT(rv.state_json, '$.candidate.content_hash'))
	      AND candidate_recipe.contract_hash = JSON_UNQUOTE(JSON_EXTRACT(rv.state_json, '$.candidate.contract_hash'))
	  ))) OR (w.purpose = 'detail_sync' AND EXISTS (
	    SELECT 1 FROM recruiting_source_jobs j
	    WHERE j.job_id = w.target_id AND j.job_status IN ('detail_pending', 'update_pending')
	  )) OR (w.purpose = 'company_import' AND EXISTS (
	    SELECT 1 FROM recruiting_company_imports ci
	    WHERE ci.parent_work_id = w.work_id AND ci.import_status = 'previewing'
	  )) OR (w.purpose = 'company_import_apply' AND EXISTS (
	    SELECT 1 FROM recruiting_company_imports ci
	    WHERE ci.import_id = w.target_id AND ci.import_status IN ('running', 'canceling')
	  )) OR (w.purpose = 'source_discovery' AND EXISTS (
	    SELECT 1 FROM recruiting_source_discoveries sd
	    WHERE sd.work_id = w.work_id AND sd.discovery_status IN ('queued', 'running')
	  )) OR (w.purpose = 'baseline_listing' AND EXISTS (
	    SELECT 1 FROM recruiting_baseline_generations bg
	    WHERE bg.work_id = w.work_id AND bg.generation_status = 'listing' AND bg.listing_finalized = FALSE
	  )) OR (w.purpose = 'profile_repair' AND EXISTS (
	    SELECT 1 FROM recruiting_profile_repair_sessions prs
	    WHERE prs.work_id = w.work_id AND prs.session_status = 'awaiting_device'
	  )) OR (w.purpose = 'profile_verify' AND EXISTS (
	    SELECT 1 FROM recruiting_profile_repair_sessions prs
	    WHERE prs.validation_work_id = w.work_id AND prs.session_status = 'submitted'
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
	var listingRun model.ListingRun
	var recipeSampleValidation model.RecipeSampleValidation
	var baseline model.BaselineGeneration
	var checkpoint *model.IncrementalCheckpoint
	var detail *DetailExecutionInput
	var companyImport model.CompanyImport
	var companyImportItems []executioncontract.CompanyImportApplyItem
	var discovery model.SourceDiscovery
	var discoveryRecipe model.Recipe
	var profileRepair model.ProfileRepairSession
	var fence model.AttemptFence
	switch work.Purpose {
	case "listing_sync":
		occurrence, err = getOccurrenceByWorkWith(ctx, tx, work.WorkID, true)
		if errors.Is(err, ErrNotFound) {
			listingRun, err = getListingRunByWorkWith(ctx, tx, work.WorkID, true)
			if err == nil {
				checkpoint, fence, err = loadStandaloneListingOfferFence(ctx, tx, listingRun, placement.ProfileID)
			}
		} else if err == nil {
			checkpoint, fence, err = loadListingOfferFence(ctx, tx, occurrence, placement.ProfileID)
		}
	case "source_validation":
		listingRun, err = getListingRunByWorkWith(ctx, tx, work.WorkID, true)
		if err == nil {
			fence, err = loadSourceValidationOfferFence(ctx, tx, listingRun, placement.ProfileID)
		}
	case "recipe_validation":
		listingRun, err = getListingRunByWorkWith(ctx, tx, work.WorkID, true)
		if errors.Is(err, ErrNotFound) {
			recipeSampleValidation, err = getRecipeSampleValidationByWorkWith(ctx, tx, work.WorkID, true)
			if err == nil {
				fence, err = loadRecipeSampleValidationOfferFence(ctx, tx, recipeSampleValidation, placement.ProfileID)
			}
		} else if err == nil {
			fence, err = loadRecipeValidationOfferFence(ctx, tx, listingRun, placement.ProfileID)
		}
	case "detail_sync":
		detail, fence, err = loadDetailOfferFence(ctx, tx, work, placement)
	case "company_import":
		companyImport, err = getCompanyImportByWorkWith(ctx, tx, work.WorkID, true)
		if err == nil {
			fence.BatchVersion = companyImport.Version
		}
	case "company_import_apply":
		companyImport, err = getCompanyImportWith(ctx, tx, work.TargetID, true)
		if err == nil {
			limit := request.CompanyImportLimit
			if limit == 0 {
				limit = 100
			}
			companyImportItems, err = listPendingCompanyImportApplyItemsWith(ctx, tx, companyImport.ImportID, limit)
			fence.BatchVersion = companyImport.Version
		}
	case "source_discovery":
		discovery, discoveryRecipe, fence, err = loadSourceDiscoveryOfferFence(ctx, tx, work, placement)
	case "baseline_listing":
		baseline, fence, err = loadBaselineOfferFence(ctx, tx, work, placement)
	case "profile_repair":
		profileRepair, fence, err = loadProfileRepairOfferFence(ctx, tx, work, placement,
			request.ExecutorActorID, request.OfferedAt)
	case "profile_verify":
		profileRepair, fence, err = loadProfileVerificationOfferFence(ctx, tx, work, placement,
			request.ExecutorActorID, request.OfferedAt)
	default:
		err = fmt.Errorf("unsupported executable work purpose %q", work.Purpose)
	}
	if err != nil {
		return ExecutionOffer{}, err
	}
	sourceID := occurrence.SourceID
	if listingRun.ListingRunID != "" {
		sourceID = listingRun.SourceID
	}
	if baseline.WorkID != "" {
		sourceID = baseline.SourceID
	}
	if detail != nil {
		sourceID = detail.Job.SourceID
	}
	if recipeSampleValidation.ValidationRunID != "" {
		sourceID = recipeSampleValidation.SourceID
	}
	var companyID string
	if recipeSampleValidation.RecipeKind == model.RecipeDiscovery {
		companyID = recipeSampleValidation.CompanyID
	} else if work.Purpose == "source_discovery" {
		companyID = discovery.CompanyID
	} else if !isBudgetlessPurpose(work.Purpose) {
		if err := tx.QueryRowContext(ctx, "SELECT company_id FROM recruiting_sources WHERE source_id = ?", sourceID).Scan(&companyID); err != nil {
			return ExecutionOffer{}, fmt.Errorf("load execution company: %w", err)
		}
	}
	attempt, err := model.NewAttempt(request.AttemptID, work)
	if err == nil {
		attempt, err = attempt.BindExecutor(strings.TrimSpace(request.ExecutorActorID), strings.TrimSpace(request.ExecutorIncarnation), request.Capability)
	}
	if err == nil {
		if isCompanyImportPurpose(work.Purpose) {
			attempt, err = attempt.WithBatchFence(fence.BatchVersion)
		} else if work.Purpose == "profile_repair" || work.Purpose == "profile_verify" {
			attempt, err = attempt.WithProfileRepairFence(fence.ProfileID, fence.ProfileVersion)
		} else if work.Purpose == "source_discovery" {
			attempt, err = attempt.WithDiscoveryFence(fence)
		} else if work.Purpose == "recipe_validation" && recipeSampleValidation.RecipeKind == model.RecipeDiscovery {
			attempt, err = attempt.WithCompanyRecipeFence(fence)
		} else {
			attempt, err = attempt.WithFence(fence)
		}
	}
	if err != nil {
		return ExecutionOffer{}, err
	}
	var permit model.BudgetPermit
	var permitExpiresAt time.Time
	if !isBudgetlessPurpose(work.Purpose) {
		permit, permitExpiresAt, err = acquireBudgetPermitTx(ctx, tx, attempt.AttemptID, placement.Origin, placement.ProfileID,
			placement.Capability, companyID, request.BudgetPolicy, request.OfferedAt)
		if err != nil {
			return ExecutionOffer{}, err
		}
	}
	offer := ExecutionOffer{
		Kind: strings.TrimSuffix(work.Purpose, "_sync"), Attempt: attempt, Work: work, Checkpoint: checkpoint,
		Detail: detail, Budget: permit,
		RequestedCapability: strings.TrimSpace(request.Capability), RequestedOrigin: strings.TrimSpace(request.Origin),
		RequestedProfileID: strings.TrimSpace(request.ProfileID),
	}
	if !permitExpiresAt.IsZero() {
		offer.BudgetExpiresAt = permitExpiresAt.Format(time.RFC3339Nano)
	}
	if work.Purpose == "listing_sync" || work.Purpose == "source_validation" ||
		(work.Purpose == "recipe_validation" && recipeSampleValidation.ValidationRunID == "") {
		if listingRun.ListingRunID != "" {
			offer.ListingRun = &listingRun
		} else {
			offer.Occurrence = &occurrence
		}
		if work.Purpose == "source_validation" || work.Purpose == "recipe_validation" {
			offer.Kind = "listing"
		}
	} else if work.Purpose == "recipe_validation" {
		offer.Kind = string(recipeSampleValidation.RecipeKind)
		offer.RecipeValidation = &recipeSampleValidation
	} else if isCompanyImportPurpose(work.Purpose) {
		offer.CompanyImport = &companyImport
		offer.CompanyImportItems = companyImportItems
	} else if work.Purpose == "source_discovery" {
		offer.Discovery = &discovery
		offer.Recipe = &discoveryRecipe
	} else if work.Purpose == "baseline_listing" {
		offer.Kind = "listing"
		offer.Baseline = &baseline
	} else if work.Purpose == "profile_repair" {
		offer.Kind = "profile_repair"
		offer.ProfileRepair = &profileRepair
	} else if work.Purpose == "profile_verify" {
		offer.Kind = "profile_verification"
		offer.ProfileRepair = &profileRepair
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

func loadStandaloneListingOfferFence(ctx context.Context, tx *sql.Tx, run model.ListingRun, profileID string) (*model.IncrementalCheckpoint, model.AttemptFence, error) {
	if err := run.ListingExecution.Validate(run.SourceID); err != nil ||
		(run.Status != model.ListingRunQueued && run.Status != model.ListingRunRunning) {
		return nil, model.AttemptFence{}, fmt.Errorf("standalone listing run is not executable")
	}
	current, err := readListingRunPreparation(ctx, tx, run.SourceID, true)
	if err != nil {
		return nil, model.AttemptFence{}, err
	}
	expected, err := current.NewRun(run.ListingRunID, run.WorkID, run.Mode)
	if err != nil {
		return nil, model.AttemptFence{}, err
	}
	// A diagnostic may intentionally use the checkpoint frozen when the user
	// started it while a scheduled run advances the current checkpoint. All
	// executable code and aggregate versions must still remain identical.
	expected.Checkpoint, expected.CheckpointVersion = run.Checkpoint, run.CheckpointVersion
	if expected.CompanyVersion != run.CompanyVersion || expected.SourceVersion != run.SourceVersion ||
		!reflect.DeepEqual(expected.ListingExecution, run.ListingExecution) {
		return nil, model.AttemptFence{}, fmt.Errorf("standalone listing run is fenced by changed source or recipe")
	}
	fence := model.AttemptFence{CompanyVersion: run.CompanyVersion, SourceVersion: run.SourceVersion,
		AssignmentVersion: run.ListingExecution.Assignment.AssignmentVersion, RecipeID: run.ListingExecution.RecipeID,
		RecipeVersion: run.ListingExecution.RecipeVersion, CheckpointVersion: run.CheckpointVersion}
	if profileID != "" {
		var profileState []byte
		if err := tx.QueryRowContext(ctx, "SELECT state_json FROM recruiting_profiles WHERE profile_id = ?", profileID).Scan(&profileState); err != nil {
			return nil, model.AttemptFence{}, fmt.Errorf("load standalone listing profile: %w", err)
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
	return run.Checkpoint, fence, nil
}

func loadSourceValidationOfferFence(ctx context.Context, tx *sql.Tx, run model.ListingRun, profileID string) (model.AttemptFence, error) {
	if run.Mode != model.ListingRunValidation || run.Checkpoint != nil || run.CheckpointVersion != 0 ||
		(run.Status != model.ListingRunQueued && run.Status != model.ListingRunRunning) {
		return model.AttemptFence{}, fmt.Errorf("source validation run is not executable")
	}
	current, err := readSourceValidationPreparation(ctx, tx, run.SourceID, run.ListingExecution.RecipeID,
		run.ListingExecution.RecipeVersion, true)
	if err != nil {
		return model.AttemptFence{}, err
	}
	if current.Source.ReadinessStatus != model.SourceValidating || current.Source.Version != run.SourceVersion ||
		current.Company.Version != run.CompanyVersion {
		return model.AttemptFence{}, fmt.Errorf("source validation run is fenced by changed aggregate state")
	}
	execution, err := model.NewCandidateListingExecutionSnapshot(current.Source, current.Recipe,
		run.ListingExecution.Assignment)
	if err != nil || !reflect.DeepEqual(execution, run.ListingExecution) {
		return model.AttemptFence{}, fmt.Errorf("source validation run is fenced by changed endpoint or recipe")
	}
	fence := model.AttemptFence{CompanyVersion: run.CompanyVersion, SourceVersion: run.SourceVersion,
		AssignmentVersion: run.ListingExecution.Assignment.AssignmentVersion, RecipeID: run.ListingExecution.RecipeID,
		RecipeVersion: run.ListingExecution.RecipeVersion}
	if profileID != "" {
		var profileState []byte
		if err := tx.QueryRowContext(ctx, "SELECT state_json FROM recruiting_profiles WHERE profile_id = ?", profileID).Scan(&profileState); err != nil {
			return model.AttemptFence{}, fmt.Errorf("load source validation profile: %w", err)
		}
		var profile model.BrowserProfile
		if err := json.Unmarshal(profileState, &profile); err != nil {
			return model.AttemptFence{}, err
		}
		if profile.AuthStatus != model.ProfileReady {
			return model.AttemptFence{}, fmt.Errorf("source validation profile is not ready")
		}
		fence.ProfileID, fence.ProfileVersion = profile.ProfileID, profile.Version
	}
	return fence, nil
}

func loadRecipeValidationOfferFence(ctx context.Context, tx *sql.Tx, run model.ListingRun, profileID string) (model.AttemptFence, error) {
	if run.Mode != model.ListingRunRecipeValidation || run.Checkpoint != nil || run.CheckpointVersion != 0 ||
		(run.Status != model.ListingRunQueued && run.Status != model.ListingRunRunning) {
		return model.AttemptFence{}, fmt.Errorf("Recipe validation run is not executable")
	}
	var companyState, sourceState, assignmentState, recipeState []byte
	err := tx.QueryRowContext(ctx, `SELECT c.state_json, s.state_json, a.state_json, r.state_json
FROM recruiting_sources s
JOIN recruiting_companies c ON c.company_id = s.company_id
JOIN recruiting_source_assignments a ON a.source_id = s.source_id AND a.recipe_kind = 'listing'
JOIN recruiting_recipes r ON r.recipe_id = ? AND r.recipe_version = ?
WHERE s.source_id = ? FOR UPDATE`, run.ListingExecution.RecipeID, run.ListingExecution.RecipeVersion,
		run.SourceID).Scan(&companyState, &sourceState, &assignmentState, &recipeState)
	if err != nil {
		return model.AttemptFence{}, fmt.Errorf("load Recipe validation fence: %w", err)
	}
	var company model.Company
	var source model.RecruitmentSource
	var currentAssignment model.SourceRecipeAssignment
	var recipe model.Recipe
	for _, item := range []struct {
		data   []byte
		target any
	}{{companyState, &company}, {sourceState, &source}, {assignmentState, &currentAssignment}, {recipeState, &recipe}} {
		if err := json.Unmarshal(item.data, item.target); err != nil {
			return model.AttemptFence{}, err
		}
	}
	if company.Version != run.CompanyVersion || company.OnboardingStatus != model.CompanyReady ||
		company.ControlStatus != model.ControlActive || source.Version != run.SourceVersion ||
		source.ReadinessStatus != model.SourceReady || source.ControlStatus != model.ControlActive ||
		source.HealthStatus != model.HealthHealthy || source.ListingAssignment == nil ||
		!reflect.DeepEqual(*source.ListingAssignment, currentAssignment) || recipe.Status != model.RecipeValidating ||
		recipe.Kind != model.RecipeListing || recipe.ContentHash != run.ListingExecution.ContentHash ||
		recipe.ContractHash != run.ListingExecution.ContractHash || recipe.Execution != run.ListingExecution.Execution {
		return model.AttemptFence{}, fmt.Errorf("Recipe validation run is fenced by changed Source or candidate Recipe")
	}
	proposed, err := currentAssignment.Replace(currentAssignment.AssignmentVersion, recipe.RecipeID, recipe.Version,
		recipe.ContractHash, run.ListingExecution.Assignment.EffectiveAt)
	if err != nil {
		return model.AttemptFence{}, err
	}
	execution, err := model.NewRecipeValidationListingExecutionSnapshot(source, recipe, proposed)
	if err != nil || !reflect.DeepEqual(execution, run.ListingExecution) {
		return model.AttemptFence{}, fmt.Errorf("Recipe validation run is fenced by changed endpoint or assignment")
	}
	fence := model.AttemptFence{CompanyVersion: run.CompanyVersion, SourceVersion: run.SourceVersion,
		AssignmentVersion: proposed.AssignmentVersion, RecipeID: recipe.RecipeID, RecipeVersion: recipe.Version}
	if profileID != "" {
		var profileState []byte
		if err := tx.QueryRowContext(ctx, "SELECT state_json FROM recruiting_profiles WHERE profile_id = ?", profileID).Scan(&profileState); err != nil {
			return model.AttemptFence{}, fmt.Errorf("load Recipe validation profile: %w", err)
		}
		var profile model.BrowserProfile
		if err := json.Unmarshal(profileState, &profile); err != nil || profile.AuthStatus != model.ProfileReady {
			return model.AttemptFence{}, fmt.Errorf("Recipe validation Profile is not ready")
		}
		fence.ProfileID, fence.ProfileVersion = profile.ProfileID, profile.Version
	}
	return fence, nil
}

func loadRecipeSampleValidationOfferFence(ctx context.Context, tx *sql.Tx, run model.RecipeSampleValidation,
	profileID string) (model.AttemptFence, error) {
	if err := run.Validate(); err != nil || (run.Status != model.RecipeSampleValidationQueued &&
		run.Status != model.RecipeSampleValidationRunning) {
		return model.AttemptFence{}, fmt.Errorf("Recipe sample validation run is not executable")
	}
	if run.RecipeKind == model.RecipeDiscovery {
		return loadDiscoveryRecipeValidationOfferFence(ctx, tx, run, profileID)
	}
	var companyState, sourceState, assignmentState, recipeState, jobState []byte
	err := tx.QueryRowContext(ctx, `SELECT c.state_json, s.state_json, a.state_json, r.state_json, j.state_json
FROM recruiting_sources s
JOIN recruiting_companies c ON c.company_id = s.company_id
JOIN recruiting_source_assignments a ON a.source_id = s.source_id AND a.recipe_kind = 'detail'
JOIN recruiting_recipes r ON r.recipe_id = ? AND r.recipe_version = ?
JOIN recruiting_source_jobs j ON j.job_id = ? AND j.source_id = s.source_id
WHERE s.source_id = ? FOR UPDATE`, run.Candidate.RecipeID, run.Candidate.Version, run.SampleJobID,
		run.SourceID).Scan(&companyState, &sourceState, &assignmentState, &recipeState, &jobState)
	if err != nil {
		return model.AttemptFence{}, fmt.Errorf("load Recipe sample validation fence: %w", err)
	}
	var company model.Company
	var source model.RecruitmentSource
	var assignment model.SourceRecipeAssignment
	var recipe model.Recipe
	var job model.SourceJob
	for _, item := range []struct {
		data   []byte
		target any
	}{{companyState, &company}, {sourceState, &source}, {assignmentState, &assignment},
		{recipeState, &recipe}, {jobState, &job}} {
		if err := json.Unmarshal(item.data, item.target); err != nil {
			return model.AttemptFence{}, err
		}
	}
	if company.Version != run.CompanyVersion || company.OnboardingStatus != model.CompanyReady ||
		company.ControlStatus != model.ControlActive || source.Version != run.SourceVersion ||
		source.ReadinessStatus != model.SourceReady || source.ControlStatus != model.ControlActive ||
		source.HealthStatus != model.HealthHealthy || source.DetailAssignment == nil ||
		!reflect.DeepEqual(*source.DetailAssignment, assignment) || recipe != run.Candidate ||
		recipe.Status != model.RecipeValidating || job.SourceID != run.SourceID ||
		job.JobID != run.SampleJobID || job.Version != run.SampleJobVersion ||
		job.DetailURL != run.EndpointURL {
		return model.AttemptFence{}, fmt.Errorf("Recipe sample validation run is fenced by changed candidate or Job")
	}
	proposed, err := assignment.Replace(assignment.AssignmentVersion, recipe.RecipeID, recipe.Version,
		recipe.ContractHash, run.ProposedAssignment.EffectiveAt)
	if err != nil || proposed != run.ProposedAssignment {
		return model.AttemptFence{}, fmt.Errorf("Recipe sample validation proposed Assignment changed")
	}
	fence := model.AttemptFence{CompanyVersion: run.CompanyVersion, SourceVersion: run.SourceVersion,
		AssignmentVersion: proposed.AssignmentVersion, RecipeID: recipe.RecipeID, RecipeVersion: recipe.Version,
		SampleVersion: job.Version}
	if profileID != "" {
		var profileState []byte
		if err := tx.QueryRowContext(ctx, "SELECT state_json FROM recruiting_profiles WHERE profile_id = ?", profileID).Scan(&profileState); err != nil {
			return model.AttemptFence{}, fmt.Errorf("load Recipe sample validation Profile: %w", err)
		}
		var profile model.BrowserProfile
		if err := json.Unmarshal(profileState, &profile); err != nil || profile.AuthStatus != model.ProfileReady {
			return model.AttemptFence{}, fmt.Errorf("Recipe sample validation Profile is not ready")
		}
		fence.ProfileID, fence.ProfileVersion = profile.ProfileID, profile.Version
	}
	return fence, nil
}

func loadDiscoveryRecipeValidationOfferFence(ctx context.Context, tx *sql.Tx, run model.RecipeSampleValidation,
	profileID string) (model.AttemptFence, error) {
	var companyState, recipeState []byte
	if err := tx.QueryRowContext(ctx, `SELECT c.state_json, r.state_json FROM recruiting_companies c
JOIN recruiting_recipes r ON r.recipe_id = ? AND r.recipe_version = ?
WHERE c.company_id = ? FOR UPDATE`, run.Candidate.RecipeID, run.Candidate.Version, run.CompanyID).Scan(
		&companyState, &recipeState); err != nil {
		return model.AttemptFence{}, fmt.Errorf("load Discovery Recipe validation fence: %w", err)
	}
	var company model.Company
	var recipe model.Recipe
	if err := json.Unmarshal(companyState, &company); err != nil {
		return model.AttemptFence{}, err
	}
	if err := json.Unmarshal(recipeState, &recipe); err != nil {
		return model.AttemptFence{}, err
	}
	if company.Version != run.CompanyVersion || company.ControlStatus != model.ControlActive ||
		company.Website != run.EndpointURL || recipe != run.Candidate || recipe.Status != model.RecipeValidating {
		return model.AttemptFence{}, fmt.Errorf("Discovery Recipe validation is fenced by changed Company or candidate")
	}
	fence := model.AttemptFence{CompanyVersion: company.Version, RecipeID: recipe.RecipeID, RecipeVersion: recipe.Version}
	if profileID != "" {
		var profileState []byte
		if err := tx.QueryRowContext(ctx, "SELECT state_json FROM recruiting_profiles WHERE profile_id = ?", profileID).Scan(&profileState); err != nil {
			return model.AttemptFence{}, fmt.Errorf("load Discovery Recipe validation Profile: %w", err)
		}
		var profile model.BrowserProfile
		if err := json.Unmarshal(profileState, &profile); err != nil || profile.AuthStatus != model.ProfileReady {
			return model.AttemptFence{}, fmt.Errorf("Discovery Recipe validation Profile is not ready")
		}
		fence.ProfileID, fence.ProfileVersion = profile.ProfileID, profile.Version
	}
	return fence, nil
}

func loadBaselineOfferFence(ctx context.Context, tx *sql.Tx, work model.Work, placement WorkPlacement) (model.BaselineGeneration, model.AttemptFence, error) {
	if work.Purpose != "baseline_listing" || work.TargetType != "source" {
		return model.BaselineGeneration{}, model.AttemptFence{}, fmt.Errorf("baseline requires source listing Work")
	}
	var state []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_baseline_generations
WHERE work_id = ? FOR UPDATE`, work.WorkID).Scan(&state); err != nil {
		return model.BaselineGeneration{}, model.AttemptFence{}, fmt.Errorf("load baseline execution: %w", err)
	}
	var baseline model.BaselineGeneration
	if err := json.Unmarshal(state, &baseline); err != nil {
		return model.BaselineGeneration{}, model.AttemptFence{}, err
	}
	preparation, err := readListingRunPreparation(ctx, tx, baseline.SourceID, true)
	if err != nil {
		return model.BaselineGeneration{}, model.AttemptFence{}, err
	}
	expected, err := model.NewExecutableBaselineGeneration(work.WorkID, preparation.Company, preparation.Source,
		baseline.Generation, preparation.Recipe)
	if err != nil || !reflect.DeepEqual(expected, baseline) || baseline.ListingFinalized ||
		placement.Capability != baseline.ListingExecution.Execution.RequiredCapability || placement.Origin != baseline.ListingExecution.Origin {
		return model.BaselineGeneration{}, model.AttemptFence{}, fmt.Errorf("baseline is fenced by changed Company, Source, Recipe, or placement")
	}
	fence := model.AttemptFence{CompanyVersion: baseline.CompanyVersion, SourceVersion: baseline.SourceVersion,
		AssignmentVersion: baseline.ListingExecution.Assignment.AssignmentVersion, RecipeID: baseline.ListingExecution.RecipeID,
		RecipeVersion: baseline.ListingExecution.RecipeVersion}
	if placement.ProfileID != "" {
		var profileState []byte
		if err := tx.QueryRowContext(ctx, "SELECT state_json FROM recruiting_profiles WHERE profile_id = ?", placement.ProfileID).Scan(&profileState); err != nil {
			return model.BaselineGeneration{}, model.AttemptFence{}, err
		}
		var profile model.BrowserProfile
		if err := json.Unmarshal(profileState, &profile); err != nil || profile.AuthStatus != model.ProfileReady {
			return model.BaselineGeneration{}, model.AttemptFence{}, fmt.Errorf("baseline Profile is not ready")
		}
		fence.ProfileID, fence.ProfileVersion = profile.ProfileID, profile.Version
	}
	return baseline, fence, nil
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
	companyAllowsDetail := company.OnboardingStatus == model.CompanyReady
	if company.OnboardingStatus == model.CompanyInitializing && work.ParentWorkID != "" {
		var baselineMember bool
		if err := tx.QueryRowContext(ctx, `SELECT EXISTS(
  SELECT 1 FROM recruiting_baseline_detail_items item
  JOIN recruiting_baseline_generations baseline
    ON baseline.source_id = item.source_id AND baseline.baseline_generation = item.baseline_generation
  WHERE item.job_id = ? AND baseline.work_id = ? AND item.accounting_status = 'pending'
)`, job.JobID, work.ParentWorkID).Scan(&baselineMember); err != nil {
			return nil, model.AttemptFence{}, fmt.Errorf("check initializing baseline detail membership: %w", err)
		}
		companyAllowsDetail = baselineMember
	}
	if !companyAllowsDetail || company.ControlStatus != model.ControlActive ||
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

func loadSourceDiscoveryOfferFence(ctx context.Context, tx *sql.Tx, work model.Work, placement WorkPlacement) (model.SourceDiscovery, model.Recipe, model.AttemptFence, error) {
	if work.TargetType != "company" || work.Purpose != "source_discovery" {
		return model.SourceDiscovery{}, model.Recipe{}, model.AttemptFence{}, fmt.Errorf("source discovery requires company work")
	}
	var discoveryState []byte
	if err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_source_discoveries WHERE work_id = ? FOR UPDATE`, work.WorkID).Scan(&discoveryState); err != nil {
		return model.SourceDiscovery{}, model.Recipe{}, model.AttemptFence{}, fmt.Errorf("load source discovery: %w", err)
	}
	var discovery model.SourceDiscovery
	if err := json.Unmarshal(discoveryState, &discovery); err != nil {
		return model.SourceDiscovery{}, model.Recipe{}, model.AttemptFence{}, err
	}
	var companyState, recipeState []byte
	if err := tx.QueryRowContext(ctx, `
SELECT c.state_json, r.state_json
FROM recruiting_companies c
JOIN recruiting_recipes r ON r.recipe_id = ? AND r.recipe_version = ?
WHERE c.company_id = ?`, discovery.RecipeID, discovery.RecipeVersion, discovery.CompanyID).Scan(&companyState, &recipeState); err != nil {
		return model.SourceDiscovery{}, model.Recipe{}, model.AttemptFence{}, fmt.Errorf("load source discovery dependencies: %w", err)
	}
	var company model.Company
	var recipe model.Recipe
	if err := json.Unmarshal(companyState, &company); err != nil {
		return model.SourceDiscovery{}, model.Recipe{}, model.AttemptFence{}, err
	}
	if err := json.Unmarshal(recipeState, &recipe); err != nil {
		return model.SourceDiscovery{}, model.Recipe{}, model.AttemptFence{}, err
	}
	origin, err := canonicalOrigin(discovery.SeedURL)
	if err != nil || discovery.WorkID != work.WorkID || discovery.CompanyID != work.TargetID ||
		(discovery.Status != model.SourceDiscoveryQueued && discovery.Status != model.SourceDiscoveryRunning) ||
		company.Version != discovery.CompanyVersion || company.ControlStatus == model.ControlArchived || company.Website != discovery.SeedURL ||
		recipe.Status != model.RecipeActive || recipe.Kind != model.RecipeDiscovery || recipe.RecipeID != discovery.RecipeID ||
		recipe.Version != discovery.RecipeVersion || recipe.ContentHash != discovery.RecipeContentHash ||
		recipe.ContractHash != discovery.ContractHash || recipe.Execution != discovery.Execution ||
		placement.Capability != recipe.Execution.RequiredCapability || placement.Origin != origin {
		return model.SourceDiscovery{}, model.Recipe{}, model.AttemptFence{}, fmt.Errorf("source discovery is fenced by changed Company, Recipe, or placement")
	}
	fence := model.AttemptFence{CompanyVersion: company.Version, DiscoveryGeneration: discovery.Generation,
		RecipeID: recipe.RecipeID, RecipeVersion: recipe.Version}
	if placement.ProfileID != "" {
		var profileState []byte
		if err := tx.QueryRowContext(ctx, "SELECT state_json FROM recruiting_profiles WHERE profile_id = ?", placement.ProfileID).Scan(&profileState); err != nil {
			return model.SourceDiscovery{}, model.Recipe{}, model.AttemptFence{}, fmt.Errorf("load source discovery profile: %w", err)
		}
		var profile model.BrowserProfile
		if err := json.Unmarshal(profileState, &profile); err != nil {
			return model.SourceDiscovery{}, model.Recipe{}, model.AttemptFence{}, err
		}
		if profile.AuthStatus != model.ProfileReady {
			return model.SourceDiscovery{}, model.Recipe{}, model.AttemptFence{}, fmt.Errorf("source discovery profile is not ready")
		}
		fence.ProfileID, fence.ProfileVersion = profile.ProfileID, profile.Version
	}
	return discovery, recipe, fence, nil
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
	causeCommandID string
	before         func(*sql.Tx) (bool, error)
	after          func(*sql.Tx, model.Attempt, model.Work) error
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
	unlockRepair, err := r.lockRepairFailureCommand(ctx, command, businessAt)
	if err != nil {
		return CommandResult{}, err
	}
	defer unlockRepair()
	hooks := &executionTransitionHooks{
		causeCommandID: command.CommandID,
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
	err = nil
	for transactionAttempt := 0; transactionAttempt < 32; transactionAttempt++ {
		_, err = r.transitionListingExecution(ctx, command.AttemptID, command.RequestedBy, command.ExecutorIncarnation,
			command.Action, command.Reason, command.Failure, failurePolicyPointer(command), businessAt, hooks)
		if !isMySQLTransactionContention(err) {
			break
		}
		if waitErr := waitForTransactionRetry(ctx, transactionAttempt); waitErr != nil {
			return CommandResult{}, waitErr
		}
	}
	if errors.Is(err, ErrCommandConflict) {
		return r.replayCommittedCommand(ctx, command.CommandID, command.RequestHash)
	}
	if command.Action == "fail" && command.Failure != nil &&
		(errors.Is(err, ErrAttemptConflict) || errors.Is(err, ErrResultFenced)) {
		if artifactErr := r.saveRejectedArtifacts(ctx, command.Failure.EvidenceArtifacts(), businessAt); artifactErr != nil {
			return CommandResult{}, fmt.Errorf("%w; also failed to retain rejected failure artifact: %v", err, artifactErr)
		}
		return CommandResult{}, fmt.Errorf("%w: late execution failure was fenced", ErrResultFenced)
	}
	if err != nil {
		return CommandResult{}, err
	}
	return CommandResult{Response: append(json.RawMessage(nil), response...), Replayed: replayed}, nil
}

func isMySQLTransactionContention(err error) bool {
	var driverError *mysql.MySQLError
	return errors.As(err, &driverError) && (driverError.Number == 1213 || driverError.Number == 1205)
}

func waitForTransactionRetry(ctx context.Context, attempt int) error {
	delay := time.Millisecond << min(attempt, 6)
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
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
	if action != "fail" && !isBudgetlessPurpose(work.Purpose) {
		if err := ensureBudgetPermitActiveTx(ctx, tx, attempt.AttemptID, businessAt); err != nil {
			return model.Attempt{}, err
		}
	}
	var occurrence model.SourceOccurrence
	var listingRun model.ListingRun
	var recipeSampleValidation model.RecipeSampleValidation
	var currentDiscovery model.SourceDiscovery
	var currentProfileRepair model.ProfileRepairSession
	var currentFence model.AttemptFence
	// Ordinary execution failures may be recorded after a domain configuration
	// change because they only close old authority. Profile control failures are
	// different: they also transition the bound repair session (and verification
	// failures move Profile back to repairing), so they must recheck that fence.
	if action != "fail" || work.Purpose == "profile_repair" || work.Purpose == "profile_verify" {
		var fenceErr error
		switch work.Purpose {
		case "listing_sync":
			occurrence, fenceErr = getOccurrenceByWorkWith(ctx, tx, work.WorkID, true)
			if errors.Is(fenceErr, ErrNotFound) {
				listingRun, fenceErr = getListingRunByWorkWith(ctx, tx, work.WorkID, true)
				if fenceErr == nil {
					_, currentFence, fenceErr = loadStandaloneListingOfferFence(ctx, tx, listingRun, attempt.ProfileID)
				}
			} else if fenceErr == nil {
				_, currentFence, fenceErr = loadListingOfferFence(ctx, tx, occurrence, attempt.ProfileID)
			}
		case "source_validation":
			listingRun, fenceErr = getListingRunByWorkWith(ctx, tx, work.WorkID, true)
			if fenceErr == nil {
				currentFence, fenceErr = loadSourceValidationOfferFence(ctx, tx, listingRun, attempt.ProfileID)
			}
		case "recipe_validation":
			listingRun, fenceErr = getListingRunByWorkWith(ctx, tx, work.WorkID, true)
			if errors.Is(fenceErr, ErrNotFound) {
				recipeSampleValidation, fenceErr = getRecipeSampleValidationByWorkWith(ctx, tx, work.WorkID, true)
				if fenceErr == nil {
					currentFence, fenceErr = loadRecipeSampleValidationOfferFence(ctx, tx, recipeSampleValidation, attempt.ProfileID)
				}
			} else if fenceErr == nil {
				currentFence, fenceErr = loadRecipeValidationOfferFence(ctx, tx, listingRun, attempt.ProfileID)
			}
		case "detail_sync":
			placement, placementErr := getWorkPlacementWith(ctx, tx, work.WorkID)
			if placementErr != nil {
				fenceErr = placementErr
			} else {
				_, currentFence, fenceErr = loadDetailOfferFence(ctx, tx, work, placement)
			}
		case "company_import":
			var batch model.CompanyImport
			batch, fenceErr = getCompanyImportByWorkWith(ctx, tx, work.WorkID, true)
			if fenceErr == nil {
				currentFence.BatchVersion = batch.Version
			}
		case "company_import_apply":
			var batch model.CompanyImport
			batch, fenceErr = getCompanyImportWith(ctx, tx, work.TargetID, true)
			if fenceErr == nil {
				currentFence.BatchVersion = batch.Version
			}
		case "source_discovery":
			placement, placementErr := getWorkPlacementWith(ctx, tx, work.WorkID)
			if placementErr != nil {
				fenceErr = placementErr
			} else {
				currentDiscovery, _, currentFence, fenceErr = loadSourceDiscoveryOfferFence(ctx, tx, work, placement)
			}
		case "baseline_listing":
			placement, placementErr := getWorkPlacementWith(ctx, tx, work.WorkID)
			if placementErr != nil {
				fenceErr = placementErr
			} else {
				_, currentFence, fenceErr = loadBaselineOfferFence(ctx, tx, work, placement)
			}
		case "profile_repair":
			placement, placementErr := getWorkPlacementWith(ctx, tx, work.WorkID)
			if placementErr != nil {
				fenceErr = placementErr
			} else if action == "fail" {
				currentProfileRepair, currentFence, fenceErr = loadActiveProfileRepairFailureFence(ctx, tx, work,
					placement, attempt, businessAt)
			} else {
				currentProfileRepair, currentFence, fenceErr = loadProfileRepairOfferFence(ctx, tx, work, placement,
					attempt.ExecutorActorID, businessAt)
			}
		case "profile_verify":
			placement, placementErr := getWorkPlacementWith(ctx, tx, work.WorkID)
			if placementErr != nil {
				fenceErr = placementErr
			} else {
				currentProfileRepair, currentFence, fenceErr = loadProfileVerificationOfferFence(ctx, tx, work, placement,
					attempt.ExecutorActorID, businessAt)
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
		if err == nil && (work.Purpose == "listing_sync" || work.Purpose == "source_validation" ||
			work.Purpose == "recipe_validation") && listingRun.ListingRunID != "" && listingRun.Status == model.ListingRunQueued {
			previousRunVersion := listingRun.Version
			listingRun, err = listingRun.Start(listingRun.Version)
			if err == nil {
				err = updateListingRunInTx(ctx, tx, previousRunVersion, listingRun, businessAt)
			}
		}
		if err == nil && work.Purpose == "recipe_validation" &&
			recipeSampleValidation.ValidationRunID != "" && recipeSampleValidation.Status == model.RecipeSampleValidationQueued {
			previousRunVersion := recipeSampleValidation.Version
			recipeSampleValidation, err = recipeSampleValidation.Start(recipeSampleValidation.Version)
			if err == nil {
				err = updateRecipeSampleValidationTx(ctx, tx, previousRunVersion, recipeSampleValidation, businessAt)
			}
		}
		if err == nil && work.Purpose == "source_discovery" {
			if currentDiscovery.Status == model.SourceDiscoveryQueued {
				next, transitionErr := currentDiscovery.Start(currentDiscovery.Version)
				if transitionErr != nil {
					err = transitionErr
				} else {
					err = updateSourceDiscoveryCAS(ctx, tx, currentDiscovery.Version, next, businessAt)
				}
			}
		}
		if err == nil && work.Purpose == "profile_repair" {
			nextSession, transitionErr := currentProfileRepair.Activate(currentProfileRepair.Version,
				attempt.AttemptID, currentProfileRepair.DeviceActorID, businessAt)
			if transitionErr != nil {
				err = transitionErr
			} else {
				err = updateProfileRepairSessionTx(ctx, tx, currentProfileRepair.Version, nextSession, businessAt)
			}
		}
	case "fail":
		attempt, err = attempt.Fail()
		if err == nil && (work.Purpose == "profile_repair" || work.Purpose == "profile_verify") {
			if report == nil || failurePolicy == nil {
				err = fmt.Errorf("Profile control failure requires a classified report and policy")
			} else {
				incident, incidentErr := getRepairIncidentForUpdate(ctx, tx, currentProfileRepair.IncidentID)
				if incidentErr != nil {
					err = incidentErr
				} else {
					previousWorkVersion := work.Version
					decision := model.ExecutionFailureDecision{PolicyVersion: failurePolicy.Version,
						AttemptCount: work.AutomaticAttempts + 1, FailureClass: report.Class,
						Route: model.FailureHuman, RepairWorkID: incident.RepairWorkID}
					work, err = work.ApplyExecutionFailure(work.Version, decision)
					if err == nil {
						err = updateFailedWorkTx(ctx, tx, previousWorkVersion, work, decision, businessAt)
					}
					if err == nil {
						var failedSession model.ProfileRepairSession
						failedSession, err = currentProfileRepair.Fail(currentProfileRepair.Version,
							attempt.AttemptID, currentProfileRepair.DeviceActorID)
						if err == nil {
							err = updateProfileRepairSessionTx(ctx, tx, currentProfileRepair.Version, failedSession, businessAt)
						}
					}
					if err == nil && work.Purpose == "profile_verify" {
						causeID := attempt.AttemptID
						if hooks != nil && hooks.causeCommandID != "" {
							causeID = hooks.causeCommandID
						}
						err = returnProfileToRepairingTx(ctx, tx, currentProfileRepair, causeID, businessAt)
					}
				}
			}
		} else if err == nil {
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
				if err == nil && decision.Route == model.FailureHuman {
					var placement WorkPlacement
					placement, err = getWorkPlacementWith(ctx, tx, work.WorkID)
					if err == nil {
						var failure repairFailure
						failure, err = newRepairFailure(work, attempt, placement, *report, failurePolicy.Version, businessAt)
						if err == nil {
							var incident model.RepairIncident
							incident, _, err = openOrJoinRepairFailureTx(ctx, tx, failure, work.WorkID, attempt.AttemptID, businessAt)
							decision.RepairWorkID = incident.RepairWorkID
							if err == nil && incident.Domain == model.FailureProfile &&
								(report.Class == "auth_expired" || report.Class == "captcha") {
								causeID := attempt.AttemptID
								if hooks != nil && hooks.causeCommandID != "" {
									causeID = hooks.causeCommandID
								}
								err = beginProfileRepairForFailureTx(ctx, tx, attempt, incident, causeID, businessAt)
							}
						}
					}
				}
				if err == nil {
					work, err = work.ApplyExecutionFailure(work.Version, decision)
				}
				if err == nil {
					err = updateFailedWorkTx(ctx, tx, previousWorkVersion, work, decision, businessAt)
				}
			}
		}
		if err == nil && !isBudgetlessPurpose(work.Purpose) {
			err = releaseBudgetPermitTx(ctx, tx, attempt.AttemptID, model.PermitReleased, businessAt)
		}
		if err == nil && report != nil {
			for _, artifact := range report.EvidenceArtifacts() {
				if err = insertArtifact(ctx, tx, artifact, false, businessAt); err != nil {
					break
				}
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
SET status = ?, resolution = ?, blocked_by_repair_work_id = ?, acceptance_version = ?, version = ?, state_json = ?, not_before = ?, updated_at = ?
WHERE work_id = ? AND version = ?`, work.Status, nullableString(string(work.Resolution)), nullableString(work.BlockedByRepairWorkID),
		work.AcceptanceVersion, work.Version, state, notBefore.UTC(), businessAt.UTC(), work.WorkID, expected)
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
		attempt.SampleVersion == current.SampleVersion &&
		attempt.ProfileVersion == current.ProfileVersion && attempt.BatchVersion == current.BatchVersion &&
		attempt.DiscoveryGeneration == current.DiscoveryGeneration
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
