package e2e

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
	"github.com/wanpengxie/atoll/drivers/tools/recruiting/store"
)

func TestRecruitingOperatorCorrectsAndClearsJobWithoutRewritingCrawlFacts(t *testing.T) {
	h := newHarnessShell(t)
	runtimeDSN := startRecruitingMySQL(t)
	h.env = append(h.env, "ATOLL_RECRUITING_MYSQL_DSN="+runtimeDSN)
	h.startServer()
	operator := newAPIClient(t, h.base)
	registered := operator.register("job-correction-operator", "job-correction-operator@example.test", "operator-local-password")
	homeID := stringField(t, registered, "home_channel_id")
	time.Sleep(500 * time.Millisecond)
	ws := dialWS(t, h.base, operator.cookieHeader(), map[string]int64{homeID: 0})
	const controlName = "job-correction-control"
	registrarRequest(t, ws, homeID, systemActor, "system.actor.template.create", map[string]any{
		"id": controlName, "name": controlName, "class": "recruiting",
		"description": "Audited Job correction control.",
		"config": map[string]any{"executor_id": "unused-job-correction-executor",
			"reconcile_interval_ms": 30000, "daily_schedule_enabled": false}, "visibility": "private",
	})
	intro := ws.request(homeID, "system.member.create", systemActor, map[string]any{"decl_id": controlName})
	controlID := stringField(t, intro, "member")
	waitRecruitingReady(t, ws, homeID, controlID, h.server)
	job := seedJobCorrectionFixture(t, runtimeDSN, time.Now().UTC().Add(-time.Minute))

	setPayload := map[string]any{
		"command_id": "e2e-job-correction-set", "target": map[string]any{"target_type": "job", "target_id": job.JobID},
		"expected_version": job.Version, "operation": "set", "field": "title",
		"override_id": "e2e-job-title-override", "value": "Human-confirmed title",
		"reason": "operator verified the employer page",
	}
	set := ws.request(homeID, "recruiting.job.correct", controlID, setPayload)
	if nestedStringField(t, set, "correction", "field") != "title" ||
		nestedStringField(t, set, "correction", "actor_id") == "" ||
		!strings.HasPrefix(stringField(t, set, "requested_by"), "human:job-correction-operator:") ||
		stringField(t, set, "effective_source") != string(model.FieldFromOverride) {
		t.Fatalf("Job correction response=%v", set)
	}
	setReplay := ws.request(homeID, "recruiting.job.correct", controlID, setPayload)
	if nestedNumberField(t, setReplay, "correction", "version") != 1 {
		t.Fatalf("Job correction replay=%v", setReplay)
	}
	view := ws.request(homeID, "recruiting.job.correction.get", controlID,
		map[string]any{"job_id": job.JobID, "field": "title", "limit": 10})
	if nestedStringField(t, view, "head", "override_id") != "e2e-job-title-override" ||
		nestedNumberField(t, view, "head", "override_version") != 1 {
		t.Fatalf("Job correction view=%v", view)
	}

	updated := appendLaterJobObservation(t, runtimeDSN, job, time.Now().UTC())
	afterCrawl := ws.request(homeID, "recruiting.job.correction.get", controlID,
		map[string]any{"job_id": job.JobID, "field": "title", "limit": 10})
	if nestedStringField(t, afterCrawl, "head", "override_id") != "e2e-job-title-override" ||
		nestedNumberField(t, afterCrawl, "head", "override_version") != 1 {
		t.Fatalf("later crawl displaced correction=%v", afterCrawl)
	}
	if _, terminal, err := ws.tryRequest(homeID, "recruiting.job.correct", controlID, map[string]any{
		"command_id": "e2e-job-correction-stale-job", "target": map[string]any{"target_type": "job", "target_id": job.JobID},
		"expected_version": job.Version, "operation": "set", "field": "title",
		"override_id": "e2e-job-title-stale", "expected_override_id": "e2e-job-title-override",
		"expected_override_version": 1, "value": "Stale human title", "reason": "stale browser tab",
	}); err == nil || terminal["error_code"] != "version_conflict" {
		t.Fatalf("stale Job correction terminal=%v err=%v", terminal, err)
	}

	clearPayload := map[string]any{
		"command_id": "e2e-job-correction-clear", "target": map[string]any{"target_type": "job", "target_id": job.JobID},
		"expected_version": updated.Version, "operation": "clear", "field": "title",
		"override_id": "e2e-job-title-override", "expected_override_id": "e2e-job-title-override",
		"expected_override_version": 1, "reason": "new verified crawl is authoritative again",
	}
	cleared := ws.request(homeID, "recruiting.job.correct", controlID, clearPayload)
	if stringField(t, cleared, "next_action") != "use_verified_or_listing_fact" ||
		nestedNumberField(t, cleared, "correction", "version") != 2 {
		t.Fatalf("cleared Job correction=%v", cleared)
	}

	h.restartServer()
	recoveredOperator := newAPIClient(t, h.base)
	if login := recoveredOperator.login("job-correction-operator@example.test", "operator-local-password"); login["id"] != "job-correction-operator" {
		t.Fatalf("Job correction operator login after restart=%v", login)
	}
	recovered := dialWS(t, h.base, recoveredOperator.cookieHeader(), map[string]int64{homeID: 0})
	waitRecruitingReady(t, recovered, homeID, controlID, h.server)
	clearReplay := recovered.request(homeID, "recruiting.job.correct", controlID, clearPayload)
	if nestedNumberField(t, clearReplay, "correction", "version") != 2 {
		t.Fatalf("clear replay after restart=%v", clearReplay)
	}
	history := recovered.request(homeID, "recruiting.job.correction.get", controlID,
		map[string]any{"job_id": job.JobID, "field": "title", "limit": 10})
	items, _ := history["history"].([]any)
	if len(items) != 2 || items[1].(map[string]any)["active"] != false {
		t.Fatalf("Job correction history after restart=%v", history)
	}
}

func seedJobCorrectionFixture(t *testing.T, dsn string, now time.Time) model.SourceJob {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	company, _ := model.NewCompany("e2e-job-correction-company", "Correction Company", "https://correction.example")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("e2e-job-correction-source", company.CompanyID,
		"https://correction.example/jobs", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}
	result, err := repository.ApplyListingObservation(ctx, store.ListingIngest{
		Observation: model.ListingObservation{ObservationID: "e2e-job-correction-observation-1",
			OccurrenceID: "e2e-job-correction-occurrence-1", SourceID: source.SourceID, SourceJobKey: "external-1",
			DetailURL: "https://correction.example/jobs/1", ActivityAt: now.Format(time.RFC3339),
			ListingFingerprint: "listing-v1", RecipeID: "e2e-job-correction-recipe", RecipeVersion: 1,
			ArtifactID: "e2e-job-correction-artifact-1"},
		ObservedAt: now, NewJobID: "e2e-job-correction-job", DetailWorkID: "e2e-job-correction-detail-work-1",
		Origin: "https://correction.example", Capability: "http.fetch", Priority: 10, NotBefore: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.Job
}

func appendLaterJobObservation(t *testing.T, dsn string, job model.SourceJob, now time.Time) model.SourceJob {
	t.Helper()
	db, err := store.Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	repository, _ := store.NewRepository(db)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	result, err := repository.ApplyListingObservation(ctx, store.ListingIngest{
		Observation: model.ListingObservation{ObservationID: "e2e-job-correction-observation-2",
			OccurrenceID: "e2e-job-correction-occurrence-2", SourceID: job.SourceID, SourceJobKey: job.SourceJobKey,
			DetailURL: job.DetailURL, ActivityAt: now.Format(time.RFC3339), ListingFingerprint: "listing-v2",
			RecipeID: "e2e-job-correction-recipe", RecipeVersion: 1, ArtifactID: "e2e-job-correction-artifact-2"},
		ObservedAt: now, NewJobID: job.JobID, DetailWorkID: "e2e-job-correction-detail-work-2",
		Origin: "https://correction.example", Capability: "http.fetch", Priority: 10, NotBefore: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return result.Job
}
