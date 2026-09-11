package recruiting

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
)

type companyAddPayload struct {
	CommandID string `json:"command_id"`
	CompanyID string `json:"company_id"`
	Name      string `json:"name"`
	Website   string `json:"website,omitempty"`
	Reason    string `json:"reason"`
}

type companyUpdatePayload struct {
	MutationCommand
	Name    *string `json:"name,omitempty"`
	Website *string `json:"website,omitempty"`
}

type companyPausePayload struct {
	MutationCommand
	PauseMode model.PauseMode `json:"pause_mode"`
}

type companyGetPayload struct {
	CompanyID string `json:"company_id"`
}

type companyCommandResponse struct {
	ContractVersion         string        `json:"contract_version"`
	CorrelationID           string        `json:"correlation_id"`
	RequestedBy             string        `json:"requested_by"`
	Company                 model.Company `json:"company"`
	NextAction              string        `json:"next_action"`
	ScopeControlOperationID string        `json:"scope_control_operation_id,omitempty"`
}

func handleCompanyMessage(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	switch msg.Type {
	case TypeCompanyAdd:
		handleCompanyAdd(sys, repository, msg)
	case TypeCompanyGet:
		var payload companyGetPayload
		if !decode(sys, msg, &payload) {
			return
		}
		if strings.TrimSpace(payload.CompanyID) == "" {
			_, _ = sys.Fail(msg, ErrorPayloadInvalid, "company_id is required")
			return
		}
		company, err := repository.GetCompany(msg.Ctx(), strings.TrimSpace(payload.CompanyID))
		if err != nil {
			failStoreError(sys, msg, err)
			return
		}
		alias, isAlias, err := repository.GetActiveCompanyAlias(msg.Ctx(), company.CompanyID)
		if err != nil {
			failStoreError(sys, msg, err)
			return
		}
		response := map[string]any{"contract_version": ContractVersion, "company": company, "is_alias": isAlias}
		if isAlias {
			response["canonical_company_id"], response["alias_mapping"] = alias.CanonicalCompanyID, alias
		}
		_, _ = sys.Reply(msg, response)
	case TypeCompanyList:
		var request PageRequest
		if !decode(sys, msg, &request) {
			return
		}
		if err := request.Validate(500); err != nil {
			_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
			return
		}
		if request.Limit == 0 {
			request.Limit = 50
		}
		page, err := repository.ListCompanies(msg.Ctx(), request.Cursor, request.Limit)
		if err != nil {
			failStoreError(sys, msg, err)
			return
		}
		_, _ = sys.Reply(msg, map[string]any{
			"contract_version": ContractVersion, "companies": page.Items,
			"page": PageInfo{NextCursor: page.NextCursor, HasMore: page.HasMore},
		})
	default:
		handleCompanyMutation(sys, repository, msg)
	}
}

func handleCompanyAdd(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload companyAddPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if strings.TrimSpace(payload.CommandID) == "" || strings.TrimSpace(payload.Reason) == "" || strings.TrimSpace(string(msg.Sender.ID)) == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "command_id, reason, and authenticated sender are required")
		return
	}
	company, err := model.NewCompany(payload.CompanyID, payload.Name, payload.Website)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	response := makeCompanyResponse(msg, company)
	result, err := applyCompanyCommandFacts(repository, msg, payload.CommandID, payload.Reason, response, company, 0, "")
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func handleCompanyMutation(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var command MutationCommand
	var update companyUpdatePayload
	var pause companyPausePayload
	switch msg.Type {
	case TypeCompanyUpdate:
		if !decode(sys, msg, &update) {
			return
		}
		command = update.MutationCommand
	case TypeCompanyPause:
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
	if err != nil || command.Target.Type != "company" {
		if err == nil {
			err = fmt.Errorf("target_type must be company")
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
	current, err := repository.GetCompany(msg.Ctx(), command.Target.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	var next model.Company
	switch msg.Type {
	case TypeCompanyUpdate:
		changed, updateErr := current.Update(command.ExpectedVersion, model.CompanyUpdate{Name: update.Name, Website: update.Website})
		if updateErr != nil {
			failStoreError(sys, msg, updateErr)
			return
		}
		if !changed.Changed {
			_, _ = sys.Fail(msg, ErrorPayloadInvalid, "company update contains no change")
			return
		}
		next = changed.Company
	case TypeCompanyPause:
		next, err = current.Pause(command.ExpectedVersion, pause.PauseMode)
	case TypeCompanyResume:
		next, err = current.Resume(command.ExpectedVersion)
	case TypeCompanyArchive:
		next, err = current.Archive(command.ExpectedVersion)
	case TypeCompanyRestore:
		next, err = current.Restore(command.ExpectedVersion)
	default:
		err = fmt.Errorf("unsupported company mutation")
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	response := makeCompanyResponse(msg, next)
	operationID := ""
	if msg.Type == TypeCompanyPause {
		operationID = "scope-control-" + stableDigest(command.CommandID+"|company|"+next.CompanyID)
		response.ScopeControlOperationID = operationID
	}
	result, err := applyCompanyCommandFacts(repository, msg, command.CommandID, command.Reason, response, next,
		commandContext.Command.ExpectedVersion, operationID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, json.RawMessage(result.Response))
}

func applyCompanyCommandFacts(repository *store.Repository, msg actorbase.Msg, commandID, reason string, response companyCommandResponse,
	company model.Company, expectedVersion uint64, operationID string) (store.CommandResult, error) {
	businessAt := time.UnixMilli(msg.TS).UTC()
	responseBytes, _ := json.Marshal(response)
	receipt, err := model.NewCommandReceipt(commandID, msg.Type, commandRequestHash(msg), responseBytes)
	if err != nil {
		return store.CommandResult{}, err
	}
	auditPayload, _ := json.Marshal(map[string]any{"requested_by": string(msg.Sender.ID), "reason": reason})
	eventKind := companyEventKind(msg.Type)
	event, err := model.NewEventIntent("event-"+stableDigest(commandID+"|"+eventKind), eventKind, "company", company.CompanyID,
		company.Version, businessAt.Format(time.RFC3339Nano), commandID, auditPayload)
	if err != nil {
		return store.CommandResult{}, err
	}
	if expectedVersion == 0 {
		return repository.ApplyCreateCompanyCommand(msg.Ctx(), company, receipt, event, businessAt)
	}
	if operationID != "" {
		return repository.ApplyCompanyPauseCommand(msg.Ctx(), expectedVersion, company, receipt, event, operationID, businessAt)
	}
	return repository.ApplyCompanyCommand(msg.Ctx(), expectedVersion, company, receipt, event, businessAt)
}

func makeCompanyResponse(msg actorbase.Msg, company model.Company) companyCommandResponse {
	nextAction := "inspect_company"
	if company.ControlStatus == model.ControlPaused {
		nextAction = "validate_or_resume"
	}
	return companyCommandResponse{
		ContractVersion: ContractVersion, CorrelationID: string(msg.CorrelationID),
		RequestedBy: string(msg.Sender.ID), Company: company, NextAction: nextAction,
	}
}

func companyEventKind(word string) string {
	switch word {
	case TypeCompanyAdd:
		return "company.added"
	case TypeCompanyUpdate:
		return "company.updated"
	case TypeCompanyPause:
		return "company.paused"
	case TypeCompanyResume:
		return "company.resumed"
	case TypeCompanyArchive:
		return "company.archived"
	case TypeCompanyRestore:
		return "company.restored"
	default:
		return "company.changed"
	}
}

func commandRequestHash(msg actorbase.Msg) string {
	sum := sha256.Sum256([]byte(msg.Type + "\n" + string(msg.Payload)))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func failStoreError(sys actorbase.Sys, msg actorbase.Msg, err error) {
	code := ErrorInternalUnavailable
	switch {
	case errors.Is(err, store.ErrInvalidCursor):
		code = ErrorPayloadInvalid
	case errors.Is(err, store.ErrNotFound):
		code = ErrorNotFound
	case errors.Is(err, store.ErrBusinessKeyExists):
		code = ErrorBusinessKeyConflict
	case errors.Is(err, store.ErrCommandConflict):
		code = ErrorCommandConflict
	case errors.Is(err, store.ErrAttemptConflict):
		code = ErrorVersionConflict
	case errors.Is(err, store.ErrAssignmentConflict):
		code = ErrorVersionConflict
	case errors.Is(err, store.ErrOverrideConflict):
		code = ErrorVersionConflict
	case errors.Is(err, store.ErrResultFenced):
		code = ErrorQualityRejected
	case errors.Is(err, store.ErrBudgetBlocked):
		code = ErrorBudgetBlocked
	case errors.Is(err, store.ErrRecipeRolloutRejected):
		code = ErrorQualityRejected
	case errors.Is(err, store.ErrRecipeValidationRejected):
		code = ErrorQualityRejected
	case errors.Is(err, store.ErrRecipeValidationInProgress):
		code = ErrorWaitingHuman
	case errors.Is(err, store.ErrRepairEvidenceRejected):
		code = ErrorQualityRejected
	case errors.Is(err, store.ErrWorkCorrectionInProgress):
		code = ErrorWaitingHuman
	default:
		var conflict *model.VersionConflictError
		var transition *model.InvalidTransitionError
		if errors.As(err, &conflict) {
			code = ErrorVersionConflict
		} else if errors.As(err, &transition) {
			code = ErrorQualityRejected
		}
	}
	_, _ = sys.Fail(msg, code, err.Error())
}
