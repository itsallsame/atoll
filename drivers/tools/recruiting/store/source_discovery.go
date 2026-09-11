package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type SourceDiscoveryCandidatePage struct {
	Items      []model.SourceDiscoveryCandidate
	NextCursor string
	HasMore    bool
}

type sourceDiscoveryCandidateCursor struct {
	DiscoveryID string `json:"discovery_id"`
	Ordinal     int64  `json:"ordinal"`
}

func (r *Repository) CreateSourceDiscovery(ctx context.Context, discovery model.SourceDiscovery, work model.Work,
	placement WorkPlacement, businessAt time.Time) error {
	if err := validateNewSourceDiscovery(discovery, work, placement, businessAt); err != nil {
		return err
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return fmt.Errorf("begin source discovery: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := lockSourceDiscoveryDependencies(ctx, tx, discovery, work, placement); err != nil {
		return err
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

// ApplyCreateSourceDiscoveryCommand makes the public command acknowledgement,
// Work, immutable discovery generation, audit event, and executor wake intent
// one atomic fact. A replay therefore cannot create a second generation.
func (r *Repository) ApplyCreateSourceDiscoveryCommand(ctx context.Context, expectedCompanyVersion uint64,
	company model.Company, discovery model.SourceDiscovery, work model.Work, placement WorkPlacement,
	receipt model.CommandReceipt, event model.EventIntent, companyEvent *model.EventIntent, dispatch *ExecutionDispatchIntent,
	businessAt time.Time) (CommandResult, error) {
	if err := validateNewSourceDiscovery(discovery, work, placement, businessAt); err != nil {
		return CommandResult{}, err
	}
	if expectedCompanyVersion == 0 || company.CompanyID != discovery.CompanyID || company.Version != discovery.CompanyVersion ||
		receipt.CommandID == "" || event.AggregateType != "source_discovery" || event.AggregateID != discovery.DiscoveryID ||
		event.AggregateVersion != discovery.Version || event.CauseCommandID != receipt.CommandID {
		return CommandResult{}, fmt.Errorf("source discovery command, receipt, and event are inconsistent")
	}
	eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
	if err != nil {
		return CommandResult{}, fmt.Errorf("event business time: %w", err)
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, fmt.Errorf("begin source discovery command: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	var currentState []byte
	if err := tx.QueryRowContext(ctx, "SELECT state_json FROM recruiting_companies WHERE company_id = ? FOR UPDATE", company.CompanyID).Scan(&currentState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CommandResult{}, ErrNotFound
		}
		return CommandResult{}, fmt.Errorf("lock discovery Company: %w", err)
	}
	var currentCompany model.Company
	if err := json.Unmarshal(currentState, &currentCompany); err != nil {
		return CommandResult{}, fmt.Errorf("decode discovery Company: %w", err)
	}
	if currentCompany.Version != expectedCompanyVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedCompanyVersion, Actual: currentCompany.Version}
	}
	expectedCompany := currentCompany
	switch currentCompany.OnboardingStatus {
	case model.CompanyNew, model.CompanyBlockedNoSources, model.CompanyBlocked:
		expectedCompany, err = currentCompany.StartDiscovery(expectedCompanyVersion)
	case model.CompanyDiscoveringSources, model.CompanyInitializing, model.CompanyReady:
		if currentCompany.ControlStatus != model.ControlActive {
			err = &model.InvalidTransitionError{Entity: "company", From: string(currentCompany.ControlStatus), Action: "start discovery"}
		}
	default:
		err = fmt.Errorf("unsupported Company onboarding state %q", currentCompany.OnboardingStatus)
	}
	if err != nil {
		return CommandResult{}, err
	}
	if !reflect.DeepEqual(expectedCompany, company) {
		return CommandResult{}, fmt.Errorf("source discovery Company transition does not match locked state")
	}
	if company.Version != currentCompany.Version {
		if companyEvent == nil || companyEvent.AggregateType != "company" || companyEvent.AggregateID != company.CompanyID ||
			companyEvent.AggregateVersion != company.Version || companyEvent.CauseCommandID != receipt.CommandID {
			return CommandResult{}, fmt.Errorf("source discovery Company event is inconsistent")
		}
		companyState, _ := json.Marshal(company)
		result, updateErr := tx.ExecContext(ctx, `UPDATE recruiting_companies
SET normalized_website = ?, name = ?, onboarding_status = ?, control_status = ?,
    configuration_version = ?, control_epoch = ?, execution_fence = ?, version = ?, state_json = ?, updated_at = ?
WHERE company_id = ? AND version = ?`, nullableString(company.Website), company.Name, company.OnboardingStatus,
			company.ControlStatus, company.ConfigurationVersion, company.ControlEpoch, company.ExecutionFence,
			company.Version, companyState, businessAt.UTC(), company.CompanyID, expectedCompanyVersion)
		if updateErr != nil {
			return CommandResult{}, updateErr
		}
		if changed, _ := result.RowsAffected(); changed != 1 {
			return CommandResult{}, &model.VersionConflictError{Expected: expectedCompanyVersion, Actual: currentCompany.Version}
		}
	} else if companyEvent != nil {
		return CommandResult{}, fmt.Errorf("unchanged discovery Company cannot emit a transition event")
	}
	if err := lockSourceDiscoveryDependencies(ctx, tx, discovery, work, placement); err != nil {
		return CommandResult{}, err
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		if errors.Is(err, ErrCommandConflict) {
			_ = tx.Rollback()
			return r.replayCommittedCommand(ctx, receipt.CommandID, receipt.RequestHash)
		}
		return CommandResult{}, err
	}
	if err := insertWork(ctx, tx, work, placement, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := insertSourceDiscovery(ctx, tx, discovery, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if companyEvent != nil {
		companyEventAt, parseErr := time.Parse(time.RFC3339, companyEvent.BusinessAt)
		if parseErr != nil {
			return CommandResult{}, parseErr
		}
		if err := appendEventIntent(ctx, tx, *companyEvent, companyEventAt, businessAt); err != nil {
			return CommandResult{}, err
		}
	}
	if err := appendWorkCommandDispatch(ctx, tx, dispatch, placement, receipt.CommandID, "source_discovery_created", businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, fmt.Errorf("commit source discovery command: %w", err)
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func validateNewSourceDiscovery(discovery model.SourceDiscovery, work model.Work, placement WorkPlacement, businessAt time.Time) error {
	if discovery.DiscoveryID == "" || discovery.Version != 1 || discovery.Status != model.SourceDiscoveryQueued ||
		discovery.WorkID != work.WorkID || work.TargetType != "company" || work.TargetID != discovery.CompanyID ||
		work.Purpose != "source_discovery" || work.Status != model.WorkOpen || work.Version != 1 || businessAt.IsZero() ||
		(discovery.WebsiteRevisionID == "") != (work.CauseWorkID == "") {
		return fmt.Errorf("source discovery requires matching queued discovery and open Work")
	}
	origin, err := canonicalOrigin(discovery.SeedURL)
	if err != nil || placement.Capability != discovery.Execution.RequiredCapability || placement.Origin != origin || placement.NotBefore.IsZero() {
		return fmt.Errorf("source discovery Work placement must match its Recipe capability and seed origin")
	}
	return nil
}

func lockSourceDiscoveryDependencies(ctx context.Context, tx *sql.Tx, discovery model.SourceDiscovery, work model.Work,
	placement WorkPlacement) error {
	var companyState []byte
	if err := tx.QueryRowContext(ctx, "SELECT state_json FROM recruiting_companies WHERE company_id = ? FOR UPDATE", discovery.CompanyID).Scan(&companyState); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	var company model.Company
	if err := json.Unmarshal(companyState, &company); err != nil {
		return fmt.Errorf("decode source discovery Company: %w", err)
	}
	if company.Version != discovery.CompanyVersion ||
		company.ControlStatus == model.ControlArchived || company.Website != discovery.SeedURL {
		return fmt.Errorf("source discovery is fenced by changed or unavailable Company")
	}
	var websiteRevisionID string
	headErr := tx.QueryRowContext(ctx, `SELECT revision_id FROM recruiting_company_website_heads
WHERE company_id = ? FOR UPDATE`, discovery.CompanyID).Scan(&websiteRevisionID)
	if headErr != nil && !errors.Is(headErr, sql.ErrNoRows) {
		return fmt.Errorf("lock source discovery website head: %w", headErr)
	}
	if errors.Is(headErr, sql.ErrNoRows) {
		if discovery.WebsiteRevisionID != "" || work.CauseWorkID != "" {
			return fmt.Errorf("source discovery cites a Company without website revision history")
		}
	} else {
		if websiteRevisionID != discovery.WebsiteRevisionID {
			return fmt.Errorf("%w: source discovery cites a superseded revision", ErrWebsiteRevisionConflict)
		}
		revision, err := getCompanyWebsiteRevisionWith(ctx, tx, websiteRevisionID, true)
		if err != nil {
			return err
		}
		if revision.CompanyID != company.CompanyID || revision.Website != company.Website ||
			revision.ConfigurationVersion != company.ConfigurationVersion || revision.ReviewWorkID != work.CauseWorkID {
			return fmt.Errorf("%w: source discovery does not descend from the current website review", ErrWebsiteRevisionConflict)
		}
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
	if err := json.Unmarshal(recipeState, &recipe); err != nil {
		return fmt.Errorf("decode source discovery Recipe: %w", err)
	}
	if recipe.Kind != model.RecipeDiscovery || recipe.Status != model.RecipeActive ||
		recipe.ContentHash != discovery.RecipeContentHash || recipe.ContractHash != discovery.ContractHash ||
		recipe.Execution != discovery.Execution {
		return fmt.Errorf("source discovery is fenced by changed or unavailable Recipe")
	}
	origin, err := canonicalOrigin(discovery.SeedURL)
	if err != nil || placement.Capability != discovery.Execution.RequiredCapability || placement.Origin != origin {
		return fmt.Errorf("source discovery placement must match its Recipe capability and seed origin")
	}
	return nil
}

func insertSourceDiscovery(ctx context.Context, executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, discovery model.SourceDiscovery, at time.Time) error {
	state, _ := json.Marshal(discovery)
	_, err := executor.ExecContext(ctx, `
INSERT INTO recruiting_source_discoveries(
  discovery_id, work_id, company_id, discovery_generation, company_version, website_revision_id,
  seed_url, recipe_id, recipe_version, discovery_status, candidate_count,
  next_chunk_sequence, version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, discovery.DiscoveryID, discovery.WorkID,
		discovery.CompanyID, discovery.Generation, discovery.CompanyVersion, nullableString(discovery.WebsiteRevisionID), discovery.SeedURL, discovery.RecipeID,
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

func (r *Repository) GetSourceDiscoveryCandidate(ctx context.Context, discoveryID, candidateID string) (model.SourceDiscoveryCandidate, error) {
	return getSourceDiscoveryCandidateWith(ctx, r.db, discoveryID, candidateID, false)
}

// ListSourceDiscoveryCandidates uses an aggregate-bound seek cursor, so a
// cursor cannot be replayed against another discovery generation.
func (r *Repository) ListSourceDiscoveryCandidates(ctx context.Context, discoveryID, cursor string, limit int) (SourceDiscoveryCandidatePage, error) {
	discoveryID = strings.TrimSpace(discoveryID)
	if discoveryID == "" || limit < 1 || limit > 500 {
		return SourceDiscoveryCandidatePage{}, fmt.Errorf("discovery ID and candidate page limit in [1,500] are required")
	}
	afterOrdinal := int64(-1)
	if strings.TrimSpace(cursor) != "" {
		content, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(cursor))
		var decoded sourceDiscoveryCandidateCursor
		if err != nil || json.Unmarshal(content, &decoded) != nil || decoded.DiscoveryID != discoveryID || decoded.Ordinal < 0 {
			return SourceDiscoveryCandidatePage{}, fmt.Errorf("%w: source discovery candidate", ErrInvalidCursor)
		}
		afterOrdinal = decoded.Ordinal
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT candidate_ordinal, state_json
FROM recruiting_source_discovery_candidates
WHERE discovery_id = ? AND candidate_ordinal > ?
ORDER BY candidate_ordinal
LIMIT ?`, discoveryID, afterOrdinal, limit+1)
	if err != nil {
		return SourceDiscoveryCandidatePage{}, fmt.Errorf("list source discovery candidates: %w", err)
	}
	defer rows.Close()
	type rowValue struct {
		ordinal   int64
		candidate model.SourceDiscoveryCandidate
	}
	values := make([]rowValue, 0, limit+1)
	for rows.Next() {
		var value rowValue
		var state []byte
		if err := rows.Scan(&value.ordinal, &state); err != nil {
			return SourceDiscoveryCandidatePage{}, err
		}
		if err := json.Unmarshal(state, &value.candidate); err != nil {
			return SourceDiscoveryCandidatePage{}, fmt.Errorf("decode source discovery candidate: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return SourceDiscoveryCandidatePage{}, err
	}
	page := SourceDiscoveryCandidatePage{HasMore: len(values) > limit}
	if page.HasMore {
		values = values[:limit]
	}
	page.Items = make([]model.SourceDiscoveryCandidate, 0, len(values))
	for _, value := range values {
		page.Items = append(page.Items, value.candidate)
	}
	if page.HasMore && len(values) > 0 {
		content, _ := json.Marshal(sourceDiscoveryCandidateCursor{DiscoveryID: discoveryID, Ordinal: values[len(values)-1].ordinal})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(content)
	}
	return page, nil
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

func getSourceDiscoveryCandidateWith(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, discoveryID, candidateID string, lock bool) (model.SourceDiscoveryCandidate, error) {
	discoveryID, candidateID = strings.TrimSpace(discoveryID), strings.TrimSpace(candidateID)
	if discoveryID == "" || candidateID == "" {
		return model.SourceDiscoveryCandidate{}, fmt.Errorf("source discovery and candidate identity are required")
	}
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var state []byte
	err := query.QueryRowContext(ctx, `SELECT state_json FROM recruiting_source_discovery_candidates
WHERE discovery_id = ? AND candidate_id = ?`+suffix, discoveryID, candidateID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.SourceDiscoveryCandidate{}, ErrNotFound
	}
	if err != nil {
		return model.SourceDiscoveryCandidate{}, fmt.Errorf("get source discovery candidate: %w", err)
	}
	var candidate model.SourceDiscoveryCandidate
	if err := json.Unmarshal(state, &candidate); err != nil {
		return model.SourceDiscoveryCandidate{}, fmt.Errorf("decode source discovery candidate: %w", err)
	}
	return candidate, nil
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
	if err := insertSourceDiscoveryCandidates(ctx, tx, current.DiscoveryID, current.CandidateCount, candidates, at); err != nil {
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

func insertSourceDiscoveryCandidates(ctx context.Context, executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, discoveryID string, ordinalOffset int, candidates []model.SourceDiscoveryCandidate, at time.Time) error {
	for index, candidate := range candidates {
		state, _ := json.Marshal(candidate)
		_, err := executor.ExecContext(ctx, `
INSERT INTO recruiting_source_discovery_candidates(
  discovery_id, candidate_id, candidate_ordinal, canonical_source_key,
  disposition, source_id, evidence_artifact_id, version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, NULL, ?, ?, ?, ?, ?)`, discoveryID, candidate.CandidateID,
			ordinalOffset+index, candidate.CandidateKey, candidate.Disposition, candidate.EvidenceArtifactID,
			candidate.Version, state, at.UTC(), at.UTC())
		if err != nil {
			if isDuplicateKey(err) {
				return fmt.Errorf("%w: source discovery candidate", ErrBusinessKeyExists)
			}
			return err
		}
	}
	return nil
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
