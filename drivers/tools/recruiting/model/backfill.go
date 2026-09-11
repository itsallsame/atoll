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

func BackfillPreviewHash(backfill Backfill, items []BackfillItem) (string, error) {
	canonical := append([]BackfillItem(nil), items...)
	sort.Slice(canonical, func(left, right int) bool { return canonical[left].ItemID < canonical[right].ItemID })
	return AdvanceBackfillPreviewHash(backfill, "", canonical)
}

// AdvanceBackfillPreviewHash incrementally binds an ordered preview without
// retaining the entire selection in memory. The repository supplies at most
// 500 immutable items per transaction and persists the returned accumulator.
func AdvanceBackfillPreviewHash(backfill Backfill, accumulator string, items []BackfillItem) (string, error) {
	if backfill.BackfillID == "" || backfill.Mode.Validate() != nil || backfill.PolicyVersion == 0 ||
		backfill.RecipeID == "" || backfill.RecipeVersion == 0 {
		return "", fmt.Errorf("backfill identity, mode, policy, and Recipe are required")
	}
	var digest [sha256.Size]byte
	accumulator = strings.TrimSpace(accumulator)
	if accumulator == "" {
		header, err := json.Marshal(struct {
			BackfillID    string       `json:"backfill_id"`
			TargetType    string       `json:"target_type"`
			TargetID      string       `json:"target_id"`
			Mode          BackfillMode `json:"mode"`
			RangeStart    string       `json:"range_start"`
			RangeEnd      string       `json:"range_end"`
			Fields        []string     `json:"fields"`
			RecipeID      string       `json:"recipe_id"`
			RecipeVersion uint64       `json:"recipe_version"`
			PolicyVersion uint64       `json:"policy_version"`
		}{backfill.BackfillID, backfill.TargetType, backfill.TargetID, backfill.Mode, backfill.RangeStart,
			backfill.RangeEnd, backfill.Fields, backfill.RecipeID, backfill.RecipeVersion, backfill.PolicyVersion})
		if err != nil {
			return "", err
		}
		digest = sha256.Sum256(append([]byte("recruiting-backfill-preview-v1\x00"), header...))
	} else {
		if !validSHA256(accumulator) {
			return "", fmt.Errorf("previous backfill preview accumulator must be SHA-256")
		}
		decoded, err := hex.DecodeString(strings.TrimPrefix(accumulator, "sha256:"))
		if err != nil || len(decoded) != sha256.Size {
			return "", fmt.Errorf("decode previous backfill preview accumulator")
		}
		copy(digest[:], decoded)
	}
	previousItemID := ""
	for index := range items {
		item := &items[index]
		if item.BackfillID != backfill.BackfillID || item.ItemID == "" || item.ItemID <= previousItemID ||
			item.Status != BackfillItemPending || item.Version != 1 {
			return "", fmt.Errorf("backfill preview items must be unique immutable pending selections")
		}
		if backfill.Mode == BackfillArtifactRecompute {
			if item.InputDetailVersionID == "" || item.InputArtifactID == "" || item.InputObservedAt == "" {
				return "", fmt.Errorf("artifact recompute preview requires historical detail lineage")
			}
		} else if item.InputDetailVersionID != "" || item.InputArtifactID != "" || item.InputObservedAt != "" {
			return "", fmt.Errorf("live refetch preview cannot contain historical detail lineage")
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			return "", err
		}
		step := sha256.New()
		_, _ = step.Write(digest[:])
		_, _ = step.Write([]byte{0})
		_, _ = step.Write(encoded)
		copy(digest[:], step.Sum(nil))
		previousItemID = item.ItemID
	}
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

type BackfillMode string

const (
	BackfillArtifactRecompute BackfillMode = "artifact_recompute"
	BackfillLiveRefetch       BackfillMode = "live_refetch"
)

func (m BackfillMode) Validate() error {
	switch m {
	case BackfillArtifactRecompute, BackfillLiveRefetch:
		return nil
	default:
		return fmt.Errorf("unknown backfill mode %q", m)
	}
}

func (m BackfillMode) MayAdvanceCheckpoint() bool { return false }

type BackfillStatus string

const (
	BackfillPreviewing BackfillStatus = "previewing"
	BackfillPreviewed  BackfillStatus = "previewed"
	BackfillRunning    BackfillStatus = "running"
	BackfillPaused     BackfillStatus = "paused"
	BackfillCanceling  BackfillStatus = "canceling"
	BackfillCompleted  BackfillStatus = "completed"
	BackfillCanceled   BackfillStatus = "canceled"
)

type Backfill struct {
	BackfillID             string         `json:"backfill_id"`
	WorkID                 string         `json:"work_id"`
	RequestedBy            string         `json:"requested_by"`
	TargetType             string         `json:"target_type"`
	TargetID               string         `json:"target_id"`
	Mode                   BackfillMode   `json:"mode"`
	RangeStart             string         `json:"range_start"`
	RangeEnd               string         `json:"range_end"`
	Fields                 []string       `json:"fields"`
	RecipeID               string         `json:"recipe_id"`
	RecipeVersion          uint64         `json:"recipe_version"`
	PolicyVersion          uint64         `json:"policy_version"`
	Status                 BackfillStatus `json:"status"`
	PreviewCursor          string         `json:"preview_cursor,omitempty"`
	PreviewedItems         uint64         `json:"previewed_items"`
	PreviewAccumulator     string         `json:"preview_accumulator,omitempty"`
	PreviewHash            string         `json:"preview_hash,omitempty"`
	ConfirmationVersion    uint64         `json:"confirmation_version,omitempty"`
	SucceededItems         uint64         `json:"succeeded_items"`
	AcceptedGapItems       uint64         `json:"accepted_gap_items"`
	FailedItems            uint64         `json:"failed_items"`
	CanceledItems          uint64         `json:"canceled_items"`
	CancelCommandID        string         `json:"cancel_command_id,omitempty"`
	CancelScopeOperationID string         `json:"cancel_scope_operation_id,omitempty"`
	Version                uint64         `json:"version"`
}

func NewBackfill(id, workID, requestedBy, targetType, targetID string, mode BackfillMode,
	rangeStart, rangeEnd string, fields []string, recipeID string, recipeVersion, policyVersion uint64) (Backfill, error) {
	start, startErr := time.Parse(time.RFC3339, rangeStart)
	end, endErr := time.Parse(time.RFC3339, rangeEnd)
	canonicalFields, fieldsErr := canonicalBackfillFields(fields)
	targetType = strings.TrimSpace(targetType)
	if strings.TrimSpace(id) == "" || strings.TrimSpace(workID) == "" || strings.TrimSpace(requestedBy) == "" ||
		(targetType != "company" && targetType != "source") || strings.TrimSpace(targetID) == "" ||
		mode.Validate() != nil || startErr != nil || endErr != nil || !start.Before(end) || fieldsErr != nil ||
		strings.TrimSpace(recipeID) == "" || recipeVersion == 0 || policyVersion == 0 {
		return Backfill{}, fmt.Errorf("backfill requires identity, actor, supported target/mode, range, fields, recipe, and policy")
	}
	return Backfill{BackfillID: strings.TrimSpace(id), WorkID: strings.TrimSpace(workID), RequestedBy: strings.TrimSpace(requestedBy),
		TargetType: targetType, TargetID: strings.TrimSpace(targetID), Mode: mode,
		RangeStart: start.UTC().Format(time.RFC3339Nano), RangeEnd: end.UTC().Format(time.RFC3339Nano), Fields: canonicalFields,
		RecipeID: strings.TrimSpace(recipeID), RecipeVersion: recipeVersion, PolicyVersion: policyVersion,
		Status: BackfillPreviewing, Version: 1}, nil
}

func canonicalBackfillFields(fields []string) ([]string, error) {
	if len(fields) == 0 || len(fields) > 64 {
		return nil, fmt.Errorf("backfill requires 1 to 64 fields")
	}
	seen := make(map[string]struct{}, len(fields))
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		field = strings.TrimSpace(field)
		if field == "" || len(field) > 128 || strings.ContainsAny(field, "\r\n\t") {
			return nil, fmt.Errorf("backfill field is invalid")
		}
		if _, found := seen[field]; found {
			return nil, fmt.Errorf("backfill fields must be unique")
		}
		seen[field] = struct{}{}
		result = append(result, field)
	}
	sort.Strings(result)
	return result, nil
}

func (b Backfill) AdvancePreview(expected, added uint64, nextCursor, previewAccumulator string, terminal bool) (Backfill, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return Backfill{}, err
	}
	if b.Status != BackfillPreviewing || added > 500 {
		return Backfill{}, &InvalidTransitionError{Entity: "backfill", From: string(b.Status), Action: "advance preview"}
	}
	nextCursor, previewAccumulator = strings.TrimSpace(nextCursor), strings.TrimSpace(previewAccumulator)
	if !validSHA256(previewAccumulator) || (!terminal && (added == 0 || nextCursor == "")) ||
		(terminal && nextCursor != "") {
		return Backfill{}, fmt.Errorf("backfill preview cursor, chunk, terminal, and hash are inconsistent")
	}
	b.PreviewedItems += added
	b.PreviewCursor = nextCursor
	b.PreviewAccumulator = previewAccumulator
	if terminal {
		b.Status, b.PreviewHash = BackfillPreviewed, previewAccumulator
	}
	b.Version++
	return b, nil
}

func (b Backfill) Confirm(expected uint64, previewHash string) (Backfill, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return Backfill{}, err
	}
	if b.Status != BackfillPreviewed || strings.TrimSpace(previewHash) == "" || previewHash != b.PreviewHash {
		return Backfill{}, &InvalidTransitionError{Entity: "backfill", From: string(b.Status), Action: "confirm"}
	}
	b.Status, b.Version = BackfillRunning, b.Version+1
	b.ConfirmationVersion = b.Version
	if b.PreviewedItems == 0 {
		b.Status = BackfillCompleted
	}
	return b, nil
}

// ReconcileCounts only accepts counts derived from normalized item rows by the
// repository. Callers cannot report their own aggregate outcome.
func (b Backfill) ReconcileCounts(expected, succeeded, acceptedGap, failed, canceled uint64) (Backfill, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return Backfill{}, err
	}
	if b.Status != BackfillRunning && b.Status != BackfillPaused {
		return Backfill{}, &InvalidTransitionError{Entity: "backfill", From: string(b.Status), Action: "reconcile counts"}
	}
	if canceled < b.CanceledItems {
		return Backfill{}, fmt.Errorf("backfill canceled item count cannot decrease")
	}
	terminal := succeeded + acceptedGap + failed + canceled
	if terminal > b.PreviewedItems {
		return Backfill{}, fmt.Errorf("backfill item counts exceed preview")
	}
	b.SucceededItems, b.AcceptedGapItems, b.FailedItems, b.CanceledItems = succeeded, acceptedGap, failed, canceled
	switch {
	case failed > 0:
		b.Status = BackfillPaused
	case terminal == b.PreviewedItems:
		b.Status = BackfillCompleted
	default:
		b.Status = BackfillRunning
	}
	b.Version++
	return b, nil
}

func (b Backfill) Resume(expected uint64) (Backfill, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return Backfill{}, err
	}
	if b.Status != BackfillPaused || b.FailedItems != 0 {
		return Backfill{}, &InvalidTransitionError{Entity: "backfill", From: string(b.Status), Action: "resume"}
	}
	b.Status, b.Version = BackfillRunning, b.Version+1
	return b, nil
}

func (b Backfill) Pause(expected uint64) (Backfill, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return Backfill{}, err
	}
	if b.Status != BackfillRunning || b.FailedItems != 0 {
		return Backfill{}, &InvalidTransitionError{Entity: "backfill", From: string(b.Status), Action: "pause"}
	}
	b.Status, b.Version = BackfillPaused, b.Version+1
	return b, nil
}

func (b Backfill) Cancel(expected uint64, commandID string) (Backfill, error) {
	canceling, err := b.RequestCancel(expected, commandID)
	if err != nil {
		return Backfill{}, err
	}
	if canceling.PreviewedItems != canceling.SucceededItems+canceling.AcceptedGapItems+canceling.CanceledItems {
		return Backfill{}, &InvalidTransitionError{Entity: "backfill", From: string(b.Status), Action: "cancel without settling items"}
	}
	return canceling.FinishCancel(canceling.Version, canceling.CanceledItems)
}

func (b Backfill) RequestCancel(expected uint64, commandID string) (Backfill, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return Backfill{}, err
	}
	commandID = strings.TrimSpace(commandID)
	if commandID == "" || (b.Status != BackfillPreviewing && b.Status != BackfillPreviewed &&
		b.Status != BackfillRunning && b.Status != BackfillPaused) {
		return Backfill{}, &InvalidTransitionError{Entity: "backfill", From: string(b.Status), Action: "request cancel"}
	}
	b.CancelCommandID, b.Status, b.Version = commandID, BackfillCanceling, b.Version+1
	return b, nil
}

func (b Backfill) RequestScopeCancel(expected uint64, operationID string) (Backfill, error) {
	operationID = strings.TrimSpace(operationID)
	if operationID == "" {
		return Backfill{}, fmt.Errorf("scope cancel operation identity is required")
	}
	next, err := b.RequestCancel(expected, "scope-control:"+operationID)
	if err != nil {
		return Backfill{}, err
	}
	next.CancelScopeOperationID = operationID
	return next, nil
}

func (b Backfill) FinishCancel(expected, canceled uint64) (Backfill, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return Backfill{}, err
	}
	if b.Status != BackfillCanceling || b.CancelCommandID == "" || canceled > b.PreviewedItems ||
		b.SucceededItems+b.AcceptedGapItems+canceled != b.PreviewedItems {
		return Backfill{}, &InvalidTransitionError{Entity: "backfill", From: string(b.Status), Action: "finish cancel"}
	}
	b.CanceledItems, b.Status, b.Version = canceled, BackfillCanceled, b.Version+1
	return b, nil
}

func (b Backfill) RecordCancellationProgress(expected, canceled, failed uint64) (Backfill, error) {
	if err := requireVersion(expected, b.Version); err != nil {
		return Backfill{}, err
	}
	if b.Status != BackfillCanceling || canceled < b.CanceledItems ||
		b.SucceededItems+b.AcceptedGapItems+canceled > b.PreviewedItems {
		return Backfill{}, &InvalidTransitionError{Entity: "backfill", From: string(b.Status), Action: "record cancellation progress"}
	}
	b.CanceledItems, b.FailedItems, b.Version = canceled, failed, b.Version+1
	return b, nil
}

type BackfillItemStatus string

const (
	BackfillItemPending     BackfillItemStatus = "pending"
	BackfillItemQueued      BackfillItemStatus = "queued"
	BackfillItemSucceeded   BackfillItemStatus = "succeeded"
	BackfillItemAcceptedGap BackfillItemStatus = "accepted_gap"
	BackfillItemFailed      BackfillItemStatus = "failed"
	BackfillItemCanceled    BackfillItemStatus = "canceled"
)

type BackfillItem struct {
	BackfillID            string             `json:"backfill_id"`
	ItemID                string             `json:"item_id"`
	JobID                 string             `json:"job_id"`
	JobVersion            uint64             `json:"job_version"`
	RefreshGeneration     uint64             `json:"refresh_generation"`
	SourceID              string             `json:"source_id"`
	SourceVersion         uint64             `json:"source_version"`
	CompanyID             string             `json:"company_id"`
	CompanyVersion        uint64             `json:"company_version"`
	DetailURL             string             `json:"detail_url"`
	ProfileID             string             `json:"profile_id,omitempty"`
	ProfileVersion        uint64             `json:"profile_version,omitempty"`
	ProfileBindingVersion uint64             `json:"profile_binding_version,omitempty"`
	InputDetailVersionID  string             `json:"input_detail_version_id,omitempty"`
	InputArtifactID       string             `json:"input_artifact_id,omitempty"`
	InputObservedAt       string             `json:"input_observed_at,omitempty"`
	WorkID                string             `json:"work_id,omitempty"`
	OutputID              string             `json:"output_id,omitempty"`
	Status                BackfillItemStatus `json:"status"`
	FailureClass          string             `json:"failure_class,omitempty"`
	Version               uint64             `json:"version"`
}

func NewBackfillItem(backfillID, itemID string, mode BackfillMode, job SourceJob, source RecruitmentSource,
	company Company, inputDetail *JobDetailVersion, binding *SourceProfileBinding, profile *BrowserProfile) (BackfillItem, error) {
	if strings.TrimSpace(backfillID) == "" || strings.TrimSpace(itemID) == "" || mode.Validate() != nil ||
		job.JobID == "" || job.SourceID == "" || job.Version == 0 || job.RefreshGeneration == 0 ||
		source.SourceID != job.SourceID || source.CompanyID == "" || source.Version == 0 ||
		company.CompanyID != source.CompanyID || company.Version == 0 {
		return BackfillItem{}, fmt.Errorf("backfill item requires matching Job, Source, Company, and frozen versions")
	}
	canonical, err := CanonicalHTTPURL(job.DetailURL)
	if err != nil || canonical != job.DetailURL {
		return BackfillItem{}, fmt.Errorf("backfill item requires canonical detail URL")
	}
	inputDetailVersionID, inputArtifactID, inputObservedAt := "", "", ""
	if inputDetail != nil {
		inputDetailVersionID = strings.TrimSpace(inputDetail.DetailVersionID)
		inputArtifactID = strings.TrimSpace(inputDetail.ArtifactID)
		observed, observedErr := time.Parse(time.RFC3339, inputDetail.ObservedAt)
		if inputDetailVersionID == "" || inputDetail.JobID != job.JobID || inputArtifactID == "" || observedErr != nil {
			return BackfillItem{}, fmt.Errorf("backfill input detail requires matching immutable version, Artifact, and observation time")
		}
		inputObservedAt = observed.UTC().Format(time.RFC3339Nano)
	}
	if (mode == BackfillArtifactRecompute) != (inputDetail != nil) {
		return BackfillItem{}, fmt.Errorf("only artifact recompute items require an input detail version")
	}
	profileID := ""
	var profileVersion, bindingVersion uint64
	if (binding == nil) != (profile == nil) {
		return BackfillItem{}, fmt.Errorf("backfill profile binding and Profile must be frozen together")
	}
	if binding != nil {
		if binding.Validate() != nil || binding.SourceID != source.SourceID || binding.RecipeKind != RecipeDetail ||
			binding.ProfileID == "" || profile.ProfileID != binding.ProfileID || profile.Version == 0 ||
			profile.AuthStatus != ProfileReady {
			return BackfillItem{}, fmt.Errorf("backfill item requires a matching ready Detail Profile fence")
		}
		profileID, profileVersion, bindingVersion = profile.ProfileID, profile.Version, binding.Version
	}
	return BackfillItem{BackfillID: strings.TrimSpace(backfillID), ItemID: strings.TrimSpace(itemID), JobID: job.JobID,
		JobVersion: job.Version, RefreshGeneration: job.RefreshGeneration, SourceID: job.SourceID,
		SourceVersion: source.Version, CompanyID: company.CompanyID, CompanyVersion: company.Version,
		DetailURL: job.DetailURL, ProfileID: profileID, ProfileVersion: profileVersion,
		ProfileBindingVersion: bindingVersion, InputDetailVersionID: inputDetailVersionID,
		InputArtifactID: inputArtifactID, InputObservedAt: inputObservedAt,
		Status: BackfillItemPending, Version: 1}, nil
}

func (i BackfillItem) Queue(expected uint64, workID string) (BackfillItem, error) {
	if err := requireVersion(expected, i.Version); err != nil {
		return BackfillItem{}, err
	}
	if i.Status != BackfillItemPending || strings.TrimSpace(workID) == "" {
		return BackfillItem{}, &InvalidTransitionError{Entity: "backfill_item", From: string(i.Status), Action: "queue"}
	}
	i.WorkID, i.Status, i.Version = strings.TrimSpace(workID), BackfillItemQueued, i.Version+1
	return i, nil
}

func (i BackfillItem) RejectBeforeQueue(expected uint64, failureClass string) (BackfillItem, error) {
	if err := requireVersion(expected, i.Version); err != nil {
		return BackfillItem{}, err
	}
	failureClass = strings.TrimSpace(failureClass)
	if i.Status != BackfillItemPending || failureClass == "" || len(failureClass) > 128 ||
		strings.ContainsAny(failureClass, "\r\n\t ") {
		return BackfillItem{}, &InvalidTransitionError{Entity: "backfill_item", From: string(i.Status), Action: "reject before queue"}
	}
	i.FailureClass, i.Status, i.Version = failureClass, BackfillItemFailed, i.Version+1
	return i, nil
}

func (i BackfillItem) Succeed(expected uint64, outputID string) (BackfillItem, error) {
	if err := requireVersion(expected, i.Version); err != nil {
		return BackfillItem{}, err
	}
	if i.Status != BackfillItemQueued || i.WorkID == "" || strings.TrimSpace(outputID) == "" {
		return BackfillItem{}, &InvalidTransitionError{Entity: "backfill_item", From: string(i.Status), Action: "succeed"}
	}
	i.OutputID, i.Status, i.Version = strings.TrimSpace(outputID), BackfillItemSucceeded, i.Version+1
	return i, nil
}

func (i BackfillItem) Fail(expected uint64, failureClass string) (BackfillItem, error) {
	if err := requireVersion(expected, i.Version); err != nil {
		return BackfillItem{}, err
	}
	failureClass = strings.TrimSpace(failureClass)
	if i.Status != BackfillItemQueued || i.WorkID == "" || failureClass == "" || len(failureClass) > 128 ||
		strings.ContainsAny(failureClass, "\r\n\t ") {
		return BackfillItem{}, &InvalidTransitionError{Entity: "backfill_item", From: string(i.Status), Action: "fail"}
	}
	i.FailureClass, i.Status, i.Version = failureClass, BackfillItemFailed, i.Version+1
	return i, nil
}

func (i BackfillItem) AcceptGap(expected uint64) (BackfillItem, error) {
	if err := requireVersion(expected, i.Version); err != nil {
		return BackfillItem{}, err
	}
	if i.Status != BackfillItemFailed || i.FailureClass == "" {
		return BackfillItem{}, &InvalidTransitionError{Entity: "backfill_item", From: string(i.Status), Action: "accept gap"}
	}
	i.Status, i.Version = BackfillItemAcceptedGap, i.Version+1
	return i, nil
}

func (i BackfillItem) Retry(expected uint64, workID string) (BackfillItem, error) {
	if err := requireVersion(expected, i.Version); err != nil {
		return BackfillItem{}, err
	}
	workID = strings.TrimSpace(workID)
	if i.Status != BackfillItemFailed || i.FailureClass == "" || i.WorkID == "" || workID == "" || workID == i.WorkID {
		return BackfillItem{}, &InvalidTransitionError{Entity: "backfill_item", From: string(i.Status), Action: "retry"}
	}
	i.WorkID, i.FailureClass, i.Status, i.Version = workID, "", BackfillItemQueued, i.Version+1
	return i, nil
}

func (i BackfillItem) RetryAllowed(mode BackfillMode) bool {
	if i.Status != BackfillItemFailed || i.WorkID == "" || i.FailureClass == "contract_violated" {
		return false
	}
	return mode != BackfillArtifactRecompute || (i.FailureClass != "parse_error" && i.FailureClass != "quality_rejected")
}

func (i BackfillItem) Cancel(expected uint64) (BackfillItem, error) {
	if err := requireVersion(expected, i.Version); err != nil {
		return BackfillItem{}, err
	}
	if i.Status != BackfillItemPending && i.Status != BackfillItemQueued && i.Status != BackfillItemFailed {
		return BackfillItem{}, &InvalidTransitionError{Entity: "backfill_item", From: string(i.Status), Action: "cancel"}
	}
	i.Status, i.Version = BackfillItemCanceled, i.Version+1
	return i, nil
}

type BackfillOutput struct {
	OutputID                 string       `json:"output_id"`
	BackfillID               string       `json:"backfill_id"`
	ItemID                   string       `json:"item_id"`
	JobID                    string       `json:"job_id"`
	Mode                     BackfillMode `json:"mode"`
	ContentHash              string       `json:"content_hash"`
	OutputArtifactID         string       `json:"output_artifact_id"`
	InputDetailVersionID     string       `json:"input_detail_version_id,omitempty"`
	InputArtifactID          string       `json:"input_artifact_id,omitempty"`
	RecipeID                 string       `json:"recipe_id"`
	RecipeVersion            uint64       `json:"recipe_version"`
	Fields                   []string     `json:"fields"`
	DerivedFromObservedAt    string       `json:"derived_from_observed_at,omitempty"`
	RefetchedAt              string       `json:"refetched_at,omitempty"`
	ClaimsHistoricalSnapshot bool         `json:"claims_historical_snapshot"`
}

func NewBackfillOutput(id string, backfill Backfill, item BackfillItem, contentHash, outputArtifactID,
	refetchedAt string) (BackfillOutput, error) {
	refetchedAt = strings.TrimSpace(refetchedAt)
	if strings.TrimSpace(id) == "" || item.BackfillID != backfill.BackfillID || item.JobID == "" ||
		strings.TrimSpace(contentHash) == "" || strings.TrimSpace(outputArtifactID) == "" {
		return BackfillOutput{}, fmt.Errorf("backfill output requires matching lineage, content, and Artifact")
	}
	if backfill.Mode == BackfillArtifactRecompute {
		if item.InputDetailVersionID == "" || item.InputArtifactID == "" || item.InputObservedAt == "" || refetchedAt != "" {
			return BackfillOutput{}, fmt.Errorf("artifact recompute output requires original observation time only")
		}
		if _, err := time.Parse(time.RFC3339, item.InputObservedAt); err != nil {
			return BackfillOutput{}, err
		}
	} else {
		if item.InputDetailVersionID != "" || item.InputArtifactID != "" || item.InputObservedAt != "" || refetchedAt == "" {
			return BackfillOutput{}, fmt.Errorf("live refetch output requires refetch time and cannot claim an input snapshot")
		}
		if _, err := time.Parse(time.RFC3339, refetchedAt); err != nil {
			return BackfillOutput{}, err
		}
	}
	return BackfillOutput{OutputID: strings.TrimSpace(id), BackfillID: backfill.BackfillID, ItemID: item.ItemID,
		JobID: item.JobID, Mode: backfill.Mode, ContentHash: strings.TrimSpace(contentHash),
		OutputArtifactID: strings.TrimSpace(outputArtifactID), InputDetailVersionID: item.InputDetailVersionID,
		InputArtifactID: item.InputArtifactID,
		RecipeID:        backfill.RecipeID, RecipeVersion: backfill.RecipeVersion, Fields: append([]string(nil), backfill.Fields...),
		DerivedFromObservedAt: item.InputObservedAt, RefetchedAt: refetchedAt,
		ClaimsHistoricalSnapshot: backfill.Mode == BackfillArtifactRecompute}, nil
}
