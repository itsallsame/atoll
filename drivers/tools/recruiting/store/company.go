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

	mysql "github.com/go-sql-driver/mysql"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type Repository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) (*Repository, error) {
	if db == nil {
		return nil, fmt.Errorf("recruiting database is required")
	}
	return &Repository{db: db}, nil
}

func (r *Repository) CreateCompany(ctx context.Context, company model.Company, businessAt time.Time) error {
	if company.Version != 1 || company.CompanyID == "" {
		return fmt.Errorf("new company must have identity and version 1")
	}
	state, err := json.Marshal(company)
	if err != nil {
		return fmt.Errorf("encode company: %w", err)
	}
	_, err = r.db.ExecContext(ctx, `
INSERT INTO recruiting_companies(
  company_id, normalized_website, name, onboarding_status, control_status,
  version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		company.CompanyID, nullableString(company.Website), company.Name, company.OnboardingStatus, company.ControlStatus,
		company.Version, state, businessAt.UTC(), businessAt.UTC())
	if err == nil {
		return nil
	}
	var mysqlError *mysql.MySQLError
	if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
		return fmt.Errorf("%w: company ID or normalized website", ErrBusinessKeyExists)
	}
	return fmt.Errorf("create company: %w", err)
}

func (r *Repository) GetCompany(ctx context.Context, companyID string) (model.Company, error) {
	var state []byte
	err := r.db.QueryRowContext(ctx, "SELECT state_json FROM recruiting_companies WHERE company_id = ?", companyID).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return model.Company{}, ErrNotFound
	}
	if err != nil {
		return model.Company{}, fmt.Errorf("get company: %w", err)
	}
	var company model.Company
	if err := json.Unmarshal(state, &company); err != nil {
		return model.Company{}, fmt.Errorf("decode company: %w", err)
	}
	return company, nil
}

func (r *Repository) UpdateCompanyCAS(ctx context.Context, expectedVersion uint64, company model.Company, businessAt time.Time) error {
	if expectedVersion == 0 || company.Version != expectedVersion+1 || company.CompanyID == "" {
		return fmt.Errorf("company update must advance exactly one expected version")
	}
	state, err := json.Marshal(company)
	if err != nil {
		return fmt.Errorf("encode company: %w", err)
	}
	// MySQL autocommit can win a race with context-driven connection
	// cancellation after a lock wait. Keep the UPDATE uncommitted until this
	// method explicitly observes success; a canceled connection then rolls the
	// transaction back. Command receipts handle commit-ack ambiguity above this
	// primitive.
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin company update: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	result, err := tx.ExecContext(ctx, `
UPDATE recruiting_companies
SET normalized_website = ?, name = ?, onboarding_status = ?, control_status = ?,
    version = ?, state_json = ?, updated_at = ?
WHERE company_id = ? AND version = ?`,
		nullableString(company.Website), company.Name, company.OnboardingStatus, company.ControlStatus,
		company.Version, state, businessAt.UTC(), company.CompanyID, expectedVersion)
	if err != nil {
		var mysqlError *mysql.MySQLError
		if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
			return fmt.Errorf("%w: normalized company website", ErrBusinessKeyExists)
		}
		return fmt.Errorf("update company: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("inspect company update: %w", err)
	}
	if changed == 1 {
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit company update: %w", err)
		}
		return nil
	}
	var actualState []byte
	readErr := tx.QueryRowContext(ctx, "SELECT state_json FROM recruiting_companies WHERE company_id = ?", company.CompanyID).Scan(&actualState)
	if errors.Is(readErr, sql.ErrNoRows) {
		return ErrNotFound
	}
	if readErr != nil {
		return readErr
	}
	var actual model.Company
	if err := json.Unmarshal(actualState, &actual); err != nil {
		return fmt.Errorf("decode company after failed update CAS: %w", err)
	}
	return &model.VersionConflictError{Expected: expectedVersion, Actual: actual.Version}
}

type CompanyPage struct {
	Items      []model.Company
	NextCursor string
	HasMore    bool
}

type companyCursor struct {
	UpdatedAt string `json:"updated_at"`
	CompanyID string `json:"company_id"`
}

func (r *Repository) ListCompanies(ctx context.Context, cursor string, limit int) (CompanyPage, error) {
	if limit <= 0 || limit > 500 {
		return CompanyPage{}, fmt.Errorf("company page limit must be in [1,500]")
	}
	afterTime := time.Date(1000, 1, 1, 0, 0, 0, 0, time.UTC)
	var afterID string
	if cursor != "" {
		decoded, err := decodeCompanyCursor(cursor)
		if err != nil {
			return CompanyPage{}, err
		}
		afterTime, err = time.Parse(time.RFC3339Nano, decoded.UpdatedAt)
		if err != nil || decoded.CompanyID == "" {
			return CompanyPage{}, fmt.Errorf("%w: company", ErrInvalidCursor)
		}
		afterID = decoded.CompanyID
	}
	rows, err := r.db.QueryContext(ctx, `
SELECT state_json, updated_at, company_id
FROM recruiting_companies
WHERE updated_at > ? OR (updated_at = ? AND company_id > ?)
ORDER BY updated_at, company_id
LIMIT ?`, afterTime.UTC(), afterTime.UTC(), afterID, limit+1)
	if err != nil {
		return CompanyPage{}, fmt.Errorf("list companies: %w", err)
	}
	defer rows.Close()
	type rowValue struct {
		company   model.Company
		updated   time.Time
		companyID string
	}
	values := make([]rowValue, 0, limit+1)
	for rows.Next() {
		var state []byte
		var value rowValue
		if err := rows.Scan(&state, &value.updated, &value.companyID); err != nil {
			return CompanyPage{}, fmt.Errorf("scan company page: %w", err)
		}
		if err := json.Unmarshal(state, &value.company); err != nil {
			return CompanyPage{}, fmt.Errorf("decode company page: %w", err)
		}
		values = append(values, value)
	}
	if err := rows.Err(); err != nil {
		return CompanyPage{}, fmt.Errorf("read company page: %w", err)
	}
	page := CompanyPage{HasMore: len(values) > limit}
	if page.HasMore {
		values = values[:limit]
	}
	page.Items = make([]model.Company, 0, len(values))
	for _, value := range values {
		page.Items = append(page.Items, value.company)
	}
	if page.HasMore && len(values) > 0 {
		last := values[len(values)-1]
		page.NextCursor = encodeCompanyCursor(companyCursor{UpdatedAt: last.updated.UTC().Format(time.RFC3339Nano), CompanyID: last.companyID})
	}
	return page, nil
}

func encodeCompanyCursor(cursor companyCursor) string {
	content, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(content)
}

func decodeCompanyCursor(value string) (companyCursor, error) {
	content, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(value))
	if err != nil {
		return companyCursor{}, fmt.Errorf("%w: company", ErrInvalidCursor)
	}
	var cursor companyCursor
	if err := json.Unmarshal(content, &cursor); err != nil {
		return companyCursor{}, fmt.Errorf("%w: company", ErrInvalidCursor)
	}
	return cursor, nil
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
