package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestJobCorrectionCommandIsAuditedVersionedAndReplaySafe(t *testing.T) {
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
	now := time.Date(2095, 2, 9, 3, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("job-correction-company", "Job Correction", "https://job-correction.example")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("job-correction-source", company.CompanyID,
		"https://job-correction.example/jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	ingested, err := repository.ApplyListingObservation(ctx, ListingIngest{
		Observation: model.ListingObservation{ObservationID: "job-correction-observation", OccurrenceID: "job-correction-occurrence",
			SourceID: source.SourceID, SourceJobKey: "external-1", DetailURL: "https://job-correction.example/jobs/1",
			ActivityAt: now.Format(time.RFC3339), RecipeID: "job-correction-recipe", RecipeVersion: 1,
			ArtifactID: "job-correction-artifact"},
		ObservedAt: now, NewJobID: "job-correction-job", DetailWorkID: "job-correction-detail-work",
		Origin: "https://job-correction.example", Capability: "http.fetch", Priority: 10, NotBefore: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	correction, _ := model.NewCuratedOverride("job-correction-title-v1", ingested.Job.JobID, "title",
		json.RawMessage(`"Human title"`), "human:operator", "confirmed against employer page")
	receipt, _ := model.NewCommandReceipt("job-correction-set-command", "recruiting.job.correct",
		"sha256:job-correction-set", json.RawMessage(`{"operation":"set"}`))
	event, _ := model.NewEventIntent("job-correction-set-event", "job.correction.set", "job_override",
		correction.OverrideID, correction.Version, now.Format(time.RFC3339Nano), receipt.CommandID,
		json.RawMessage(`{"field":"title"}`))
	result, err := repository.ApplyJobCorrectionCommand(ctx, ingested.Job.Version, nil, correction,
		receipt, event, now)
	if err != nil || result.Replayed {
		t.Fatalf("set correction=%+v err=%v", result, err)
	}
	replay, err := repository.ApplyJobCorrectionCommand(ctx, ingested.Job.Version, nil, correction,
		receipt, event, now)
	if err != nil || !replay.Replayed || string(replay.Response) != string(result.Response) {
		t.Fatalf("replay correction=%+v err=%v", replay, err)
	}
	head, current, err := repository.GetOverrideHead(ctx, ingested.Job.JobID, "title")
	if err != nil || !current.Active || current.ActorID != "human:operator" {
		t.Fatalf("stored correction head=%+v current=%+v err=%v", head, current, err)
	}
	staleReplacement, _ := model.NewCuratedOverride("job-correction-title-stale", ingested.Job.JobID, "title",
		json.RawMessage(`"Stale"`), "human:other", "stale correction")
	staleReceipt, _ := model.NewCommandReceipt("job-correction-stale-command", "recruiting.job.correct",
		"sha256:job-correction-stale", json.RawMessage(`{"operation":"set"}`))
	staleEvent, _ := model.NewEventIntent("job-correction-stale-event", "job.correction.set", "job_override",
		staleReplacement.OverrideID, staleReplacement.Version, now.Add(time.Second).Format(time.RFC3339Nano),
		staleReceipt.CommandID, json.RawMessage(`{}`))
	staleHead := head
	staleHead.OverrideVersion--
	if _, err := repository.ApplyJobCorrectionCommand(ctx, ingested.Job.Version, &staleHead, staleReplacement,
		staleReceipt, staleEvent, now.Add(time.Second)); !errors.Is(err, ErrOverrideConflict) {
		t.Fatalf("stale replacement err=%v", err)
	}
	cleared, _ := current.Clear(current.Version, "human:operator", "verified detail is correct again")
	clearReceipt, _ := model.NewCommandReceipt("job-correction-clear-command", "recruiting.job.correct",
		"sha256:job-correction-clear", json.RawMessage(`{"operation":"clear"}`))
	clearAt := now.Add(2 * time.Second)
	clearEvent, _ := model.NewEventIntent("job-correction-clear-event", "job.correction.clear", "job_override",
		cleared.OverrideID, cleared.Version, clearAt.Format(time.RFC3339Nano), clearReceipt.CommandID,
		json.RawMessage(`{"field":"title"}`))
	if _, err := repository.ApplyJobCorrectionCommand(ctx, ingested.Job.Version, &head, cleared,
		clearReceipt, clearEvent, clearAt); err != nil {
		t.Fatal(err)
	}
	history, err := repository.ListOverrideHistory(ctx, ingested.Job.JobID, "title", 10)
	if err != nil || len(history) != 2 || history[0].Active != true || history[1].Active != false {
		t.Fatalf("correction history=%+v err=%v", history, err)
	}
	var receipts, events int
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_command_receipts
WHERE command_id IN ('job-correction-set-command','job-correction-clear-command','job-correction-stale-command')`).Scan(&receipts)
	_ = db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_event_outbox WHERE aggregate_type = 'job_override'
AND aggregate_id = ?`, correction.OverrideID).Scan(&events)
	if receipts != 2 || events != 2 {
		t.Fatalf("atomic correction receipts=%d events=%d", receipts, events)
	}
}
