package store

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestBackfillPreviewFreezesHistoricalVersionsAndLeavesCheckpointUntouched(t *testing.T) {
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
	now := time.Date(2090, 1, 1, 0, 0, 0, 0, time.UTC)

	company, _ := model.NewCompany("backfill-company", "Backfill Company", "https://backfill.example.com")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("backfill-source", company.CompanyID,
		"https://backfill.example.com/jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	recipe := activeRecipe(t, "backfill-detail-recipe", model.RecipeDetail, "backfill.example.com", 1, "backfill-detail")
	if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
		t.Fatal(err)
	}
	job, _ := model.NewSourceJob("backfill-job", source.SourceID, "remote-1", "https://backfill.example.com/jobs/1")
	seedTx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertJob(ctx, seedTx, job, now); err != nil {
		t.Fatal(err)
	}
	if err := seedTx.Commit(); err != nil {
		t.Fatal(err)
	}
	evidenceWork, _ := model.NewWork("backfill-evidence-work", "job", job.JobID, "detail_sync", "schedule")
	if err := repository.CreateWork(ctx, evidenceWork, WorkPlacement{NotBefore: now}, now); err != nil {
		t.Fatal(err)
	}
	for index, observed := range []time.Time{now.Add(24 * time.Hour), now.Add(48 * time.Hour)} {
		id := "backfill-detail-1"
		artifactID := "backfill-artifact-1"
		if index == 1 {
			id, artifactID = "backfill-detail-2", "backfill-artifact-2"
		}
		artifact, _ := model.NewArtifactMetadata(artifactID, model.ArtifactResponse, "sha256:artifact-"+id,
			"file://backfill/"+artifactID, evidenceWork.WorkID, "", "recruiting", "test", false)
		if err := insertArtifact(ctx, db, artifact, false, observed); err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, `INSERT INTO recruiting_job_detail_versions(
detail_version_id, job_id, refresh_generation, detail_version, content_hash, artifact_id,
recipe_id, recipe_version, observed_at, detail_json) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, JSON_OBJECT('title','role'))`,
			id, job.JobID, uint64(index+1), uint64(index+1), "sha256:detail-"+id, artifactID,
			recipe.RecipeID, recipe.Version, observed); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.ExecContext(ctx, `INSERT INTO recruiting_listing_observations(
observation_id, occurrence_id, source_id, job_id, source_job_key, detail_url, activity_at,
listing_fingerprint, recipe_id, recipe_version, artifact_id, observed_at, observation_json)
VALUES ('backfill-observation', 'manual-history', ?, ?, ?, ?, ?, 'sha256:listing', ?, ?,
'backfill-artifact-1', ?, JSON_OBJECT('job_id', ?))`, source.SourceID, job.JobID, job.SourceJobKey,
		job.DetailURL, now.Add(12*time.Hour), recipe.RecipeID, recipe.Version, now.Add(12*time.Hour), job.JobID); err != nil {
		t.Fatal(err)
	}

	create := func(id string, mode model.BackfillMode) model.Backfill {
		parent, _ := model.NewWork("work-"+id, "source", source.SourceID, "historical_backfill", "human")
		parent, _ = parent.WithCausality("human:backfill-operator", "message:"+id, "")
		backfill, err := model.NewBackfill(id, parent.WorkID, parent.InitiatorActorID, "source", source.SourceID,
			mode, now.Format(time.RFC3339), now.Add(72*time.Hour).Format(time.RFC3339),
			[]string{"description", "title"}, recipe.RecipeID, recipe.Version, 1)
		if err != nil {
			t.Fatal(err)
		}
		response, _ := json.Marshal(backfill)
		receipt, _ := model.NewCommandReceipt("command-"+id, "recruiting.backfill.create", "sha256:"+id, response)
		event, _ := model.NewEventIntent("event-"+id, "backfill.created", "work", parent.WorkID,
			parent.Version, now.Format(time.RFC3339Nano), receipt.CommandID, json.RawMessage(`{}`))
		if _, err := repository.ApplyCreateBackfillCommand(ctx, parent,
			WorkPlacement{BusinessKey: "backfill|" + id, NotBefore: now}, backfill, receipt, event, now); err != nil {
			t.Fatal(err)
		}
		return backfill
	}

	var checkpointsBefore int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_checkpoints").Scan(&checkpointsBefore); err != nil {
		t.Fatal(err)
	}
	historical := create("backfill-historical", model.BackfillArtifactRecompute)
	historical, selected, err := repository.PreviewBackfillChunk(ctx, historical.BackfillID, historical.Version, now.Add(time.Minute))
	if err != nil || historical.Status != model.BackfillPreviewed || len(selected) != 2 ||
		selected[0].JobID != selected[1].JobID || selected[0].InputDetailVersionID == selected[1].InputDetailVersionID {
		t.Fatalf("historical=%+v selected=%+v err=%v", historical, selected, err)
	}
	historicalWork, err := repository.GetWork(ctx, historical.WorkID)
	if err != nil || historicalWork.Status != model.WorkWaitingHuman || historicalWork.WaitingReason != "preview_ready" {
		t.Fatalf("historical preview Work=%+v err=%v", historicalWork, err)
	}
	confirmed, _ := historical.Confirm(historical.Version, historical.PreviewHash)
	confirmedWork, _ := historicalWork.Start(historicalWork.Version)
	confirmResponse, _ := json.Marshal(map[string]any{"backfill": confirmed, "work": confirmedWork})
	confirmReceipt, _ := model.NewCommandReceipt("command-backfill-confirm", "recruiting.backfill.confirm",
		"sha256:backfill-confirm", confirmResponse)
	confirmAt := now.Add(90 * time.Second)
	confirmEvent, _ := model.NewEventIntent("event-backfill-confirm", "backfill.confirmed", "work",
		historicalWork.WorkID, confirmedWork.Version, confirmAt.Format(time.RFC3339Nano), confirmReceipt.CommandID, json.RawMessage(`{}`))
	if _, err := repository.ApplyConfirmBackfillCommand(ctx, historical.BackfillID, historical.Version,
		historicalWork.Version, historical.PreviewHash, confirmReceipt, confirmEvent, confirmAt); err != nil {
		t.Fatal(err)
	}
	storedConfirmed, _ := repository.GetBackfill(ctx, historical.BackfillID)
	storedConfirmedWork, _ := repository.GetWork(ctx, historicalWork.WorkID)
	if storedConfirmed.Status != model.BackfillRunning || storedConfirmedWork.Status != model.WorkRunning {
		t.Fatalf("confirmed backfill=%+v work=%+v", storedConfirmed, storedConfirmedWork)
	}
	page, err := repository.ListBackfillItems(ctx, historical.BackfillID, "", 1)
	if err != nil || len(page.Items) != 1 || page.NextCursor == "" {
		t.Fatalf("historical page=%+v err=%v", page, err)
	}
	pageTwo, err := repository.ListBackfillItems(ctx, historical.BackfillID, page.NextCursor, 1)
	if err != nil || len(pageTwo.Items) != 1 || pageTwo.NextCursor != "" {
		t.Fatalf("historical page two=%+v err=%v", pageTwo, err)
	}

	live := create("backfill-live", model.BackfillLiveRefetch)
	live, selected, err = repository.PreviewBackfillChunk(ctx, live.BackfillID, live.Version, now.Add(2*time.Minute))
	if err != nil || live.Status != model.BackfillPreviewed || len(selected) != 1 ||
		selected[0].InputDetailVersionID != "" || selected[0].InputArtifactID != "" || selected[0].InputObservedAt != "" {
		t.Fatalf("live=%+v selected=%+v err=%v", live, selected, err)
	}
	liveWork, err := repository.GetWork(ctx, live.WorkID)
	if err != nil || liveWork.Status != model.WorkWaitingHuman || liveWork.WaitingReason != "preview_ready" {
		t.Fatalf("live preview Work=%+v err=%v", liveWork, err)
	}
	var checkpointsAfter int
	if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM recruiting_checkpoints").Scan(&checkpointsAfter); err != nil {
		t.Fatal(err)
	}
	if checkpointsAfter != checkpointsBefore {
		t.Fatalf("backfill preview changed checkpoints: before=%d after=%d", checkpointsBefore, checkpointsAfter)
	}
}
