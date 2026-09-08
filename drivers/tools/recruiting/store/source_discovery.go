package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func (r *Repository) CreateSourceDiscovery(ctx context.Context, discovery model.SourceDiscovery, work model.Work,
	placement WorkPlacement, businessAt time.Time) error {
	if discovery.DiscoveryID == "" || discovery.Version != 1 || discovery.Status != model.SourceDiscoveryQueued ||
		discovery.WorkID != work.WorkID || work.TargetType != "company" || work.TargetID != discovery.CompanyID ||
		work.Purpose != "source_discovery" || work.Status != model.WorkOpen || work.Version != 1 || businessAt.IsZero() {
		return fmt.Errorf("source discovery requires matching queued discovery and open Work")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin source discovery: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var companyState []byte
	if err := tx.QueryRowContext(ctx, "SELECT state_json FROM recruiting_companies WHERE company_id = ? FOR UPDATE", discovery.CompanyID).Scan(&companyState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	var company model.Company
	if json.Unmarshal(companyState, &company) != nil || company.Version != discovery.CompanyVersion ||
		company.ControlStatus == model.ControlArchived || company.Website != discovery.SeedURL {
		return fmt.Errorf("source discovery is fenced by changed or unavailable Company")
	}
	var recipeState []byte
	if err := tx.QueryRowContext(ctx, `
SELECT state_json FROM recruiting_recipes
WHERE recipe_id = ? AND recipe_version = ? FOR UPDATE`, discovery.RecipeID, discovery.RecipeVersion).Scan(&recipeState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	var recipe model.Recipe
	if json.Unmarshal(recipeState, &recipe) != nil || recipe.Kind != model.RecipeDiscovery || recipe.Status != model.RecipeActive ||
		recipe.ContentHash != discovery.RecipeContentHash || recipe.ContractHash != discovery.ContractHash ||
		recipe.Execution != discovery.Execution {
		return fmt.Errorf("source discovery is fenced by changed or unavailable Recipe")
	}
	if err := insertWork(ctx, tx, work, placement, businessAt); err != nil {
		return err
	}
	if err := insertSourceDiscovery(ctx, tx, discovery, businessAt); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit source discovery: %w", err)
	}
	return nil
}

func insertSourceDiscovery(ctx context.Context, executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, discovery model.SourceDiscovery, at time.Time) error {
	state, _ := json.Marshal(discovery)
	_, err := executor.ExecContext(ctx, `
INSERT INTO recruiting_source_discoveries(
  discovery_id, work_id, company_id, discovery_generation, company_version,
  seed_url, recipe_id, recipe_version, discovery_status, candidate_count,
  next_chunk_sequence, version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, discovery.DiscoveryID, discovery.WorkID,
		discovery.CompanyID, discovery.Generation, discovery.CompanyVersion, discovery.SeedURL, discovery.RecipeID,
		discovery.RecipeVersion, discovery.Status, discovery.CandidateCount, discovery.NextChunkSequence,
		discovery.Version, state, at.UTC(), at.UTC())
	if err == nil {
		return nil
	}
	if isDuplicateKey(err) {
		return fmt.Errorf("%w: discovery ID, Work, or Company generation", ErrBusinessKeyExists)
	}
	return fmt.Errorf("insert source discovery: %w", err)
}

func (r *Repository) GetSourceDiscovery(ctx context.Context, discoveryID string) (model.SourceDiscovery, error) {
	return getSourceDiscoveryWith(ctx, r.db, discoveryID, false)
}

func getSourceDiscoveryWith(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, discoveryID string, lock bool) (model.SourceDiscovery, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var state []byte
	err := query.QueryRowContext(ctx, "SELECT state_json FROM recruiting_source_discoveries WHERE discovery_id = ?"+suffix, discoveryID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.SourceDiscovery{}, ErrNotFound
	}
	if err != nil {
		return model.SourceDiscovery{}, err
	}
	var discovery model.SourceDiscovery
	if err := json.Unmarshal(state, &discovery); err != nil {
		return model.SourceDiscovery{}, fmt.Errorf("decode source discovery: %w", err)
	}
	return discovery, nil
}

func (r *Repository) StartSourceDiscovery(ctx context.Context, discoveryID string, expectedVersion uint64, at time.Time) (model.SourceDiscovery, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return model.SourceDiscovery{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := getSourceDiscoveryWith(ctx, tx, discoveryID, true)
	if err != nil {
		return model.SourceDiscovery{}, err
	}
	next, err := current.Start(expectedVersion)
	if err != nil {
		return model.SourceDiscovery{}, err
	}
	if err := updateSourceDiscoveryCAS(ctx, tx, current.Version, next, at); err != nil {
		return model.SourceDiscovery{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.SourceDiscovery{}, err
	}
	return next, nil
}

func (r *Repository) AppendSourceDiscoveryCandidates(ctx context.Context, discoveryID string, expectedVersion, sequence uint64,
	candidates []model.SourceDiscoveryCandidate, at time.Time) (model.SourceDiscovery, error) {
	if len(candidates) < 1 || len(candidates) > 500 {
		return model.SourceDiscovery{}, fmt.Errorf("source discovery candidate chunk must contain 1..500 items")
	}
	seen := make(map[string]struct{}, len(candidates))
	for _, candidate := range candidates {
		validated, err := model.NewSourceDiscoveryCandidate(candidate.Endpoint, candidate.Category, candidate.FinalURL,
			candidate.ConfidenceBasis, candidate.EvidenceArtifactID)
		if err != nil || validated != candidate || candidate.Disposition != model.SourceCandidatePending || candidate.Version != 1 {
			return model.SourceDiscovery{}, fmt.Errorf("source discovery candidate is not normalized")
		}
		if _, duplicate := seen[candidate.CandidateID]; duplicate {
			return model.SourceDiscovery{}, fmt.Errorf("duplicate source discovery candidate")
		}
		seen[candidate.CandidateID] = struct{}{}
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return model.SourceDiscovery{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := getSourceDiscoveryWith(ctx, tx, discoveryID, true)
	if err != nil {
		return model.SourceDiscovery{}, err
	}
	next, err := current.AppendCandidates(expectedVersion, sequence, len(candidates))
	if err != nil {
		return model.SourceDiscovery{}, err
	}
	for index, candidate := range candidates {
		state, _ := json.Marshal(candidate)
		_, err := tx.ExecContext(ctx, `
INSERT INTO recruiting_source_discovery_candidates(
  discovery_id, candidate_id, candidate_ordinal, canonical_source_key,
  disposition, source_id, evidence_artifact_id, version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?)`, current.DiscoveryID, candidate.CandidateID,
			current.CandidateCount+index, candidate.CandidateKey, candidate.Disposition, candidate.EvidenceArtifactID,
			candidate.Version, state, at.UTC(), at.UTC())
		if err != nil {
			if isDuplicateKey(err) {
				return model.SourceDiscovery{}, fmt.Errorf("%w: source discovery candidate", ErrBusinessKeyExists)
			}
			return model.SourceDiscovery{}, err
		}
	}
	if err := updateSourceDiscoveryCAS(ctx, tx, current.Version, next, at); err != nil {
		return model.SourceDiscovery{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.SourceDiscovery{}, err
	}
	return next, nil
}

func (r *Repository) FinishSourceDiscovery(ctx context.Context, discoveryID string, expectedVersion uint64,
	at time.Time) (model.SourceDiscovery, error) {
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return model.SourceDiscovery{}, err
	}
	defer func() { _ = tx.Rollback() }()
	current, err := getSourceDiscoveryWith(ctx, tx, discoveryID, true)
	if err != nil {
		return model.SourceDiscovery{}, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_source_discovery_candidates WHERE discovery_id = ?", discoveryID).Scan(&count); err != nil {
		return model.SourceDiscovery{}, err
	}
	next, err := current.Complete(expectedVersion, count)
	if err != nil {
		return model.SourceDiscovery{}, err
	}
	if err := updateSourceDiscoveryCAS(ctx, tx, current.Version, next, at); err != nil {
		return model.SourceDiscovery{}, err
	}
	if err := tx.Commit(); err != nil {
		return model.SourceDiscovery{}, err
	}
	return next, nil
}

func updateSourceDiscoveryCAS(ctx context.Context, executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, expected uint64, discovery model.SourceDiscovery, at time.Time) error {
	state, _ := json.Marshal(discovery)
	result, err := executor.ExecContext(ctx, `
UPDATE recruiting_source_discoveries
SET discovery_status = ?, candidate_count = ?, next_chunk_sequence = ?, version = ?, state_json = ?, updated_at = ?
WHERE discovery_id = ? AND version = ?`, discovery.Status, discovery.CandidateCount, discovery.NextChunkSequence,
		discovery.Version, state, at.UTC(), discovery.DiscoveryID, expected)
	if err != nil {
		return err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return &model.VersionConflictError{Expected: expected, Actual: discovery.Version}
	}
	return nil
}
