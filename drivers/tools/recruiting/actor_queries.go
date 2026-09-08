package recruiting

import (
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
	"github.com/wanpengxie/atoll/lib/actorbase"
)

type entityGetPayload struct {
	ID string `json:"id"`
}

type runnableWorkPayload struct {
	DueAt      string `json:"due_at"`
	Capability string `json:"capability"`
	Origin     string `json:"origin,omitempty"`
	ProfileID  string `json:"profile_id,omitempty"`
	Limit      int    `json:"limit,omitempty"`
}

type sourceListPayload struct {
	CompanyID string `json:"company_id,omitempty"`
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

func handleResourceQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	if repository == nil {
		_, _ = sys.Fail(msg, ErrorInternalUnavailable, "recruiting database is not configured")
		return
	}
	if msg.Type == TypeWorkList {
		handleRunnableWorkQuery(sys, repository, msg)
		return
	}
	if msg.Type == TypeSourceList {
		handleSourceListQuery(sys, repository, msg)
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

func handleRunnableWorkQuery(sys actorbase.Sys, repository *store.Repository, msg actorbase.Msg) {
	var payload runnableWorkPayload
	if !decode(sys, msg, &payload) {
		return
	}
	dueAt, err := time.Parse(time.RFC3339, payload.DueAt)
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
	_, _ = sys.Reply(msg, map[string]any{"contract_version": ContractVersion, "works": works})
}
