package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestCompanyMergeLogicalMappingContract(t *testing.T) {
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("RECRUITING_MYSQL_TEST_DSN is not set")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	now := time.Date(2091, 4, 5, 6, 0, 0, 123000000, time.UTC)

	canonical := createMergeCompany(t, ctx, repository, "merge-canonical", "https://canonical.merge.example", now)
	aliasA := createMergeCompany(t, ctx, repository, "merge-alias-a", "https://a.merge.example", now)
	aliasB := createMergeCompany(t, ctx, repository, "merge-alias-b", "https://b.merge.example", now)
	canonical = readyMergeCompany(t, ctx, repository, canonical, now)
	aliasA = readyMergeCompany(t, ctx, repository, aliasA, now)
	canonicalDailySource := persistReadyDailySource(t, ctx, repository, canonical, "merge-canonical-daily", now)
	aliasDailySource := persistReadyDailySource(t, ctx, repository, aliasA, "merge-alias-daily", now)
	defer pauseExecutionSource(t, ctx, repository, canonicalDailySource.SourceID, now.Add(10*time.Minute))
	defer pauseExecutionSource(t, ctx, repository, aliasDailySource.SourceID, now.Add(10*time.Minute))
	source, err := model.NewRecruitmentSource("merge-alias-source", aliasA.CompanyID, "https://jobs.merge.example/a", "all", 1)
	if err != nil || repository.CreateSource(ctx, source, now) != nil {
		t.Fatalf("create alias source=%+v err=%v", source, err)
	}
	job, _ := model.NewSourceJob("merge-alias-job", source.SourceID, "job-1", "https://jobs.merge.example/a/1")
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertJob(ctx, tx, job, now); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	expected := map[string]uint64{canonical.CompanyID: canonical.Version, aliasA.CompanyID: aliasA.Version, aliasB.CompanyID: aliasB.Version}
	preview, err := repository.InspectCompanyMerge(ctx, CompanyMergePreviewRequest{
		MergePreviewID: "merge-preview-1", Action: model.CompanyMergeApply, CanonicalCompanyID: canonical.CompanyID,
		AliasCompanyIDs: []string{aliasB.CompanyID, aliasA.CompanyID}, ExpectedVersions: expected,
		RequestedBy: "operator-1", Reason: "deduplicate logical companies", ObservedAt: now,
	})
	if err != nil || len(preview.Members) != 2 || preview.Members[0].CompanyID != aliasA.CompanyID ||
		preview.Members[0].SourceCount != 2 || preview.Members[0].JobCount != 1 {
		t.Fatalf("merge preview=%+v err=%v", preview, err)
	}
	previewResult := applyMergePreviewFixture(t, ctx, repository, preview, "merge-preview-command", "hash-preview", now)
	if previewResult.Replayed {
		t.Fatal("first merge preview command was replayed")
	}
	replay := applyMergePreviewFixture(t, ctx, repository, preview, "merge-preview-command", "hash-preview", now)
	if !replay.Replayed || string(replay.Response) != string(previewResult.Response) {
		t.Fatalf("merge preview replay=%+v", replay)
	}

	confirmReceipt, _ := model.NewCommandReceipt("merge-confirm-command", "recruiting.company.merge.confirm", "hash-confirm", []byte(`{"next_action":"inspect"}`))
	confirmed, err := repository.ApplyCompanyMergeConfirmCommand(ctx, preview.MergePreviewID, preview.Version, preview.PreviewHash,
		confirmReceipt, "event-merge-confirmed", "operator-1", "confirm reviewed impact", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	var response struct {
		Preview model.CompanyMergePreview `json:"preview"`
		Aliases []CompanyAliasProjection  `json:"aliases"`
	}
	if err := json.Unmarshal(confirmed.Response, &response); err != nil || response.Preview.Status != model.CompanyMergeConfirmed || len(response.Aliases) != 2 {
		t.Fatalf("merge confirm response=%s err=%v", confirmed.Response, err)
	}
	replayedConfirm, err := repository.ApplyCompanyMergeConfirmCommand(ctx, preview.MergePreviewID, preview.Version, preview.PreviewHash,
		confirmReceipt, "event-merge-confirmed", "operator-1", "confirm reviewed impact", now.Add(time.Minute))
	if err != nil || !replayedConfirm.Replayed || string(replayedConfirm.Response) != string(confirmed.Response) {
		t.Fatalf("merge confirm replay=%+v err=%v", replayedConfirm, err)
	}

	storedCanonical, _ := repository.GetCompany(ctx, canonical.CompanyID)
	storedAlias, _ := repository.GetCompany(ctx, aliasA.CompanyID)
	storedSource, _ := repository.GetSource(ctx, source.SourceID)
	storedJob, _ := repository.GetJob(ctx, job.JobID)
	if storedCanonical != canonical || storedAlias != aliasA || !reflect.DeepEqual(storedSource, source) || storedJob != job {
		t.Fatalf("logical merge rewrote history company=%+v alias=%+v source=%+v job=%+v", storedCanonical, storedAlias, storedSource, storedJob)
	}
	var activeAliases, sourceCompany, jobSource, previewEvents, confirmEvents int
	if err := db.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM recruiting_company_aliases WHERE canonical_company_id = ? AND active_alias_key IS NOT NULL),
  (SELECT COUNT(*) FROM recruiting_sources WHERE source_id = ? AND company_id = ?),
  (SELECT COUNT(*) FROM recruiting_source_jobs WHERE job_id = ? AND source_id = ?),
  (SELECT COUNT(*) FROM recruiting_event_outbox WHERE event_id = 'event-merge-preview-command'),
  (SELECT COUNT(*) FROM recruiting_event_outbox WHERE event_id = 'event-merge-confirmed')`,
		canonical.CompanyID, source.SourceID, aliasA.CompanyID, job.JobID, source.SourceID).
		Scan(&activeAliases, &sourceCompany, &jobSource, &previewEvents, &confirmEvents); err != nil {
		t.Fatal(err)
	}
	if activeAliases != 2 || sourceCompany != 1 || jobSource != 1 || previewEvents != 1 || confirmEvents != 1 {
		t.Fatalf("merge facts aliases=%d source=%d job=%d events=%d/%d", activeAliases, sourceCompany, jobSource, previewEvents, confirmEvents)
	}
	mergedPlan, err := repository.PlanDailyRunAtCutoff(ctx, DailyPlanRequest{DailyRunID: "merge-daily-1", ScheduleDate: "2091-04-06",
		Schedule:  model.DailySchedule{PolicyVersion: 1, CutoffAt: "2091-04-06T00:00:00Z", WindowStartAt: "2091-04-06T01:00:00Z", WindowEndAt: "2091-04-06T03:00:00Z"},
		TriggerID: "merge-daily-trigger-1", EventID: "merge-daily-event-1"}, now.Add(time.Hour))
	if err != nil || mergedPlan.Occurrences != 1 {
		t.Fatalf("merged daily roster=%+v err=%v", mergedPlan, err)
	}
	mergedOccurrences, _ := repository.ListOccurrences(ctx, mergedPlan.Run.DailyRunID, "", 10)
	if len(mergedOccurrences.Items) != 1 || mergedOccurrences.Items[0].SourceID != canonicalDailySource.SourceID {
		t.Fatalf("logical alias remained in daily roster: %+v alias_source=%s", mergedOccurrences.Items, aliasDailySource.SourceID)
	}

	reverse, err := repository.InspectCompanyMerge(ctx, CompanyMergePreviewRequest{
		MergePreviewID: "merge-reverse-1", Action: model.CompanyMergeReverse, CanonicalCompanyID: canonical.CompanyID,
		AliasCompanyIDs: []string{aliasA.CompanyID}, ExpectedVersions: map[string]uint64{
			canonical.CompanyID: canonical.Version, aliasA.CompanyID: aliasA.Version},
		RequestedBy: "operator-1", Reason: "reverse one mistaken alias", ObservedAt: now.Add(2 * time.Minute),
	})
	if err != nil || reverse.Members[0].AliasVersion != 1 {
		t.Fatalf("reverse preview=%+v err=%v", reverse, err)
	}
	applyMergePreviewFixture(t, ctx, repository, reverse, "merge-reverse-preview-command", "hash-reverse-preview", now.Add(2*time.Minute))
	reverseReceipt, _ := model.NewCommandReceipt("merge-reverse-confirm-command", "recruiting.company.merge.confirm", "hash-reverse-confirm", []byte(`{"next_action":"inspect"}`))
	if _, err := repository.ApplyCompanyMergeConfirmCommand(ctx, reverse.MergePreviewID, reverse.Version, reverse.PreviewHash,
		reverseReceipt, "event-merge-reversed", "operator-1", "confirm reversal", now.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_company_aliases WHERE alias_company_id = ? AND active_alias_key IS NOT NULL`, aliasA.CompanyID).Scan(&activeAliases); err != nil || activeAliases != 0 {
		t.Fatalf("reversed alias remains active count=%d err=%v", activeAliases, err)
	}
	reversedPlan, err := repository.PlanDailyRunAtCutoff(ctx, DailyPlanRequest{DailyRunID: "merge-daily-2", ScheduleDate: "2091-04-07",
		Schedule:  model.DailySchedule{PolicyVersion: 1, CutoffAt: "2091-04-07T00:00:00Z", WindowStartAt: "2091-04-07T01:00:00Z", WindowEndAt: "2091-04-07T03:00:00Z"},
		TriggerID: "merge-daily-trigger-2", EventID: "merge-daily-event-2"}, now.Add(3*time.Hour))
	if err != nil || reversedPlan.Occurrences != 2 {
		t.Fatalf("reversed daily roster=%+v err=%v", reversedPlan, err)
	}

	stale, err := repository.InspectCompanyMerge(ctx, CompanyMergePreviewRequest{
		MergePreviewID: "merge-stale", Action: model.CompanyMergeApply, CanonicalCompanyID: canonical.CompanyID,
		AliasCompanyIDs: []string{aliasA.CompanyID}, ExpectedVersions: map[string]uint64{
			canonical.CompanyID: canonical.Version, aliasA.CompanyID: aliasA.Version},
		RequestedBy: "operator-1", Reason: "stale preview", ObservedAt: now.Add(4 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	applyMergePreviewFixture(t, ctx, repository, stale, "merge-stale-preview-command", "hash-stale-preview", now.Add(4*time.Minute))
	newName := "Alias A corrected"
	updated, _ := aliasA.Update(aliasA.Version, model.CompanyUpdate{Name: &newName})
	if err := repository.UpdateCompanyCAS(ctx, aliasA.Version, updated.Company, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	staleReceipt, _ := model.NewCommandReceipt("merge-stale-confirm", "recruiting.company.merge.confirm", "hash-stale-confirm", []byte(`{}`))
	_, err = repository.ApplyCompanyMergeConfirmCommand(ctx, stale.MergePreviewID, stale.Version, stale.PreviewHash,
		staleReceipt, "event-merge-stale", "operator-1", "must reject stale company", now.Add(6*time.Minute))
	var versionConflict *model.VersionConflictError
	if !errors.As(err, &versionConflict) {
		t.Fatalf("stale merge confirmation=%v", err)
	}
	var staleReceipts int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_command_receipts WHERE command_id = 'merge-stale-confirm'`).Scan(&staleReceipts); err != nil || staleReceipts != 0 {
		t.Fatalf("stale confirmation left receipt=%d err=%v", staleReceipts, err)
	}

	aliasC := createMergeCompany(t, ctx, repository, "merge-alias-c", "https://c.merge.example", now)
	racePreview, err := repository.InspectCompanyMerge(ctx, CompanyMergePreviewRequest{
		MergePreviewID: "merge-race", Action: model.CompanyMergeApply, CanonicalCompanyID: canonical.CompanyID,
		AliasCompanyIDs: []string{aliasC.CompanyID}, ExpectedVersions: map[string]uint64{
			canonical.CompanyID: canonical.Version, aliasC.CompanyID: aliasC.Version},
		RequestedBy: "operator-1", Reason: "confirm concurrently", ObservedAt: now.Add(7 * time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	applyMergePreviewFixture(t, ctx, repository, racePreview, "merge-race-preview", "hash-race-preview", now.Add(7*time.Minute))
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, suffix := range []string{"a", "b"} {
		suffix := suffix
		group.Add(1)
		go func() {
			defer group.Done()
			receipt, _ := model.NewCommandReceipt("merge-race-confirm-"+suffix, "recruiting.company.merge.confirm",
				"hash-race-confirm-"+suffix, []byte(`{}`))
			_, confirmErr := repository.ApplyCompanyMergeConfirmCommand(ctx, racePreview.MergePreviewID, racePreview.Version,
				racePreview.PreviewHash, receipt, "event-merge-race-"+suffix, "operator-1", "one winner", now.Add(8*time.Minute))
			results <- confirmErr
		}()
	}
	group.Wait()
	close(results)
	var winners, conflicts int
	for confirmErr := range results {
		if confirmErr == nil {
			winners++
			continue
		}
		if errors.As(confirmErr, &versionConflict) {
			conflicts++
			continue
		}
		t.Fatalf("unexpected concurrent confirmation error=%v", confirmErr)
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("concurrent confirmations winners=%d conflicts=%d", winners, conflicts)
	}
}

func createMergeCompany(t *testing.T, ctx context.Context, repository *Repository, id, website string, now time.Time) model.Company {
	t.Helper()
	company, err := model.NewCompany(id, "Company "+id, website)
	if err != nil || repository.CreateCompany(ctx, company, now) != nil {
		t.Fatalf("create company=%+v err=%v", company, err)
	}
	return company
}

func readyMergeCompany(t *testing.T, ctx context.Context, repository *Repository, company model.Company, now time.Time) model.Company {
	t.Helper()
	next, err := company.StartDiscovery(company.Version)
	if err == nil {
		err = repository.UpdateCompanyCAS(ctx, company.Version, next, now)
	}
	company = next
	if err == nil {
		next, err = company.StartInitialization(company.Version)
	}
	if err == nil {
		err = repository.UpdateCompanyCAS(ctx, company.Version, next, now)
	}
	company = next
	if err == nil {
		next, err = company.MarkReady(company.Version)
	}
	if err == nil {
		err = repository.UpdateCompanyCAS(ctx, company.Version, next, now)
	}
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func applyMergePreviewFixture(t *testing.T, ctx context.Context, repository *Repository, preview model.CompanyMergePreview,
	commandID, requestHash string, now time.Time) CommandResult {
	t.Helper()
	response, _ := json.Marshal(map[string]any{"preview": preview})
	receipt, _ := model.NewCommandReceipt(commandID, "recruiting.company.merge.preview", requestHash, response)
	event, _ := model.NewEventIntent("event-"+commandID, "company.merge.previewed", "company_merge_preview", preview.MergePreviewID,
		preview.Version, now.UTC().Format(time.RFC3339Nano), commandID, json.RawMessage(`{"requested_by":"operator-1"}`))
	result, err := repository.ApplyCompanyMergePreviewCommand(ctx, preview, receipt, event, now)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
