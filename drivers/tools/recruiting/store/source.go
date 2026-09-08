package store

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
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

type SourcePage struct {
	Items      []model.RecruitmentSource
	NextCursor string
	HasMore    bool
}

type sourceCursor struct {
	CompanyID string `json:"company_id,omitempty"`
	UpdatedAt string `json:"updated_at"`
	SourceID  string `json:"source_id"`
}

// ListSources uses a selector-bound seek cursor. A cursor created for one
// company cannot be replayed against another company (or the global list).
func (r *Repository) ListSources(ctx context.Context, companyID, cursor string, limit int) (SourcePage, error) {
	companyID = strings.TrimSpace(companyID)
	if limit <= 0 || limit > 500 {
		return SourcePage{}, fmt.Errorf("source page limit must be in [1,500]")
	}
	afterTime := time.Date(1000, 1, 1, 0, 0, 0, 0, time.UTC)
	var afterID string
	if cursor != "" {
		decoded, err := decodeSourceCursor(cursor)
		if err != nil || decoded.CompanyID != companyID {
			return SourcePage{}, fmt.Errorf("%w: source selector", ErrInvalidCursor)
		}
		afterTime, err = time.Parse(time.RFC3339Nano, decoded.UpdatedAt)
		if err != nil || decoded.SourceID == "" {
			return SourcePage{}, fmt.Errorf("%w: source", ErrInvalidCursor)
		}
		afterID = decoded.SourceID
	}
	query := `
SELECT state_json, updated_at, source_id
FROM recruiting_sources
WHERE updated_at > ? OR (updated_at = ? AND source_id > ?)
ORDER BY updated_at, source_id
LIMIT ?`
	args := []any{afterTime.UTC(), afterTime.UTC(), afterID, limit + 1}
	if companyID != "" {
		query = `
SELECT state_json, updated_at, source_id
FROM recruiting_sources
WHERE company_id = ? AND (updated_at > ? OR (updated_at = ? AND source_id > ?))
ORDER BY updated_at, source_id
LIMIT ?`
		args = []any{companyID, afterTime.UTC(), afterTime.UTC(), afterID, limit + 1}
	}
	rows, err := r.db.QueryContext(ctx, query, args...)
	if err != nil {
		return SourcePage{}, fmt.Errorf("list sources: %w", err)
	}
	defer rows.Close()
	type rowValue struct {
		source   model.RecruitmentSource
		updated  time.Time
		sourceID string
	}
	values := make([]rowValue, 0, limit+1)
	for rows.Next() {
		var state []byte
		var value rowValue
		if err := rows.Scan(&state, &value.updated, &value.sourceID); err != nil {
			return SourcePage{}, fmt.Errorf("scan source page: %w", err)
		}
		if err := json.Unmarshal(state, &value.source); err != nil {
			return SourcePage{}, fmt.Errorf("decode source page: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return SourcePage{}, fmt.Errorf("read source page: %w", err)
	}
	page := SourcePage{HasMore: len(values) > limit}
	if page.HasMore {
		values = values[:limit]
	}
	page.Items = make([]model.RecruitmentSource, 0, len(values))
	for _, value := range values {
		page.Items = append(page.Items, value.source)
	}
	if page.HasMore && len(values) > 0 {
		last := values[len(values)-1]
		page.NextCursor = encodeSourceCursor(sourceCursor{
			CompanyID: companyID, UpdatedAt: last.updated.UTC().Format(time.RFC3339Nano), SourceID: last.sourceID,
		})
	}
	return page, nil
}

func encodeSourceCursor(cursor sourceCursor) string {
	content, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(content)
}

func decodeSourceCursor(value string) (sourceCursor, error) {
	content, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return sourceCursor{}, fmt.Errorf("%w: source", ErrInvalidCursor)
	}
	var cursor sourceCursor
	if err := json.Unmarshal(content, &cursor); err != nil {
		return sourceCursor{}, fmt.Errorf("%w: source", ErrInvalidCursor)
	}
	return cursor, nil
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
