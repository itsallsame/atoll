package model

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

type RecipeRolloutBatchStatus string

const (
	RecipeRolloutBatchPreviewing     RecipeRolloutBatchStatus = "previewing"
	RecipeRolloutBatchPreviewed      RecipeRolloutBatchStatus = "previewed"
	RecipeRolloutBatchRunning        RecipeRolloutBatchStatus = "running"
	RecipeRolloutBatchPaused         RecipeRolloutBatchStatus = "paused"
	RecipeRolloutBatchRollingBack    RecipeRolloutBatchStatus = "rolling_back"
	RecipeRolloutBatchRollbackPaused RecipeRolloutBatchStatus = "rollback_paused"
	RecipeRolloutBatchRolledBack     RecipeRolloutBatchStatus = "rolled_back"
	RecipeRolloutBatchCompleted      RecipeRolloutBatchStatus = "completed"
	RecipeRolloutBatchCanceled       RecipeRolloutBatchStatus = "canceled"
)

type RecipeRolloutPhase string

const (
	RecipeRolloutCanary   RecipeRolloutPhase = "canary"
	RecipeRolloutWave     RecipeRolloutPhase = "rollout"
	RecipeRolloutRollback RecipeRolloutPhase = "rollback"
)

type RecipeRolloutBatch struct {
	BatchID             string                   `json:"batch_id"`
	ParentWorkID        string                   `json:"parent_work_id"`
	RecipeID            string                   `json:"recipe_id"`
	RecipeVersion       uint64                   `json:"recipe_version"`
	Kind                RecipeKind               `json:"kind"`
	Scope               string                   `json:"scope"`
	ContractHash        string                   `json:"contract_hash"`
	Capability          string                   `json:"capability"`
	InputArtifactRef    string                   `json:"input_artifact_ref"`
	InputArtifactHash   string                   `json:"input_artifact_hash"`
	SchemaVersion       string                   `json:"schema_version"`
	PolicyVersion       uint64                   `json:"policy_version"`
	PreviewHash         string                   `json:"preview_hash"`
	SourceCount         int                      `json:"source_count"`
	PreviewedCount      int                      `json:"previewed_count"`
	NextChunkSequence   int                      `json:"next_chunk_sequence"`
	CanarySize          int                      `json:"canary_size"`
	WaveSize            int                      `json:"wave_size"`
	ActiveFrom          int                      `json:"active_from,omitempty"`
	ActiveThrough       int                      `json:"active_through,omitempty"`
	SucceededCount      int                      `json:"succeeded_count"`
	FailedCount         int                      `json:"failed_count"`
	RollbackThrough     int                      `json:"rollback_through,omitempty"`
	RolledBackCount     int                      `json:"rolled_back_count,omitempty"`
	RollbackFailedCount int                      `json:"rollback_failed_count,omitempty"`
	Status              RecipeRolloutBatchStatus `json:"status"`
	Phase               RecipeRolloutPhase       `json:"phase,omitempty"`
	Version             uint64                   `json:"version"`
}

type RecipeRolloutWaveProgress struct {
	From               int `json:"from"`
	Through            int `json:"through"`
	Pending            int `json:"pending"`
	Applying           int `json:"applying"`
	AwaitingValidation int `json:"awaiting_validation"`
	Succeeded          int `json:"succeeded"`
	Failed             int `json:"failed"`
}

type RecipeRollbackProgress struct {
	Through            int `json:"through"`
	Unstarted          int `json:"unstarted"`
	Pending            int `json:"pending"`
	Applying           int `json:"applying"`
	AwaitingValidation int `json:"awaiting_validation"`
	Succeeded          int `json:"succeeded"`
	Failed             int `json:"failed"`
	Skipped            int `json:"skipped"`
}

type RecipeRolloutItemStatus string

const (
	RecipeRolloutItemPending            RecipeRolloutItemStatus = "pending"
	RecipeRolloutItemApplying           RecipeRolloutItemStatus = "applying"
	RecipeRolloutItemAwaitingValidation RecipeRolloutItemStatus = "awaiting_validation"
	RecipeRolloutItemSucceeded          RecipeRolloutItemStatus = "succeeded"
	RecipeRolloutItemFailed             RecipeRolloutItemStatus = "failed"
)

type RecipeRollbackItemStatus string

const (
	RecipeRollbackItemPending            RecipeRollbackItemStatus = "pending"
	RecipeRollbackItemApplying           RecipeRollbackItemStatus = "applying"
	RecipeRollbackItemAwaitingValidation RecipeRollbackItemStatus = "awaiting_validation"
	RecipeRollbackItemSucceeded          RecipeRollbackItemStatus = "succeeded"
	RecipeRollbackItemFailed             RecipeRollbackItemStatus = "failed"
	RecipeRollbackItemSkipped            RecipeRollbackItemStatus = "skipped"
)

type RecipeRolloutBatchItem struct {
	BatchID                         string                   `json:"batch_id"`
	Ordinal                         int                      `json:"ordinal"`
	SourceID                        string                   `json:"source_id"`
	ExpectedSourceVersion           uint64                   `json:"expected_source_version"`
	ExpectedAssignmentVersion       uint64                   `json:"expected_assignment_version"`
	PreviousAssignment              SourceRecipeAssignment   `json:"previous_assignment"`
	PreviousEndpointRevision        uint64                   `json:"previous_endpoint_revision,omitempty"`
	PreviousUpdateRetop             ContractVerification     `json:"previous_update_retop,omitempty"`
	PreviousCheckpointStrategy      CheckpointStrategy       `json:"previous_checkpoint_strategy,omitempty"`
	PreviousOverlapPages            int                      `json:"previous_overlap_pages,omitempty"`
	PreviousAssessmentVersion       uint64                   `json:"previous_assessment_version,omitempty"`
	ApplyRequestedAt                string                   `json:"apply_requested_at,omitempty"`
	AppliedSourceVersion            uint64                   `json:"applied_source_version,omitempty"`
	AppliedAssignmentVersion        uint64                   `json:"applied_assignment_version,omitempty"`
	AppliedAt                       string                   `json:"applied_at,omitempty"`
	ValidationWorkID                string                   `json:"validation_work_id,omitempty"`
	ValidationRunID                 string                   `json:"validation_run_id,omitempty"`
	ValidationSourceVersion         uint64                   `json:"validation_source_version,omitempty"`
	ValidatedAt                     string                   `json:"validated_at,omitempty"`
	Status                          RecipeRolloutItemStatus  `json:"status"`
	FailureCode                     string                   `json:"failure_code,omitempty"`
	RollbackStatus                  RecipeRollbackItemStatus `json:"rollback_status,omitempty"`
	RollbackRequestedAt             string                   `json:"rollback_requested_at,omitempty"`
	RolledBackSourceVersion         uint64                   `json:"rolled_back_source_version,omitempty"`
	RolledBackAssignmentVersion     uint64                   `json:"rolled_back_assignment_version,omitempty"`
	RollbackValidationWorkID        string                   `json:"rollback_validation_work_id,omitempty"`
	RollbackValidationRunID         string                   `json:"rollback_validation_run_id,omitempty"`
	RollbackValidationSourceVersion uint64                   `json:"rollback_validation_source_version,omitempty"`
	RolledBackAt                    string                   `json:"rolled_back_at,omitempty"`
	RollbackFailureCode             string                   `json:"rollback_failure_code,omitempty"`
	Version                         uint64                   `json:"version"`
}

func RecipeRolloutOrderKey(batchID, sourceID string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(batchID) + "|" + strings.TrimSpace(sourceID)))
	return hex.EncodeToString(sum[:])
}

func NewRecipeRolloutBatchItem(batchID string, ordinal int, source RecruitmentSource,
	assignment SourceRecipeAssignment) (RecipeRolloutBatchItem, error) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" || ordinal < 1 || source.SourceID == "" || source.Version == 0 ||
		assignment.SourceID != source.SourceID || assignment.AssignmentVersion == 0 {
		return RecipeRolloutBatchItem{}, fmt.Errorf("rollout item requires batch, ordinal, Source, and current assignment")
	}
	projected := source.DetailAssignment
	if assignment.Kind == RecipeListing {
		projected = source.ListingAssignment
	}
	if projected == nil || *projected != assignment {
		return RecipeRolloutBatchItem{}, fmt.Errorf("rollout item assignment does not match Source projection")
	}
	item := RecipeRolloutBatchItem{BatchID: batchID, Ordinal: ordinal, SourceID: source.SourceID,
		ExpectedSourceVersion: source.Version, ExpectedAssignmentVersion: assignment.AssignmentVersion,
		PreviousAssignment: assignment, Status: RecipeRolloutItemPending, Version: 1}
	if assignment.Kind == RecipeListing {
		assessment := source.ContractAssessment
		if assessment == nil || !assessment.ProductionIncrementalEligible() ||
			assessment.SourceID != source.SourceID || assessment.RecipeID != assignment.RecipeID ||
			assessment.RecipeVersion != assignment.RecipeVersion || assessment.ContractHash != assignment.ContractHash {
			return RecipeRolloutBatchItem{}, fmt.Errorf("Listing rollout item requires the current verified Source assessment")
		}
		item.PreviousEndpointRevision = assessment.EndpointRevision
		item.PreviousUpdateRetop = assessment.UpdateRetop
		item.PreviousCheckpointStrategy = assessment.CheckpointStrategy
		item.PreviousOverlapPages = assessment.OverlapPages
		item.PreviousAssessmentVersion = assessment.Version
	}
	return item, nil
}

func (i RecipeRolloutBatchItem) PlanApply(expected uint64, requestedAt string) (RecipeRolloutBatchItem, error) {
	if err := requireVersion(expected, i.Version); err != nil {
		return RecipeRolloutBatchItem{}, err
	}
	if i.Status != RecipeRolloutItemPending || !validRFC3339(requestedAt) {
		return RecipeRolloutBatchItem{}, fmt.Errorf("pending rollout item and application request time are required")
	}
	i.ApplyRequestedAt = strings.TrimSpace(requestedAt)
	i.Status = RecipeRolloutItemApplying
	i.Version++
	return i, nil
}

func (i RecipeRolloutBatchItem) MarkApplied(expected uint64, sourceVersion, assignmentVersion uint64) (RecipeRolloutBatchItem, error) {
	if err := requireVersion(expected, i.Version); err != nil {
		return RecipeRolloutBatchItem{}, err
	}
	if i.Status != RecipeRolloutItemApplying || sourceVersion != i.ExpectedSourceVersion+1 ||
		assignmentVersion != i.ExpectedAssignmentVersion+1 || !validRFC3339(i.ApplyRequestedAt) {
		return RecipeRolloutBatchItem{}, fmt.Errorf("applying rollout item and exact applied Source/Assignment versions are required")
	}
	i.AppliedSourceVersion, i.AppliedAssignmentVersion = sourceVersion, assignmentVersion
	i.AppliedAt = i.ApplyRequestedAt
	i.Status = RecipeRolloutItemAwaitingValidation
	i.Version++
	return i, nil
}

func (i RecipeRolloutBatchItem) BindValidation(expected uint64, workID, runID string,
	sourceVersion uint64) (RecipeRolloutBatchItem, error) {
	if err := requireVersion(expected, i.Version); err != nil {
		return RecipeRolloutBatchItem{}, err
	}
	workID, runID = strings.TrimSpace(workID), strings.TrimSpace(runID)
	validSourceVersion := sourceVersion > i.AppliedSourceVersion
	if i.PreviousAssignment.Kind == RecipeDetail {
		validSourceVersion = sourceVersion == i.AppliedSourceVersion
	}
	if i.Status != RecipeRolloutItemAwaitingValidation || i.ValidationWorkID != "" ||
		workID == "" || runID == "" || !validSourceVersion {
		return RecipeRolloutBatchItem{}, fmt.Errorf("awaiting rollout item and validation Work/run are required")
	}
	i.ValidationWorkID, i.ValidationRunID = workID, runID
	i.ValidationSourceVersion = sourceVersion
	i.Version++
	return i, nil
}

func (i RecipeRolloutBatchItem) MarkSucceeded(expected uint64, validationWorkID, validatedAt string) (RecipeRolloutBatchItem, error) {
	if err := requireVersion(expected, i.Version); err != nil {
		return RecipeRolloutBatchItem{}, err
	}
	validationWorkID = strings.TrimSpace(validationWorkID)
	if i.Status != RecipeRolloutItemAwaitingValidation || validationWorkID == "" || !validRFC3339(validatedAt) ||
		(i.ValidationWorkID != "" && i.ValidationWorkID != validationWorkID) ||
		(i.PreviousAssignment.Kind == RecipeListing && (i.ValidationWorkID == "" || i.ValidationRunID == "" ||
			i.ValidationSourceVersion <= i.AppliedSourceVersion)) {
		return RecipeRolloutBatchItem{}, &InvalidTransitionError{Entity: "recipe_rollout_item", From: string(i.Status), Action: "succeed"}
	}
	i.ValidationWorkID = validationWorkID
	i.ValidatedAt = strings.TrimSpace(validatedAt)
	i.Status = RecipeRolloutItemSucceeded
	i.Version++
	return i, nil
}

func (i RecipeRolloutBatchItem) Retry(expected uint64) (RecipeRolloutBatchItem, error) {
	if err := requireVersion(expected, i.Version); err != nil {
		return RecipeRolloutBatchItem{}, err
	}
	if i.Status != RecipeRolloutItemFailed {
		return RecipeRolloutBatchItem{}, &InvalidTransitionError{Entity: "recipe_rollout_item", From: string(i.Status), Action: "retry"}
	}
	if (i.AppliedSourceVersion == 0) != (i.AppliedAssignmentVersion == 0) ||
		(i.AppliedSourceVersion == 0) != (i.AppliedAt == "") {
		return RecipeRolloutBatchItem{}, fmt.Errorf("failed rollout item has inconsistent application evidence")
	}
	i.Status, i.FailureCode = RecipeRolloutItemPending, ""
	if i.AppliedSourceVersion != 0 {
		// An item which already switched Assignment retries only its evidence
		// validation. Re-applying would create a second Assignment version.
		i.Status = RecipeRolloutItemAwaitingValidation
	} else {
		i.ApplyRequestedAt = ""
	}
	i.ValidationWorkID, i.ValidationRunID, i.ValidatedAt = "", "", ""
	i.ValidationSourceVersion = 0
	i.Version++
	return i, nil
}

func (i RecipeRolloutBatchItem) MarkFailed(expected uint64, code string) (RecipeRolloutBatchItem, error) {
	if err := requireVersion(expected, i.Version); err != nil {
		return RecipeRolloutBatchItem{}, err
	}
	code = strings.TrimSpace(code)
	if (i.Status != RecipeRolloutItemPending && i.Status != RecipeRolloutItemApplying &&
		i.Status != RecipeRolloutItemAwaitingValidation) ||
		code == "" || len(code) > 128 {
		return RecipeRolloutBatchItem{}, fmt.Errorf("active rollout item and bounded failure code are required")
	}
	i.Status, i.FailureCode = RecipeRolloutItemFailed, code
	i.Version++
	return i, nil
}

func (i RecipeRolloutBatchItem) BeginRollback(expected uint64) (RecipeRolloutBatchItem, error) {
	if err := requireVersion(expected, i.Version); err != nil {
		return RecipeRolloutBatchItem{}, err
	}
	if i.RollbackStatus != "" || i.Status == RecipeRolloutItemApplying {
		return RecipeRolloutBatchItem{}, &InvalidTransitionError{Entity: "recipe_rollout_item", From: string(i.Status), Action: "begin rollback"}
	}
	if i.AppliedAssignmentVersion == 0 {
		if i.AppliedSourceVersion != 0 || i.AppliedAt != "" ||
			(i.Status != RecipeRolloutItemPending && i.Status != RecipeRolloutItemFailed) {
			return RecipeRolloutBatchItem{}, fmt.Errorf("unapplied rollout item has inconsistent forward evidence")
		}
		i.RollbackStatus = RecipeRollbackItemSkipped
	} else {
		if i.AppliedSourceVersion == 0 || !validRFC3339(i.AppliedAt) ||
			(i.Status != RecipeRolloutItemAwaitingValidation && i.Status != RecipeRolloutItemSucceeded &&
				i.Status != RecipeRolloutItemFailed) {
			return RecipeRolloutBatchItem{}, fmt.Errorf("applied rollout item has inconsistent forward evidence")
		}
		i.RollbackStatus = RecipeRollbackItemPending
	}
	i.Version++
	return i, nil
}

func (i RecipeRolloutBatchItem) PlanRollback(expected uint64, requestedAt string) (RecipeRolloutBatchItem, error) {
	if err := requireVersion(expected, i.Version); err != nil {
		return RecipeRolloutBatchItem{}, err
	}
	if i.RollbackStatus != RecipeRollbackItemPending || !validRFC3339(requestedAt) {
		return RecipeRolloutBatchItem{}, fmt.Errorf("pending rollback item and request time are required")
	}
	i.RollbackRequestedAt = strings.TrimSpace(requestedAt)
	i.RollbackStatus = RecipeRollbackItemApplying
	i.Version++
	return i, nil
}

func (i RecipeRolloutBatchItem) MarkRollbackApplied(expected, sourceVersion,
	assignmentVersion uint64) (RecipeRolloutBatchItem, error) {
	if err := requireVersion(expected, i.Version); err != nil {
		return RecipeRolloutBatchItem{}, err
	}
	if i.RollbackStatus != RecipeRollbackItemApplying || sourceVersion <= i.AppliedSourceVersion ||
		assignmentVersion <= i.AppliedAssignmentVersion || !validRFC3339(i.RollbackRequestedAt) {
		return RecipeRolloutBatchItem{}, fmt.Errorf("applying rollback item and monotonic Source/Assignment versions are required")
	}
	i.RolledBackSourceVersion, i.RolledBackAssignmentVersion = sourceVersion, assignmentVersion
	i.RollbackStatus = RecipeRollbackItemAwaitingValidation
	i.Version++
	return i, nil
}

func (i RecipeRolloutBatchItem) BindRollbackValidation(expected uint64, workID, runID string,
	sourceVersion uint64) (RecipeRolloutBatchItem, error) {
	if err := requireVersion(expected, i.Version); err != nil {
		return RecipeRolloutBatchItem{}, err
	}
	workID, runID = strings.TrimSpace(workID), strings.TrimSpace(runID)
	if i.RollbackStatus != RecipeRollbackItemAwaitingValidation || i.RollbackValidationWorkID != "" ||
		workID == "" || runID == "" || sourceVersion <= i.RolledBackSourceVersion {
		return RecipeRolloutBatchItem{}, fmt.Errorf("awaiting rollback item and validation Work/run are required")
	}
	i.RollbackValidationWorkID, i.RollbackValidationRunID = workID, runID
	i.RollbackValidationSourceVersion = sourceVersion
	i.Version++
	return i, nil
}

func (i RecipeRolloutBatchItem) MarkRollbackSucceeded(expected uint64, validatedAt string) (RecipeRolloutBatchItem, error) {
	if err := requireVersion(expected, i.Version); err != nil {
		return RecipeRolloutBatchItem{}, err
	}
	if i.RollbackStatus != RecipeRollbackItemAwaitingValidation || !validRFC3339(validatedAt) ||
		i.RollbackValidationWorkID == "" || i.RollbackValidationRunID == "" ||
		i.RollbackValidationSourceVersion <= i.RolledBackSourceVersion {
		return RecipeRolloutBatchItem{}, &InvalidTransitionError{Entity: "recipe_rollout_item", From: string(i.RollbackStatus), Action: "complete rollback"}
	}
	i.RolledBackAt = strings.TrimSpace(validatedAt)
	i.RollbackStatus = RecipeRollbackItemSucceeded
	i.Version++
	return i, nil
}

func (i RecipeRolloutBatchItem) MarkRollbackFailed(expected uint64, code string) (RecipeRolloutBatchItem, error) {
	if err := requireVersion(expected, i.Version); err != nil {
		return RecipeRolloutBatchItem{}, err
	}
	code = strings.TrimSpace(code)
	if (i.RollbackStatus != RecipeRollbackItemPending && i.RollbackStatus != RecipeRollbackItemApplying &&
		i.RollbackStatus != RecipeRollbackItemAwaitingValidation) || code == "" || len(code) > 128 {
		return RecipeRolloutBatchItem{}, fmt.Errorf("active rollback item and bounded failure code are required")
	}
	i.RollbackStatus, i.RollbackFailureCode = RecipeRollbackItemFailed, code
	i.Version++
	return i, nil
}

func (i RecipeRolloutBatchItem) RetryRollback(expected uint64) (RecipeRolloutBatchItem, error) {
	if err := requireVersion(expected, i.Version); err != nil {
		return RecipeRolloutBatchItem{}, err
	}
	if i.RollbackStatus != RecipeRollbackItemFailed {
		return RecipeRolloutBatchItem{}, &InvalidTransitionError{Entity: "recipe_rollout_item", From: string(i.RollbackStatus), Action: "retry rollback"}
	}
	i.RollbackStatus, i.RollbackFailureCode = RecipeRollbackItemPending, ""
	if i.RolledBackAssignmentVersion != 0 {
		i.RollbackStatus = RecipeRollbackItemAwaitingValidation
	} else {
		i.RollbackRequestedAt = ""
	}
	i.RollbackValidationWorkID, i.RollbackValidationRunID, i.RolledBackAt = "", "", ""
	i.RollbackValidationSourceVersion = 0
	i.Version++
	return i, nil
}

func CanonicalRecipeRolloutItems(batchID string, items []RecipeRolloutBatchItem) ([]RecipeRolloutBatchItem, error) {
	batchID = strings.TrimSpace(batchID)
	if batchID == "" || len(items) == 0 {
		return nil, fmt.Errorf("batch and rollout items are required")
	}
	canonical := append([]RecipeRolloutBatchItem(nil), items...)
	sort.Slice(canonical, func(left, right int) bool {
		return RecipeRolloutOrderKey(batchID, canonical[left].SourceID) < RecipeRolloutOrderKey(batchID, canonical[right].SourceID)
	})
	seen := make(map[string]struct{}, len(canonical))
	for index := range canonical {
		if canonical[index].SourceID == "" || canonical[index].ExpectedSourceVersion == 0 ||
			canonical[index].ExpectedAssignmentVersion == 0 {
			return nil, fmt.Errorf("rollout preview contains an invalid Source")
		}
		if _, duplicate := seen[canonical[index].SourceID]; duplicate {
			return nil, fmt.Errorf("rollout preview contains a duplicate Source")
		}
		seen[canonical[index].SourceID] = struct{}{}
		canonical[index].Ordinal = index + 1
		canonical[index].BatchID = batchID
		canonical[index].Status = ""
		canonical[index].ApplyRequestedAt = ""
		canonical[index].AppliedSourceVersion = 0
		canonical[index].AppliedAssignmentVersion = 0
		canonical[index].AppliedAt = ""
		canonical[index].ValidationWorkID = ""
		canonical[index].ValidationRunID = ""
		canonical[index].ValidationSourceVersion = 0
		canonical[index].ValidatedAt = ""
		canonical[index].FailureCode = ""
		canonical[index].RollbackStatus = ""
		canonical[index].RollbackRequestedAt = ""
		canonical[index].RolledBackSourceVersion = 0
		canonical[index].RolledBackAssignmentVersion = 0
		canonical[index].RollbackValidationWorkID = ""
		canonical[index].RollbackValidationRunID = ""
		canonical[index].RollbackValidationSourceVersion = 0
		canonical[index].RolledBackAt = ""
		canonical[index].RollbackFailureCode = ""
		canonical[index].Version = 0
	}
	return canonical, nil
}

func RecipeRolloutPreviewHash(batch RecipeRolloutBatch, recipe Recipe, items []RecipeRolloutBatchItem) (string, error) {
	if recipe.RecipeID == "" || recipe.Version == 0 || !validSHA256(batch.InputArtifactHash) ||
		strings.TrimSpace(batch.SchemaVersion) == "" || batch.PolicyVersion == 0 || len(items) == 0 {
		return "", fmt.Errorf("Recipe identity, input contract, and rollout items are required")
	}
	canonical, err := CanonicalRecipeRolloutItems(batch.BatchID, items)
	if err != nil {
		return "", err
	}
	payload, _ := json.Marshal(struct {
		BatchID           string                   `json:"batch_id"`
		RecipeID          string                   `json:"recipe_id"`
		RecipeVersion     uint64                   `json:"recipe_version"`
		Kind              RecipeKind               `json:"kind"`
		Scope             string                   `json:"scope"`
		ContractHash      string                   `json:"contract_hash"`
		Capability        string                   `json:"capability"`
		InputArtifactHash string                   `json:"input_artifact_hash"`
		SchemaVersion     string                   `json:"schema_version"`
		PolicyVersion     uint64                   `json:"policy_version"`
		Items             []RecipeRolloutBatchItem `json:"items"`
	}{batch.BatchID, recipe.RecipeID, recipe.Version, recipe.Kind, recipe.Scope, recipe.ContractHash,
		recipe.Execution.RequiredCapability, batch.InputArtifactHash, batch.SchemaVersion, batch.PolicyVersion, canonical})
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func NewRecipeRolloutBatch(batchID, parentWorkID string, recipe Recipe, inputArtifactRef,
	inputArtifactHash, schemaVersion string, policyVersion uint64, canarySize, waveSize int) (RecipeRolloutBatch, error) {
	batchID, parentWorkID = strings.TrimSpace(batchID), strings.TrimSpace(parentWorkID)
	inputArtifactRef, inputArtifactHash = strings.TrimSpace(inputArtifactRef), strings.TrimSpace(inputArtifactHash)
	schemaVersion = strings.TrimSpace(schemaVersion)
	if batchID == "" || parentWorkID == "" || inputArtifactRef == "" || !validSHA256(inputArtifactHash) ||
		schemaVersion == "" || policyVersion == 0 {
		return RecipeRolloutBatch{}, fmt.Errorf("batch, parent Work, input Artifact/hash, schema, and policy are required")
	}
	if recipe.Status != RecipeActive || (recipe.Kind != RecipeListing && recipe.Kind != RecipeDetail) ||
		strings.TrimSpace(recipe.Scope) == "" || strings.TrimSpace(recipe.ContractHash) == "" ||
		strings.TrimSpace(recipe.Execution.RequiredCapability) == "" {
		return RecipeRolloutBatch{}, fmt.Errorf("batch rollout requires an active Listing or Detail Recipe with complete execution identity")
	}
	if canarySize < 1 || waveSize < 1 || waveSize > 500 {
		return RecipeRolloutBatch{}, fmt.Errorf("canary_size must be positive and wave_size must be in [1,500]")
	}
	return RecipeRolloutBatch{
		BatchID: batchID, ParentWorkID: parentWorkID, RecipeID: recipe.RecipeID, RecipeVersion: recipe.Version,
		Kind: recipe.Kind, Scope: recipe.Scope, ContractHash: recipe.ContractHash,
		Capability: recipe.Execution.RequiredCapability, InputArtifactRef: inputArtifactRef,
		InputArtifactHash: inputArtifactHash, SchemaVersion: schemaVersion, PolicyVersion: policyVersion,
		CanarySize: canarySize, WaveSize: waveSize,
		Status: RecipeRolloutBatchPreviewing, NextChunkSequence: 1, Version: 1,
	}, nil
}

func (b RecipeRolloutBatch) AppendPreviewChunk(expected uint64, sequence, itemCount int) (RecipeRolloutBatch, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return RecipeRolloutBatch{}, err
	}
	if b.Status != RecipeRolloutBatchPreviewing {
		return RecipeRolloutBatch{}, &InvalidTransitionError{Entity: "recipe_rollout_batch", From: string(b.Status), Action: "append preview chunk"}
	}
	if sequence != b.NextChunkSequence || itemCount < 1 || itemCount > 500 {
		return RecipeRolloutBatch{}, fmt.Errorf("Recipe rollout preview requires the next sequence and a chunk of 1..500 items")
	}
	b.PreviewedCount += itemCount
	b.NextChunkSequence++
	b.Version++
	return b, nil
}

func (b RecipeRolloutBatch) FinishPreview(expected uint64, previewHash string) (RecipeRolloutBatch, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return RecipeRolloutBatch{}, err
	}
	if b.Status != RecipeRolloutBatchPreviewing || b.PreviewedCount < 1 || b.CanarySize > b.PreviewedCount ||
		!validSHA256(strings.TrimSpace(previewHash)) {
		return RecipeRolloutBatch{}, fmt.Errorf("non-empty preview, bounded canary, and SHA-256 preview hash are required")
	}
	b.SourceCount, b.PreviewHash = b.PreviewedCount, strings.TrimSpace(previewHash)
	b.Status = RecipeRolloutBatchPreviewed
	b.Version++
	return b, nil
}

func (b RecipeRolloutBatch) Start(expected uint64, previewHash string) (RecipeRolloutBatch, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return RecipeRolloutBatch{}, err
	}
	if b.Status != RecipeRolloutBatchPreviewed {
		return RecipeRolloutBatch{}, &InvalidTransitionError{Entity: "recipe_rollout_batch", From: string(b.Status), Action: "start"}
	}
	if strings.TrimSpace(previewHash) != b.PreviewHash {
		return RecipeRolloutBatch{}, fmt.Errorf("Recipe rollout preview hash does not match")
	}
	b.Status, b.Phase = RecipeRolloutBatchRunning, RecipeRolloutCanary
	b.ActiveFrom, b.ActiveThrough = 1, minInt(b.CanarySize, b.SourceCount)
	b.Version++
	return b, nil
}

// ObserveWave advances only after every member in the currently open range is
// successfully revalidated. A single failed member pauses the whole batch and
// leaves the range fixed for an explicit repair or rollback decision.
func (b RecipeRolloutBatch) ObserveWave(expected uint64, progress RecipeRolloutWaveProgress) (RecipeRolloutBatch, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return RecipeRolloutBatch{}, err
	}
	if b.Status != RecipeRolloutBatchRunning {
		return RecipeRolloutBatch{}, &InvalidTransitionError{Entity: "recipe_rollout_batch", From: string(b.Status), Action: "observe wave"}
	}
	activeCount := b.ActiveThrough - b.ActiveFrom + 1
	if progress.From != b.ActiveFrom || progress.Through != b.ActiveThrough || activeCount < 1 ||
		progress.Pending < 0 || progress.AwaitingValidation < 0 || progress.Succeeded < 0 || progress.Failed < 0 ||
		progress.Pending+progress.Applying+progress.AwaitingValidation+progress.Succeeded+progress.Failed != activeCount {
		return RecipeRolloutBatch{}, fmt.Errorf("Recipe rollout progress does not match the active wave")
	}
	if progress.Failed > 0 {
		b.Status = RecipeRolloutBatchPaused
		b.FailedCount = progress.Failed
		b.Version++
		return b, nil
	}
	if progress.Pending > 0 || progress.Applying > 0 || progress.AwaitingValidation > 0 {
		return b, nil
	}
	if progress.Succeeded != activeCount {
		return RecipeRolloutBatch{}, fmt.Errorf("completed Recipe rollout wave must contain only succeeded items")
	}
	b.SucceededCount = b.ActiveThrough
	b.FailedCount = 0
	if b.ActiveThrough == b.SourceCount {
		b.Status = RecipeRolloutBatchCompleted
		b.Version++
		return b, nil
	}
	b.Phase = RecipeRolloutWave
	b.ActiveFrom = b.ActiveThrough + 1
	b.ActiveThrough = minInt(b.ActiveFrom+b.WaveSize-1, b.SourceCount)
	b.Version++
	return b, nil
}

func (b RecipeRolloutBatch) Resume(expected uint64) (RecipeRolloutBatch, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return RecipeRolloutBatch{}, err
	}
	if b.Status != RecipeRolloutBatchPaused {
		return RecipeRolloutBatch{}, &InvalidTransitionError{Entity: "recipe_rollout_batch", From: string(b.Status), Action: "resume"}
	}
	b.Status = RecipeRolloutBatchRunning
	b.FailedCount = 0
	b.Version++
	return b, nil
}

// BeginRollback freezes forward expansion and covers every member which may
// have been published so far. Repository reconciliation determines which of
// those members were actually applied; future unopened members are excluded.
func (b RecipeRolloutBatch) BeginRollback(expected uint64) (RecipeRolloutBatch, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return RecipeRolloutBatch{}, err
	}
	if b.Status != RecipeRolloutBatchPaused || b.ActiveThrough < 1 || b.ActiveThrough > b.SourceCount {
		return RecipeRolloutBatch{}, &InvalidTransitionError{Entity: "recipe_rollout_batch", From: string(b.Status), Action: "begin rollback"}
	}
	b.Status, b.Phase = RecipeRolloutBatchRollingBack, RecipeRolloutRollback
	b.RollbackThrough = b.ActiveThrough
	b.RolledBackCount, b.RollbackFailedCount = 0, 0
	b.Version++
	return b, nil
}

func (b RecipeRolloutBatch) ObserveRollback(expected uint64,
	progress RecipeRollbackProgress) (RecipeRolloutBatch, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return RecipeRolloutBatch{}, err
	}
	if b.Status != RecipeRolloutBatchRollingBack || b.RollbackThrough < 1 ||
		progress.Through != b.RollbackThrough {
		return RecipeRolloutBatch{}, &InvalidTransitionError{Entity: "recipe_rollout_batch", From: string(b.Status), Action: "observe rollback"}
	}
	total := progress.Unstarted + progress.Pending + progress.Applying + progress.AwaitingValidation +
		progress.Succeeded + progress.Failed + progress.Skipped
	if progress.Unstarted < 0 || progress.Pending < 0 || progress.Applying < 0 ||
		progress.AwaitingValidation < 0 || progress.Succeeded < 0 || progress.Failed < 0 ||
		progress.Skipped < 0 || total != b.RollbackThrough {
		return RecipeRolloutBatch{}, fmt.Errorf("Recipe rollback progress does not match the published prefix")
	}
	if progress.Failed > 0 {
		b.Status = RecipeRolloutBatchRollbackPaused
		b.RolledBackCount = progress.Succeeded
		b.RollbackFailedCount = progress.Failed
		b.Version++
		return b, nil
	}
	if progress.Unstarted > 0 || progress.Pending > 0 || progress.Applying > 0 || progress.AwaitingValidation > 0 {
		return b, nil
	}
	if progress.Succeeded+progress.Skipped != b.RollbackThrough {
		return RecipeRolloutBatch{}, fmt.Errorf("completed Recipe rollback has inconsistent outcomes")
	}
	b.Status = RecipeRolloutBatchRolledBack
	b.RolledBackCount = progress.Succeeded
	b.RollbackFailedCount = 0
	b.Version++
	return b, nil
}

func (b RecipeRolloutBatch) ResumeRollback(expected uint64) (RecipeRolloutBatch, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return RecipeRolloutBatch{}, err
	}
	if b.Status != RecipeRolloutBatchRollbackPaused {
		return RecipeRolloutBatch{}, &InvalidTransitionError{Entity: "recipe_rollout_batch", From: string(b.Status), Action: "resume rollback"}
	}
	b.Status = RecipeRolloutBatchRollingBack
	b.RollbackFailedCount = 0
	b.Version++
	return b, nil
}

func (b RecipeRolloutBatch) Cancel(expected uint64) (RecipeRolloutBatch, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return RecipeRolloutBatch{}, err
	}
	if b.Status == RecipeRolloutBatchCompleted || b.Status == RecipeRolloutBatchCanceled ||
		b.Status == RecipeRolloutBatchRolledBack || b.Status == RecipeRolloutBatchRollingBack ||
		b.Status == RecipeRolloutBatchRollbackPaused {
		return RecipeRolloutBatch{}, &InvalidTransitionError{Entity: "recipe_rollout_batch", From: string(b.Status), Action: "cancel"}
	}
	b.Status = RecipeRolloutBatchCanceled
	b.Version++
	return b, nil
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func validRFC3339(value string) bool {
	_, err := time.Parse(time.RFC3339, strings.TrimSpace(value))
	return err == nil
}
