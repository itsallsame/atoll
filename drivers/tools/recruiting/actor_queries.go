package recruiting

import (
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
)

type entityGetPayload struct {
	ID string `json:"id"`
}

type workListPayload struct {
	View             string           `json:"view,omitempty"`
	DueAt            string           `json:"due_at,omitempty"`
	Capability       string           `json:"capability,omitempty"`
	Origin           string           `json:"origin,omitempty"`
	ProfileID        string           `json:"profile_id,omitempty"`
	Status           model.WorkStatus `json:"status,omitempty"`
	Purpose          string           `json:"purpose,omitempty"`
	Trigger          string           `json:"trigger,omitempty"`
	WaitingReason    string           `json:"waiting_reason,omitempty"`
	Target           Target           `json:"target,omitempty"`
	InitiatorActorID string           `json:"initiator_actor_id,omitempty"`
	UpdatedFrom      string           `json:"updated_from,omitempty"`
	UpdatedBefore    string           `json:"updated_before,omitempty"`
	PageRequest
}

type sourceListPayload struct {
	CompanyID string `json:"company_id,omitempty"`
	PageRequest
}

type sourceDiscoveryCandidateListPayload struct {
	DiscoveryID string `json:"discovery_id"`
	PageRequest
}

type jobListPayload struct {
	SourceID string `json:"source_id"`
	PageRequest
}

type dailyRunSummaryPayload struct {
	ID string `json:"id"`
	PageRequest
}

type operationalStatusPayload struct {
	Limit int `json:"limit,omitempty"`
}

func handleResourceQuery(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	if msg.Type == TypeWorkList {
		handleWorkListQuery(sys, repository, msg)
		return
	}
	if msg.Type == TypeSourceList {
		handleSourceListQuery(sys, repository, msg)
		return
	}
	if msg.Type == TypeSourceDiscoveryCandidates {
		handleSourceDiscoveryCandidateListQuery(sys, repository, msg)
		return
	}
	if msg.Type == TypeJobList {
		handleJobListQuery(sys, repository, msg)
		return
	}
	if msg.Type == TypeDailyRunList {
		handleDailyRunListQuery(sys, repository, msg)
		return
	}
	if msg.Type == TypeDailyRunSummary {
		handleDailyRunSummaryQuery(sys, repository, msg)
		return
	}
	if msg.Type == TypeSystemStatus || msg.Type == TypeCapacityStatus {
		handleOperationalStatusQuery(sys, cfg, repository, msg)
		return
	}
	var payload entityGetPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.ID = strings.TrimSpace(payload.ID)
	if payload.ID == "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "id is required")
		return
	}
	var value any
	var err error
	switch msg.Type {
	case TypeSourceGet:
		value, err = repository.GetSource(msg.Ctx(), payload.ID)
	case TypeSourceDiscoveryGet:
		value, err = repository.GetSourceDiscovery(msg.Ctx(), payload.ID)
	case TypeJobGet:
		value, err = repository.GetJob(msg.Ctx(), payload.ID)
	case TypeWorkGet:
		var record store.WorkRecord
		record, err = repository.GetWorkRecord(msg.Ctx(), payload.ID)
		value = record.Work
		if err == nil {
			_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "entity": value, "placement": record.Placement})
			return
		}
	case TypeDailyRunGet:
		value, err = repository.GetDailyRun(msg.Ctx(), payload.ID)
	}
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "entity": value})
}

func handleSourceDiscoveryCandidateListQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload sourceDiscoveryCandidateListPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if err := payload.PageRequest.Validate(500); err != nil || strings.TrimSpace(payload.DiscoveryID) == "" {
		if err == nil {
			err = fmt.Errorf("discovery_id is required")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	if payload.Limit == 0 {
		payload.Limit = 50
	}
	page, err := repository.ListSourceDiscoveryCandidates(msg.Ctx(), payload.DiscoveryID, payload.Cursor, payload.Limit)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "items": page.Items,
		"next_cursor": page.NextCursor, "has_more": page.HasMore})
}

func handleOperationalStatusQuery(sys actorbase.Sys, cfg Config, repository *store.Repository, msg actorbase.Msg) {
	var payload operationalStatusPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if payload.Limit < 0 || payload.Limit > 100 {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "limit must be in [0,100]")
		return
	}
	if payload.Limit == 0 {
		payload.Limit = 20
	}
	asOf := time.UnixMilli(msg.TS).UTC()
	if msg.Type == TypeSystemStatus {
		status, err := repository.GetOperationalStatus(msg.Ctx(), asOf)
		if err != nil {
			failStoreError(sys, msg, err)
			return
		}
		_, _ = sys.Reply(msg, map[string]any{
			"contract_version": ContractVersion, "correlation_id": string(msg.CorrelationID), "system_status": status,
		})
		return
	}
	snapshot, err := repository.GetCapacitySnapshot(msg.Ctx(), asOf, payload.Limit)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	fleet := map[string]int{}
	for _, executor := range cfg.Executors {
		fleet[executor.Capability]++
	}
	policy := cfg.executionBudgetPolicy()
	_, _ = sys.Reply(msg, map[string]any{
		"contract_version": ContractVersion, "correlation_id": string(msg.CorrelationID),
		"capacity": snapshot, "executor_fleet_by_capability": fleet,
		"budget_policy": map[string]any{
			"version": policy.Version, "max_active": policy.MaxActive, "max_per_capability": policy.MaxPerCapability,
			"max_per_origin": policy.MaxPerOrigin, "max_per_company": policy.MaxPerCompany,
			"max_per_profile": policy.MaxPerProfile, "permit_ttl_ms": policy.PermitTTL.Milliseconds(),
		},
	})
}

func handleJobListQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload jobListPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if err := payload.PageRequest.Validate(500); err != nil || strings.TrimSpace(payload.SourceID) == "" {
		if err == nil {
			err = fmt.Errorf("source_id is required")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	if payload.Limit == 0 {
		payload.Limit = 50
	}
	page, err := repository.ListJobs(msg.Ctx(), payload.SourceID, payload.Cursor, payload.Limit)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "jobs": page.Items,
		"page": PageInfo{NextCursor: page.NextCursor, HasMore: page.HasMore}})
}

func handleDailyRunListQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload PageRequest
	if !decode(sys, msg, &payload) {
		return
	}
	if err := payload.Validate(500); err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	if payload.Limit == 0 {
		payload.Limit = 50
	}
	page, err := repository.ListDailyRuns(msg.Ctx(), payload.Cursor, payload.Limit)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "daily_runs": page.Items,
		"page": PageInfo{NextCursor: page.NextCursor, HasMore: page.HasMore}})
}

func handleDailyRunSummaryQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload dailyRunSummaryPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if err := payload.PageRequest.Validate(500); err != nil || strings.TrimSpace(payload.ID) == "" {
		if err == nil {
			err = fmt.Errorf("id is required")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	if payload.Limit == 0 {
		payload.Limit = 50
	}
	run, progress, err := repository.GetDailyRunProgress(msg.Ctx(), payload.ID)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	occurrences, err := repository.ListOccurrences(msg.Ctx(), run.DailyRunID, payload.Cursor, payload.Limit)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{
		"contract_version": ContractVersion, "daily_run": run, "progress": progress, "occurrences": occurrences.Items,
		"page": PageInfo{NextCursor: occurrences.NextCursor, HasMore: occurrences.HasMore},
	})
}

func handleSourceListQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload sourceListPayload
	if !decode(sys, msg, &payload) {
		return
	}
	if err := payload.PageRequest.Validate(500); err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	if payload.Limit == 0 {
		payload.Limit = 50
	}
	page, err := repository.ListSources(msg.Ctx(), strings.TrimSpace(payload.CompanyID), payload.Cursor, payload.Limit)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{
		"contract_version": ContractVersion, "sources": page.Items,
		"page": PageInfo{NextCursor: page.NextCursor, HasMore: page.HasMore},
	})
}

func handleWorkListQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload workListPayload
	if !decode(sys, msg, &payload) {
		return
	}
	payload.View = strings.TrimSpace(payload.View)
	if payload.View == "" && strings.TrimSpace(payload.DueAt) != "" {
		payload.View = "runnable"
	}
	if payload.View == "runnable" {
		handleRunnableWorkList(sys, repository, msg, payload)
		return
	}
	if payload.View != "" && payload.View != "operational" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "view must be operational or runnable")
		return
	}
	if strings.TrimSpace(payload.DueAt) != "" || strings.TrimSpace(payload.Capability) != "" ||
		strings.TrimSpace(payload.Origin) != "" || strings.TrimSpace(payload.ProfileID) != "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "operational view does not accept runnable queue fields")
		return
	}
	if err := payload.PageRequest.Validate(500); err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	if payload.Limit == 0 {
		payload.Limit = 50
	}
	query, err := operationalWorkQuery(payload)
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	page, err := repository.ListWorks(msg.Ctx(), query)
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	works := make([]model.Work, 0, len(page.Items))
	placements := make(map[string]store.WorkPlacement, len(page.Items))
	for _, record := range page.Items {
		works = append(works, record.Work)
		placements[record.Work.WorkID] = record.Placement
	}
	_, _ = sys.Reply(msg, map[string]any{
		"contract_version": ContractVersion, "view": "operational", "works": works, "placements": placements,
		"page": PageInfo{NextCursor: page.NextCursor, HasMore: page.HasMore},
	})
}

func handleRunnableWorkList(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg, payload workListPayload) {
	if payload.Cursor != "" || payload.Status != "" || strings.TrimSpace(payload.Purpose) != "" || strings.TrimSpace(payload.Trigger) != "" ||
		strings.TrimSpace(payload.WaitingReason) != "" || strings.TrimSpace(payload.Target.Type) != "" || strings.TrimSpace(payload.Target.ID) != "" ||
		strings.TrimSpace(payload.InitiatorActorID) != "" || strings.TrimSpace(payload.UpdatedFrom) != "" || strings.TrimSpace(payload.UpdatedBefore) != "" {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "runnable view does not accept operational filters or cursor")
		return
	}
	if err := payload.PageRequest.Validate(500); err != nil || strings.TrimSpace(payload.Capability) == "" {
		if err == nil {
			err = fmt.Errorf("capability is required")
		}
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, err.Error())
		return
	}
	dueAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(payload.DueAt))
	if err != nil {
		_, _ = sys.Fail(msg, ErrorPayloadInvalid, "due_at must be RFC3339")
		return
	}
	if payload.Limit == 0 {
		payload.Limit = 50
	}
	works, err := repository.ListRunnableWorks(msg.Ctx(), store.RunnableWorkQuery{
		DueAt: dueAt, Capability: strings.TrimSpace(payload.Capability), Origin: strings.TrimSpace(payload.Origin),
		ProfileID: strings.TrimSpace(payload.ProfileID), Limit: payload.Limit,
	})
	if err != nil {
		failStoreError(sys, msg, err)
		return
	}
	_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "view": "runnable", "works": works})
}

func operationalWorkQuery(payload workListPayload) (store.WorkListQuery, error) {
	query := store.WorkListQuery{
		Status: payload.Status, Purpose: strings.TrimSpace(payload.Purpose), Trigger: strings.TrimSpace(payload.Trigger),
		WaitingReason: strings.TrimSpace(payload.WaitingReason), TargetType: strings.TrimSpace(payload.Target.Type),
		TargetID: strings.TrimSpace(payload.Target.ID), InitiatorActorID: strings.TrimSpace(payload.InitiatorActorID),
		Cursor: payload.Cursor, Limit: payload.Limit,
	}
	if (query.TargetType == "") != (query.TargetID == "") {
		return store.WorkListQuery{}, fmt.Errorf("target type and ID must be supplied together")
	}
	if query.Status != "" && !operationalWorkStatus(query.Status) {
		return store.WorkListQuery{}, fmt.Errorf("unknown work status %q", query.Status)
	}
	if query.WaitingReason != "" && query.Status != model.WorkWaitingHuman {
		return store.WorkListQuery{}, fmt.Errorf("waiting_reason requires waiting_human status")
	}
	if strings.TrimSpace(payload.UpdatedFrom) != "" {
		value, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(payload.UpdatedFrom))
		if err != nil {
			return store.WorkListQuery{}, fmt.Errorf("updated_from must be RFC3339")
		}
		query.UpdatedFrom = &value
	}
	if strings.TrimSpace(payload.UpdatedBefore) != "" {
		value, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(payload.UpdatedBefore))
		if err != nil {
			return store.WorkListQuery{}, fmt.Errorf("updated_before must be RFC3339")
		}
		query.UpdatedBefore = &value
	}
	if query.UpdatedFrom != nil && query.UpdatedBefore != nil && !query.UpdatedFrom.Before(*query.UpdatedBefore) {
		return store.WorkListQuery{}, fmt.Errorf("updated_from must be earlier than updated_before")
	}
	return query, nil
}

func operationalWorkStatus(status model.WorkStatus) bool {
	switch status {
	case model.WorkOpen, model.WorkRunning, model.WorkWaitingRetry, model.WorkWaitingHuman,
		model.WorkPaused, model.WorkCompleted, model.WorkFailed, model.WorkCanceled:
		return true
	default:
		return false
	}
}
