package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestCompanyRepositoryContract(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	repository, _ := NewRepository(db)
	businessAt := time.Date(2090, 9, 7, 10, 0, 0, 123000, time.UTC)
	for _, identity := range []string{"company-a", "company-b", "company-c"} {
		company, _ := model.NewCompany(identity, "Name "+identity, "https://"+identity+".example.com")
		if err := repository.CreateCompany(ctx, company, businessAt); err != nil {
			t.Fatal(err)
		}
	}

	duplicateWebsite, _ := model.NewCompany("company-duplicate", "Duplicate", "https://company-a.example.com")
	if err := repository.CreateCompany(ctx, duplicateWebsite, businessAt); !errors.Is(err, ErrBusinessKeyExists) {
		t.Fatalf("duplicate normalized website = %v", err)
	}

	startCursor := encodeCompanyCursor(companyCursor{UpdatedAt: businessAt.Add(-time.Second).Format(time.RFC3339Nano), CompanyID: "cursor-floor"})
	first, err := repository.ListCompanies(ctx, startCursor, 2)
	if err != nil || len(first.Items) != 2 || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("first page = %+v %v", first, err)
	}
	second, err := repository.ListCompanies(ctx, first.NextCursor, 2)
	if err != nil || len(second.Items) != 1 || second.HasMore || second.Items[0].CompanyID != "company-c" {
		t.Fatalf("second page = %+v %v", second, err)
	}
	var explain string
	if err := db.QueryRowContext(ctx, `EXPLAIN FORMAT=JSON
SELECT state_json, updated_at, company_id
FROM recruiting_companies
WHERE updated_at > ? OR (updated_at = ? AND company_id > ?)
ORDER BY updated_at, company_id
LIMIT 3`, businessAt, businessAt, "company-a").Scan(&explain); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "ix_recruiting_company_page") {
		t.Fatalf("company seek query did not use page index: %s", explain)
	}

	current, err := repository.GetCompany(ctx, "company-a")
	if err != nil {
		t.Fatal(err)
	}
	leftName, rightName := "Left", "Right"
	left, _ := current.Update(current.Version, model.CompanyUpdate{Name: &leftName})
	right, _ := current.Update(current.Version, model.CompanyUpdate{Name: &rightName})
	errorsFound := make(chan error, 2)
	var group sync.WaitGroup
	for _, candidate := range []model.Company{left.Company, right.Company} {
		candidate := candidate
		group.Add(1)
		go func() {
			defer group.Done()
			errorsFound <- repository.UpdateCompanyCAS(ctx, current.Version, candidate, businessAt.Add(time.Second))
		}()
	}
	group.Wait()
	close(errorsFound)
	var succeeded, conflicted int
	for updateErr := range errorsFound {
		if updateErr == nil {
			succeeded++
			continue
		}
		var conflict *model.VersionConflictError
		if errors.As(updateErr, &conflict) {
			conflicted++
			continue
		}
		t.Fatalf("unexpected CAS result: %v", updateErr)
	}
	if succeeded != 1 || conflicted != 1 {
		t.Fatalf("CAS outcomes succeeded=%d conflicted=%d", succeeded, conflicted)
	}
	stored, err := repository.GetCompany(ctx, "company-a")
	if err != nil || stored.Version != 2 || (stored.Name != "Left" && stored.Name != "Right") {
		t.Fatalf("stored company = %+v %v", stored, err)
	}
}
