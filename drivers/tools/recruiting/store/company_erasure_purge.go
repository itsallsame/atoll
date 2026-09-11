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

var companyErasurePurgePhases = []string{
	"validation_artifact_pages", "profile_repair_sessions",
	"baseline_detail_items", "baseline_staging", "listing_page_progress", "budget_permits", "artifacts",
	"backfill_outputs", "backfill_items", "backfills", "repair_affected_works",
	"scope_catchups", "scope_control_roots", "recipe_validation_runs", "listing_runs",
	"source_discovery_candidates", "source_discoveries", "baseline_generations", "attempts",
	"listing_observations", "override_heads", "override_versions", "job_detail_versions",
	"recipe_proposals", "recipe_rollout_items", "endpoint_activations", "endpoint_changes",
	"assignment_versions", "assignments", "profile_binding_history", "profile_bindings", "checkpoints",
	"source_occurrences", "source_lineage", "source_reassignment_previews", "company_import_items",
	"website_heads", "website_revision_links", "website_revisions", "repair_validation_links",
	"work_blocked_links", "scope_control_operations", "works", "source_jobs", "company_aliases",
	"company_merge_previews", "sources", "company",
}

type CompanyErasurePurgeProgress struct {
	Erasure  model.CompanyErasure `json:"erasure"`
	Phase    string               `json:"phase"`
	Affected int64                `json:"affected"`
	Advanced bool                 `json:"advanced"`
}

func (r *Repository) PurgeNextCompanyErasurePage(ctx context.Context, limit int,
	businessAt time.Time) (CompanyErasurePurgeProgress, error) {
	if limit < 1 || limit > 500 || businessAt.IsZero() {
		return CompanyErasurePurgeProgress{}, fmt.Errorf("bounded Company erasure purge limit and time are required")
	}
	var erasureID string
	err := r.db.QueryRowContext(ctx, `SELECT erasure_id FROM recruiting_company_erasures
WHERE erasure_status = 'erasing' AND purge_phase <> 'materialize_resources'
ORDER BY updated_at, erasure_id LIMIT 1`).Scan(&erasureID)
	if errors.Is(err, sql.ErrNoRows) {
		return CompanyErasurePurgeProgress{}, ErrNotFound
	}
	if err != nil {
		return CompanyErasurePurgeProgress{}, fmt.Errorf("find Company erasure purge: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CompanyErasurePurgeProgress{}, fmt.Errorf("begin Company erasure purge page: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := getCompanyErasureWith(ctx, tx, erasureID, true)
	if err != nil {
		return CompanyErasurePurgeProgress{}, err
	}
	if current.Status != model.CompanyErasureErasing || current.PurgePhase == "materialize_resources" {
		return CompanyErasurePurgeProgress{}, ErrNotFound
	}
	phase := current.PurgePhase
	if phase == "purge_database" {
		phase = companyErasurePurgePhases[0]
		current.PurgePhase = phase
	}
	result, err := executeCompanyErasurePurgePhase(ctx, tx, current, phase, limit)
	if err != nil {
		return CompanyErasurePurgeProgress{}, fmt.Errorf("purge Company phase %s: %w", phase, err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected < 0 || affected > int64(limit) {
		return CompanyErasurePurgeProgress{}, fmt.Errorf("invalid Company erasure purge count phase=%s count=%d: %w", phase, affected, err)
	}
	nextPhase := phase
	advanced := false
	if affected == 0 {
		nextPhase, err = nextCompanyErasurePurgePhase(phase)
		if err != nil {
			return CompanyErasurePurgeProgress{}, err
		}
		advanced = true
	}
	next, err := current.AdvancePurge(current.Version, phase, uint64(affected), nextPhase)
	if err != nil {
		return CompanyErasurePurgeProgress{}, err
	}
	if err := updateCompanyErasureCAS(ctx, tx, current.Version, next, businessAt); err != nil {
		return CompanyErasurePurgeProgress{}, err
	}
	if err := tx.Commit(); err != nil {
		return CompanyErasurePurgeProgress{}, fmt.Errorf("commit Company erasure purge page: %w", err)
	}
	return CompanyErasurePurgeProgress{Erasure: next, Phase: phase, Affected: affected, Advanced: advanced}, nil
}

func (r *Repository) FinalizeNextCompanyErasure(ctx context.Context,
	businessAt time.Time) (model.CompanyErasureProof, error) {
	if businessAt.IsZero() {
		return model.CompanyErasureProof{}, fmt.Errorf("Company erasure completion time is required")
	}
	var erasureID string
	err := r.db.QueryRowContext(ctx, `SELECT erasure_id FROM recruiting_company_erasures
WHERE erasure_status = 'resource_cleanup' ORDER BY updated_at, erasure_id LIMIT 1`).Scan(&erasureID)
	if errors.Is(err, sql.ErrNoRows) {
		return model.CompanyErasureProof{}, ErrNotFound
	}
	if err != nil {
		return model.CompanyErasureProof{}, fmt.Errorf("find completable Company erasure: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return model.CompanyErasureProof{}, fmt.Errorf("begin Company erasure completion: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	current, err := getCompanyErasureWith(ctx, tx, erasureID, true)
	if err != nil {
		return model.CompanyErasureProof{}, err
	}
	if current.Status != model.CompanyErasureResourceCleanup || current.PurgePhase != "resource_cleanup" {
		return model.CompanyErasureProof{}, ErrNotFound
	}
	var pending, verified, companies, sources, works, jobs, artifacts uint64
	if err := tx.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM recruiting_company_erasure_resources WHERE erasure_id = ? AND cleanup_status = 'pending'),
  (SELECT COUNT(*) FROM recruiting_company_erasure_resources WHERE erasure_id = ? AND cleanup_status = 'deleted'),
  (SELECT COUNT(*) FROM recruiting_companies WHERE company_id = ?),
  (SELECT COUNT(*) FROM recruiting_sources WHERE company_id = ?),
  (SELECT COUNT(*) FROM recruiting_works WHERE company_id = ? AND work_id <> ?),
  (SELECT COUNT(*) FROM recruiting_source_jobs WHERE source_id IN
    (SELECT source_id FROM recruiting_company_erasure_sources WHERE erasure_id = ?)),
  (SELECT COUNT(*) FROM recruiting_artifacts WHERE artifact_id IN
    (SELECT artifact_id FROM recruiting_company_erasure_resources WHERE erasure_id = ?))`, current.ErasureID,
		current.ErasureID, current.CompanyID, current.CompanyID, current.CompanyID, current.WorkID,
		current.ErasureID, current.ErasureID).Scan(&pending, &verified, &companies, &sources, &works, &jobs, &artifacts); err != nil {
		return model.CompanyErasureProof{}, fmt.Errorf("verify Company erasure completion: %w", err)
	}
	if pending != 0 || verified != current.ResourceCount || companies != 0 || sources != 0 || works != 0 ||
		jobs != 0 || artifacts != 0 {
		return model.CompanyErasureProof{}, fmt.Errorf("Company erasure cleanup is not complete")
	}
	proof, err := model.NewCompanyErasureProof(current, verified, businessAt)
	if err != nil {
		return model.CompanyErasureProof{}, err
	}
	next, err := current.Complete(current.Version, proof)
	if err != nil {
		return model.CompanyErasureProof{}, err
	}
	work, err := getWorkForErasureUpdate(ctx, tx, current.WorkID)
	if err != nil {
		return model.CompanyErasureProof{}, err
	}
	completedWork, err := work.Complete(work.Version, model.ResolutionSucceeded, "", "")
	if err != nil {
		return model.CompanyErasureProof{}, err
	}
	proofState, _ := json.Marshal(proof)
	if _, err := tx.ExecContext(ctx, `INSERT INTO recruiting_company_erasure_proofs(
  erasure_id, proof_hash, state_json, completed_at) VALUES (?, ?, ?, ?)`, proof.ErasureID,
		proof.ProofHash, proofState, businessAt.UTC()); err != nil {
		return model.CompanyErasureProof{}, fmt.Errorf("insert Company erasure proof: %w", err)
	}
	if err := updateCompanyErasureCAS(ctx, tx, current.Version, next, businessAt); err != nil {
		return model.CompanyErasureProof{}, err
	}
	if err := updateWorkTx(ctx, tx, work.Version, completedWork, businessAt); err != nil {
		return model.CompanyErasureProof{}, err
	}
	audit, _ := json.Marshal(map[string]any{"proof_hash": proof.ProofHash, "policy_version": proof.PolicyVersion,
		"source_count": proof.SourceCount, "resource_count": proof.ResourceCount})
	event, err := model.NewEventIntent("event-company-erasure-completed-"+current.ErasureID,
		"company.erasure.completed", "company_erasure", next.ErasureID, next.Version,
		businessAt.Format(time.RFC3339Nano), "system:company-erasure-complete:"+current.ErasureID, audit)
	if err != nil {
		return model.CompanyErasureProof{}, err
	}
	if err := appendEventIntent(ctx, tx, event, businessAt, businessAt); err != nil {
		return model.CompanyErasureProof{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.CompanyErasureProof{}, fmt.Errorf("commit Company erasure completion: %w", err)
	}
	return proof, nil
}

func nextCompanyErasurePurgePhase(current string) (string, error) {
	for index, phase := range companyErasurePurgePhases {
		if phase != current {
			continue
		}
		if index+1 == len(companyErasurePurgePhases) {
			return "resource_cleanup", nil
		}
		return companyErasurePurgePhases[index+1], nil
	}
	return "", fmt.Errorf("unknown Company erasure purge phase %q", current)
}

func executeCompanyErasurePurgePhase(ctx context.Context, tx *sql.Tx, erasure model.CompanyErasure,
	phase string, limit int) (sql.Result, error) {
	companyID, erasureID, workID := erasure.CompanyID, erasure.ErasureID, erasure.WorkID
	workIDs := `SELECT work_id FROM recruiting_works WHERE company_id = ? AND work_id <> ?`
	sourceIDs := `SELECT source_id FROM recruiting_company_erasure_sources WHERE erasure_id = ?`
	jobIDs := `SELECT job_id FROM recruiting_source_jobs WHERE source_id IN (` + sourceIDs + `)`
	var query string
	var args []any
	switch phase {
	case "validation_artifact_pages":
		query, args = `DELETE FROM recruiting_validation_artifact_pages WHERE work_id IN (`+workIDs+`) LIMIT ?`, []any{companyID, workID, limit}
	case "profile_repair_sessions":
		query, args = `DELETE FROM recruiting_profile_repair_sessions WHERE work_id IN (`+workIDs+`) OR validation_work_id IN (`+workIDs+`) LIMIT ?`, []any{companyID, workID, companyID, workID, limit}
	case "baseline_detail_items":
		query, args = `DELETE FROM recruiting_baseline_detail_items WHERE source_id IN (`+sourceIDs+`) LIMIT ?`, []any{erasureID, limit}
	case "baseline_staging":
		query, args = `DELETE FROM recruiting_baseline_staging WHERE source_id IN (`+sourceIDs+`) LIMIT ?`, []any{erasureID, limit}
	case "listing_page_progress":
		query, args = `DELETE FROM recruiting_listing_page_progress WHERE work_id IN (`+workIDs+`) LIMIT ?`, []any{companyID, workID, limit}
	case "budget_permits":
		query, args = `DELETE FROM recruiting_budget_permits WHERE company_id = ? LIMIT ?`, []any{companyID, limit}
	case "artifacts":
		query, args = `DELETE FROM recruiting_artifacts WHERE work_id IN (`+workIDs+`) LIMIT ?`, []any{companyID, workID, limit}
	case "backfill_outputs":
		query, args = `DELETE FROM recruiting_backfill_outputs WHERE (backfill_id,item_id) IN (SELECT backfill_id,item_id FROM recruiting_backfill_items WHERE company_id = ?) LIMIT ?`, []any{companyID, limit}
	case "backfill_items":
		query, args = `DELETE FROM recruiting_backfill_items WHERE company_id = ? LIMIT ?`, []any{companyID, limit}
	case "backfills":
		query, args = `DELETE FROM recruiting_backfills WHERE (target_type = 'company' AND target_id = ?) OR (target_type = 'source' AND target_id IN (`+sourceIDs+`)) OR work_id IN (`+workIDs+`) LIMIT ?`, []any{companyID, erasureID, companyID, workID, limit}
	case "repair_affected_works":
		query, args = `DELETE FROM recruiting_repair_affected_works WHERE work_id IN (`+workIDs+`) LIMIT ?`, []any{companyID, workID, limit}
	case "scope_catchups":
		query, args = `DELETE FROM recruiting_scope_catchup_occurrences WHERE source_id IN (`+sourceIDs+`) OR work_id IN (`+workIDs+`) LIMIT ?`, []any{erasureID, companyID, workID, limit}
	case "scope_control_roots":
		query, args = `DELETE FROM recruiting_scope_control_roots WHERE root_work_id IN (`+workIDs+`) LIMIT ?`, []any{companyID, workID, limit}
	case "recipe_validation_runs":
		query, args = `DELETE FROM recruiting_recipe_validation_runs WHERE company_id = ? OR source_id IN (`+sourceIDs+`) OR work_id IN (`+workIDs+`) LIMIT ?`, []any{companyID, erasureID, companyID, workID, limit}
	case "listing_runs":
		query, args = `DELETE FROM recruiting_listing_runs WHERE source_id IN (`+sourceIDs+`) OR work_id IN (`+workIDs+`) LIMIT ?`, []any{erasureID, companyID, workID, limit}
	case "source_discovery_candidates":
		query, args = `DELETE FROM recruiting_source_discovery_candidates WHERE discovery_id IN (SELECT discovery_id FROM recruiting_source_discoveries WHERE company_id = ?) LIMIT ?`, []any{companyID, limit}
	case "source_discoveries":
		query, args = `DELETE FROM recruiting_source_discoveries WHERE company_id = ? LIMIT ?`, []any{companyID, limit}
	case "baseline_generations":
		query, args = `DELETE FROM recruiting_baseline_generations WHERE source_id IN (`+sourceIDs+`) LIMIT ?`, []any{erasureID, limit}
	case "attempts":
		query, args = `DELETE FROM recruiting_attempts WHERE work_id IN (`+workIDs+`) LIMIT ?`, []any{companyID, workID, limit}
	case "listing_observations":
		query, args = `DELETE FROM recruiting_listing_observations WHERE source_id IN (`+sourceIDs+`) LIMIT ?`, []any{erasureID, limit}
	case "override_heads":
		query, args = `DELETE FROM recruiting_override_heads WHERE target_id IN (`+jobIDs+`) LIMIT ?`, []any{erasureID, limit}
	case "override_versions":
		query, args = `DELETE FROM recruiting_override_versions WHERE target_id IN (`+jobIDs+`) LIMIT ?`, []any{erasureID, limit}
	case "job_detail_versions":
		query, args = `DELETE FROM recruiting_job_detail_versions WHERE job_id IN (`+jobIDs+`) LIMIT ?`, []any{erasureID, limit}
	case "recipe_proposals":
		query, args = `DELETE FROM recruiting_recipe_proposals WHERE source_id IN (`+sourceIDs+`) LIMIT ?`, []any{erasureID, limit}
	case "recipe_rollout_items":
		query, args = `DELETE FROM recruiting_recipe_rollout_items WHERE source_id IN (`+sourceIDs+`) LIMIT ?`, []any{erasureID, limit}
	case "endpoint_activations":
		query, args = `DELETE FROM recruiting_source_endpoint_activations WHERE source_id IN (`+sourceIDs+`) LIMIT ?`, []any{erasureID, limit}
	case "endpoint_changes":
		query, args = `DELETE FROM recruiting_source_endpoint_changes WHERE source_id IN (`+sourceIDs+`) LIMIT ?`, []any{erasureID, limit}
	case "assignment_versions":
		query, args = `DELETE FROM recruiting_source_assignment_versions WHERE source_id IN (`+sourceIDs+`) LIMIT ?`, []any{erasureID, limit}
	case "assignments":
		query, args = `DELETE FROM recruiting_source_assignments WHERE source_id IN (`+sourceIDs+`) LIMIT ?`, []any{erasureID, limit}
	case "profile_binding_history":
		query, args = `DELETE FROM recruiting_source_profile_binding_history WHERE source_id IN (`+sourceIDs+`) LIMIT ?`, []any{erasureID, limit}
	case "profile_bindings":
		query, args = `DELETE FROM recruiting_source_profile_bindings WHERE source_id IN (`+sourceIDs+`) LIMIT ?`, []any{erasureID, limit}
	case "checkpoints":
		query, args = `DELETE FROM recruiting_checkpoints WHERE source_id IN (`+sourceIDs+`) LIMIT ?`, []any{erasureID, limit}
	case "source_occurrences":
		query, args = `DELETE FROM recruiting_source_occurrences WHERE source_id IN (`+sourceIDs+`) LIMIT ?`, []any{erasureID, limit}
	case "source_lineage":
		query, args = `DELETE FROM recruiting_source_lineage WHERE from_company_id = ? OR to_company_id = ? LIMIT ?`, []any{companyID, companyID, limit}
	case "source_reassignment_previews":
		query, args = `DELETE FROM recruiting_source_reassignment_previews WHERE target_company_id = ? OR source_id IN (`+sourceIDs+`) LIMIT ?`, []any{companyID, erasureID, limit}
	case "company_import_items":
		query, args = `DELETE FROM recruiting_company_import_items WHERE company_id = ? OR child_work_id IN (`+workIDs+`) LIMIT ?`, []any{companyID, companyID, workID, limit}
	case "website_heads":
		query, args = `DELETE FROM recruiting_company_website_heads WHERE company_id = ? LIMIT ?`, []any{companyID, limit}
	case "website_revision_links":
		query, args = `UPDATE recruiting_company_website_revisions SET reverts_revision_id = NULL WHERE company_id = ? AND reverts_revision_id IS NOT NULL LIMIT ?`, []any{companyID, limit}
	case "website_revisions":
		query, args = `DELETE FROM recruiting_company_website_revisions WHERE company_id = ? LIMIT ?`, []any{companyID, limit}
	case "repair_validation_links":
		query, args = `UPDATE recruiting_repair_incidents SET validation_work_id = NULL, state_json = JSON_REMOVE(state_json, '$.validation_work_id') WHERE validation_work_id IN (`+workIDs+`) LIMIT ?`, []any{companyID, workID, limit}
	case "work_blocked_links":
		query, args = `UPDATE recruiting_works SET blocked_by_repair_work_id = NULL, state_json = JSON_REMOVE(state_json, '$.blocked_by_repair_work_id') WHERE blocked_by_repair_work_id IN (SELECT work_id FROM (SELECT work_id FROM recruiting_works WHERE company_id = ? AND work_id <> ?) purge_work_ids) LIMIT ?`, []any{companyID, workID, limit}
	case "scope_control_operations":
		query, args = `DELETE FROM recruiting_scope_control_operations WHERE (scope_type = 'company' AND scope_id = ?) OR (scope_type = 'source' AND scope_id IN (`+sourceIDs+`)) LIMIT ?`, []any{companyID, erasureID, limit}
	case "works":
		query, args = `DELETE FROM recruiting_works WHERE company_id = ? AND work_id <> ? LIMIT ?`, []any{companyID, workID, limit}
	case "source_jobs":
		query, args = `DELETE FROM recruiting_source_jobs WHERE source_id IN (`+sourceIDs+`) LIMIT ?`, []any{erasureID, limit}
	case "company_aliases":
		query, args = `DELETE FROM recruiting_company_aliases WHERE alias_company_id = ? OR canonical_company_id = ? LIMIT ?`, []any{companyID, companyID, limit}
	case "company_merge_previews":
		query, args = `DELETE FROM recruiting_company_merge_previews WHERE canonical_company_id = ? LIMIT ?`, []any{companyID, limit}
	case "sources":
		query, args = `DELETE FROM recruiting_sources WHERE company_id = ? LIMIT ?`, []any{companyID, limit}
	case "company":
		query, args = `DELETE FROM recruiting_companies WHERE company_id = ? LIMIT ?`, []any{companyID, limit}
	default:
		return nil, fmt.Errorf("unknown Company erasure purge phase %q", strings.TrimSpace(phase))
	}
	return tx.ExecContext(ctx, query, args...)
}
