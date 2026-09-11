package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

const (
	backupCompanyID       = "backup-restore-company"
	backupCompanyCommand  = "backup-company-command"
	backupCompanyEvent    = "backup-company-event"
	backupWorkID          = "backup-restore-work"
	backupWorkCommand     = "backup-work-command"
	backupWorkEvent       = "backup-work-event"
	backupDispatchID      = "backup-work-dispatch"
	backupCompanyHash     = "sha256:backup-company-request"
	backupWorkRequestHash = "sha256:backup-work-request"
)

// TestBackupRestoreContract is driven by scripts/recruiting-backup-restore.sh.
// Seed and verify run in separate processes against different database names,
// so verification cannot accidentally observe the source connection pool.
func TestBackupRestoreContract(t *testing.T) {
	mode := os.Getenv("RECRUITING_BACKUP_RESTORE_MODE")
	if mode == "" {
		t.Skip("RECRUITING_BACKUP_RESTORE_MODE is not set")
	}
	if mode != "seed" && mode != "verify" {
		t.Fatalf("unknown backup restore mode %q", mode)
	}
	if mode == "seed" {
		migrationDSN := os.Getenv("RECRUITING_MYSQL_MIGRATION_TEST_DSN")
		if migrationDSN == "" {
			t.Fatal("seed requires RECRUITING_MYSQL_MIGRATION_TEST_DSN")
		}
		migrationDB, err := Open(migrationDSN)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		if err := Migrate(ctx, migrationDB); err != nil {
			cancel()
			_ = migrationDB.Close()
			t.Fatal(err)
		}
		cancel()
		_ = migrationDB.Close()
	}

	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Fatal("backup restore contract requires RECRUITING_MYSQL_TEST_DSN")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	repository, _ := NewRepository(db)
	if mode == "seed" {
		seedBackupRestoreFacts(t, ctx, repository)
	}
	assertBackupRestoreFacts(t, ctx, db, repository)
}

func backupRestoreFixtures(t *testing.T) (model.Company, model.CommandReceipt, model.EventIntent,
	model.Work, WorkPlacement, model.CommandReceipt, model.EventIntent, ExecutionDispatchIntent, time.Time) {
	t.Helper()
	now := time.Date(2099, 4, 5, 6, 7, 8, 0, time.UTC)
	company, err := model.NewCompany(backupCompanyID, "Backup Restore Company", "https://backup.example.test")
	if err != nil {
		t.Fatal(err)
	}
	companyResponse := json.RawMessage(`{"company_id":"backup-restore-company","version":1}`)
	companyReceipt, _ := model.NewCommandReceipt(backupCompanyCommand, "recruiting.company.add", backupCompanyHash, companyResponse)
	companyEvent, _ := model.NewEventIntent(backupCompanyEvent, "company.added", "company", company.CompanyID,
		company.Version, now.Format(time.RFC3339Nano), companyReceipt.CommandID, json.RawMessage(`{"requested_by":"human:backup-operator"}`))

	work, err := model.NewWork(backupWorkID, "company", company.CompanyID, "source_discovery", "manual")
	if err == nil {
		work, err = work.WithCausality("human:backup-operator", "backup-source-message", "")
	}
	if err != nil {
		t.Fatal(err)
	}
	placement := WorkPlacement{BusinessKey: "backup|source-discovery|" + company.CompanyID, Priority: 80,
		Capability: "http.fetch", Origin: "https://backup.example.test", NotBefore: now}
	workResponse := json.RawMessage(`{"work_id":"backup-restore-work","version":1}`)
	workReceipt, _ := model.NewCommandReceipt(backupWorkCommand, "recruiting.work.create", backupWorkRequestHash, workResponse)
	workEvent, _ := model.NewEventIntent(backupWorkEvent, "work.created", "work", work.WorkID, work.Version,
		now.Format(time.RFC3339Nano), workReceipt.CommandID, json.RawMessage(`{"requested_by":"human:backup-operator"}`))
	dispatch, err := NewExecutionDispatchIntent(backupDispatchID, "tool:backup-executor", placement.Capability,
		placement.Origin, "", "work_created", workReceipt.CommandID, now)
	if err != nil {
		t.Fatal(err)
	}
	return company, companyReceipt, companyEvent, work, placement, workReceipt, workEvent, dispatch, now
}

func seedBackupRestoreFacts(t *testing.T, ctx context.Context, repository *Repository) {
	t.Helper()
	company, companyReceipt, companyEvent, work, placement, workReceipt, workEvent, dispatch, now := backupRestoreFixtures(t)
	if result, err := repository.ApplyCreateCompanyCommand(ctx, company, companyReceipt, companyEvent, now); err != nil || result.Replayed {
		t.Fatalf("seed backup Company: result=%+v err=%v", result, err)
	}
	if result, err := repository.ApplyCreateWorkCommandWithDispatch(ctx, work, placement, workReceipt, workEvent,
		&dispatch, now); err != nil || result.Replayed {
		t.Fatalf("seed backup Work: result=%+v err=%v", result, err)
	}
}

func assertBackupRestoreFacts(t *testing.T, ctx context.Context, db *sql.DB, repository *Repository) {
	t.Helper()
	company, companyReceipt, companyEvent, work, placement, workReceipt, workEvent, dispatch, now := backupRestoreFixtures(t)
	var currentUser string
	if err := db.QueryRowContext(ctx, "SELECT CURRENT_USER()").Scan(&currentUser); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(currentUser, "staircase_runtime@") {
		t.Fatalf("restored facts were not read with runtime identity: %s", currentUser)
	}
	storedCompany, err := repository.GetCompany(ctx, company.CompanyID)
	if err != nil || storedCompany != company {
		t.Fatalf("restored Company=%+v err=%v", storedCompany, err)
	}
	storedWork, err := repository.GetWorkRecord(ctx, work.WorkID)
	if err != nil || storedWork.Work != work || storedWork.Placement.BusinessKey != placement.BusinessKey ||
		storedWork.Placement.Capability != placement.Capability || storedWork.Placement.Origin != placement.Origin {
		t.Fatalf("restored Work=%+v err=%v", storedWork, err)
	}
	for _, receipt := range []model.CommandReceipt{companyReceipt, workReceipt} {
		result, found, err := repository.LookupCommand(ctx, receipt.CommandID, receipt.RequestHash)
		if err != nil || !found || !result.Replayed || string(result.Response) != string(receipt.Response) {
			t.Fatalf("restored receipt %s: result=%+v found=%v err=%v", receipt.CommandID, result, found, err)
		}
	}
	events, err := repository.ListPendingEvents(ctx, now.Add(time.Hour), 10)
	if err != nil || len(events) != 2 || events[0].Intent.EventID != companyEvent.EventID ||
		events[1].Intent.EventID != workEvent.EventID {
		t.Fatalf("restored event outbox=%+v err=%v", events, err)
	}
	dispatches, err := repository.ListPendingExecutionDispatches(ctx, now.Add(time.Hour), 10)
	if err != nil || len(dispatches) != 1 || dispatches[0].Intent != dispatch {
		t.Fatalf("restored execution dispatch=%+v err=%v", dispatches, err)
	}
	if result, err := repository.ApplyCreateCompanyCommand(ctx, company, companyReceipt, companyEvent, now); err != nil || !result.Replayed {
		t.Fatalf("restored Company command did not replay: result=%+v err=%v", result, err)
	}
	if result, err := repository.ApplyCreateWorkCommandWithDispatch(ctx, work, placement, workReceipt, workEvent,
		&dispatch, now); err != nil || !result.Replayed {
		t.Fatalf("restored Work command did not replay: result=%+v err=%v", result, err)
	}
	migrations, err := Migrations()
	if err != nil {
		t.Fatal(err)
	}
	var applied int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_schema_migrations
WHERE migration_status = 'applied'`).Scan(&applied); err != nil || applied != len(migrations) {
		t.Fatalf("restored migration ledger=%d/%d err=%v", applied, len(migrations), err)
	}
	if _, err := db.ExecContext(ctx, "CREATE TABLE recruiting_backup_restore_forbidden(id INT)"); err == nil {
		t.Fatal("runtime identity unexpectedly retained DDL after restore")
	}
}
