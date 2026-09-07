package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestCompanyCommandReceiptAndOutboxAreAtomic(t *testing.T) {
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
	now := time.Date(2026, 9, 7, 11, 0, 0, 0, time.UTC)

	company, _ := model.NewCompany("command-company", "Command Company", "https://command.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	paused, _ := company.Pause(company.Version, model.PauseDrain)
	receipt, _ := model.NewCommandReceipt("command-pause", "recruiting.company.pause", "sha256:pause", json.RawMessage(`{"company_id":"command-company","version":2,"status":"paused"}`))
	event, _ := model.NewEventIntent("event-pause", "company.paused", "company", company.CompanyID, paused.Version, now.Format(time.RFC3339), receipt.CommandID, json.RawMessage(`{"mode":"drain"}`))

	first, err := repository.ApplyCompanyCommand(ctx, company.Version, paused, receipt, event, now)
	if err != nil || first.Replayed || string(first.Response) != string(receipt.Response) {
		t.Fatalf("first command = %+v %v", first, err)
	}
	replay, err := repository.ApplyCompanyCommand(ctx, company.Version, paused, receipt, event, now)
	if err != nil || !replay.Replayed || string(replay.Response) != string(receipt.Response) {
		t.Fatalf("command replay = %+v %v", replay, err)
	}
	changedRequest := receipt
	changedRequest.RequestHash = "sha256:different"
	if _, err := repository.ApplyCompanyCommand(ctx, company.Version, paused, changedRequest, event, now); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("different request reused command ID: %v", err)
	}
	stored, err := repository.GetCompany(ctx, company.CompanyID)
	if err != nil || stored.Version != paused.Version || stored.ControlStatus != model.ControlPaused {
		t.Fatalf("stored company = %+v %v", stored, err)
	}
	pending, err := repository.ListPendingEvents(ctx, now, 10)
	if err != nil {
		t.Fatal(err)
	}
	foundEvent := false
	for _, candidate := range pending {
		foundEvent = foundEvent || candidate.Intent.EventID == event.EventID
	}
	if !foundEvent {
		t.Fatalf("committed event was not recoverable before ledger delivery: %+v", pending)
	}
	if err := repository.MarkEventDelivered(ctx, event.EventID, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentCommandReplayMutatesOnce(t *testing.T) {
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
	now := time.Date(2026, 9, 7, 12, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("concurrent-command-company", "Concurrent", "https://concurrent-command.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	paused, _ := company.Pause(company.Version, model.PauseDrain)
	receipt, _ := model.NewCommandReceipt("concurrent-pause", "recruiting.company.pause", "sha256:concurrent", json.RawMessage(`{"version":2}`))
	event, _ := model.NewEventIntent("concurrent-event", "company.paused", "company", company.CompanyID, paused.Version, now.Format(time.RFC3339), receipt.CommandID, json.RawMessage(`{"mode":"drain"}`))

	results := make(chan CommandResult, 2)
	failures := make(chan error, 2)
	var group sync.WaitGroup
	for range 2 {
		group.Add(1)
		go func() {
			defer group.Done()
			result, applyErr := repository.ApplyCompanyCommand(ctx, company.Version, paused, receipt, event, now)
			results <- result
			failures <- applyErr
		}()
	}
	group.Wait()
	close(results)
	close(failures)
	for applyErr := range failures {
		if applyErr != nil {
			t.Fatalf("concurrent replay failed: %v", applyErr)
		}
	}
	var applied, replayed int
	for result := range results {
		if result.Replayed {
			replayed++
		} else {
			applied++
		}
	}
	if applied != 1 || replayed != 1 {
		t.Fatalf("concurrent outcomes applied=%d replayed=%d", applied, replayed)
	}
	stored, _ := repository.GetCompany(ctx, company.CompanyID)
	if stored.Version != 2 {
		t.Fatalf("command mutated aggregate more than once: %+v", stored)
	}
}

func TestOutboxFailureRollsBackAggregateAndReceipt(t *testing.T) {
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
	now := time.Date(2026, 9, 7, 13, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("rollback-company", "Rollback", "https://rollback.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	paused, _ := company.Pause(company.Version, model.PauseDrain)
	receipt, _ := model.NewCommandReceipt("rollback-command", "recruiting.company.pause", "sha256:rollback", json.RawMessage(`{"version":2}`))
	if _, err := db.ExecContext(ctx, `
INSERT INTO recruiting_event_outbox(
  event_id, event_kind, aggregate_type, aggregate_id, aggregate_version,
  cause_command_id, business_at, payload_json, delivery_status, next_attempt_at
) VALUES ('forced-duplicate-event', 'fixture', 'fixture', 'fixture', 1, 'fixture', ?, '{}', 'delivered', ?)`, now, now); err != nil {
		t.Fatal(err)
	}
	// Reusing the fixture primary key forces the final outbox insert to fail
	// after the aggregate UPDATE, proving the whole command rolls back.
	event, _ := model.NewEventIntent("forced-duplicate-event", "company.paused", "company", company.CompanyID, paused.Version, now.Format(time.RFC3339), receipt.CommandID, json.RawMessage(`{"mode":"drain"}`))
	if _, err := repository.ApplyCompanyCommand(ctx, company.Version, paused, receipt, event, now); err == nil {
		t.Fatal("duplicate outbox event unexpectedly committed")
	}
	stored, err := repository.GetCompany(ctx, company.CompanyID)
	if err != nil || stored.Version != 1 || stored.ControlStatus != model.ControlActive {
		t.Fatalf("aggregate survived rolled-back outbox failure: %+v %v", stored, err)
	}
	if _, found, err := readCommandReceipt(ctx, db, receipt.CommandID, receipt.RequestHash); err != nil || found {
		t.Fatalf("receipt survived rolled-back outbox failure: found=%v err=%v", found, err)
	}
}
