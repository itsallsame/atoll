package recruiting

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
	"github.com/wanpengxie/atoll/protocol/access"
	"github.com/wanpengxie/atoll/protocol/resource"
)

const (
	recipeRolloutSourceSchema = "recipe-rollout-sources.v1"
	recipeRolloutMaxBytes     = int64(8 << 20)
	recipeRolloutMaxSources   = 20_000
)

type recipeRolloutBatchPayload struct {
	CommandID         string `json:"command_id"`
	BatchID           string `json:"batch_id"`
	RecipeID          string `json:"recipe_id"`
	RecipeVersion     uint64 `json:"recipe_version"`
	InputArtifactRef  string `json:"input_artifact_ref"`
	InputArtifactHash string `json:"input_artifact_hash"`
	SchemaVersion     string `json:"schema_version"`
	PolicyVersion     uint64 `json:"policy_version"`
	CanarySize        int    `json:"canary_size"`
	WaveSize          int    `json:"wave_size"`
	Reason            string `json:"reason"`
}

type recipeRolloutBatchControlPayload struct {
	CommandID       string `json:"command_id"`
	BatchID         string `json:"batch_id"`
	ExpectedVersion uint64 `json:"expected_version"`
	PreviewHash     string `json:"preview_hash,omitempty"`
	Reason          string `json:"reason"`
}

type recipeRolloutBatchQueryPayload struct {
	BatchID string `json:"batch_id"`
	Cursor  int    `json:"cursor,omitempty"`
	Limit   int    `json:"limit,omitempty"`
}

type recipeRolloutBatchResponse struct {
	ContractVersion string                   `json:"contract_version"`
	CorrelationID   string                   `json:"correlation_id"`
	RequestedBy     string                   `json:"requested_by"`
	Batch           model.RecipeRolloutBatch `json:"batch"`
	Work            model.Work               `json:"work"`
	NextAction      string                   `json:"next_action"`
}

func handleRecipeRolloutBatch(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	if msg.Type == TypeRecipeRolloutBatchConfirm || msg.Type == TypeRecipeRolloutBatchCancel {
		handleRecipeRolloutBatchControl(sys, repository, msg)
		return
	}
	var payload recipeRolloutBatchPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CommandID, payload.BatchID = strings.TrimSpace(payload.CommandID), strings.TrimSpace(payload.BatchID)
	payload.RecipeID, payload.InputArtifactRef = strings.TrimSpace(payload.RecipeID), strings.TrimSpace(payload.InputArtifactRef)
	payload.InputArtifactHash, payload.SchemaVersion = strings.TrimSpace(payload.InputArtifactHash), strings.TrimSpace(payload.SchemaVersion)
	payload.Reason = strings.TrimSpace(payload.Reason)
	if payload.CommandID == "" || payload.BatchID == "" || payload.RecipeID == "" || payload.RecipeVersion == 0 ||
		payload.InputArtifactRef == "" || payload.InputArtifactHash == "" || payload.SchemaVersion != recipeRolloutSourceSchema ||
		payload.PolicyVersion == 0 || payload.CanarySize < 1 || payload.CanarySize > recipeRolloutMaxSources ||
		payload.WaveSize < 1 || payload.WaveSize > 500 || payload.Reason == "" ||
		len(payload.BatchID) > 191 || strings.TrimSpace(string(msg.Sender.ID)) == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "complete batch, Recipe, immutable Resource, schema/policy, reason, and authenticated sender are required")
		return
	}
	requestHash := commandRequestHash(msg)
	if replay, found, lookupErr := repository.LookupCommand(msg.Ctx(), payload.CommandID, requestHash); lookupErr != nil {
		failStoreError(sys, msg, lookupErr)
		return
	} else if found {
		batch, getErr := repository.GetRecipeRolloutBatch(msg.Ctx(), payload.BatchID)
		if getErr != nil {
			failStoreError(sys, msg, getErr)
			return
		}
		if batch.Status == model.RecipeRolloutBatchPreviewing {
			sourceIDs, readErr := readRecipeRolloutSourceResource(sys.Resource(), payload.BatchID,
				payload.InputArtifactRef, payload.InputArtifactHash, payload.SchemaVersion, recipeRolloutMaxBytes)
			if readErr == nil && payload.CanarySize <= len(sourceIDs) {
				_, _ = materializeRecipeRolloutPreview(msg.Ctx(), repository, batch, sourceIDs,
					time.UnixMilli(msg.TS).UTC(), payload.CommandID)
			}
		}
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	sourceIDs, err := readRecipeRolloutSourceResource(sys.Resource(), payload.BatchID, payload.InputArtifactRef,
		payload.InputArtifactHash, payload.SchemaVersion, recipeRolloutMaxBytes)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorQualityRejected, err.Error())
		return
	}
	if payload.CanarySize > len(sourceIDs) {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "canary_size cannot exceed the immutable Source set")
		return
	}
	recipe, err := repository.GetRecipe(msg.Ctx(), payload.RecipeID, payload.RecipeVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	work, err := model.NewWork("work-recipe-rollout-"+rolloutBatchDigest(payload.BatchID), "recipe",
		payload.RecipeID+"@"+strconv.FormatUint(payload.RecipeVersion, 10), "recipe_rollout_batch", "human")
	if err == nil {
		work, err = work.WithCausality(string(msg.Sender.ID), string(msg.ID), "")
	}
	var batch model.RecipeRolloutBatch
	if err == nil {
		batch, err = model.NewRecipeRolloutBatch(payload.BatchID, work.WorkID, recipe, payload.InputArtifactRef,
			payload.InputArtifactHash, payload.SchemaVersion, payload.PolicyVersion, payload.CanarySize, payload.WaveSize)
	}
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	placement := store.WorkPlacement{BusinessKey: "recipe-rollout-batch|" + batch.BatchID, NotBefore: businessAt}
	response := recipeRolloutBatchResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: string(msg.Sender.ID), Batch: batch, Work: work, NextAction: "inspect_preview"}
	responseBytes, _ := json.Marshal(response)
	receipt, _ := model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
	audit, _ := json.Marshal(map[string]any{"requested_by": string(msg.Sender.ID), "reason": payload.Reason,
		"input_artifact_ref": batch.InputArtifactRef, "input_artifact_hash": batch.InputArtifactHash,
		"schema_version": batch.SchemaVersion, "policy_version": batch.PolicyVersion})
	event, _ := model.NewEventIntent("event-"+rolloutBatchDigest(payload.CommandID+"|created"),
		"recipe.rollout_batch.created", "work", work.WorkID, work.Version, businessAt.Format(time.RFC3339Nano),
		payload.CommandID, audit)
	result, err := repository.ApplyCreateRecipeRolloutBatchCommand(msg.Ctx(), work, placement, batch, receipt, event, businessAt)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if _, err = materializeRecipeRolloutPreview(msg.Ctx(), repository, batch, sourceIDs, businessAt,
		payload.CommandID); err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func materializeRecipeRolloutPreview(ctx context.Context, repository *store.Repository,
	batch model.RecipeRolloutBatch, sourceIDs []string, businessAt time.Time, causeCommandID string) (model.RecipeRolloutBatch, error) {
	if batch.PreviewedCount > len(sourceIDs) {
		return model.RecipeRolloutBatch{}, fmt.Errorf("stored Recipe rollout preview exceeds immutable input")
	}
	for batch.PreviewedCount < len(sourceIDs) {
		from := batch.PreviewedCount
		through := from + 500
		if through > len(sourceIDs) {
			through = len(sourceIDs)
		}
		sequence := batch.NextChunkSequence
		next, err := batch.AppendPreviewChunk(batch.Version, sequence, through-from)
		if err != nil {
			return model.RecipeRolloutBatch{}, err
		}
		response, _ := json.Marshal(map[string]any{"batch_id": batch.BatchID, "sequence": sequence,
			"previewed_count": through})
		internalID := "rollout-preview-" + rolloutBatchDigest(causeCommandID+"|"+strconv.Itoa(sequence))
		receipt, _ := model.NewCommandReceipt(internalID, "recruiting.internal.recipe_rollout.preview.chunk",
			"sha256:"+rolloutBatchDigest(string(response)), response)
		result, err := repository.ApplyRecipeRolloutPreviewChunk(ctx, batch.Version, sequence,
			sourceIDs[from:through], next, receipt, businessAt.Add(time.Duration(sequence)*time.Microsecond))
		if err != nil {
			return model.RecipeRolloutBatch{}, err
		}
		_ = result
		batch = next
	}
	current, next, currentParent, nextParent, err := repository.PrepareFinishRecipeRolloutPreview(ctx, batch.BatchID)
	if err != nil {
		return model.RecipeRolloutBatch{}, err
	}
	response, _ := json.Marshal(map[string]any{"batch_id": batch.BatchID, "preview_hash": next.PreviewHash,
		"source_count": next.SourceCount})
	internalID := "rollout-preview-finish-" + rolloutBatchDigest(causeCommandID)
	receipt, _ := model.NewCommandReceipt(internalID, "recruiting.internal.recipe_rollout.preview.finished",
		"sha256:"+rolloutBatchDigest(string(response)), response)
	event, _ := model.NewEventIntent("event-"+rolloutBatchDigest(internalID), "recipe.rollout_batch.previewed",
		"work", batch.ParentWorkID, nextParent.Version, businessAt.Add(time.Duration(batch.NextChunkSequence)*time.Microsecond).Format(time.RFC3339Nano),
		internalID, json.RawMessage(`{}`))
	if _, err := repository.ApplyFinishRecipeRolloutPreview(ctx, current.Version, currentParent.Version,
		next, nextParent, receipt, event,
		businessAt.Add(time.Duration(batch.NextChunkSequence)*time.Microsecond)); err != nil {
		return model.RecipeRolloutBatch{}, err
	}
	return next, nil
}

func handleRecipeRolloutBatchControl(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload recipeRolloutBatchControlPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.CommandID, payload.BatchID = strings.TrimSpace(payload.CommandID), strings.TrimSpace(payload.BatchID)
	payload.PreviewHash, payload.Reason = strings.TrimSpace(payload.PreviewHash), strings.TrimSpace(payload.Reason)
	if payload.CommandID == "" || payload.BatchID == "" || payload.ExpectedVersion == 0 || payload.Reason == "" ||
		strings.TrimSpace(string(msg.Sender.ID)) == "" || (msg.Type == TypeRecipeRolloutBatchConfirm && payload.PreviewHash == "") {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "command_id, batch_id, expected_version, reason, and confirm preview_hash are required")
		return
	}
	if replay, found, err := repository.LookupCommand(msg.Ctx(), payload.CommandID, commandRequestHash(msg)); err != nil {
		failStoreError(sys, msg, err)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	batch, err := repository.GetRecipeRolloutBatch(msg.Ctx(), payload.BatchID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	parent, err := repository.GetWork(msg.Ctx(), batch.ParentWorkID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	var nextBatch model.RecipeRolloutBatch
	var nextParent model.Work
	if msg.Type == TypeRecipeRolloutBatchConfirm {
		nextBatch, err = batch.Start(payload.ExpectedVersion, payload.PreviewHash)
		if err == nil {
			nextParent, err = parent.Start(parent.Version)
		}
	} else {
		nextBatch, err = batch.Cancel(payload.ExpectedVersion)
		if err == nil {
			nextParent, err = parent.Cancel(parent.Version)
		}
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	nextAction, eventType := "monitor_canary", "recipe.rollout_batch.started"
	if msg.Type == TypeRecipeRolloutBatchCancel {
		nextAction, eventType = "none", "recipe.rollout_batch.canceled"
	}
	response := recipeRolloutBatchResponse{ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: string(msg.Sender.ID), Batch: nextBatch, Work: nextParent, NextAction: nextAction}
	responseBytes, _ := json.Marshal(response)
	receipt, _ := model.NewCommandReceipt(payload.CommandID, msg.Type, commandRequestHash(msg), responseBytes)
	businessAt := time.UnixMilli(msg.TS).UTC()
	audit, _ := json.Marshal(map[string]any{"requested_by": string(msg.Sender.ID), "reason": payload.Reason,
		"batch_id": payload.BatchID, "preview_hash": payload.PreviewHash})
	event, _ := model.NewEventIntent("event-"+rolloutBatchDigest(payload.CommandID+"|"+eventType), eventType,
		"work", parent.WorkID, nextParent.Version, businessAt.Format(time.RFC3339Nano), payload.CommandID, audit)
	var result store.CommandResult
	if msg.Type == TypeRecipeRolloutBatchConfirm {
		result, err = repository.ApplyStartRecipeRolloutBatchCommand(msg.Ctx(), batch.Version, parent.Version,
			nextBatch, nextParent, receipt, event, businessAt)
	} else {
		result, err = repository.ApplyCancelRecipeRolloutBatchCommand(msg.Ctx(), batch.Version, parent.Version,
			nextBatch, nextParent, receipt, event, businessAt)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleRecipeRolloutBatchQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload recipeRolloutBatchQueryPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.BatchID = strings.TrimSpace(payload.BatchID)
	if payload.BatchID == "" || payload.Cursor < 0 || payload.Limit < 0 || payload.Limit > 500 {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "batch_id, non-negative cursor, and limit in [0,500] are required")
		return
	}
	batch, err := repository.GetRecipeRolloutBatch(msg.Ctx(), payload.BatchID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if msg.Type == TypeRecipeRolloutBatchGet {
		work, err := repository.GetWork(msg.Ctx(), batch.ParentWorkID)
		if err != nil {
			failStoreError(sys, msg, err)
			return
		}
		_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "batch": batch, "work": work})
		return
	}
	if payload.Limit == 0 {
		payload.Limit = 50
	}
	page, err := repository.ListRecipeRolloutBatchItems(msg.Ctx(), batch.BatchID, payload.Cursor, payload.Limit)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "batch": batch,
		"items": page.Items, "page": map[string]any{"next_cursor": page.NextCursor, "has_more": page.NextCursor != 0}})
}

func readRecipeRolloutSourceResource(resources actorbase.ResourceHandle, batchID, reference, expectedHash,
	schemaVersion string, maxBytes int64) ([]string, error) {
	parsed, err := url.Parse(reference)
	if err != nil || (parsed.Scheme != "artifact" && parsed.Scheme != "daemon") || parsed.Host == "" ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("Recipe rollout input must be an opaque Artifact Resource reference")
	}
	var content []byte
	file, outcome, openErr := resources.Open(resource.ResourceID(reference), access.OpRead)
	if openErr == nil && outcome.Accepted() {
		if reader, readable := file.Reader(); readable {
			defer reader.Close()
			content, err = io.ReadAll(io.LimitReader(reader, maxBytes+1))
			if err != nil {
				return nil, fmt.Errorf("stream Recipe rollout Resource: %w", err)
			}
		}
	}
	if content == nil {
		outcome, readErr := resources.Read(resource.ResourceID(reference))
		if readErr != nil || !outcome.Accepted() || !outcome.Found {
			return nil, fmt.Errorf("Recipe rollout Resource is not readable")
		}
		content = outcome.Value
	}
	if len(content) == 0 || int64(len(content)) > maxBytes {
		return nil, fmt.Errorf("Recipe rollout Resource size must be in [1,%d] bytes", maxBytes)
	}
	sum := sha256.Sum256(content)
	actual := "sha256:" + hex.EncodeToString(sum[:])
	if actual != expectedHash {
		return nil, fmt.Errorf("Recipe rollout Resource hash mismatch")
	}
	return parseRecipeRolloutSourceDocument(content, batchID, schemaVersion)
}

func parseRecipeRolloutSourceDocument(content []byte, batchID, schemaVersion string) ([]string, error) {
	if strings.TrimSpace(batchID) == "" || schemaVersion != recipeRolloutSourceSchema {
		return nil, fmt.Errorf("batch ID and supported Recipe rollout schema are required")
	}
	var document struct {
		SchemaVersion string   `json:"schema_version"`
		SourceIDs     []string `json:"source_ids"`
	}
	decoder := json.NewDecoder(strings.NewReader(string(content)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode Recipe rollout input: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("Recipe rollout input contains trailing JSON")
	}
	if document.SchemaVersion != schemaVersion || len(document.SourceIDs) == 0 {
		return nil, fmt.Errorf("Recipe rollout input schema must match and contain at least one Source")
	}
	seen := make(map[string]struct{})
	sourceIDs := append([]string(nil), document.SourceIDs...)
	for index, sourceID := range sourceIDs {
		if sourceID == "" || sourceID != strings.TrimSpace(sourceID) || len(sourceID) > 191 {
			return nil, fmt.Errorf("Recipe rollout member %d has a non-canonical Source ID", index+1)
		}
		if _, duplicate := seen[sourceID]; duplicate {
			return nil, fmt.Errorf("Recipe rollout member %d repeats Source %s", index+1, sourceID)
		}
		seen[sourceID] = struct{}{}
	}
	if len(sourceIDs) > recipeRolloutMaxSources {
		return nil, fmt.Errorf("Recipe rollout input exceeds %d Sources", recipeRolloutMaxSources)
	}
	sort.Slice(sourceIDs, func(left, right int) bool {
		leftKey := model.RecipeRolloutOrderKey(batchID, sourceIDs[left])
		rightKey := model.RecipeRolloutOrderKey(batchID, sourceIDs[right])
		if leftKey == rightKey {
			return sourceIDs[left] < sourceIDs[right]
		}
		return leftKey < rightKey
	})
	return sourceIDs, nil
}

func rolloutBatchDigest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}
