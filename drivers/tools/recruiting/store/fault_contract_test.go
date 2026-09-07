package store

import (
	"bufio"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"sync"
	"testing"
	"time"

	mysql "github.com/go-sql-driver/mysql"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestRepositoryTimeoutLeavesNoPartialCompanyUpdate(t *testing.T) {
	db, repository, ctx := faultTestRepository(t)
	now := time.Date(2026, 9, 8, 5, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("fault-timeout-company", "Before", "https://fault-timeout.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	lock, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	if _, err := lock.ExecContext(ctx, "SELECT version FROM recruiting_companies WHERE company_id = ? FOR UPDATE", company.CompanyID); err != nil {
		t.Fatal(err)
	}
	name := "Must Roll Back"
	updated, _ := company.Update(company.Version, model.CompanyUpdate{Name: &name})
	timeoutCtx, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer cancel()
	if err := repository.UpdateCompanyCAS(timeoutCtx, company.Version, updated.Company, now); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("locked update did not time out: %v", err)
	}
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	stored, err := repository.GetCompany(ctx, company.CompanyID)
	if err != nil || stored.Version != 1 || stored.Name != "Before" {
		t.Fatalf("timeout left partial company: %+v %v", stored, err)
	}
}

func TestRepositoryDeadlockHasOneAtomicWinner(t *testing.T) {
	db, _, ctx := faultTestRepository(t)
	now := time.Date(2026, 9, 8, 5, 30, 0, 0, time.UTC)
	for _, id := range []string{"deadlock-receipt-1", "deadlock-receipt-2"} {
		if _, err := db.ExecContext(ctx, "INSERT INTO recruiting_command_receipts(command_id, word_name, request_hash, response_bytes, committed_at) VALUES (?, 'fixture', 'initial', '{}', ?)", id, now); err != nil {
			t.Fatal(err)
		}
	}
	txA, _ := db.BeginTx(ctx, nil)
	txB, _ := db.BeginTx(ctx, nil)
	defer txA.Rollback()
	defer txB.Rollback()
	if _, err := txA.ExecContext(ctx, "UPDATE recruiting_command_receipts SET request_hash = 'winner-a' WHERE command_id = 'deadlock-receipt-1'"); err != nil {
		t.Fatal(err)
	}
	if _, err := txB.ExecContext(ctx, "UPDATE recruiting_command_receipts SET request_hash = 'winner-b' WHERE command_id = 'deadlock-receipt-2'"); err != nil {
		t.Fatal(err)
	}
	type outcome struct {
		name string
		err  error
	}
	outcomes := make(chan outcome, 2)
	var group sync.WaitGroup
	group.Add(2)
	go func() {
		defer group.Done()
		_, err := txA.ExecContext(ctx, "UPDATE recruiting_command_receipts SET request_hash = 'winner-a' WHERE command_id = 'deadlock-receipt-2'")
		if err == nil {
			err = txA.Commit()
		}
		outcomes <- outcome{name: "winner-a", err: err}
	}()
	go func() {
		defer group.Done()
		_, err := txB.ExecContext(ctx, "UPDATE recruiting_command_receipts SET request_hash = 'winner-b' WHERE command_id = 'deadlock-receipt-1'")
		if err == nil {
			err = txB.Commit()
		}
		outcomes <- outcome{name: "winner-b", err: err}
	}()
	group.Wait()
	close(outcomes)
	var winner string
	var deadlocks int
	for result := range outcomes {
		if result.err == nil {
			winner = result.name
			continue
		}
		var mysqlError *mysql.MySQLError
		if errors.As(result.err, &mysqlError) && mysqlError.Number == 1213 {
			deadlocks++
			continue
		}
		t.Fatalf("unexpected deadlock result: %v", result.err)
	}
	if winner == "" || deadlocks != 1 {
		t.Fatalf("deadlock winner=%q losers=%d", winner, deadlocks)
	}
	for _, id := range []string{"deadlock-receipt-1", "deadlock-receipt-2"} {
		var hash string
		if err := db.QueryRowContext(ctx, "SELECT request_hash FROM recruiting_command_receipts WHERE command_id = ?", id).Scan(&hash); err != nil || hash != winner {
			t.Fatalf("deadlock left mixed transaction for %s: hash=%q err=%v", id, hash, err)
		}
	}
}

func TestRepositoryProcessKillCrashCutsAreRecoverable(t *testing.T) {
	_, repository, ctx := faultTestRepository(t)
	now := time.Date(2026, 9, 8, 6, 0, 0, 0, time.UTC)
	uncommitted, _ := model.NewCompany("fault-kill-uncommitted", "Before Kill", "https://fault-kill-uncommitted.example.com")
	if err := repository.CreateCompany(ctx, uncommitted, now); err != nil {
		t.Fatal(err)
	}
	runCrashHelper(t, "uncommitted")
	stored, err := repository.GetCompany(ctx, uncommitted.CompanyID)
	if err != nil || stored.Version != 1 || stored.Name != "Before Kill" {
		t.Fatalf("killed transaction survived: %+v %v", stored, err)
	}

	committed, _ := model.NewCompany("fault-kill-committed", "Before Commit", "https://fault-kill-committed.example.com")
	if err := repository.CreateCompany(ctx, committed, now); err != nil {
		t.Fatal(err)
	}
	runCrashHelper(t, "committed")
	paused, _ := committed.Pause(committed.Version, model.PauseDrain)
	receipt, event := crashCommandFacts(t, paused, now)
	replay, err := repository.ApplyCompanyCommand(ctx, committed.Version, paused, receipt, event, now)
	if err != nil || !replay.Replayed {
		t.Fatalf("committed-before-kill command was not replayable: %+v %v", replay, err)
	}
	pending, err := repository.ListPendingEvents(ctx, now, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, candidate := range pending {
		found = found || candidate.Intent.EventID == event.EventID
	}
	if !found {
		t.Fatal("committed-before-kill outbox intent was lost")
	}
}

func TestRecruitingCrashHelper(t *testing.T) {
	mode := os.Getenv("RECRUITING_CRASH_HELPER")
	if mode == "" {
		t.Skip("helper process only")
	}
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	now := time.Date(2026, 9, 8, 6, 0, 0, 0, time.UTC)
	switch mode {
	case "uncommitted":
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.ExecContext(ctx, "UPDATE recruiting_companies SET name = 'Killed', version = 2 WHERE company_id = 'fault-kill-uncommitted'"); err != nil {
			t.Fatal(err)
		}
	case "committed":
		repository, _ := NewRepository(db)
		company, err := repository.GetCompany(ctx, "fault-kill-committed")
		if err != nil {
			t.Fatal(err)
		}
		paused, _ := company.Pause(company.Version, model.PauseDrain)
		receipt, event := crashCommandFacts(t, paused, now)
		if _, err := repository.ApplyCompanyCommand(ctx, company.Version, paused, receipt, event, now); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown helper mode %q", mode)
	}
	if _, err := os.Stdout.WriteString("READY\n"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Second)
}

func runCrashHelper(t *testing.T, mode string) {
	t.Helper()
	command := exec.Command(os.Args[0], "-test.run=^TestRecruitingCrashHelper$")
	command.Env = append(os.Environ(), "RECRUITING_CRASH_HELPER="+mode)
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil || line != "READY\n" {
		_ = command.Process.Kill()
		t.Fatalf("crash helper readiness=%q err=%v", line, err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := command.Wait(); err == nil {
		t.Fatal("crash helper was not killed")
	}
}

func crashCommandFacts(t *testing.T, paused model.Company, now time.Time) (model.CommandReceipt, model.EventIntent) {
	t.Helper()
	receipt, err := model.NewCommandReceipt("fault-kill-command", "recruiting.company.pause", "sha256:fault-kill", json.RawMessage("{\"version\":2}"))
	if err != nil {
		t.Fatal(err)
	}
	event, err := model.NewEventIntent("fault-kill-event", "company.paused", "company", paused.CompanyID, paused.Version, now.Format(time.RFC3339), receipt.CommandID, json.RawMessage("{\"mode\":\"drain\"}"))
	if err != nil {
		t.Fatal(err)
	}
	return receipt, event
}

func faultTestRepository(t *testing.T) (*sql.DB, *Repository, context.Context) {
	t.Helper()
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	t.Cleanup(cancel)
	migrateTestDatabase(t, ctx, db)

	repository, _ := NewRepository(db)
	return db, repository, ctx
}
