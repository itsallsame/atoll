package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func (r *Repository) CreateSource(ctx context.Context, source model.RecruitmentSource, businessAt time.Time) error {
	if source.SourceID == "" || source.CompanyID == "" || source.Version != 1 {
		return fmt.Errorf("new source must have identity, company, and version 1")
	}
	endpoint := source.ActiveEndpoint
	if endpoint == nil {
		endpoint = source.CandidateEndpoint
	}
	if endpoint == nil || endpoint.CanonicalKey == "" {
		return fmt.Errorf("source endpoint and canonical key are required")
	}
	parsed, err := url.Parse(endpoint.URL)
	if err != nil || parsed.Hostname() == "" {
		return fmt.Errorf("source origin is invalid")
	}
	state, err := json.Marshal(source)
	if err != nil {
		return fmt.Errorf("encode source: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
INSERT INTO recruiting_sources(
  source_id, company_id, canonical_source_key, origin, readiness_status,
  control_status, health_status, discovery_generation, version, state_json,
  created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		source.SourceID, source.CompanyID, endpoint.CanonicalKey, parsed.Hostname(), source.ReadinessStatus,
		source.ControlStatus, source.HealthStatus, source.DiscoveryGeneration, source.Version, state,
		businessAt.UTC(), businessAt.UTC())
	if err == nil {
		return nil
	}
	var mysqlError *mysql.MySQLError
	if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
		return fmt.Errorf("%w: source ID or company canonical source key", ErrBusinessKeyExists)
	}
	return fmt.Errorf("create source: %w", err)
}

func (r *Repository) GetSource(ctx context.Context, sourceID string) (model.RecruitmentSource, error) {
	var state []byte
	err := r.db.QueryRowContext(ctx, "SELECT state_json FROM recruiting_sources WHERE source_id = ?", sourceID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.RecruitmentSource{}, ErrNotFound
	}
	if err != nil {
		return model.RecruitmentSource{}, fmt.Errorf("get source: %w", err)
	}
	var source model.RecruitmentSource
	if err := json.Unmarshal(state, &source); err != nil {
		return model.RecruitmentSource{}, fmt.Errorf("decode source: %w", err)
	}
	return source, nil
}

func (r *Repository) UpdateSourceCAS(ctx context.Context, expectedVersion uint64, source model.RecruitmentSource, businessAt time.Time) error {
	if source.SourceID == "" || source.Version != expectedVersion+1 {
		return fmt.Errorf("source update must advance exactly one expected version")
	}
	endpoint, origin, err := sourceStorageIdentity(source)
	if err != nil {
		return err
	}
	state, _ := json.Marshal(source)
	result, err := r.db.ExecContext(ctx, `
UPDATE recruiting_sources
SET canonical_source_key = ?, origin = ?, readiness_status = ?, control_status = ?,
    health_status = ?, discovery_generation = ?, version = ?, state_json = ?, updated_at = ?
WHERE source_id = ? AND version = ?`,
		endpoint.CanonicalKey, origin, source.ReadinessStatus, source.ControlStatus, source.HealthStatus,
		source.DiscoveryGeneration, source.Version, state, businessAt.UTC(), source.SourceID, expectedVersion)
	if err != nil {
		return fmt.Errorf("update source: %w", err)
	}
	changed, _ := result.RowsAffected()
	if changed == 1 {
		return nil
	}
	actual, readErr := r.GetSource(ctx, source.SourceID)
	if errors.Is(readErr, ErrNotFound) {
		return ErrNotFound
	}
	if readErr != nil {
		return readErr
	}
	return &model.VersionConflictError{Expected: expectedVersion, Actual: actual.Version}
}

func sourceStorageIdentity(source model.RecruitmentSource) (*model.SourceEndpoint, string, error) {
	endpoint := source.ActiveEndpoint
	if endpoint == nil {
		endpoint = source.CandidateEndpoint
	}
	if endpoint == nil || endpoint.CanonicalKey == "" {
		return nil, "", fmt.Errorf("source endpoint and canonical key are required")
	}
	parsed, err := url.Parse(endpoint.URL)
	if err != nil || parsed.Hostname() == "" {
		return nil, "", fmt.Errorf("source origin is invalid")
	}
	return endpoint, parsed.Hostname(), nil
}
