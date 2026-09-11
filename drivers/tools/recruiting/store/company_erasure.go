package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type CompanyErasurePreviewProgress struct {
	Erasure   model.CompanyErasure `json:"erasure"`
	Processed int                  `json:"processed"`
	Completed bool                 `json:"completed"`
}

func (r *Repository) ApplyCreateCompanyErasureCommand(ctx context.Context, erasure model.CompanyErasure,
	parent model.Work, placement WorkPlacement, receipt model.CommandReceipt, event model.EventIntent,
	businessAt time.Time) (CommandResult, error) {
	if erasure.Status != model.CompanyErasurePreviewing || erasure.Version != 1 || erasure.WorkID != parent.WorkID ||
		parent.Version != 1 || parent.Status != model.WorkOpen || parent.TargetType != "company" ||
		parent.TargetID != erasure.CompanyID || parent.Purpose != "company_compliance_erasure" ||
		placement.Capability != "" || placement.NotBefore.IsZero() || receipt.CommandID == "" ||
		event.AggregateType != "company_erasure" || event.AggregateID != erasure.ErasureID ||
		event.AggregateVersion != erasure.Version || event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("Company erasure creation facts are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339Nano, event.BusinessAt)
	if err != nil || !eventAt.Equal(businessAt) {
		return CommandResult{}, fmt.Errorf("Company erasure event time is inconsistent")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin Company erasure creation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	company, err := readCompanyForMerge(ctx, tx, erasure.CompanyID, true)
	if err != nil {
		return CommandResult{}, err
	}
	if company.Version != erasure.CompanyVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: erasure.CompanyVersion, Actual: company.Version}
	}
	if company.ControlStatus != model.ControlArchived {
		return CommandResult{}, &model.InvalidTransitionError{Entity: "company", From: string(company.ControlStatus), Action: "create compliance erasure"}
	}
	var nonArchivedSources, activeWorks, activeAliases uint64
	if err := tx.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM recruiting_sources WHERE company_id = ? AND control_status <> 'archived'),
  (SELECT COUNT(*) FROM recruiting_works WHERE company_id = ? AND status IN ('open','waiting_retry','running','waiting_human','paused')),
  (SELECT COUNT(*) FROM recruiting_company_aliases WHERE active_alias_key IS NOT NULL AND (alias_company_id = ? OR canonical_company_id = ?))`,
		erasure.CompanyID, erasure.CompanyID, erasure.CompanyID, erasure.CompanyID).
		Scan(&nonArchivedSources, &activeWorks, &activeAliases); err != nil {
		return CommandResult{}, fmt.Errorf("inspect Company erasure prerequisites: %w", err)
	}
	if nonArchivedSources != 0 || activeWorks != 0 || activeAliases != 0 {
		return CommandResult{}, fmt.Errorf("Company erasure requires archived Sources, settled Work, and no active alias mapping")
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	if err := insertWork(ctx, tx, parent, placement, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := insertCompanyErasure(ctx, tx, erasure, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit Company erasure creation: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func (r *Repository) BuildNextCompanyErasurePreview(ctx context.Context, erasureID string, limit int,
	businessAt time.Time) (CompanyErasurePreviewProgress, error) {
	if strings.TrimSpace(erasureID) == "" || limit < 1 || limit > 500 || businessAt.IsZero() {
		return CompanyErasurePreviewProgress{}, fmt.Errorf("Company erasure, bounded preview limit, and time are required")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CompanyErasurePreviewProgress{}, fmt.Errorf("begin Company erasure preview page: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := getCompanyErasureWith(ctx, tx, erasureID, true)
	if err != nil {
		return CompanyErasurePreviewProgress{}, err
	}
	if current.Status != model.CompanyErasurePreviewing {
		return CompanyErasurePreviewProgress{Erasure: current, Completed: true}, nil
	}
	company, err := readCompanyForMerge(ctx, tx, current.CompanyID, true)
	if err != nil {
		return CompanyErasurePreviewProgress{}, err
	}
	if company.Version != current.CompanyVersion {
		return CompanyErasurePreviewProgress{}, &model.VersionConflictError{Expected: current.CompanyVersion, Actual: company.Version}
	}
	if company.ControlStatus != model.ControlArchived {
		return CompanyErasurePreviewProgress{}, fmt.Errorf("Company left archived state during erasure preview")
	}
	// The exclusive Company lock freezes membership because the only legal
	// mutation of an archived Source is restore, and restore takes a shared
	// lock on its owning Company. Read one look-ahead row without locking it so
	// a limit of 500 never locks 501 Source rows.
	rows, err := tx.QueryContext(ctx, `SELECT state_json FROM recruiting_sources
WHERE company_id = ? AND source_id > ? ORDER BY source_id LIMIT ?`, current.CompanyID,
		current.PreviewCursor, limit+1)
	if err != nil {
		return CompanyErasurePreviewProgress{}, fmt.Errorf("read Company erasure Source page: %w", err)
	}
	sources := make([]model.RecruitmentSource, 0, limit+1)
	for rows.Next() {
		var state []byte
		if err := rows.Scan(&state); err != nil {
			_ = rows.Close()
			return CompanyErasurePreviewProgress{}, err
		}
		var source model.RecruitmentSource
		if err := json.Unmarshal(state, &source); err != nil {
			_ = rows.Close()
			return CompanyErasurePreviewProgress{}, fmt.Errorf("decode Company erasure Source: %w", err)
		}
		sources = append(sources, source)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return CompanyErasurePreviewProgress{}, err
	}
	if err := rows.Close(); err != nil {
		return CompanyErasurePreviewProgress{}, err
	}
	hasMore := len(sources) > limit
	if hasMore {
		sources = sources[:limit]
	}
	members := make([]model.CompanyErasureMember, 0, len(sources))
	for _, source := range sources {
		member, err := model.NewCompanyErasureMember(current.ErasureID, source)
		if err != nil {
			return CompanyErasurePreviewProgress{}, err
		}
		members = append(members, member)
	}
	accumulator, err := model.AdvanceCompanyErasurePreviewHash(current, current.PreviewAccumulator, members)
	if err != nil {
		return CompanyErasurePreviewProgress{}, err
	}
	nextCursor := ""
	if hasMore {
		nextCursor = members[len(members)-1].SourceID
	}
	impact := model.CompanyErasureImpact{}
	if !hasMore {
		impact, err = companyErasureImpact(ctx, tx, current.CompanyID, current.WorkID)
		if err != nil {
			return CompanyErasurePreviewProgress{}, err
		}
		impact.Sources = current.SourceCount + uint64(len(members))
	}
	next, err := current.AdvancePreview(current.Version, members, nextCursor, accumulator, hasMore, impact)
	if err != nil {
		return CompanyErasurePreviewProgress{}, err
	}
	for _, member := range members {
		state, err := json.Marshal(member)
		if err != nil {
			return CompanyErasurePreviewProgress{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO recruiting_company_erasure_sources(
  erasure_id, source_id, source_version, execution_fence, state_json, created_at
) VALUES (?, ?, ?, ?, ?, ?)`, member.ErasureID, member.SourceID, member.SourceVersion,
			member.ExecutionFence, state, businessAt.UTC()); err != nil {
			return CompanyErasurePreviewProgress{}, fmt.Errorf("insert Company erasure Source member: %w", err)
		}
	}
	if err := updateCompanyErasureCAS(ctx, tx, current.Version, next, businessAt); err != nil {
		return CompanyErasurePreviewProgress{}, err
	}
	if !hasMore {
		work, err := getWorkForErasureUpdate(ctx, tx, current.WorkID)
		if err != nil {
			return CompanyErasurePreviewProgress{}, err
		}
		running, err := work.Start(work.Version)
		if err != nil {
			return CompanyErasurePreviewProgress{}, err
		}
		if err := updateWorkTx(ctx, tx, work.Version, running, businessAt); err != nil {
			return CompanyErasurePreviewProgress{}, err
		}
		waiting, err := running.WaitHuman(running.Version, "compliance_approval_required")
		if err != nil {
			return CompanyErasurePreviewProgress{}, err
		}
		if err := updateWorkTx(ctx, tx, running.Version, waiting, businessAt); err != nil {
			return CompanyErasurePreviewProgress{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return CompanyErasurePreviewProgress{}, fmt.Errorf("commit Company erasure preview page: %w", err)
	}
	return CompanyErasurePreviewProgress{Erasure: next, Processed: len(members), Completed: !hasMore}, nil
}

func (r *Repository) NextCompanyErasurePreview(ctx context.Context) (model.CompanyErasure, error) {
	var state []byte
	err := r.db.QueryRowContext(ctx, `SELECT state_json FROM recruiting_company_erasures
WHERE erasure_status = 'previewing' ORDER BY created_at, erasure_id LIMIT 1`).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.CompanyErasure{}, ErrNotFound
	}
	if err != nil {
		return model.CompanyErasure{}, fmt.Errorf("find next Company erasure preview: %w", err)
	}
	var erasure model.CompanyErasure
	if err := json.Unmarshal(state, &erasure); err != nil {
		return model.CompanyErasure{}, fmt.Errorf("decode next Company erasure preview: %w", err)
	}
	return erasure, nil
}

func (r *Repository) ApplyApproveCompanyErasureCommand(ctx context.Context, erasureID string,
	expectedVersion uint64, previewHash, approver string, receipt model.CommandReceipt,
	eventID, reason string, businessAt time.Time) (CommandResult, error) {
	if strings.TrimSpace(erasureID) == "" || expectedVersion == 0 || strings.TrimSpace(previewHash) == "" ||
		strings.TrimSpace(approver) == "" || strings.TrimSpace(reason) == "" || receipt.CommandID == "" ||
		strings.TrimSpace(eventID) == "" || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("exact Company erasure approval, approver, reason, receipt, event, and time are required")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin Company erasure approval: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	current, err := getCompanyErasureWith(ctx, tx, erasureID, true)
	if err != nil {
		return CommandResult{}, err
	}
	company, err := readCompanyForMerge(ctx, tx, current.CompanyID, true)
	if err != nil {
		return CommandResult{}, err
	}
	if company.Version != current.CompanyVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: current.CompanyVersion, Actual: company.Version}
	}
	if company.ControlStatus != model.ControlArchived {
		return CommandResult{}, &model.InvalidTransitionError{Entity: "company", From: string(company.ControlStatus), Action: "approve compliance erasure"}
	}
	var sources, members, changedMembers, activeWorks uint64
	if err := tx.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM recruiting_sources WHERE company_id = ?),
  (SELECT COUNT(*) FROM recruiting_company_erasure_sources WHERE erasure_id = ?),
  (SELECT COUNT(*) FROM recruiting_sources source
     LEFT JOIN recruiting_company_erasure_sources member
       ON member.erasure_id = ? AND member.source_id = source.source_id
   WHERE source.company_id = ? AND
     (member.source_id IS NULL OR member.source_version <> source.version OR
      member.execution_fence <> source.execution_fence OR source.control_status <> 'archived')),
  (SELECT COUNT(*) FROM recruiting_works
   WHERE company_id = ? AND work_id <> ? AND status IN ('open','waiting_retry','running','waiting_human','paused'))`,
		current.CompanyID, current.ErasureID, current.ErasureID, current.CompanyID,
		current.CompanyID, current.WorkID).Scan(&sources, &members, &changedMembers, &activeWorks); err != nil {
		return CommandResult{}, fmt.Errorf("revalidate Company erasure preview: %w", err)
	}
	if sources != current.SourceCount || members != current.SourceCount || changedMembers != 0 || activeWorks != 0 {
		return CommandResult{}, fmt.Errorf("Company erasure preview is stale or execution has not settled")
	}
	next, err := current.Approve(expectedVersion, previewHash, approver, businessAt)
	if err != nil {
		return CommandResult{}, err
	}
	work, err := getWorkForErasureUpdate(ctx, tx, current.WorkID)
	if err != nil {
		return CommandResult{}, err
	}
	if work.Status != model.WorkWaitingHuman || work.WaitingReason != "compliance_approval_required" {
		return CommandResult{}, fmt.Errorf("Company erasure approval Work is not awaiting compliance approval")
	}
	running, err := work.Start(work.Version)
	if err != nil {
		return CommandResult{}, err
	}
	waiting, err := running.WaitHuman(running.Version, "compliance_retention_wait")
	if err != nil {
		return CommandResult{}, err
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	if err := updateCompanyErasureCAS(ctx, tx, current.Version, next, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := updateWorkTx(ctx, tx, work.Version, running, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := updateWorkTx(ctx, tx, running.Version, waiting, businessAt); err != nil {
		return CommandResult{}, err
	}
	audit, _ := json.Marshal(map[string]any{"approved_by": approver, "reason": reason,
		"preview_hash": previewHash, "policy_version": current.PolicyVersion})
	event, err := model.NewEventIntent(eventID, "company.erasure.approved", "company_erasure",
		next.ErasureID, next.Version, businessAt.Format(time.RFC3339Nano), receipt.CommandID, audit)
	if err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, event, businessAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit Company erasure approval: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func companyErasureImpact(ctx context.Context, tx *sql.Tx, companyID, erasureWorkID string) (model.CompanyErasureImpact, error) {
	var impact model.CompanyErasureImpact
	err := tx.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM recruiting_source_jobs job JOIN recruiting_sources source ON source.source_id = job.source_id WHERE source.company_id = ?),
  (SELECT COUNT(*) FROM recruiting_works WHERE company_id = ? AND work_id <> ?),
  (SELECT COUNT(*) FROM recruiting_attempts attempt JOIN recruiting_works work ON work.work_id = attempt.work_id WHERE work.company_id = ? AND work.work_id <> ?),
  (SELECT COUNT(*) FROM recruiting_artifacts artifact JOIN recruiting_works work ON work.work_id = artifact.work_id WHERE work.company_id = ? AND work.work_id <> ?),
  (SELECT COUNT(DISTINCT artifact.object_ref) FROM recruiting_artifacts artifact JOIN recruiting_works work ON work.work_id = artifact.work_id WHERE work.company_id = ? AND work.work_id <> ?),
  (SELECT COUNT(*) FROM recruiting_listing_observations observation JOIN recruiting_sources source ON source.source_id = observation.source_id WHERE source.company_id = ?),
  (SELECT COUNT(*) FROM recruiting_job_detail_versions detail JOIN recruiting_source_jobs job ON job.job_id = detail.job_id JOIN recruiting_sources source ON source.source_id = job.source_id WHERE source.company_id = ?),
  (SELECT COUNT(*) FROM recruiting_source_occurrences occurrence JOIN recruiting_sources source ON source.source_id = occurrence.source_id WHERE source.company_id = ?),
  (SELECT COUNT(*) FROM recruiting_source_discoveries WHERE company_id = ?),
  (SELECT COUNT(*) FROM recruiting_company_website_revisions WHERE company_id = ?),
  (SELECT COUNT(*) FROM recruiting_override_versions override_row JOIN recruiting_source_jobs job ON job.job_id = override_row.target_id JOIN recruiting_sources source ON source.source_id = job.source_id WHERE source.company_id = ?),
  (SELECT COUNT(*) FROM recruiting_backfills WHERE target_type = 'company' AND target_id = ?),
  (SELECT COUNT(*) FROM recruiting_backfill_items WHERE company_id = ?),
  (SELECT COUNT(*) FROM recruiting_works WHERE company_id = ? AND work_id <> ? AND status IN ('open','waiting_retry','running','waiting_human','paused'))`,
		companyID,
		companyID, erasureWorkID,
		companyID, erasureWorkID,
		companyID, erasureWorkID,
		companyID, erasureWorkID,
		companyID,
		companyID,
		companyID,
		companyID,
		companyID,
		companyID,
		companyID,
		companyID,
		companyID, erasureWorkID).
		Scan(&impact.Jobs, &impact.Works, &impact.Attempts, &impact.Artifacts, &impact.ResourceObjects,
			&impact.Observations, &impact.DetailVersions, &impact.DailyOccurrences, &impact.SourceDiscoveries,
			&impact.WebsiteRevisions, &impact.Overrides, &impact.Backfills, &impact.BackfillItems,
			&impact.ActiveExecutions)
	if err != nil {
		return model.CompanyErasureImpact{}, fmt.Errorf("read Company erasure impact: %w", err)
	}
	return impact, nil
}

func insertCompanyErasure(ctx context.Context, tx *sql.Tx, erasure model.CompanyErasure, at time.Time) error {
	state, err := json.Marshal(erasure)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO recruiting_company_erasures(
  erasure_id, work_id, company_id, active_company_key, erasure_status, company_version, policy_version,
  execute_after, preview_cursor, source_count, preview_accumulator, preview_hash, version, state_json,
  created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, NULL, 0, NULL, NULL, ?, ?, ?, ?)`, erasure.ErasureID, erasure.WorkID,
		erasure.CompanyID, erasure.CompanyID, erasure.Status, erasure.CompanyVersion, erasure.PolicyVersion,
		mustParseErasureTime(erasure.ExecuteAfter), erasure.Version, state, at.UTC(), at.UTC())
	if err != nil {
		if isDuplicateKey(err) {
			return fmt.Errorf("%w: Company erasure identity, Work, or active Company scope", ErrBusinessKeyExists)
		}
		return fmt.Errorf("insert Company erasure: %w", err)
	}
	return nil
}

func updateCompanyErasureCAS(ctx context.Context, tx *sql.Tx, expected uint64, erasure model.CompanyErasure,
	at time.Time) error {
	state, err := json.Marshal(erasure)
	if err != nil {
		return err
	}
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_company_erasures
SET erasure_status = ?, preview_cursor = ?, source_count = ?, preview_accumulator = ?, preview_hash = ?,
    version = ?, state_json = ?, updated_at = ? WHERE erasure_id = ? AND version = ?`, erasure.Status,
		nullableString(erasure.PreviewCursor), erasure.SourceCount, nullableString(erasure.PreviewAccumulator),
		nullableString(erasure.PreviewHash), erasure.Version, state, at.UTC(), erasure.ErasureID, expected)
	if err != nil {
		return fmt.Errorf("update Company erasure: %w", err)
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return &model.VersionConflictError{Expected: expected, Actual: erasure.Version}
	}
	return nil
}

func (r *Repository) GetCompanyErasure(ctx context.Context, erasureID string) (model.CompanyErasure, error) {
	return getCompanyErasureWith(ctx, r.db, erasureID, false)
}

func getCompanyErasureWith(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, erasureID string, lock bool) (model.CompanyErasure, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var state []byte
	err := query.QueryRowContext(ctx, `SELECT state_json FROM recruiting_company_erasures WHERE erasure_id = ?`+suffix,
		strings.TrimSpace(erasureID)).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.CompanyErasure{}, ErrNotFound
	}
	if err != nil {
		return model.CompanyErasure{}, fmt.Errorf("get Company erasure: %w", err)
	}
	var erasure model.CompanyErasure
	if err := json.Unmarshal(state, &erasure); err != nil {
		return model.CompanyErasure{}, fmt.Errorf("decode Company erasure: %w", err)
	}
	return erasure, nil
}

func getWorkForErasureUpdate(ctx context.Context, tx *sql.Tx, workID string) (model.Work, error) {
	var state []byte
	err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_works WHERE work_id = ? FOR UPDATE`, workID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Work{}, ErrNotFound
	}
	if err != nil {
		return model.Work{}, err
	}
	var work model.Work
	if err := json.Unmarshal(state, &work); err != nil {
		return model.Work{}, err
	}
	return work, nil
}

func mustParseErasureTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed.UTC()
}
