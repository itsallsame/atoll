package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

// FindCompaniesByExactName resolves the natural-language onboarding key. A
// name is not globally unique, so callers must route multiple matches to a
// human instead of silently selecting one.
func (r *Repository) FindCompaniesByExactName(ctx context.Context, name string, limit int) ([]model.Company, error) {
	name = strings.TrimSpace(name)
	if name == "" || limit < 1 || limit > 20 {
		return nil, fmt.Errorf("company name and limit in [1,20] are required")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT state_json FROM recruiting_companies
WHERE normalized_name = LOWER(TRIM(?)) ORDER BY company_id LIMIT ?`, name, limit)
	if err != nil {
		return nil, fmt.Errorf("find Company by exact name: %w", err)
	}
	defer rows.Close()
	companies := make([]model.Company, 0, limit)
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var company model.Company
		if err := json.Unmarshal(raw, &company); err != nil {
			return nil, err
		}
		companies = append(companies, company)
	}
	return companies, rows.Err()
}

func (r *Repository) GetLatestDeepDiscoveryMissionForCompany(ctx context.Context, companyID string) (model.DeepDiscoveryMission, error) {
	companyID = strings.TrimSpace(companyID)
	if companyID == "" {
		return model.DeepDiscoveryMission{}, fmt.Errorf("company ID is required")
	}
	var raw []byte
	err := r.db.QueryRowContext(ctx, `SELECT state_json FROM recruiting_deep_discovery_missions
WHERE company_id=? ORDER BY discovery_generation DESC, mission_id DESC LIMIT 1`, companyID).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return model.DeepDiscoveryMission{}, ErrNotFound
	}
	if err != nil {
		return model.DeepDiscoveryMission{}, err
	}
	var mission model.DeepDiscoveryMission
	if err := json.Unmarshal(raw, &mission); err != nil {
		return model.DeepDiscoveryMission{}, err
	}
	return mission, nil
}

func (r *Repository) ListValidatedDeepDiscoveryURLs(ctx context.Context, missionID string) ([]model.DiscoveryEvidenceNode, error) {
	missionID = strings.TrimSpace(missionID)
	if missionID == "" {
		return nil, fmt.Errorf("mission ID is required")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT state_json FROM recruiting_deep_discovery_nodes
WHERE mission_id=? AND node_kind='list_url' AND node_state='validated' ORDER BY node_ordinal LIMIT 2001`, missionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]model.DiscoveryEvidenceNode, 0)
	for rows.Next() {
		if len(result) == 2000 {
			return nil, fmt.Errorf("validated Deep Discovery URL result exceeds frozen mission bound")
		}
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		var node model.DiscoveryEvidenceNode
		if err := json.Unmarshal(raw, &node); err != nil {
			return nil, err
		}
		result = append(result, node)
	}
	return result, rows.Err()
}

// ApplyCreateOnboardingCompanyMission is the atomic zero-to-one boundary for
// a user who supplies only a company name. The Agent may explore afterwards,
// but it can never leave an orphan Company or Mission if this transaction
// fails.
func (r *Repository) ApplyCreateOnboardingCompanyMission(ctx context.Context, company model.Company,
	mission model.DeepDiscoveryMission, receipt model.CommandReceipt, events []model.EventIntent,
	businessAt time.Time) (CommandResult, error) {
	if company.Version != 1 || mission.Version != 1 || mission.CompanyID != company.CompanyID ||
		mission.CompanyVersion != company.Version || receipt.CommandID == "" || len(events) != 2 || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("onboarding Company, Mission, receipt, and events are inconsistent")
	}
	for _, event := range events {
		if event.CauseCommandID != receipt.CommandID || event.AggregateVersion != 1 ||
			(event.AggregateID != company.CompanyID && event.AggregateID != mission.MissionID) {
			return CommandResult{}, fmt.Errorf("onboarding event is inconsistent")
		}
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		return CommandResult{}, err
	}
	if err := insertCompany(ctx, tx, company, businessAt); err != nil {
		return CommandResult{}, err
	}
	missionState, _ := json.Marshal(mission)
	_, err = tx.ExecContext(ctx, `INSERT INTO recruiting_deep_discovery_missions(
mission_id,company_id,active_company_key,discovery_generation,company_version,mission_stage,mission_status,
checkpoint_count,node_count,edge_count,candidate_count,version,state_json,created_at,updated_at)
VALUES (?,?,?,?,?,?,?,0,0,0,0,1,?,?,?)`, mission.MissionID, company.CompanyID, company.CompanyID,
		mission.Generation, mission.CompanyVersion, mission.Stage, mission.Status, missionState, businessAt.UTC(), businessAt.UTC())
	if err != nil {
		var mysqlError *mysql.MySQLError
		if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
			return CommandResult{}, fmt.Errorf("%w: onboarding Deep Discovery Mission", ErrBusinessKeyExists)
		}
		return CommandResult{}, err
	}
	for _, event := range events {
		eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
		if err != nil {
			return CommandResult{}, err
		}
		if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
			return CommandResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, err
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}

func (r *Repository) ApplyMaterializeDeepDiscoverySources(ctx context.Context, missionID string,
	sources []model.RecruitmentSource, receipt model.CommandReceipt, events []model.EventIntent,
	businessAt time.Time) (CommandResult, error) {
	missionID = strings.TrimSpace(missionID)
	if missionID == "" || len(sources) < 1 || len(sources) > 2000 || len(events) != len(sources) ||
		receipt.CommandID == "" || businessAt.IsZero() {
		return CommandResult{}, fmt.Errorf("bounded Deep Discovery source materialization facts are required")
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
		return CommandResult{}, err
	} else if found {
		return CommandResult{Response: replay, Replayed: true}, nil
	}
	mission, err := getDeepDiscoveryMissionWith(ctx, tx, missionID, false)
	if err != nil {
		return CommandResult{}, err
	}
	if mission.Status != model.DeepDiscoveryDone {
		return CommandResult{}, fmt.Errorf("completed Deep Discovery Mission is required before Source materialization")
	}
	var companyControl model.ControlStatus
	if err := tx.QueryRowContext(ctx, `SELECT control_status FROM recruiting_companies WHERE company_id=? FOR SHARE`,
		mission.CompanyID).Scan(&companyControl); err != nil {
		return CommandResult{}, err
	}
	if companyControl == model.ControlArchived {
		return CommandResult{}, fmt.Errorf("cannot materialize Sources for an archived Company")
	}
	validatedKeys := make(map[string]struct{})
	rows, err := tx.QueryContext(ctx, `SELECT state_json FROM recruiting_deep_discovery_nodes
WHERE mission_id=? AND node_kind='list_url' AND node_state='validated' ORDER BY node_ordinal LIMIT 2001`, missionID)
	if err != nil {
		return CommandResult{}, err
	}
	for rows.Next() {
		if len(validatedKeys) == 2000 {
			_ = rows.Close()
			return CommandResult{}, fmt.Errorf("validated Deep Discovery URL result exceeds frozen mission bound")
		}
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			_ = rows.Close()
			return CommandResult{}, err
		}
		var node model.DiscoveryEvidenceNode
		if err := json.Unmarshal(raw, &node); err != nil {
			_ = rows.Close()
			return CommandResult{}, err
		}
		category, err := node.SourceCategory()
		if err != nil {
			_ = rows.Close()
			return CommandResult{}, fmt.Errorf("completed Mission contains unclassified URL evidence: %w", err)
		}
		key, err := model.CanonicalSourceKey(node.CanonicalValue, category)
		if err != nil {
			_ = rows.Close()
			return CommandResult{}, err
		}
		validatedKeys[key] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return CommandResult{}, err
	}
	if err := rows.Close(); err != nil {
		return CommandResult{}, err
	}
	seenIDs, seenKeys := map[string]struct{}{}, map[string]struct{}{}
	for index, source := range sources {
		if source.CompanyID != mission.CompanyID || source.DiscoveryGeneration != mission.Generation || source.Version != 1 {
			return CommandResult{}, fmt.Errorf("materialized Source does not match its completed Mission")
		}
		endpoint, origin, err := sourceStorageIdentity(source)
		if err != nil {
			return CommandResult{}, err
		}
		if _, duplicate := seenIDs[source.SourceID]; duplicate {
			return CommandResult{}, fmt.Errorf("duplicate materialized Source ID")
		}
		if _, duplicate := seenKeys[endpoint.CanonicalKey]; duplicate {
			return CommandResult{}, fmt.Errorf("duplicate materialized Source endpoint")
		}
		if _, validated := validatedKeys[endpoint.CanonicalKey]; !validated {
			return CommandResult{}, fmt.Errorf("materialized Source is not backed by classified URL evidence from its Mission")
		}
		seenIDs[source.SourceID], seenKeys[endpoint.CanonicalKey] = struct{}{}, struct{}{}
		event := events[index]
		if event.AggregateType != "source" || event.AggregateID != source.SourceID || event.AggregateVersion != 1 ||
			event.CauseCommandID != receipt.CommandID {
			return CommandResult{}, fmt.Errorf("materialized Source event is inconsistent")
		}
		if err := insertSourceWith(ctx, tx, source, endpoint, origin, businessAt); err != nil {
			return CommandResult{}, err
		}
	}
	if err := reserveCommandReceipt(ctx, tx, receipt, businessAt); err != nil {
		return CommandResult{}, err
	}
	for _, event := range events {
		eventAt, err := time.Parse(time.RFC3339, event.BusinessAt)
		if err != nil {
			return CommandResult{}, err
		}
		if err := appendEventIntent(ctx, tx, event, eventAt, businessAt); err != nil {
			return CommandResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, err
	}
	return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
}
