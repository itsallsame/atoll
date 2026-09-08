package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestSourceCommandsAreAtomicReplayableAndSeekPaged(t *testing.T) {
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
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	now := time.Date(2090, 9, 8, 9, 0, 0, 123000, time.UTC)

	for _, id := range []string{"source-command-company", "source-command-other-company"} {
		company, _ := model.NewCompany(id, id, "https://"+id+".example.com")
		if err := repository.CreateCompany(ctx, company, now); err != nil {
			t.Fatal(err)
		}
	}
	source, _ := model.NewRecruitmentSource("source-command-a", "source-command-company", "https://jobs-a.example.com/roles", "engineering", 1)
	response := json.RawMessage(`{"source_id":"source-command-a","version":1}`)
	receipt, _ := model.NewCommandReceipt("source-create-command", "recruiting.source.add", "sha256:create-source", response)
	event, _ := model.NewEventIntent("source-create-event", "source.added", "source", source.SourceID, source.Version,
		now.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{"requested_by":"human:alice"}`))
	first, err := repository.ApplyCreateSourceCommand(ctx, source, receipt, event, now)
	if err != nil || first.Replayed || string(first.Response) != string(response) {
		t.Fatalf("create source command = %+v %v", first, err)
	}
	replay, err := repository.ApplyCreateSourceCommand(ctx, source, receipt, event, now)
	if err != nil || !replay.Replayed || string(replay.Response) != string(response) {
		t.Fatalf("source create replay = %+v %v", replay, err)
	}
	conflictingReceipt := receipt
	conflictingReceipt.RequestHash = "sha256:different"
	if _, err := repository.ApplyCreateSourceCommand(ctx, source, conflictingReceipt, event, now); !errors.Is(err, ErrCommandConflict) {
		t.Fatalf("source command ID accepted changed request: %v", err)
	}

	validating, _ := source.BeginValidation(source.Version)
	updateResponse := json.RawMessage(`{"source_id":"source-command-a","version":2}`)
	updateReceipt, _ := model.NewCommandReceipt("source-validate-command", "recruiting.source.validate", "sha256:validate-source", updateResponse)
	updateEvent, _ := model.NewEventIntent("source-validate-event", "source.validation_started", "source", source.SourceID, validating.Version,
		now.Add(time.Second).Format(time.RFC3339Nano), updateReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplySourceCommand(ctx, source.Version, validating, updateReceipt, updateEvent, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	stored, err := repository.GetSource(ctx, source.SourceID)
	if err != nil || stored.ReadinessStatus != model.SourceValidating || stored.Version != 2 {
		t.Fatalf("stored source = %+v %v", stored, err)
	}
	if _, err := repository.ApplySourceCommand(ctx, source.Version, validating, updateReceipt, updateEvent, now.Add(time.Second)); err != nil {
		t.Fatalf("source mutation replay failed: %v", err)
	}
	for _, fixture := range []struct{ id, company, endpoint string }{
		{"source-command-b", "source-command-company", "https://jobs-b.example.com/roles"},
		{"source-command-c", "source-command-other-company", "https://jobs-c.example.com/roles"},
	} {
		candidate, _ := model.NewRecruitmentSource(fixture.id, fixture.company, fixture.endpoint, "", 1)
		if err := repository.CreateSource(ctx, candidate, now.Add(2*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	page, err := repository.ListSources(ctx, "source-command-company", "", 1)
	if err != nil || len(page.Items) != 1 || !page.HasMore || page.NextCursor == "" {
		t.Fatalf("first company source page = %+v %v", page, err)
	}
	second, err := repository.ListSources(ctx, "source-command-company", page.NextCursor, 1)
	if err != nil || len(second.Items) != 1 || second.HasMore {
		t.Fatalf("second company source page = %+v %v", second, err)
	}
	if _, err := repository.ListSources(ctx, "source-command-other-company", page.NextCursor, 1); err == nil {
		t.Fatal("source cursor was reusable across company selectors")
	}
	var explain string
	if err := db.QueryRowContext(ctx, `EXPLAIN FORMAT=JSON
SELECT state_json, updated_at, source_id
FROM recruiting_sources
WHERE company_id = ? AND (updated_at > ? OR (updated_at = ? AND source_id > ?))
ORDER BY updated_at, source_id LIMIT 2`, "source-command-company", now, now, "source-command-a").Scan(&explain); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "ix_recruiting_source_company_page") {
		t.Fatalf("company source seek did not use intended index: %s", explain)
	}

	pending, err := repository.ListPendingEvents(ctx, now.Add(time.Minute), 100)
	if err != nil {
		t.Fatal(err)
	}
	var createEvents, validateEvents int
	for _, pendingEvent := range pending {
		if pendingEvent.Intent.EventID == event.EventID {
			createEvents++
		}
		if pendingEvent.Intent.EventID == updateEvent.EventID {
			validateEvents++
		}
	}
	if createEvents != 1 || validateEvents != 1 {
		t.Fatalf("source command outbox events create=%d validate=%d", createEvents, validateEvents)
	}
	company, err := repository.GetCompany(ctx, source.CompanyID)
	if err != nil {
		t.Fatal(err)
	}
	archivedCompany, _ := company.Archive(company.Version)
	if err := repository.UpdateCompanyCAS(ctx, company.Version, archivedCompany, now.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if replay, err := repository.ApplyCreateSourceCommand(ctx, source, receipt, event, now.Add(3*time.Minute)); err != nil || !replay.Replayed {
		t.Fatalf("committed source command did not replay after company archive: %+v %v", replay, err)
	}
}

func TestArchivedCompanyCannotGainSourceInsideCommandTransaction(t *testing.T) {
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
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	now := time.Date(2090, 9, 8, 10, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("archived-source-company", "Archived", "https://archived-source.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	company, _ = company.Archive(company.Version)
	if err := repository.UpdateCompanyCAS(ctx, 1, company, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("source-under-archived", company.CompanyID, "https://jobs-archived.example.com", "", 1)
	receipt, _ := model.NewCommandReceipt("archived-source-command", "recruiting.source.add", "sha256:archived", json.RawMessage(`{}`))
	event, _ := model.NewEventIntent("archived-source-event", "source.added", "source", source.SourceID, 1, now.Format(time.RFC3339), receipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyCreateSourceCommand(ctx, source, receipt, event, now); err == nil {
		t.Fatal("archived company accepted a source")
	}
	if _, err := repository.GetSource(ctx, source.SourceID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("rejected source was persisted: %v", err)
	}
	if _, found, err := repository.LookupCommand(ctx, receipt.CommandID, receipt.RequestHash); err != nil || found {
		t.Fatalf("rejected source left a command receipt: found=%v err=%v", found, err)
	}
}
