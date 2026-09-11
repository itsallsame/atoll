package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type CompanyMergePreviewRequest struct {
	MergePreviewID     string
	Action             model.CompanyMergeAction
	CanonicalCompanyID string
	AliasCompanyIDs    []string
	ExpectedVersions   map[string]uint64
	RequestedBy        string
	Reason             string
	ObservedAt         time.Time
}

type CompanyAliasProjection struct {
	AliasCompanyID     string `json:"alias_company_id"`
	CanonicalCompanyID string `json:"canonical_company_id"`
	AliasVersion       uint64 `json:"alias_version"`
	EffectiveFrom      string `json:"effective_from"`
	EffectiveUntil     string `json:"effective_until,omitempty"`
}

type CompanyMergeConfirmOutcome struct {
	Preview model.CompanyMergePreview `json:"preview"`
	Aliases []CompanyAliasProjection  `json:"aliases"`
}

func (r *Repository) GetActiveCompanyAlias(ctx context.Context, companyID string) (CompanyAliasProjection, bool, error) {
	var projection CompanyAliasProjection
	var effectiveFrom time.Time
	err := r.db.QueryRowContext(ctx, `
SELECT alias_company_id, canonical_company_id, alias_version, effective_from
FROM recruiting_company_aliases
WHERE alias_company_id = ? AND active_alias_key = ?`, companyID, companyID).
		Scan(&projection.AliasCompanyID, &projection.CanonicalCompanyID, &projection.AliasVersion, &effectiveFrom)
	if errors.Is(err, sql.ErrNoRows) {
		return CompanyAliasProjection{}, false, nil
	}
	if err != nil {
		return CompanyAliasProjection{}, false, fmt.Errorf("get active company alias: %w", err)
	}
	projection.EffectiveFrom = effectiveFrom.UTC().Format(time.RFC3339Nano)
	return projection, true, nil
}

// InspectCompanyMerge takes the human-visible impact snapshot. Source, Job,
// and Work counts are deliberately advisory: confirmation changes only the
// canonical/alias relation and never rewrites those historical aggregates.
// Company and mapping versions are the actual confirmation fence.
func (r *Repository) InspectCompanyMerge(ctx context.Context, request CompanyMergePreviewRequest) (model.CompanyMergePreview, error) {
	if request.ObservedAt.IsZero() || len(request.AliasCompanyIDs) == 0 || len(request.AliasCompanyIDs) > 500 {
		return model.CompanyMergePreview{}, fmt.Errorf("merge preview requires time and 1 through 500 aliases")
	}
	ids := append([]string{request.CanonicalCompanyID}, request.AliasCompanyIDs...)
	sort.Strings(ids)
	if len(request.ExpectedVersions) != len(ids) {
		return model.CompanyMergePreview{}, fmt.Errorf("expected_versions must cover exactly the canonical and alias companies")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return model.CompanyMergePreview{}, fmt.Errorf("begin company merge inspection: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	companies := make(map[string]model.Company, len(ids))
	for index, id := range ids {
		if id == "" || (index > 0 && id == ids[index-1]) {
			return model.CompanyMergePreview{}, fmt.Errorf("merge companies must be non-empty and unique")
		}
		company, err := readCompanyForMerge(ctx, tx, id, false)
		if err != nil {
			return model.CompanyMergePreview{}, err
		}
		expected, exists := request.ExpectedVersions[id]
		if !exists || expected == 0 || expected != company.Version {
			return model.CompanyMergePreview{}, &model.VersionConflictError{Expected: expected, Actual: company.Version}
		}
		companies[id] = company
	}
	if err := validateCanonicalForMerge(ctx, tx, request.CanonicalCompanyID); err != nil {
		return model.CompanyMergePreview{}, err
	}
	members := make([]model.CompanyMergeMember, 0, len(request.AliasCompanyIDs))
	for _, aliasID := range request.AliasCompanyIDs {
		aliasVersion, activeCanonical, active, err := latestCompanyAlias(ctx, tx, aliasID, false)
		if err != nil {
			return model.CompanyMergePreview{}, err
		}
		switch request.Action {
		case model.CompanyMergeApply:
			if active {
				return model.CompanyMergePreview{}, fmt.Errorf("%w: company %s is already an active alias of %s", ErrBusinessKeyExists, aliasID, activeCanonical)
			}
			var childAliases uint64
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_company_aliases WHERE canonical_company_id = ? AND active_alias_key IS NOT NULL`, aliasID).Scan(&childAliases); err != nil {
				return model.CompanyMergePreview{}, fmt.Errorf("inspect alias chain: %w", err)
			}
			if childAliases != 0 {
				return model.CompanyMergePreview{}, fmt.Errorf("%w: company %s is canonical for active aliases", ErrBusinessKeyExists, aliasID)
			}
		case model.CompanyMergeReverse:
			if !active || activeCanonical != request.CanonicalCompanyID {
				return model.CompanyMergePreview{}, fmt.Errorf("%w: company %s is not an active alias of %s", ErrBusinessKeyExists, aliasID, request.CanonicalCompanyID)
			}
		default:
			return model.CompanyMergePreview{}, fmt.Errorf("merge action must be merge or reverse")
		}
		sources, jobs, works, err := companyMergeImpact(ctx, tx, aliasID)
		if err != nil {
			return model.CompanyMergePreview{}, err
		}
		members = append(members, model.CompanyMergeMember{CompanyID: aliasID, CompanyVersion: companies[aliasID].Version,
			AliasVersion: aliasVersion, SourceCount: sources, JobCount: jobs, OpenWorkCount: works})
	}
	preview, err := model.NewCompanyMergePreview(request.MergePreviewID, request.Action, request.CanonicalCompanyID,
		companies[request.CanonicalCompanyID].Version, members, request.RequestedBy, request.Reason,
		request.ObservedAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return model.CompanyMergePreview{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.CompanyMergePreview{}, fmt.Errorf("finish company merge inspection: %w", err)
	}
	return preview, nil
}

func (r *Repository) ApplyCompanyMergePreviewCommand(ctx context.Context, preview model.CompanyMergePreview,
	receipt model.CommandReceipt, event model.EventIntent, businessAt time.Time) (CommandResult, error) {
	if preview.Version != 1 || preview.Status != model.CompanyMergePreviewReady || receipt.CommandID == "" ||
		event.AggregateType != "company_merge_preview" || event.AggregateID != preview.MergePreviewID ||
		event.AggregateVersion != preview.Version || event.CauseCommandID != receipt.CommandID {
		return CommandResult{}, fmt.Errorf("company merge preview command facts are inconsistent")
	}
	rebuilt, err := model.NewCompanyMergePreview(preview.MergePreviewID, preview.Action, preview.CanonicalCompanyID,
		preview.CanonicalCompanyVersion, preview.Members, preview.RequestedBy, preview.Reason, preview.CreatedAt)
	if err != nil {
		return CommandResult{}, fmt.Errorf("validate company merge preview: %w", err)
	}
	if rebuilt.PreviewHash != preview.PreviewHash {
		return CommandResult{}, fmt.Errorf("company merge preview is not a canonical inspected snapshot")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin company merge preview command: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	if err := lockAndValidateCompanyMergePreview(ctx, tx, preview); err != nil {
		return CommandResult{}, err
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	state, _ := json.Marshal(preview)
	if _, err := tx.ExecContext(ctx, `
INSERT INTO recruiting_company_merge_previews(
  merge_preview_id, canonical_company_id, merge_action, preview_hash, merge_status,
  version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, preview.MergePreviewID, preview.CanonicalCompanyID, preview.Action,
		preview.PreviewHash, preview.Status, preview.Version, state, businessAt.UTC(), businessAt.UTC()); err != nil {
		var mysqlError *mysql.MySQLError
		if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
			return CommandResult{}, fmt.Errorf("%w: merge_preview_id", ErrBusinessKeyExists)
		}
		return CommandResult{}, fmt.Errorf("insert company merge preview: %w", err)
	}
	eventAt, err := time.Parse(time.RFC3339Nano, event.BusinessAt)
	if err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit company merge preview: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func (r *Repository) ApplyCompanyMergeConfirmCommand(ctx context.Context, previewID string, expectedVersion uint64,
	previewHash string, receipt model.CommandReceipt, eventID, requestedBy, reason string,
	businessAt time.Time) (CommandResult, error) {
	if previewID == "" || expectedVersion == 0 || previewHash == "" || receipt.CommandID == "" || eventID == "" ||
		requestedBy == "" || reason == "" || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("company merge confirmation identity, preview fence, operator, reason, and time are required")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin company merge confirmation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	preview, err := readCompanyMergePreview(ctx, tx, previewID, true)
	if err != nil {
		return CommandResult{}, err
	}
	confirmed, err := preview.Confirm(expectedVersion, previewHash, businessAt.UTC().Format(time.RFC3339Nano))
	if err != nil {
		return CommandResult{}, err
	}
	if err := lockAndValidateCompanyMergePreview(ctx, tx, preview); err != nil {
		return CommandResult{}, err
	}
	aliases := make([]CompanyAliasProjection, 0, len(preview.Members))
	for _, member := range preview.Members {
		switch preview.Action {
		case model.CompanyMergeApply:
			nextVersion := member.AliasVersion + 1
			if _, err := tx.ExecContext(ctx, `
INSERT INTO recruiting_company_aliases(
  alias_company_id, canonical_company_id, merge_preview_id, ended_by_merge_preview_id, alias_version, active_alias_key,
  effective_from, effective_until, created_at, updated_at
) VALUES (?, ?, ?, NULL, ?, ?, ?, NULL, ?, ?)`, member.CompanyID, preview.CanonicalCompanyID, preview.MergePreviewID,
				nextVersion, member.CompanyID, businessAt.UTC(), businessAt.UTC(), businessAt.UTC()); err != nil {
				return CommandResult{}, fmt.Errorf("activate company alias %s: %w", member.CompanyID, err)
			}
			aliases = append(aliases, CompanyAliasProjection{AliasCompanyID: member.CompanyID,
				CanonicalCompanyID: preview.CanonicalCompanyID, AliasVersion: nextVersion,
				EffectiveFrom: businessAt.UTC().Format(time.RFC3339Nano)})
		case model.CompanyMergeReverse:
			var effectiveFrom time.Time
			if err := tx.QueryRowContext(ctx, `SELECT effective_from FROM recruiting_company_aliases
WHERE alias_company_id = ? AND alias_version = ? AND canonical_company_id = ? AND active_alias_key = ?`,
				member.CompanyID, member.AliasVersion, preview.CanonicalCompanyID, member.CompanyID).Scan(&effectiveFrom); err != nil {
				return CommandResult{}, fmt.Errorf("read company alias interval %s: %w", member.CompanyID, err)
			}
			result, err := tx.ExecContext(ctx, `
UPDATE recruiting_company_aliases
SET active_alias_key = NULL, effective_until = ?, ended_by_merge_preview_id = ?, updated_at = ?
WHERE alias_company_id = ? AND alias_version = ? AND canonical_company_id = ? AND active_alias_key = ?`,
				businessAt.UTC(), preview.MergePreviewID, businessAt.UTC(), member.CompanyID, member.AliasVersion, preview.CanonicalCompanyID, member.CompanyID)
			if err != nil {
				return CommandResult{}, fmt.Errorf("close company alias %s: %w", member.CompanyID, err)
			}
			changed, _ := result.RowsAffected()
			if changed != 1 {
				return CommandResult{}, &model.VersionConflictError{Expected: member.AliasVersion, Actual: member.AliasVersion + 1}
			}
			aliases = append(aliases, CompanyAliasProjection{AliasCompanyID: member.CompanyID,
				CanonicalCompanyID: preview.CanonicalCompanyID, AliasVersion: member.AliasVersion,
				EffectiveFrom: effectiveFrom.UTC().Format(time.RFC3339Nano), EffectiveUntil: businessAt.UTC().Format(time.RFC3339Nano)})
		}
	}
	outcome := CompanyMergeConfirmOutcome{Preview: confirmed, Aliases: aliases}
	response, err := patchCompanyMergeReceiptResponse(receipt.Response, outcome)
	if err != nil {
		return CommandResult{}, err
	}
	receipt.Response = response
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	state, _ := json.Marshal(confirmed)
	result, err := tx.ExecContext(ctx, `
UPDATE recruiting_company_merge_previews
SET merge_status = ?, version = ?, state_json = ?, updated_at = ?
WHERE merge_preview_id = ? AND version = ?`, confirmed.Status, confirmed.Version, state, businessAt.UTC(), preview.MergePreviewID, preview.Version)
	if err != nil {
		return CommandResult{}, fmt.Errorf("update company merge preview: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed != 1 {
		return CommandResult{}, &model.VersionConflictError{Expected: preview.Version, Actual: confirmed.Version}
	}
	audit, _ := json.Marshal(map[string]any{"requested_by": requestedBy, "reason": reason, "action": preview.Action,
		"canonical_company_id": preview.CanonicalCompanyID, "alias_count": len(preview.Members), "preview_hash": preview.PreviewHash})
	eventKind := "company.merge.confirmed"
	if preview.Action == model.CompanyMergeReverse {
		eventKind = "company.merge.reversed"
	}
	event, err := model.NewEventIntent(eventID, eventKind, "company_merge_preview", preview.MergePreviewID,
		confirmed.Version, businessAt.UTC().Format(time.RFC3339Nano), receipt.CommandID, audit)
	if err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, event, businessAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit company merge confirmation: %w", err)
	}
	return CommandResult{Response: response}, nil
}

func patchCompanyMergeReceiptResponse(base json.RawMessage, outcome CompanyMergeConfirmOutcome) (json.RawMessage, error) {
	var response map[string]any
	if err := json.Unmarshal(base, &response); err != nil {
		return nil, fmt.Errorf("decode company merge response skeleton: %w", err)
	}
	response["preview"], response["aliases"] = outcome.Preview, outcome.Aliases
	return json.Marshal(response)
}

func lockAndValidateCompanyMergePreview(ctx context.Context, tx *sql.Tx, preview model.CompanyMergePreview) error {
	ids := make([]string, 0, len(preview.Members)+1)
	ids = append(ids, preview.CanonicalCompanyID)
	for _, member := range preview.Members {
		ids = append(ids, member.CompanyID)
	}
	sort.Strings(ids)
	for _, id := range ids {
		company, err := readCompanyForMerge(ctx, tx, id, true)
		if err != nil {
			return err
		}
		expected := preview.CanonicalCompanyVersion
		if id != preview.CanonicalCompanyID {
			for _, member := range preview.Members {
				if member.CompanyID == id {
					expected = member.CompanyVersion
					break
				}
			}
		}
		if company.Version != expected {
			return &model.VersionConflictError{Expected: expected, Actual: company.Version}
		}
	}
	if err := validateCanonicalForMerge(ctx, tx, preview.CanonicalCompanyID); err != nil {
		return err
	}
	for _, member := range preview.Members {
		latest, canonical, active, err := latestCompanyAlias(ctx, tx, member.CompanyID, true)
		if err != nil {
			return err
		}
		if latest != member.AliasVersion {
			return &model.VersionConflictError{Expected: member.AliasVersion, Actual: latest}
		}
		if preview.Action == model.CompanyMergeApply && active {
			return fmt.Errorf("%w: company %s is already an active alias", ErrBusinessKeyExists, member.CompanyID)
		}
		if preview.Action == model.CompanyMergeApply {
			var childAliases uint64
			if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_company_aliases WHERE canonical_company_id = ? AND active_alias_key IS NOT NULL`, member.CompanyID).Scan(&childAliases); err != nil {
				return fmt.Errorf("inspect alias chain: %w", err)
			}
			if childAliases != 0 {
				return fmt.Errorf("%w: company %s is canonical for active aliases", ErrBusinessKeyExists, member.CompanyID)
			}
		}
		if preview.Action == model.CompanyMergeReverse && (!active || canonical != preview.CanonicalCompanyID) {
			return fmt.Errorf("%w: company %s alias relation changed", ErrBusinessKeyExists, member.CompanyID)
		}
	}
	return nil
}

func validateCanonicalForMerge(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, canonicalID string) error {
	var count uint64
	if err := query.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_company_aliases WHERE alias_company_id = ? AND active_alias_key IS NOT NULL`, canonicalID).Scan(&count); err != nil {
		return fmt.Errorf("inspect canonical company alias status: %w", err)
	}
	if count != 0 {
		return fmt.Errorf("%w: canonical company %s is itself an active alias", ErrBusinessKeyExists, canonicalID)
	}
	return nil
}

func latestCompanyAlias(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, aliasID string, lock bool) (uint64, string, bool, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var version uint64
	var canonical string
	var activeKey sql.NullString
	err := query.QueryRowContext(ctx, `SELECT alias_version, canonical_company_id, active_alias_key
FROM recruiting_company_aliases WHERE alias_company_id = ? ORDER BY alias_version DESC LIMIT 1`+suffix, aliasID).
		Scan(&version, &canonical, &activeKey)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, fmt.Errorf("read company alias %s: %w", aliasID, err)
	}
	return version, canonical, activeKey.Valid, nil
}

func readCompanyForMerge(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, companyID string, lock bool) (model.Company, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var state []byte
	err := query.QueryRowContext(ctx, "SELECT state_json FROM recruiting_companies WHERE company_id = ?"+suffix, companyID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Company{}, ErrNotFound
	}
	if err != nil {
		return model.Company{}, fmt.Errorf("read merge company: %w", err)
	}
	var company model.Company
	if err := json.Unmarshal(state, &company); err != nil {
		return model.Company{}, fmt.Errorf("decode merge company: %w", err)
	}
	return company, nil
}

func companyMergeImpact(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, companyID string) (uint64, uint64, uint64, error) {
	var sources, jobs, works uint64
	err := query.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM recruiting_sources WHERE company_id = ?),
  (SELECT COUNT(*) FROM recruiting_source_jobs j JOIN recruiting_sources s ON s.source_id = j.source_id WHERE s.company_id = ?),
  (SELECT COUNT(*) FROM recruiting_works w WHERE w.status IN ('open','waiting_retry','running','waiting_human','paused') AND (
    (w.target_type = 'company' AND w.target_id = ?) OR
    (w.target_type = 'source' AND EXISTS (SELECT 1 FROM recruiting_sources s WHERE s.source_id = w.target_id AND s.company_id = ?)) OR
    (w.target_type = 'job' AND EXISTS (SELECT 1 FROM recruiting_source_jobs j JOIN recruiting_sources s ON s.source_id = j.source_id WHERE j.job_id = w.target_id AND s.company_id = ?))
  ))`, companyID, companyID, companyID, companyID, companyID).Scan(&sources, &jobs, &works)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("read company merge impact: %w", err)
	}
	return sources, jobs, works, nil
}

func readCompanyMergePreview(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, previewID string, lock bool) (model.CompanyMergePreview, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var state []byte
	err := query.QueryRowContext(ctx, "SELECT state_json FROM recruiting_company_merge_previews WHERE merge_preview_id = ?"+suffix, previewID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.CompanyMergePreview{}, ErrNotFound
	}
	if err != nil {
		return model.CompanyMergePreview{}, fmt.Errorf("read company merge preview: %w", err)
	}
	var preview model.CompanyMergePreview
	if err := json.Unmarshal(state, &preview); err != nil {
		return model.CompanyMergePreview{}, fmt.Errorf("decode company merge preview: %w", err)
	}
	return preview, nil
}
