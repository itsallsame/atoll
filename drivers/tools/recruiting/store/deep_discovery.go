package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type DeepDiscoveryGraphPage struct {
	Nodes      []model.DiscoveryEvidenceNode `json:"nodes"`
	Edges      []model.DiscoveryEvidenceEdge `json:"edges"`
	NextCursor string                        `json:"next_cursor,omitempty"`
	HasMore    bool                          `json:"has_more"`
}

type deepDiscoveryCursor struct {
	NodeOrdinal int `json:"node_ordinal"`
	EdgeOrdinal int `json:"edge_ordinal"`
}

func (r *Repository) CreateDeepDiscoveryMission(ctx context.Context, expectedCompanyVersion uint64,
	mission model.DeepDiscoveryMission, at time.Time) error {
	_, err := r.createDeepDiscoveryMission(ctx, expectedCompanyVersion, mission, nil, nil, at)
	return err
}

func (r *Repository) ApplyCreateDeepDiscoveryMissionCommand(ctx context.Context, expectedCompanyVersion uint64,
	mission model.DeepDiscoveryMission, receipt model.CommandReceipt, event model.EventIntent, at time.Time) (CommandResult, error) {
	return r.createDeepDiscoveryMission(ctx, expectedCompanyVersion, mission, &receipt, &event, at)
}

func (r *Repository) createDeepDiscoveryMission(ctx context.Context, expectedCompanyVersion uint64,
	mission model.DeepDiscoveryMission, receipt *model.CommandReceipt, event *model.EventIntent, at time.Time) (CommandResult, error) {
	if mission.Version != 1 || mission.CompanyVersion != expectedCompanyVersion || at.IsZero() {
		return CommandResult{}, fmt.Errorf("new deep discovery mission is inconsistent")
	}
	var eventAt time.Time
	if receipt != nil || event != nil {
		if receipt == nil || event == nil || receipt.CommandID == "" || event.AggregateType != "deep_discovery" || event.AggregateID != mission.MissionID || event.AggregateVersion != mission.Version || event.CauseCommandID != receipt.CommandID {
			return CommandResult{}, fmt.Errorf("deep discovery command receipt and event are inconsistent")
		}
		var err error
		eventAt, err = time.Parse(time.RFC3339, event.BusinessAt)
		if err != nil {
			return CommandResult{}, err
		}
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return CommandResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if receipt != nil {
		if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
			return CommandResult{}, err
		} else if found {
			return CommandResult{Response: replay, Replayed: true}, nil
		}
	}
	var state []byte
	if err := tx.QueryRowContext(ctx, "SELECT state_json FROM recruiting_companies WHERE company_id = ? FOR SHARE", mission.CompanyID).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return CommandResult{}, ErrNotFound
		}
		return CommandResult{}, err
	}
	var company model.Company
	if err := json.Unmarshal(state, &company); err != nil {
		return CommandResult{}, err
	}
	if company.Version != expectedCompanyVersion {
		return CommandResult{}, &model.VersionConflictError{Expected: expectedCompanyVersion, Actual: company.Version}
	}
	if company.CompanyID != mission.CompanyID || company.Name != mission.CompanyName || company.Website != mission.SeedWebsite || company.ControlStatus != model.ControlActive {
		return CommandResult{}, fmt.Errorf("deep discovery mission does not match its active Company snapshot")
	}
	if receipt != nil {
		if err := reserveCommandReceipt(ctx, tx, *receipt, at); err != nil {
			return CommandResult{}, err
		}
	}
	encoded, _ := json.Marshal(mission)
	_, err = tx.ExecContext(ctx, `INSERT INTO recruiting_deep_discovery_missions(
mission_id, company_id, active_company_key, discovery_generation, company_version, mission_stage, mission_status, checkpoint_count, node_count, edge_count,
candidate_count, version, state_json, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, 0, 0, 0, 0, 1, ?, ?, ?)`,
		mission.MissionID, mission.CompanyID, mission.CompanyID, mission.Generation, mission.CompanyVersion, mission.Stage, mission.Status, encoded, at.UTC(), at.UTC())
	if err != nil {
		if isDuplicateKey(err) {
			return CommandResult{}, fmt.Errorf("%w: deep discovery mission", ErrBusinessKeyExists)
		}
		return CommandResult{}, err
	}
	if event != nil {
		if err := appendEventIntent(ctx, tx, *event, eventAt, at); err != nil {
			return CommandResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return CommandResult{}, err
	}
	if receipt != nil {
		return CommandResult{Response: append(json.RawMessage(nil), receipt.Response...)}, nil
	}
	return CommandResult{}, nil
}

func (r *Repository) GetDeepDiscoveryMission(ctx context.Context, missionID string) (model.DeepDiscoveryMission, error) {
	return getDeepDiscoveryMissionWith(ctx, r.db, strings.TrimSpace(missionID), false)
}

// PrepareDeepDiscoveryGraphDelta removes unchanged evidence identities and
// retains monotonic candidate resolutions. The node row is the current graph
// projection; checkpoint claims preserve every accepted state transition.
func (r *Repository) PrepareDeepDiscoveryGraphDelta(ctx context.Context, missionID string,
	nodes []model.DiscoveryEvidenceNode, edges []model.DiscoveryEvidenceEdge) ([]model.DiscoveryEvidenceNode, []model.DiscoveryEvidenceEdge, int, int, error) {
	missionID = strings.TrimSpace(missionID)
	if missionID == "" {
		return nil, nil, 0, 0, fmt.Errorf("mission ID is required")
	}
	return prepareDeepDiscoveryGraphDeltaWith(ctx, r.db, missionID, nodes, edges)
}

type deepDiscoveryRowQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func prepareDeepDiscoveryGraphDeltaWith(ctx context.Context, query deepDiscoveryRowQuerier, missionID string,
	nodes []model.DiscoveryEvidenceNode, edges []model.DiscoveryEvidenceEdge) ([]model.DiscoveryEvidenceNode, []model.DiscoveryEvidenceEdge, int, int, error) {
	claims := make([]model.DiscoveryEvidenceNode, 0, len(nodes))
	addedNodes, validatedCandidates := 0, 0
	for _, node := range nodes {
		var state []byte
		err := query.QueryRowContext(ctx, `SELECT state_json FROM recruiting_deep_discovery_nodes WHERE mission_id=? AND node_id=?`, missionID, node.NodeID).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			claims = append(claims, node)
			addedNodes++
			if node.Kind == model.EvidenceListURL && node.State == model.EvidenceValidated {
				validatedCandidates++
			}
			continue
		}
		if err != nil {
			return nil, nil, 0, 0, err
		}
		var existing model.DiscoveryEvidenceNode
		if err := json.Unmarshal(state, &existing); err != nil {
			return nil, nil, 0, 0, err
		}
		if existing.Kind != node.Kind || existing.CanonicalValue != node.CanonicalValue {
			return nil, nil, 0, 0, fmt.Errorf("deep discovery node identity collision")
		}
		if existing.State == node.State {
			continue
		}
		if existing.State != model.EvidenceCandidate || node.State == model.EvidenceCandidate {
			return nil, nil, 0, 0, fmt.Errorf("deep discovery evidence may only resolve a candidate to a terminal state")
		}
		if node.State != model.EvidenceValidated && node.State != model.EvidenceRejected && node.State != model.EvidenceExcluded {
			return nil, nil, 0, 0, fmt.Errorf("deep discovery candidate resolution state is invalid")
		}
		claims = append(claims, node)
		if node.Kind == model.EvidenceListURL && node.State == model.EvidenceValidated {
			validatedCandidates++
		}
	}
	newEdges := make([]model.DiscoveryEvidenceEdge, 0, len(edges))
	for _, edge := range edges {
		var state []byte
		err := query.QueryRowContext(ctx, `SELECT state_json FROM recruiting_deep_discovery_edges WHERE mission_id=? AND edge_id=?`, missionID, edge.EdgeID).Scan(&state)
		if errors.Is(err, sql.ErrNoRows) {
			newEdges = append(newEdges, edge)
			continue
		}
		if err != nil {
			return nil, nil, 0, 0, err
		}
		var existing model.DiscoveryEvidenceEdge
		if err := json.Unmarshal(state, &existing); err != nil {
			return nil, nil, 0, 0, err
		}
		if existing.FromNodeID != edge.FromNodeID || existing.ToNodeID != edge.ToNodeID || existing.Relation != edge.Relation {
			return nil, nil, 0, 0, fmt.Errorf("deep discovery edge identity collision")
		}
	}
	return claims, newEdges, addedNodes, validatedCandidates, nil
}

func getDeepDiscoveryMissionWith(ctx context.Context, query interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, missionID string, lock bool) (model.DeepDiscoveryMission, error) {
	if missionID == "" {
		return model.DeepDiscoveryMission{}, fmt.Errorf("mission ID is required")
	}
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	var state []byte
	err := query.QueryRowContext(ctx, "SELECT state_json FROM recruiting_deep_discovery_missions WHERE mission_id = ?"+suffix, missionID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.DeepDiscoveryMission{}, ErrNotFound
	}
	if err != nil {
		return model.DeepDiscoveryMission{}, err
	}
	var mission model.DeepDiscoveryMission
	if err := json.Unmarshal(state, &mission); err != nil {
		return model.DeepDiscoveryMission{}, err
	}
	return mission, nil
}

// CheckpointDeepDiscovery atomically appends an immutable evidence graph delta
// and advances at most one mission stage. Existing graph nodes are never
// silently overwritten; a later claim must cite a distinct evidence node.
func (r *Repository) CheckpointDeepDiscovery(ctx context.Context, missionID, commandID, summary string,
	expected uint64, nextStage model.DeepDiscoveryStage, coverage model.DiscoveryCoverage,
	searchRounds, operations int, nodes []model.DiscoveryEvidenceNode, edges []model.DiscoveryEvidenceEdge,
	at time.Time) (model.DeepDiscoveryMission, error) {
	next, _, err := r.checkpointDeepDiscovery(ctx, missionID, commandID, summary, expected, nextStage, coverage, searchRounds, operations, nodes, edges, nil, nil, at)
	return next, err
}

func (r *Repository) ApplyDeepDiscoveryCheckpointCommand(ctx context.Context, missionID, commandID, summary string,
	expected uint64, nextStage model.DeepDiscoveryStage, coverage model.DiscoveryCoverage,
	searchRounds, operations int, nodes []model.DiscoveryEvidenceNode, edges []model.DiscoveryEvidenceEdge,
	receipt model.CommandReceipt, event model.EventIntent, at time.Time) (CommandResult, error) {
	_, result, err := r.checkpointDeepDiscovery(ctx, missionID, commandID, summary, expected, nextStage, coverage, searchRounds, operations, nodes, edges, &receipt, &event, at)
	return result, err
}

func (r *Repository) checkpointDeepDiscovery(ctx context.Context, missionID, commandID, summary string,
	expected uint64, nextStage model.DeepDiscoveryStage, coverage model.DiscoveryCoverage,
	searchRounds, operations int, nodes []model.DiscoveryEvidenceNode, edges []model.DiscoveryEvidenceEdge,
	receipt *model.CommandReceipt, event *model.EventIntent, at time.Time) (model.DeepDiscoveryMission, CommandResult, error) {
	missionID, commandID, summary = strings.TrimSpace(missionID), strings.TrimSpace(commandID), strings.TrimSpace(summary)
	if missionID == "" || commandID == "" || summary == "" || len(summary) > 2048 || len(nodes) > 200 || len(edges) > 400 {
		return model.DeepDiscoveryMission{}, CommandResult{}, fmt.Errorf("bounded checkpoint identity, summary, and graph delta are required")
	}
	var eventAt time.Time
	if receipt != nil || event != nil {
		if receipt == nil || event == nil || receipt.CommandID != commandID || event.AggregateType != "deep_discovery" || event.AggregateID != missionID || event.CauseCommandID != commandID {
			return model.DeepDiscoveryMission{}, CommandResult{}, fmt.Errorf("deep discovery checkpoint receipt and event are inconsistent")
		}
		var err error
		eventAt, err = time.Parse(time.RFC3339, event.BusinessAt)
		if err != nil {
			return model.DeepDiscoveryMission{}, CommandResult{}, err
		}
	}
	nodeIDs := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		validated, err := model.NewDiscoveryEvidenceNodeWithType(node.Kind, node.CanonicalValue, node.Label, node.State, node.Sensor, node.EvidenceURL, node.EvidenceArtifactID, node.Basis, node.RecruitmentType, node.SpecialProgram)
		if err != nil || validated != node {
			return model.DeepDiscoveryMission{}, CommandResult{}, fmt.Errorf("deep discovery node is not canonical")
		}
		if _, duplicate := nodeIDs[node.NodeID]; duplicate {
			return model.DeepDiscoveryMission{}, CommandResult{}, fmt.Errorf("duplicate deep discovery node")
		}
		nodeIDs[node.NodeID] = struct{}{}
	}
	edgeIDs := make(map[string]struct{}, len(edges))
	for _, edge := range edges {
		validated, err := model.NewDiscoveryEvidenceEdge(edge.FromNodeID, edge.ToNodeID, edge.Relation, edge.Basis)
		if err != nil || validated != edge {
			return model.DeepDiscoveryMission{}, CommandResult{}, fmt.Errorf("deep discovery edge is not canonical")
		}
		if _, duplicate := edgeIDs[edge.EdgeID]; duplicate {
			return model.DeepDiscoveryMission{}, CommandResult{}, fmt.Errorf("duplicate deep discovery edge")
		}
		edgeIDs[edge.EdgeID] = struct{}{}
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return model.DeepDiscoveryMission{}, CommandResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if receipt != nil {
		if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
			return model.DeepDiscoveryMission{}, CommandResult{}, err
		} else if found {
			return model.DeepDiscoveryMission{}, CommandResult{Response: replay, Replayed: true}, nil
		}
	}
	current, err := getDeepDiscoveryMissionWith(ctx, tx, missionID, true)
	if err != nil {
		return model.DeepDiscoveryMission{}, CommandResult{}, err
	}
	nodes, edges, addedNodes, candidateCount, err := prepareDeepDiscoveryGraphDeltaWith(ctx, tx, missionID, nodes, edges)
	if err != nil {
		return model.DeepDiscoveryMission{}, CommandResult{}, err
	}
	nodeIDs = make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		nodeIDs[node.NodeID] = struct{}{}
	}
	edgeIDs = make(map[string]struct{}, len(edges))
	for _, edge := range edges {
		edgeIDs[edge.EdgeID] = struct{}{}
	}
	if err := validateDeepDiscoveryStageEvidence(ctx, tx, missionID, nextStage, nodes); err != nil {
		return model.DeepDiscoveryMission{}, CommandResult{}, err
	}
	next, err := current.Checkpoint(expected, nextStage, coverage, searchRounds, operations, addedNodes, len(edges), candidateCount)
	if err != nil {
		return model.DeepDiscoveryMission{}, CommandResult{}, err
	}
	for _, edge := range edges {
		for _, id := range []string{edge.FromNodeID, edge.ToNodeID} {
			if _, incoming := nodeIDs[id]; incoming {
				continue
			}
			var exists int
			if err := tx.QueryRowContext(ctx, "SELECT 1 FROM recruiting_deep_discovery_nodes WHERE mission_id = ? AND node_id = ?", missionID, id).Scan(&exists); err != nil {
				if errors.Is(err, sql.ErrNoRows) {
					return model.DeepDiscoveryMission{}, CommandResult{}, fmt.Errorf("deep discovery edge references unknown node")
				}
				return model.DeepDiscoveryMission{}, CommandResult{}, err
			}
		}
	}
	createdNodes := 0
	for _, node := range nodes {
		encoded, _ := json.Marshal(node)
		var previousState string
		err = tx.QueryRowContext(ctx, `SELECT node_state FROM recruiting_deep_discovery_nodes WHERE mission_id=? AND node_id=?`, missionID, node.NodeID).Scan(&previousState)
		revision := uint64(1)
		if errors.Is(err, sql.ErrNoRows) {
			_, err = tx.ExecContext(ctx, `INSERT INTO recruiting_deep_discovery_nodes(
mission_id,node_id,node_ordinal,node_kind,node_state,canonical_value,state_json,created_at) VALUES (?,?,?,?,?,?,?,?)`, missionID, node.NodeID, current.NodeCount+createdNodes, node.Kind, node.State, node.CanonicalValue, encoded, at.UTC())
			createdNodes++
		} else if err == nil {
			result, updateErr := tx.ExecContext(ctx, `UPDATE recruiting_deep_discovery_nodes SET node_state=?,state_json=?
WHERE mission_id=? AND node_id=? AND node_state='candidate'`, node.State, encoded, missionID, node.NodeID)
			if updateErr != nil {
				return model.DeepDiscoveryMission{}, CommandResult{}, updateErr
			}
			if changed, _ := result.RowsAffected(); changed != 1 {
				return model.DeepDiscoveryMission{}, CommandResult{}, fmt.Errorf("deep discovery candidate state changed concurrently")
			}
			if err := tx.QueryRowContext(ctx, `SELECT COALESCE(MAX(claim_revision),0)+1 FROM recruiting_deep_discovery_node_claims
WHERE mission_id=? AND node_id=?`, missionID, node.NodeID).Scan(&revision); err != nil {
				return model.DeepDiscoveryMission{}, CommandResult{}, err
			}
		} else {
			return model.DeepDiscoveryMission{}, CommandResult{}, err
		}
		if err != nil {
			if isDuplicateKey(err) {
				return model.DeepDiscoveryMission{}, CommandResult{}, fmt.Errorf("%w: deep discovery node", ErrBusinessKeyExists)
			}
			return model.DeepDiscoveryMission{}, CommandResult{}, err
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO recruiting_deep_discovery_node_claims(
mission_id,node_id,claim_revision,checkpoint_sequence,command_id,node_state,state_json,created_at) VALUES (?,?,?,?,?,?,?,?)`,
			missionID, node.NodeID, revision, next.CheckpointCount, commandID, node.State, encoded, at.UTC()); err != nil {
			return model.DeepDiscoveryMission{}, CommandResult{}, err
		}
	}
	for index, edge := range edges {
		encoded, _ := json.Marshal(edge)
		_, err = tx.ExecContext(ctx, `INSERT INTO recruiting_deep_discovery_edges(
mission_id,edge_id,edge_ordinal,from_node_id,to_node_id,relation_kind,state_json,created_at) VALUES (?,?,?,?,?,?,?,?)`, missionID, edge.EdgeID, current.EdgeCount+index, edge.FromNodeID, edge.ToNodeID, edge.Relation, encoded, at.UTC())
		if err != nil {
			if isDuplicateKey(err) {
				return model.DeepDiscoveryMission{}, CommandResult{}, fmt.Errorf("%w: deep discovery edge", ErrBusinessKeyExists)
			}
			return model.DeepDiscoveryMission{}, CommandResult{}, err
		}
	}
	encoded, _ := json.Marshal(next)
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_deep_discovery_missions SET mission_stage=?,mission_status=?,checkpoint_count=?,node_count=?,edge_count=?,candidate_count=?,version=?,state_json=?,updated_at=? WHERE mission_id=? AND version=?`, next.Stage, next.Status, next.CheckpointCount, next.NodeCount, next.EdgeCount, next.CandidateCount, next.Version, encoded, at.UTC(), missionID, current.Version)
	if err != nil {
		return model.DeepDiscoveryMission{}, CommandResult{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return model.DeepDiscoveryMission{}, CommandResult{}, &model.VersionConflictError{Expected: expected, Actual: current.Version}
	}
	checkpoint, _ := json.Marshal(map[string]any{"mission": next, "node_ids": mapKeys(nodeIDs), "edge_ids": mapKeys(edgeIDs), "node_claims": nodes})
	_, err = tx.ExecContext(ctx, `INSERT INTO recruiting_deep_discovery_checkpoints(mission_id,checkpoint_sequence,command_id,mission_version,stage_name,summary,state_json,created_at) VALUES (?,?,?,?,?,?,?,?)`, missionID, next.CheckpointCount, commandID, next.Version, next.Stage, summary, checkpoint, at.UTC())
	if err != nil {
		if isDuplicateKey(err) {
			return model.DeepDiscoveryMission{}, CommandResult{}, fmt.Errorf("%w: deep discovery checkpoint command", ErrBusinessKeyExists)
		}
		return model.DeepDiscoveryMission{}, CommandResult{}, err
	}
	if receipt != nil {
		if event.AggregateVersion != next.Version {
			return model.DeepDiscoveryMission{}, CommandResult{}, fmt.Errorf("deep discovery checkpoint event version is inconsistent")
		}
		if err := reserveCommandReceipt(ctx, tx, *receipt, at); err != nil {
			return model.DeepDiscoveryMission{}, CommandResult{}, err
		}
		if err := appendEventIntent(ctx, tx, *event, eventAt, at); err != nil {
			return model.DeepDiscoveryMission{}, CommandResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return model.DeepDiscoveryMission{}, CommandResult{}, err
	}
	commandResult := CommandResult{}
	if receipt != nil {
		commandResult.Response = append(json.RawMessage(nil), receipt.Response...)
	}
	return next, commandResult, nil
}

func validateDeepDiscoveryStageEvidence(ctx context.Context, tx *sql.Tx, missionID string, stage model.DeepDiscoveryStage,
	incoming []model.DiscoveryEvidenceNode) error {
	requiredKinds := []model.DiscoveryEvidenceKind{}
	switch stage {
	case model.DeepDiscoveryBrandExpansion:
		requiredKinds = []model.DiscoveryEvidenceKind{model.EvidenceCompany}
	case model.DeepDiscoverySiteEnumeration:
		// Brand review can legitimately conclude that the requested Company has
		// no separately modelled brands or legal entities. Checkpoint enforces
		// BrandsReviewed independently; the validated Company identity keeps the
		// evidence graph grounded without fabricating a child Brand node.
		requiredKinds = []model.DiscoveryEvidenceKind{model.EvidenceCompany, model.EvidenceBrand, model.EvidenceLegalEntity}
	case model.DeepDiscoverySiteExploration:
		requiredKinds = []model.DiscoveryEvidenceKind{model.EvidenceDomain, model.EvidenceSite}
	case model.DeepDiscoveryPoolDetection:
		requiredKinds = []model.DiscoveryEvidenceKind{model.EvidenceSite}
	case model.DeepDiscoveryCandidateValidation:
		requiredKinds = []model.DiscoveryEvidenceKind{model.EvidenceListingPool, model.EvidenceAPIEndpoint}
	case model.DeepDiscoveryCoverageReview:
		requiredKinds = []model.DiscoveryEvidenceKind{model.EvidenceListURL}
	default:
		return nil
	}
	for _, node := range incoming {
		if deepDiscoveryEvidenceSupportsStage(stage, node) {
			for _, kind := range requiredKinds {
				if node.Kind == kind {
					return nil
				}
			}
		}
	}
	args := make([]any, 0, len(requiredKinds)+1)
	args = append(args, missionID)
	marks := make([]string, len(requiredKinds))
	for index, kind := range requiredKinds {
		marks[index] = "?"
		args = append(args, kind)
	}
	query := `SELECT state_json FROM recruiting_deep_discovery_nodes WHERE mission_id=? AND node_kind IN (` + strings.Join(marks, ",") + `) AND node_state IN ('candidate','validated')`
	rows, err := tx.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	supported := false
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		var node model.DiscoveryEvidenceNode
		if err := json.Unmarshal(raw, &node); err != nil {
			return err
		}
		if deepDiscoveryEvidenceSupportsStage(stage, node) {
			supported = true
			break
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !supported {
		return fmt.Errorf("deep discovery stage %s is not supported by its evidence graph", stage)
	}
	return nil
}

func deepDiscoveryEvidenceSupportsStage(stage model.DeepDiscoveryStage, node model.DiscoveryEvidenceNode) bool {
	if node.State == model.EvidenceRejected || node.State == model.EvidenceExcluded {
		return false
	}
	switch stage {
	case model.DeepDiscoveryPoolDetection:
		return node.State == model.EvidenceValidated && (node.Sensor == model.SensorOfficialSite || node.Sensor == model.SensorSitemap || node.Sensor == model.SensorBrowser || node.Sensor == model.SensorNetwork)
	case model.DeepDiscoveryCandidateValidation:
		return node.Sensor == model.SensorOfficialSite || node.Sensor == model.SensorBrowser || node.Sensor == model.SensorNetwork || node.Sensor == model.SensorATS || node.Sensor == model.SensorDetailReverse
	case model.DeepDiscoveryCoverageReview:
		return node.State == model.EvidenceValidated && (node.Sensor == model.SensorOfficialSite || node.Sensor == model.SensorBrowser || node.Sensor == model.SensorNetwork || node.Sensor == model.SensorATS || node.Sensor == model.SensorDetailReverse)
	default:
		return true
	}
}

func mapKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for key := range values {
		result = append(result, key)
	}
	return result
}

func (r *Repository) UpdateDeepDiscoveryStatus(ctx context.Context, missionID string, expected uint64, action, reason string, at time.Time) (model.DeepDiscoveryMission, error) {
	next, _, err := r.updateDeepDiscoveryStatus(ctx, missionID, expected, action, reason, nil, nil, at)
	return next, err
}

func (r *Repository) ApplyDeepDiscoveryStatusCommand(ctx context.Context, missionID string, expected uint64,
	action, reason string, receipt model.CommandReceipt, event model.EventIntent, at time.Time) (CommandResult, error) {
	_, result, err := r.updateDeepDiscoveryStatus(ctx, missionID, expected, action, reason, &receipt, &event, at)
	return result, err
}

func (r *Repository) updateDeepDiscoveryStatus(ctx context.Context, missionID string, expected uint64, action, reason string,
	receipt *model.CommandReceipt, event *model.EventIntent, at time.Time) (model.DeepDiscoveryMission, CommandResult, error) {
	var eventAt time.Time
	if receipt != nil || event != nil {
		if receipt == nil || event == nil || receipt.CommandID == "" || event.AggregateType != "deep_discovery" || event.AggregateID != strings.TrimSpace(missionID) || event.CauseCommandID != receipt.CommandID {
			return model.DeepDiscoveryMission{}, CommandResult{}, fmt.Errorf("deep discovery status receipt and event are inconsistent")
		}
		var err error
		eventAt, err = time.Parse(time.RFC3339, event.BusinessAt)
		if err != nil {
			return model.DeepDiscoveryMission{}, CommandResult{}, err
		}
	}
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if err != nil {
		return model.DeepDiscoveryMission{}, CommandResult{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if receipt != nil {
		if replay, found, err := readCommandReceipt(ctx, tx, receipt.CommandID, receipt.RequestHash); err != nil {
			return model.DeepDiscoveryMission{}, CommandResult{}, err
		} else if found {
			return model.DeepDiscoveryMission{}, CommandResult{Response: replay, Replayed: true}, nil
		}
	}
	current, err := getDeepDiscoveryMissionWith(ctx, tx, strings.TrimSpace(missionID), true)
	if err != nil {
		return model.DeepDiscoveryMission{}, CommandResult{}, err
	}
	var next model.DeepDiscoveryMission
	switch action {
	case "wait":
		next, err = current.WaitForHuman(expected, reason)
	case "resume":
		next, err = current.Resume(expected)
	case "complete":
		if err = ensureDeepDiscoveryListURLClassifications(ctx, tx, current.MissionID); err != nil {
			return model.DeepDiscoveryMission{}, CommandResult{}, err
		}
		next, err = current.Complete(expected)
	case "cancel":
		next, err = current.Cancel(expected, reason)
	default:
		err = fmt.Errorf("unsupported deep discovery action")
	}
	if err != nil {
		return model.DeepDiscoveryMission{}, CommandResult{}, err
	}
	encoded, _ := json.Marshal(next)
	activeKey := any(next.CompanyID)
	if next.Status == model.DeepDiscoveryDone || next.Status == model.DeepDiscoveryCanceled {
		activeKey = nil
	}
	result, err := tx.ExecContext(ctx, `UPDATE recruiting_deep_discovery_missions SET active_company_key=?,mission_stage=?,mission_status=?,version=?,state_json=?,updated_at=? WHERE mission_id=? AND version=?`, activeKey, next.Stage, next.Status, next.Version, encoded, at.UTC(), next.MissionID, current.Version)
	if err != nil {
		return model.DeepDiscoveryMission{}, CommandResult{}, err
	}
	if changed, _ := result.RowsAffected(); changed != 1 {
		return model.DeepDiscoveryMission{}, CommandResult{}, &model.VersionConflictError{Expected: expected, Actual: current.Version}
	}
	if receipt != nil {
		if event.AggregateVersion != next.Version {
			return model.DeepDiscoveryMission{}, CommandResult{}, fmt.Errorf("deep discovery status event version is inconsistent")
		}
		if err := reserveCommandReceipt(ctx, tx, *receipt, at); err != nil {
			return model.DeepDiscoveryMission{}, CommandResult{}, err
		}
		if err := appendEventIntent(ctx, tx, *event, eventAt, at); err != nil {
			return model.DeepDiscoveryMission{}, CommandResult{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return model.DeepDiscoveryMission{}, CommandResult{}, err
	}
	commandResult := CommandResult{}
	if receipt != nil {
		commandResult.Response = append(json.RawMessage(nil), receipt.Response...)
	}
	return next, commandResult, nil
}

func ensureDeepDiscoveryListURLClassifications(ctx context.Context, tx *sql.Tx, missionID string) error {
	rows, err := tx.QueryContext(ctx, `SELECT state_json FROM recruiting_deep_discovery_nodes
WHERE mission_id=? AND node_kind='list_url' FOR SHARE`, missionID)
	if err != nil {
		return err
	}
	defer rows.Close()
	validatedCount, unresolvedCount := 0, 0
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return err
		}
		var node model.DiscoveryEvidenceNode
		if err := json.Unmarshal(raw, &node); err != nil {
			return err
		}
		switch node.State {
		case model.EvidenceCandidate:
			unresolvedCount++
		case model.EvidenceValidated:
			validated, validationErr := model.NewDiscoveryEvidenceNodeWithType(node.Kind, node.CanonicalValue, node.Label,
				node.State, node.Sensor, node.EvidenceURL, node.EvidenceArtifactID, node.Basis, node.RecruitmentType, node.SpecialProgram)
			if validationErr != nil || validated != node {
				return fmt.Errorf("validated list URL %s lacks an evidence-backed recruitment type", node.CanonicalValue)
			}
			validatedCount++
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if unresolvedCount != 0 {
		return fmt.Errorf("deep discovery completion requires every list URL candidate to be validated, rejected, or excluded")
	}
	if validatedCount == 0 {
		return fmt.Errorf("deep discovery completion requires a classified validated list URL")
	}
	return nil
}

func (r *Repository) ListDeepDiscoveryGraph(ctx context.Context, missionID, cursor string, limit int) (DeepDiscoveryGraphPage, error) {
	if strings.TrimSpace(missionID) == "" || limit < 1 || limit > 500 {
		return DeepDiscoveryGraphPage{}, fmt.Errorf("mission ID and limit in [1,500] are required")
	}
	position := deepDiscoveryCursor{NodeOrdinal: -1, EdgeOrdinal: -1}
	if strings.TrimSpace(cursor) != "" {
		raw, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil || json.Unmarshal(raw, &position) != nil {
			return DeepDiscoveryGraphPage{}, fmt.Errorf("%w: deep discovery graph", ErrInvalidCursor)
		}
	}
	page := DeepDiscoveryGraphPage{}
	rows, err := r.db.QueryContext(ctx, `SELECT node_ordinal,state_json FROM recruiting_deep_discovery_nodes WHERE mission_id=? AND node_ordinal>? ORDER BY node_ordinal LIMIT ?`, missionID, position.NodeOrdinal, limit+1)
	if err != nil {
		return page, err
	}
	lastNode := position.NodeOrdinal
	for rows.Next() {
		var ordinal int
		var raw []byte
		if err := rows.Scan(&ordinal, &raw); err != nil {
			_ = rows.Close()
			return page, err
		}
		if len(page.Nodes) < limit {
			var node model.DiscoveryEvidenceNode
			if err := json.Unmarshal(raw, &node); err != nil {
				_ = rows.Close()
				return page, err
			}
			page.Nodes = append(page.Nodes, node)
			lastNode = ordinal
		} else {
			page.HasMore = true
		}
	}
	_ = rows.Close()
	remaining := limit - len(page.Nodes)
	if remaining > 0 {
		rows, err = r.db.QueryContext(ctx, `SELECT edge_ordinal,state_json FROM recruiting_deep_discovery_edges WHERE mission_id=? AND edge_ordinal>? ORDER BY edge_ordinal LIMIT ?`, missionID, position.EdgeOrdinal, remaining+1)
		if err != nil {
			return page, err
		}
		lastEdge := position.EdgeOrdinal
		for rows.Next() {
			var ordinal int
			var raw []byte
			if err := rows.Scan(&ordinal, &raw); err != nil {
				_ = rows.Close()
				return page, err
			}
			if len(page.Edges) < remaining {
				var edge model.DiscoveryEvidenceEdge
				if err := json.Unmarshal(raw, &edge); err != nil {
					_ = rows.Close()
					return page, err
				}
				page.Edges = append(page.Edges, edge)
				lastEdge = ordinal
			} else {
				page.HasMore = true
			}
		}
		_ = rows.Close()
		position.EdgeOrdinal = lastEdge
	} else if !page.HasMore {
		var exists int
		err := r.db.QueryRowContext(ctx, `SELECT 1 FROM recruiting_deep_discovery_edges
WHERE mission_id=? AND edge_ordinal>? ORDER BY edge_ordinal LIMIT 1`, missionID, position.EdgeOrdinal).Scan(&exists)
		if err == nil {
			page.HasMore = true
		} else if !errors.Is(err, sql.ErrNoRows) {
			return page, err
		}
	}
	position.NodeOrdinal = lastNode
	if page.HasMore {
		raw, _ := json.Marshal(position)
		page.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return page, nil
}
