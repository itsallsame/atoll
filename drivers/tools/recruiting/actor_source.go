package recruiting

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
)

type sourceAddPayload struct {
	CommandID           string `json:"command_id"`
	SourceID            string `json:"source_id"`
	CompanyID           string `json:"company_id"`
	Endpoint            string `json:"endpoint"`
	Category            string `json:"category,omitempty"`
	DiscoveryGeneration uint64 `json:"discovery_generation"`
	Reason              string `json:"reason"`
}

type sourceUpdatePayload struct {
	MutationCommand
	Endpoint *string `json:"endpoint,omitempty"`
	Category *string `json:"category,omitempty"`
}

type sourcePausePayload struct {
	MutationCommand
	PauseMode model.PauseMode `json:"pause_mode"`
}

type sourceValidationPublishPayload struct {
	MutationCommand
	RecipeID                  string                     `json:"recipe_id"`
	RecipeVersion             uint64                     `json:"recipe_version"`
	ExpectedAssignmentVersion uint64                     `json:"expected_assignment_version"`
	Identity                  model.ContractVerification `json:"identity"`
	Pagination                model.ContractVerification `json:"pagination"`
	Ordering                  model.ContractVerification `json:"ordering"`
	UpdateRetop               model.ContractVerification `json:"update_retop"`
	CheckpointStrategy        model.CheckpointStrategy   `json:"checkpoint_strategy"`
	OverlapPages              int                        `json:"overlap_pages"`
	EvidenceArtifactIDs       []string                   `json:"evidence_artifact_ids"`
}

type sourceCommandResponse struct {
	ContractVersion string                  `json:"contract_version"`
	CorrelationID   string                  `json:"correlation_id"`
	RequestedBy     string                  `json:"requested_by"`
	Source          model.RecruitmentSource `json:"source"`
	Target          Target                  `json:"target"`
	NextAction      string                  `json:"next_action"`
}

func handleSourceMessage(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	if msg.Type == TypeSourceAdd {
		handleSourceAdd(sys, repository, msg)
		return
	}
	if msg.Type == TypeSourceValidationPublish {
		handleSourceValidationPublish(sys, repository, msg)
		return
	}
	handleSourceMutation(sys, repository, msg)
}

func handleSourceAdd(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload sourceAddPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if strings.TrimSpace(payload.CommandID) == "" || strings.TrimSpace(payload.SourceID) == "" ||
		strings.TrimSpace(payload.CompanyID) == "" || strings.TrimSpace(payload.Endpoint) == "" || strings.TrimSpace(payload.Reason) == "" ||
		strings.TrimSpace(string(msg.Sender.ID)) == "" || payload.DiscoveryGeneration == 0 {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "command_id, source_id, company_id, endpoint, reason, authenticated sender, and discovery_generation are required")
		return
	}
	if replay, found, lookupErr := repository.LookupCommand(msg.Ctx(), payload.CommandID, commandRequestHash(msg)); lookupErr != nil {
		failStoreError(sys, msg, lookupErr)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	company, err := repository.GetCompany(msg.Ctx(), strings.TrimSpace(payload.CompanyID))
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	if company.ControlStatus == model.ControlArchived {
		_, _ = sys.Fail(msg, ErrorQualityRejected, "cannot add a source to an archived company")
		return
	}
	source, err := model.NewRecruitmentSource(payload.SourceID, company.CompanyID, payload.Endpoint, payload.Category, payload.DiscoveryGeneration)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	response := makeSourceResponse(msg, source)
	result, err := applySourceCommandFacts(repository, msg, payload.CommandID, payload.Reason, response, source, 0)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleSourceValidationPublish(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload sourceValidationPublishPayload
	if !decode(sys, msg, &payload) {
		return
	}
	commandContext, err := NewCommandContext(payload.MutationCommand, string(msg.Sender.ID))
	if err != nil || payload.Target.Type != "source" || strings.TrimSpace(payload.RecipeID) == "" || payload.RecipeVersion == 0 {
		if err == nil {
			err = fmt.Errorf("source target, recipe_id, and recipe_version are required")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	requestHash := commandRequestHash(msg)
	if replay, found, lookupErr := repository.LookupCommand(msg.Ctx(), payload.CommandID, requestHash); lookupErr != nil {
		failStoreError(sys, msg, lookupErr)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	current, err := repository.GetSource(msg.Ctx(), payload.Target.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	recipe, err := repository.GetRecipe(msg.Ctx(), strings.TrimSpace(payload.RecipeID), payload.RecipeVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	businessAt := time.UnixMilli(msg.TS).UTC()
	var assignment model.SourceRecipeAssignment
	if payload.ExpectedAssignmentVersion == 0 {
		assignment, err = model.NewSourceRecipeAssignment(current.SourceID, model.RecipeListing, recipe.RecipeID,
			recipe.Version, recipe.ContractHash, businessAt.Format(time.RFC3339Nano))
	} else {
		var existing model.SourceRecipeAssignment
		existing, err = repository.GetAssignment(msg.Ctx(), current.SourceID, model.RecipeListing)
		if err == nil {
			assignment, err = existing.Replace(payload.ExpectedAssignmentVersion, recipe.RecipeID, recipe.Version,
				recipe.ContractHash, businessAt.Format(time.RFC3339Nano))
		}
	}
	assessmentVersion := uint64(1)
	if current.ContractAssessment != nil {
		assessmentVersion = current.ContractAssessment.Version + 1
	}
	endpointRevision := uint64(0)
	if current.CandidateEndpoint != nil {
		endpointRevision = current.CandidateEndpoint.Revision
	}
	assessment := model.SourceContractAssessment{
		SourceID: current.SourceID, EndpointRevision: endpointRevision, RecipeID: recipe.RecipeID,
		RecipeVersion: recipe.Version, ContractHash: recipe.ContractHash, Identity: payload.Identity,
		Pagination: payload.Pagination, Ordering: payload.Ordering, UpdateRetop: payload.UpdateRetop,
		CheckpointStrategy: payload.CheckpointStrategy, OverlapPages: payload.OverlapPages,
		EvidenceArtifactIDs: append([]string(nil), payload.EvidenceArtifactIDs...),
		AssessedAt:          businessAt.Format(time.RFC3339Nano), Version: assessmentVersion,
	}
	var next model.RecruitmentSource
	if err == nil {
		next, err = current.PublishValidated(payload.ExpectedVersion, assignment, assessment)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := makeSourceResponse(msg, next)
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(payload.CommandID, msg.Type, requestHash, responseBytes)
	auditPayload, _ := json.Marshal(map[string]any{
		"requested_by": commandContext.RequestedBy, "reason": payload.Reason, "recipe_id": recipe.RecipeID,
		"recipe_version": recipe.Version, "evidence_artifact_ids": payload.EvidenceArtifactIDs,
	})
	var event model.EventIntent
	if err == nil {
		event, err = model.NewEventIntent("event-"+stableDigest(payload.CommandID+"|source.validation.published"),
			"source.validation.published", "source", next.SourceID, next.Version,
			businessAt.Format(time.RFC3339Nano), payload.CommandID, auditPayload)
	}
	var result store.CommandResult
	if err == nil {
		result, err = repository.ApplyPublishSourceValidationCommand(msg.Ctx(), payload.ExpectedVersion,
			payload.ExpectedAssignmentVersion, next, assignment, receipt, event, businessAt)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleSourceMutation(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var command MutationCommand
	var update sourceUpdatePayload
	var pause sourcePausePayload
	switch msg.Type {
	case TypeSourceUpdate:
		if !decode(sys, msg, &update) {
			return
		}
		command = update.MutationCommand
	case TypeSourcePause:
		if !decode(sys, msg, &pause) {
			return
		}
		command = pause.MutationCommand
	default:
		if !decode(sys, msg, &command) {
			return
		}
	}
	commandContext, err := NewCommandContext(command, string(msg.Sender.ID))
	if err != nil || command.Target.Type != "source" {
		if err == nil {
			err = fmt.Errorf("target_type must be source")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	if replay, found, lookupErr := repository.LookupCommand(msg.Ctx(), command.CommandID, commandRequestHash(msg)); lookupErr != nil {
		failStoreError(sys, msg, lookupErr)
		return
	} else if found {
		_, _ = sys.Reply(msg, json.RawMessage(replay.Response))
		return
	}
	current, err := repository.GetSource(msg.Ctx(), command.Target.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	var next model.RecruitmentSource
	switch msg.Type {
	case TypeSourceUpdate:
		next, err = applySourceEndpointUpdate(current, command.ExpectedVersion, update)
	case TypeSourceValidate:
		next, err = current.BeginValidation(command.ExpectedVersion)
	case TypeSourcePause:
		next, err = current.Pause(command.ExpectedVersion, pause.PauseMode)
	case TypeSourceResume:
		next, err = current.Resume(command.ExpectedVersion)
	case TypeSourceArchive:
		next, err = current.Archive(command.ExpectedVersion)
	case TypeSourceRestore:
		next, err = current.Restore(command.ExpectedVersion)
	default:
		err = fmt.Errorf("unsupported source mutation")
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := makeSourceResponse(msg, next)
	result, err := applySourceCommandFacts(repository, msg, command.CommandID, command.Reason, response, next, commandContext.Command.ExpectedVersion)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func applySourceEndpointUpdate(current model.RecruitmentSource, expected uint64, update sourceUpdatePayload) (model.RecruitmentSource, error) {
	if expected != current.Version {
		return model.RecruitmentSource{}, &model.VersionConflictError{Expected: expected, Actual: current.Version}
	}
	if update.Endpoint == nil && update.Category == nil {
		return model.RecruitmentSource{}, fmt.Errorf("source update contains no change")
	}
	base := current.CandidateEndpoint
	if base == nil {
		base = current.ActiveEndpoint
	}
	if base == nil {
		return model.RecruitmentSource{}, fmt.Errorf("source has no endpoint to update")
	}
	endpoint, category := base.URL, base.Category
	if update.Endpoint != nil {
		endpoint = *update.Endpoint
	}
	if update.Category != nil {
		category = *update.Category
	}
	canonical, err := model.CanonicalHTTPURL(endpoint)
	if err != nil {
		return model.RecruitmentSource{}, err
	}
	category = strings.TrimSpace(category)
	if canonical == base.URL && category == base.Category {
		return model.RecruitmentSource{}, fmt.Errorf("source update contains no change")
	}
	return current.StageEndpoint(expected, canonical, category)
}

func applySourceCommandFacts(repository *store.Repository, msg actorbase.Msg, commandID, reason string, response sourceCommandResponse, source model.RecruitmentSource, expectedVersion uint64) (store.CommandResult, error) {
	businessAt := time.UnixMilli(msg.TS).UTC()
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(commandID, msg.Type, commandRequestHash(msg), responseBytes)
	if err != nil {
		return store.CommandResult{}, err
	}
	auditPayload, _ := json.Marshal(map[string]any{"requested_by": string(msg.Sender.ID), "reason": reason})
	eventKind := sourceEventKind(msg.Type)
	event, err := model.NewEventIntent("event-"+stableDigest(commandID+"|"+eventKind), eventKind, "source", source.SourceID,
		source.Version, businessAt.Format(time.RFC3339Nano), commandID, auditPayload)
	if err != nil {
		return store.CommandResult{}, err
	}
	if expectedVersion == 0 {
		return repository.ApplyCreateSourceCommand(msg.Ctx(), source, receipt, event, businessAt)
	}
	return repository.ApplySourceCommand(msg.Ctx(), expectedVersion, source, receipt, event, businessAt)
}

func makeSourceResponse(msg actorbase.Msg, source model.RecruitmentSource) sourceCommandResponse {
	nextAction := "inspect_source"
	switch {
	case source.ControlStatus == model.ControlArchived:
		nextAction = "restore_source"
	case source.ControlStatus == model.ControlPaused:
		nextAction = "resume_source"
	case source.ReadinessStatus == model.SourceCandidate || source.ReadinessStatus == model.SourceRepairing:
		nextAction = "validate_source"
	case source.ReadinessStatus == model.SourceValidating:
		nextAction = "await_validation"
	case source.ReadinessStatus == model.SourceReady:
		nextAction = "run_or_inspect_source"
	}
	return sourceCommandResponse{
		ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID), RequestedBy: string(msg.Sender.ID),
		Source: source, Target: Target{Type: "source", ID: source.SourceID}, NextAction: nextAction,
	}
}

func sourceEventKind(word string) string {
	switch word {
	case TypeSourceAdd:
		return "source.added"
	case TypeSourceUpdate:
		return "source.updated"
	case TypeSourceValidate:
		return "source.validation_started"
	case TypeSourceValidationPublish:
		return "source.validation.published"
	case TypeSourcePause:
		return "source.paused"
	case TypeSourceResume:
		return "source.resumed"
	case TypeSourceArchive:
		return "source.archived"
	case TypeSourceRestore:
		return "source.restored"
	default:
		return "source.changed"
	}
}
