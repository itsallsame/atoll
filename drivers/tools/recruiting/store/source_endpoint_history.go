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

func appendSourceEndpointActivation(ctx context.Context, tx *sql.Tx, current,
	published model.RecruitmentSource, assignment model.SourceRecipeAssignment, businessAt time.Time) error {
	if current.ActiveEndpoint == nil || current.CandidateEndpoint == nil || published.ActiveEndpoint == nil ||
		*current.ActiveEndpoint == *current.CandidateEndpoint {
		return nil
	}
	var state []byte
	err := tx.QueryRowContext(ctx, `SELECT state_json FROM recruiting_source_endpoint_changes
WHERE source_id = ? AND to_revision = ? FOR UPDATE`, current.SourceID, current.CandidateEndpoint.Revision).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("endpoint replacement has no immutable staged change fact")
	}
	if err != nil {
		return fmt.Errorf("lock Source endpoint change: %w", err)
	}
	var change model.SourceEndpointChange
	if err := json.Unmarshal(state, &change); err != nil {
		return fmt.Errorf("decode Source endpoint change: %w", err)
	}
	if change.SourceID != current.SourceID || change.FromEndpoint != *current.ActiveEndpoint ||
		change.ToEndpoint != *current.CandidateEndpoint || change.StagedSourceVersion > current.Version {
		return fmt.Errorf("Source endpoint change does not match validation cutover")
	}
	activation, err := model.NewSourceEndpointActivation(change.ChangeID, change, published, assignment,
		published.ContractAssessment.EvidenceArtifactIDs, businessAt)
	if err != nil {
		return err
	}
	activationState, err := json.Marshal(activation)
	if err != nil {
		return fmt.Errorf("encode Source endpoint activation: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO recruiting_source_endpoint_activations(
  activation_id, change_id, source_id, from_revision, to_revision, activated_source_version,
  state_json, activated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`, activation.ActivationID, activation.ChangeID, activation.SourceID,
		activation.FromEndpoint.Revision, activation.ToEndpoint.Revision, activation.ActivatedSourceVersion,
		activationState, businessAt.UTC()); err != nil {
		if isDuplicateKey(err) {
			return fmt.Errorf("%w: Source endpoint activation", ErrBusinessKeyExists)
		}
		return fmt.Errorf("insert Source endpoint activation: %w", err)
	}
	return nil
}

func (r *Repository) GetSourceEndpointChange(ctx context.Context, changeID string) (model.SourceEndpointChange, error) {
	var state []byte
	err := r.db.QueryRowContext(ctx, `SELECT state_json FROM recruiting_source_endpoint_changes WHERE change_id = ?`,
		strings.TrimSpace(changeID)).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.SourceEndpointChange{}, ErrNotFound
	}
	if err != nil {
		return model.SourceEndpointChange{}, fmt.Errorf("get Source endpoint change: %w", err)
	}
	var change model.SourceEndpointChange
	if err := json.Unmarshal(state, &change); err != nil {
		return model.SourceEndpointChange{}, fmt.Errorf("decode Source endpoint change: %w", err)
	}
	return change, nil
}

func (r *Repository) GetSourceEndpointActivation(ctx context.Context, changeID string) (model.SourceEndpointActivation, error) {
	var state []byte
	err := r.db.QueryRowContext(ctx, `SELECT state_json FROM recruiting_source_endpoint_activations WHERE change_id = ?`,
		strings.TrimSpace(changeID)).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.SourceEndpointActivation{}, ErrNotFound
	}
	if err != nil {
		return model.SourceEndpointActivation{}, fmt.Errorf("get Source endpoint activation: %w", err)
	}
	var activation model.SourceEndpointActivation
	if err := json.Unmarshal(state, &activation); err != nil {
		return model.SourceEndpointActivation{}, fmt.Errorf("decode Source endpoint activation: %w", err)
	}
	return activation, nil
}

type SourceEndpointHistoryItem struct {
	Change     model.SourceEndpointChange      `json:"change"`
	Activation *model.SourceEndpointActivation `json:"activation,omitempty"`
}

func (r *Repository) ListSourceEndpointHistory(ctx context.Context, sourceID string, limit int) ([]SourceEndpointHistoryItem, error) {
	sourceID = strings.TrimSpace(sourceID)
	if sourceID == "" || limit < 1 || limit > 100 {
		return nil, fmt.Errorf("Source identity and history limit in [1,100] are required")
	}
	rows, err := r.db.QueryContext(ctx, `SELECT change_row.state_json, activation.state_json
FROM recruiting_source_endpoint_changes change_row
LEFT JOIN recruiting_source_endpoint_activations activation ON activation.change_id = change_row.change_id
WHERE change_row.source_id = ?
ORDER BY change_row.to_revision DESC, change_row.change_id DESC LIMIT ?`, sourceID, limit)
	if err != nil {
		return nil, fmt.Errorf("list Source endpoint history: %w", err)
	}
	defer rows.Close()
	items := make([]SourceEndpointHistoryItem, 0)
	for rows.Next() {
		var changeState []byte
		var activationState []byte
		if err := rows.Scan(&changeState, &activationState); err != nil {
			return nil, fmt.Errorf("scan Source endpoint history: %w", err)
		}
		var item SourceEndpointHistoryItem
		if err := json.Unmarshal(changeState, &item.Change); err != nil {
			return nil, fmt.Errorf("decode Source endpoint history change: %w", err)
		}
		if len(activationState) != 0 {
			var activation model.SourceEndpointActivation
			if err := json.Unmarshal(activationState, &activation); err != nil {
				return nil, fmt.Errorf("decode Source endpoint history activation: %w", err)
			}
			item.Activation = &activation
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate Source endpoint history: %w", err)
	}
	return items, nil
}
