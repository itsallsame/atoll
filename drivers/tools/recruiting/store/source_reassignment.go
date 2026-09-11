package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type SourceReassignmentPreviewRequest struct {
	PreviewID              string
	Relation               model.SourceLineageRelation
	SourceID               string
	ExpectedSourceVersion  uint64
	TargetCompanyID        string
	ExpectedCompanyVersion uint64
	NewSourceID            string
	DiscoveryGeneration    uint64
	RequestedBy            string
	Reason                 string
	ObservedAt             time.Time
}

type SourceReassignmentConfirmOutcome struct {
	Preview          model.SourceReassignmentPreview `json:"preview"`
	PreviousSource   model.RecruitmentSource         `json:"previous_source"`
	NewSource        model.RecruitmentSource         `json:"new_source"`
	Lineage          model.SourceLineage             `json:"lineage"`
	ScopeOperationID string                          `json:"scope_control_operation_id,omitempty"`
}

func (r *Repository) InspectSourceReassignment(ctx context.Context,
	request SourceReassignmentPreviewRequest) (model.SourceReassignmentPreview, error) {
	if strings.TrimSpace(request.PreviewID) == "" || strings.TrimSpace(request.SourceID) == "" ||
		strings.TrimSpace(request.TargetCompanyID) == "" || strings.TrimSpace(request.NewSourceID) == "" ||
		request.ExpectedSourceVersion == 0 || request.ExpectedCompanyVersion == 0 || request.DiscoveryGeneration == 0 ||
		strings.TrimSpace(request.RequestedBy) == "" || strings.TrimSpace(request.Reason) == "" || request.ObservedAt.IsZero() {
		return model.SourceReassignmentPreview{}, fmt.Errorf("Source reassignment preview requires identities, versions, generation, operator, reason, and time")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return model.SourceReassignmentPreview{}, fmt.Errorf("begin Source reassignment inspection: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	source, target, checkpointVersion, jobCount, openWorkCount, err := inspectSourceReassignmentFacts(ctx, tx,
		request.SourceID, request.TargetCompanyID, request.NewSourceID, false)
	if err != nil {
		return model.SourceReassignmentPreview{}, err
	}
	if source.Version != request.ExpectedSourceVersion {
		return model.SourceReassignmentPreview{}, &model.VersionConflictError{Expected: request.ExpectedSourceVersion, Actual: source.Version}
	}
	if target.Version != request.ExpectedCompanyVersion {
		return model.SourceReassignmentPreview{}, &model.VersionConflictError{Expected: request.ExpectedCompanyVersion, Actual: target.Version}
	}
	preview, err := model.NewSourceReassignmentPreview(request.PreviewID, request.Relation, source, target,
		request.NewSourceID, request.DiscoveryGeneration, checkpointVersion, jobCount, openWorkCount,
		request.RequestedBy, request.Reason, request.ObservedAt)
	if err != nil {
		return model.SourceReassignmentPreview{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.SourceReassignmentPreview{}, fmt.Errorf("finish Source reassignment inspection: %w", err)
	}
	return preview, nil
}

func (r *Repository) ApplySourceReassignmentPreviewCommand(ctx context.Context, preview model.SourceReassignmentPreview,
	receipt model.CommandReceipt, event model.EventIntent, businessAt time.Time) (CommandResult, error) {
	if preview.Version != 1 || preview.Status != model.SourceReassignmentPreviewReady || receipt.CommandID == "" ||
		event.AggregateType != "source_reassignment_preview" || event.AggregateID != preview.PreviewID ||
		event.AggregateVersion != preview.Version || event.CauseCommandID != receipt.CommandID || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("Source reassignment preview command facts are inconsistent")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin Source reassignment preview: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	if err := lockAndValidateSourceReassignmentPreview(ctx, tx, preview); err != nil {
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
	if _, err := tx.ExecContext(ctx, `INSERT INTO recruiting_source_reassignment_previews(
  preview_id, source_id, target_company_id, new_source_id, relation_kind, preview_hash,
  preview_status, version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, preview.PreviewID, preview.SourceID, preview.TargetCompanyID,
		preview.NewSourceID, preview.Relation, preview.PreviewHash, preview.Status, preview.Version, state,
		businessAt.UTC(), businessAt.UTC()); err != nil {
		if isDuplicateKey(err) {
			return CommandResult{}, fmt.Errorf("%w: Source reassignment preview", ErrBusinessKeyExists)
		}
		return CommandResult{}, fmt.Errorf("insert Source reassignment preview: %w", err)
	}
	eventAt, err := time.Parse(time.RFC3339Nano, event.BusinessAt)
	if err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit Source reassignment preview: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func (r *Repository) ApplySourceReassignmentConfirmCommand(ctx context.Context, previewID string,
	expectedVersion uint64, previewHash string, receipt model.CommandReceipt, lineageID, eventPrefix, requestedBy,
	reason, scopeOperationID string, businessAt time.Time) (CommandResult, error) {
	if strings.TrimSpace(previewID) == "" || expectedVersion == 0 || strings.TrimSpace(previewHash) == "" ||
		receipt.CommandID == "" || strings.TrimSpace(lineageID) == "" || strings.TrimSpace(eventPrefix) == "" ||
		strings.TrimSpace(requestedBy) == "" || strings.TrimSpace(reason) == "" || len(strings.TrimSpace(reason)) > 2048 || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("Source reassignment confirmation requires preview fence, command, audit identity, reason, and time")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin Source reassignment confirmation: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	preview, err := getSourceReassignmentPreviewWith(ctx, tx, previewID, true)
	if err != nil {
		return CommandResult{}, err
	}
	confirmed, err := preview.Confirm(expectedVersion, previewHash, businessAt)
	if err != nil {
		return CommandResult{}, err
	}
	source, target, checkpointVersion, _, _, err := inspectSourceReassignmentFacts(ctx, tx,
		preview.SourceID, preview.TargetCompanyID, preview.NewSourceID, true)
	if err != nil {
		return CommandResult{}, err
	}
	if source.Version != preview.SourceVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: preview.SourceVersion, Actual: source.Version}
	}
	if target.Version != preview.TargetCompanyVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: preview.TargetCompanyVersion, Actual: target.Version}
	}
	if source.CompanyID != preview.SourceCompanyID || source.ActiveEndpoint == nil ||
		!reflect.DeepEqual(*source.ActiveEndpoint, preview.Endpoint) || checkpointVersion != preview.CheckpointVersion ||
		(source.ListingAssignment == nil) != (preview.ListingAssignmentVersion == 0) ||
		(source.ListingAssignment != nil && source.ListingAssignment.AssignmentVersion != preview.ListingAssignmentVersion) {
		return CommandResult{}, fmt.Errorf("Source reassignment input changed after preview")
	}
	newSource, err := model.NewRecruitmentSource(preview.NewSourceID, preview.TargetCompanyID, preview.Endpoint.URL,
		preview.Endpoint.Category, preview.DiscoveryGeneration)
	if err != nil {
		return CommandResult{}, err
	}
	previousSource := source
	if preview.Relation == model.SourceSupersedes {
		if strings.TrimSpace(scopeOperationID) == "" {
			return CommandResult{}, fmt.Errorf("superseding Source reassignment requires scope operation identity")
		}
		previousSource, err = source.Archive(source.Version)
		if err != nil {
			return CommandResult{}, err
		}
	} else if strings.TrimSpace(scopeOperationID) != "" {
		return CommandResult{}, fmt.Errorf("split_from Source reassignment cannot cancel the retained Source")
	}
	lineage, err := model.NewSourceLineage(strings.TrimSpace(lineageID), confirmed, requestedBy, reason)
	if err != nil {
		return CommandResult{}, err
	}
	outcome := SourceReassignmentConfirmOutcome{Preview: confirmed, PreviousSource: previousSource,
		NewSource: newSource, Lineage: lineage, ScopeOperationID: strings.TrimSpace(scopeOperationID)}
	response, err := patchSourceReassignmentReceiptResponse(receipt.Response, outcome)
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
	if preview.Relation == model.SourceSupersedes {
		if err := updateSourceInTx(ctx, tx, source.Version, previousSource, businessAt); err != nil {
			return CommandResult{}, err
		}
		if err := createPauseScopeControlOperationTx(ctx, tx, scopeOperationID, "source", source.SourceID,
			model.PauseCancel, previousSource.Version, previousSource.ConfigurationVersion, previousSource.ControlEpoch,
			previousSource.ExecutionFence, businessAt); err != nil {
			return CommandResult{}, err
		}
	}
	endpoint, origin, err := sourceStorageIdentity(newSource)
	if err != nil {
		return CommandResult{}, err
	}
	if err := insertSourceWith(ctx, tx, newSource, endpoint, origin, businessAt); err != nil {
		return CommandResult{}, err
	}
	lineageState, err := json.Marshal(lineage)
	if err != nil {
		return CommandResult{}, fmt.Errorf("encode Source lineage: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO recruiting_source_lineage(
  lineage_id, from_source_id, to_source_id, from_company_id, to_company_id, relation_kind,
  canonical_source_key, preview_id, state_json, effective_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, lineage.LineageID, lineage.FromSourceID, lineage.ToSourceID,
		lineage.FromCompanyID, lineage.ToCompanyID, lineage.Relation, lineage.CanonicalSourceKey,
		lineage.ReassignmentPreview, lineageState, businessAt.UTC()); err != nil {
		return CommandResult{}, fmt.Errorf("insert Source lineage: %w", err)
	}
	previewState, err := json.Marshal(confirmed)
	if err != nil {
		return CommandResult{}, fmt.Errorf("encode confirmed Source reassignment preview: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_source_reassignment_previews
SET preview_status = ?, version = ?, state_json = ?, updated_at = ? WHERE preview_id = ? AND version = ?`,
		confirmed.Status, confirmed.Version, previewState, businessAt.UTC(), preview.PreviewID, preview.Version)
	if err != nil {
		return CommandResult{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return CommandResult{}, ErrProgressConflict
	}
	audit, err := json.Marshal(map[string]any{"requested_by": requestedBy, "reason": reason,
		"relation": preview.Relation, "from_source_id": source.SourceID, "to_source_id": newSource.SourceID,
		"preview_hash": preview.PreviewHash})
	if err != nil {
		return CommandResult{}, fmt.Errorf("encode Source reassignment audit: %w", err)
	}
	events := []model.EventIntent{}
	confirmedEvent, err := model.NewEventIntent(eventPrefix+"-confirmed", "source.reassignment.confirmed",
		"source_reassignment_preview", confirmed.PreviewID, confirmed.Version, businessAt.Format(time.RFC3339Nano),
		receipt.CommandID, audit)
	if err != nil {
		return CommandResult{}, fmt.Errorf("create Source reassignment confirmation event: %w", err)
	}
	createdEvent, err := model.NewEventIntent(eventPrefix+"-created", "source.created_from_lineage", "source",
		newSource.SourceID, newSource.Version, businessAt.Format(time.RFC3339Nano), receipt.CommandID, audit)
	if err != nil {
		return CommandResult{}, fmt.Errorf("create reassigned Source event: %w", err)
	}
	events = append(events, confirmedEvent, createdEvent)
	if preview.Relation == model.SourceSupersedes {
		archivedEvent, eventErr := model.NewEventIntent(eventPrefix+"-superseded", "source.superseded", "source",
			previousSource.SourceID, previousSource.Version, businessAt.Format(time.RFC3339Nano), receipt.CommandID, audit)
		if eventErr != nil {
			return CommandResult{}, fmt.Errorf("create superseded Source event: %w", eventErr)
		}
		events = append(events, archivedEvent)
	}
	for _, event := range events {
		if err := appendEventIntent(ctx, tx, event, businessAt, businessAt); err != nil {
			return CommandResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit Source reassignment confirmation: %w", err)
	}
	return CommandResult{Response: response}, nil
}

func (r *Repository) GetSourceReassignmentPreview(ctx context.Context, previewID string) (model.SourceReassignmentPreview, error) {
	return getSourceReassignmentPreviewWith(ctx, r.db, previewID, false)
}

func (r *Repository) GetSourceLineage(ctx context.Context, lineageID string) (model.SourceLineage, error) {
	var state []byte
	err := r.db.QueryRowContext(ctx, `SELECT state_json FROM recruiting_source_lineage WHERE lineage_id = ?`,
		strings.TrimSpace(lineageID)).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.SourceLineage{}, ErrNotFound
	}
	if err != nil {
		return model.SourceLineage{}, fmt.Errorf("get Source lineage: %w", err)
	}
	var lineage model.SourceLineage
	if err := json.Unmarshal(state, &lineage); err != nil {
		return model.SourceLineage{}, err
	}
	return lineage, nil
}

func inspectSourceReassignmentFacts(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, sourceID, targetCompanyID, newSourceID string, lock bool) (model.RecruitmentSource, model.Company, uint64, uint64, uint64, error) {
	source, err := getSourceForReassignment(ctx, query, sourceID, lock)
	if err != nil {
		return model.RecruitmentSource{}, model.Company{}, 0, 0, 0, err
	}
	targetCompanyID = strings.TrimSpace(targetCompanyID)
	if source.CompanyID == targetCompanyID {
		return model.RecruitmentSource{}, model.Company{}, 0, 0, 0,
			fmt.Errorf("Source reassignment requires a distinct target company")
	}
	companies := make(map[string]model.Company, 2)
	for _, companyID := range sortedReassignmentCompanies(source.CompanyID, targetCompanyID) {
		company, companyErr := getCompanyForReassignment(ctx, query, companyID, lock)
		if companyErr != nil {
			return model.RecruitmentSource{}, model.Company{}, 0, 0, 0, companyErr
		}
		companies[companyID] = company
	}
	fromCompany, target := companies[source.CompanyID], companies[targetCompanyID]
	if fromCompany.ControlStatus == model.ControlArchived || target.ControlStatus == model.ControlArchived ||
		source.ControlStatus == model.ControlArchived || source.ActiveEndpoint == nil {
		return model.RecruitmentSource{}, model.Company{}, 0, 0, 0, fmt.Errorf("Source reassignment requires active companies and a non-archived Source with active endpoint")
	}
	for _, companyID := range []string{source.CompanyID, target.CompanyID} {
		var aliases uint64
		if err := query.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_company_aliases
WHERE alias_company_id = ? AND active_alias_key IS NOT NULL`, companyID).Scan(&aliases); err != nil {
			return model.RecruitmentSource{}, model.Company{}, 0, 0, 0, err
		}
		if aliases != 0 {
			return model.RecruitmentSource{}, model.Company{}, 0, 0, 0,
				fmt.Errorf("%w: Source reassignment company %s is an active alias", ErrBusinessKeyExists, companyID)
		}
	}
	var identityConflicts uint64
	if err := query.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_sources
WHERE source_id = ? OR (company_id = ? AND canonical_source_key = ?)`, newSourceID, target.CompanyID,
		source.ActiveEndpoint.CanonicalKey).Scan(&identityConflicts); err != nil {
		return model.RecruitmentSource{}, model.Company{}, 0, 0, 0, err
	}
	if identityConflicts != 0 {
		return model.RecruitmentSource{}, model.Company{}, 0, 0, 0,
			fmt.Errorf("%w: target Source identity already exists", ErrBusinessKeyExists)
	}
	var checkpointVersion, jobCount, openWorkCount uint64
	if err := query.QueryRowContext(ctx, `SELECT
  COALESCE((SELECT checkpoint_version FROM recruiting_checkpoints WHERE source_id = ?), 0),
  (SELECT COUNT(*) FROM recruiting_source_jobs WHERE source_id = ?),
  (SELECT COUNT(*) FROM recruiting_works WHERE source_id = ? AND status IN ('open','waiting_retry','running','waiting_human','paused'))`,
		source.SourceID, source.SourceID, source.SourceID).Scan(&checkpointVersion, &jobCount, &openWorkCount); err != nil {
		return model.RecruitmentSource{}, model.Company{}, 0, 0, 0, err
	}
	return source, target, checkpointVersion, jobCount, openWorkCount, nil
}

func lockAndValidateSourceReassignmentPreview(ctx context.Context, tx *sql.Tx, preview model.SourceReassignmentPreview) error {
	source, target, checkpointVersion, _, _, err := inspectSourceReassignmentFacts(ctx, tx, preview.SourceID,
		preview.TargetCompanyID, preview.NewSourceID, true)
	if err != nil {
		return err
	}
	if source.Version != preview.SourceVersion {
		return &model.VersionConflictError{Expected: preview.SourceVersion, Actual: source.Version}
	}
	if target.Version != preview.TargetCompanyVersion {
		return &model.VersionConflictError{Expected: preview.TargetCompanyVersion, Actual: target.Version}
	}
	if source.CompanyID != preview.SourceCompanyID || source.ActiveEndpoint == nil ||
		!reflect.DeepEqual(*source.ActiveEndpoint, preview.Endpoint) || checkpointVersion != preview.CheckpointVersion ||
		(source.ListingAssignment == nil) != (preview.ListingAssignmentVersion == 0) ||
		(source.ListingAssignment != nil && source.ListingAssignment.AssignmentVersion != preview.ListingAssignmentVersion) {
		return fmt.Errorf("Source reassignment preview no longer matches current facts")
	}
	rebuilt, err := model.NewSourceReassignmentPreview(preview.PreviewID, preview.Relation, source, target,
		preview.NewSourceID, preview.DiscoveryGeneration, preview.CheckpointVersion, preview.JobCount, preview.OpenWorkCount,
		preview.RequestedBy, preview.Reason, mustParseReassignmentTime(preview.CreatedAt))
	if err != nil || rebuilt.PreviewHash != preview.PreviewHash {
		return fmt.Errorf("Source reassignment preview is not canonical")
	}
	return nil
}

func getSourceForReassignment(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, sourceID string, lock bool) (model.RecruitmentSource, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var state []byte
	if err := query.QueryRowContext(ctx, `SELECT state_json FROM recruiting_sources WHERE source_id = ?`+suffix,
		strings.TrimSpace(sourceID)).Scan(&state); errors.Is(err, sql.ErrNoRows) {
		return model.RecruitmentSource{}, ErrNotFound
	} else if err != nil {
		return model.RecruitmentSource{}, err
	}
	var source model.RecruitmentSource
	if err := json.Unmarshal(state, &source); err != nil {
		return model.RecruitmentSource{}, err
	}
	return source, nil
}

func getCompanyForReassignment(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, companyID string, lock bool) (model.Company, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var state []byte
	if err := query.QueryRowContext(ctx, `SELECT state_json FROM recruiting_companies WHERE company_id = ?`+suffix,
		strings.TrimSpace(companyID)).Scan(&state); errors.Is(err, sql.ErrNoRows) {
		return model.Company{}, ErrNotFound
	} else if err != nil {
		return model.Company{}, err
	}
	var company model.Company
	if err := json.Unmarshal(state, &company); err != nil {
		return model.Company{}, err
	}
	return company, nil
}

func getSourceReassignmentPreviewWith(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, previewID string, lock bool) (model.SourceReassignmentPreview, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var state []byte
	err := query.QueryRowContext(ctx, `SELECT state_json FROM recruiting_source_reassignment_previews
WHERE preview_id = ?`+suffix, strings.TrimSpace(previewID)).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.SourceReassignmentPreview{}, ErrNotFound
	}
	if err != nil {
		return model.SourceReassignmentPreview{}, err
	}
	var preview model.SourceReassignmentPreview
	if err := json.Unmarshal(state, &preview); err != nil {
		return model.SourceReassignmentPreview{}, err
	}
	return preview, nil
}

func patchSourceReassignmentReceiptResponse(base json.RawMessage,
	outcome SourceReassignmentConfirmOutcome) (json.RawMessage, error) {
	var response map[string]any
	if err := json.Unmarshal(base, &response); err != nil {
		return nil, fmt.Errorf("decode Source reassignment response skeleton: %w", err)
	}
	response["preview"], response["previous_source"], response["new_source"] = outcome.Preview, outcome.PreviousSource, outcome.NewSource
	response["lineage"] = outcome.Lineage
	if outcome.ScopeOperationID != "" {
		response["scope_control_operation_id"] = outcome.ScopeOperationID
	}
	return json.Marshal(response)
}

func mustParseReassignmentTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

// Keep Company row acquisition order deterministic for future batch extension.
func sortedReassignmentCompanies(from, to string) []string {
	values := []string{from, to}
	sort.Strings(values)
	return values
}
