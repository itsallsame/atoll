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

func TestCompanyWebsiteChangeCreatesReviewAndRollbackAppendsHistory(t *testing.T) {
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
	now := time.Date(2026, 9, 12, 5, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("website-change-company", "Website Change", "https://old.example/careers")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	source, _ := model.NewRecruitmentSource("website-change-source", company.CompanyID,
		"https://jobs.example/openings", "all", 1)
	if err := repository.CreateSource(ctx, source, now); err != nil {
		t.Fatal(err)
	}

	apply := func(before model.Company, website, revisionID, reviewWorkID, reverts, commandID string,
		at time.Time) (model.Company, model.CompanyWebsiteRevision, model.Work, CommandResult, error) {
		updated, updateErr := before.Update(before.Version, model.CompanyUpdate{Website: &website})
		if updateErr != nil {
			t.Fatal(updateErr)
		}
		work, _ := model.NewWork(reviewWorkID, "company", before.CompanyID, "source_relationship_review", "human")
		work, _ = work.WithCausality("human:website-operator", commandID+"-message", "")
		revision, revisionErr := model.NewCompanyWebsiteRevision(revisionID, before, updated.Company,
			reviewWorkID, work.InitiatorActorID, "official website correction", reverts, at)
		if revisionErr != nil {
			t.Fatal(revisionErr)
		}
		response, _ := json.Marshal(CompanyWebsiteChangeOutcome{Company: updated.Company, Revision: revision, Work: work})
		receipt, _ := model.NewCommandReceipt(commandID, "recruiting.company.update", "sha256:"+commandID, response)
		companyEvent, _ := model.NewEventIntent(commandID+"-company-event", "company.updated", "company",
			before.CompanyID, updated.Company.Version, at.Format(time.RFC3339Nano), commandID, json.RawMessage(`{}`))
		reviewEvent, _ := model.NewEventIntent(commandID+"-review-event", "company.website_review_required", "work",
			work.WorkID, work.Version, at.Format(time.RFC3339Nano), commandID, json.RawMessage(`{}`))
		result, applyErr := repository.ApplyCompanyWebsiteChangeCommand(ctx, before.Version, updated.Company,
			revision, work, WorkPlacement{BusinessKey: "company-website-review|" + before.CompanyID + "|" +
				jsonNumber(updated.Company.ConfigurationVersion), NotBefore: at}, receipt, companyEvent, reviewEvent, at)
		return updated.Company, revision, work, result, applyErr
	}

	firstCompany, firstRevision, firstWork, first, err := apply(company, "https://new.example/careers",
		"website-revision-1", "website-review-1", "", "website-change-1", now.Add(time.Minute))
	if err != nil || first.Replayed {
		t.Fatalf("website change result=%+v err=%v", first, err)
	}
	replayCompany, _, _, replay, err := apply(company, "https://new.example/careers",
		"website-revision-1", "website-review-1", "", "website-change-1", now.Add(time.Minute))
	if err != nil || !replay.Replayed || replayCompany != firstCompany {
		t.Fatalf("website change replay=%+v err=%v", replay, err)
	}
	head, headVersion, err := repository.GetCurrentCompanyWebsiteRevision(ctx, company.CompanyID)
	storedWork, workErr := repository.GetWork(ctx, firstWork.WorkID)
	storedSource, sourceErr := repository.GetSource(ctx, source.SourceID)
	if err != nil || workErr != nil || sourceErr != nil || head != firstRevision || headVersion != 1 ||
		storedWork.Status != model.WorkOpen || storedWork.Purpose != "source_relationship_review" ||
		!reflect.DeepEqual(storedSource, source) {
		t.Fatalf("website review facts head=%+v/%d Work=%+v Source=%+v err=%v/%v/%v",
			head, headVersion, storedWork, storedSource, err, workErr, sourceErr)
	}

	rollbackCompany, rollbackRevision, _, rollback, err := apply(firstCompany, firstRevision.PreviousWebsite,
		"website-revision-2", "website-review-2", firstRevision.RevisionID, "website-rollback-1", now.Add(2*time.Minute))
	if err != nil || rollback.Replayed || rollbackCompany.Website != company.Website ||
		rollbackCompany.ConfigurationVersion != 3 || rollbackRevision.RevertsRevisionID != firstRevision.RevisionID {
		t.Fatalf("website rollback result=%+v Company=%+v revision=%+v err=%v",
			rollback, rollbackCompany, rollbackRevision, err)
	}
	head, headVersion, err = repository.GetCurrentCompanyWebsiteRevision(ctx, company.CompanyID)
	if err != nil || head != rollbackRevision || headVersion != 2 {
		t.Fatalf("rollback head=%+v/%d err=%v", head, headVersion, err)
	}

	_, _, _, _, staleErr := apply(rollbackCompany, firstRevision.Website,
		"website-revision-stale", "website-review-stale", firstRevision.RevisionID,
		"website-rollback-stale", now.Add(3*time.Minute))
	if !errors.Is(staleErr, ErrWebsiteRevisionConflict) {
		t.Fatal("stale rollback reversed a revision that is no longer the current head")
	}
	storedCompany, _ := repository.GetCompany(ctx, company.CompanyID)
	var revisions, reviewWorks, receipts int
	if err := db.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM recruiting_company_website_revisions WHERE company_id = ?),
  (SELECT COUNT(*) FROM recruiting_works WHERE purpose = 'source_relationship_review' AND company_id = ?),
  (SELECT COUNT(*) FROM recruiting_command_receipts WHERE command_id = 'website-rollback-stale')`,
		company.CompanyID, company.CompanyID).Scan(&revisions, &reviewWorks, &receipts); err != nil {
		t.Fatal(err)
	}
	if storedCompany != rollbackCompany || revisions != 2 || reviewWorks != 2 || receipts != 0 {
		t.Fatalf("stale rollback left facts: err=%v Company=%+v revisions=%d works=%d receipts=%d",
			staleErr, storedCompany, revisions, reviewWorks, receipts)
	}

	recipe := activeRecipe(t, "website-rediscovery-recipe", model.RecipeDiscovery, "old.example", 1,
		"website-rediscovery-contract")
	if err := repository.CreateRecipe(ctx, recipe, now.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	wrongWork, _ := model.NewWork("website-rediscovery-wrong-work", "company", company.CompanyID,
		"source_discovery", "human")
	wrongWork, _ = wrongWork.WithCausality("human:website-operator", "website-rediscovery-wrong-message",
		firstRevision.ReviewWorkID)
	wrongDiscovery, _ := model.NewSourceDiscovery("website-rediscovery-wrong", wrongWork.WorkID,
		rollbackCompany, 1, rollbackCompany.Website, recipe)
	wrongDiscovery, _ = wrongDiscovery.BindWebsiteRevision(rollbackCompany, rollbackRevision)
	placement := WorkPlacement{BusinessKey: "source-discovery|" + company.CompanyID + "|1",
		Capability: recipe.Execution.RequiredCapability, Origin: "https://old.example", NotBefore: now.Add(5 * time.Minute)}
	if err := repository.CreateSourceDiscovery(ctx, wrongDiscovery, wrongWork, placement, now.Add(5*time.Minute)); err == nil {
		t.Fatal("rediscovery accepted the current revision with a superseded review Work")
	}
	rediscoveryWork, _ := model.NewWork("website-rediscovery-work", "company", company.CompanyID,
		"source_discovery", "human")
	rediscoveryWork, _ = rediscoveryWork.WithCausality("human:website-operator", "website-rediscovery-message",
		rollbackRevision.ReviewWorkID)
	rediscovery, _ := model.NewSourceDiscovery("website-rediscovery", rediscoveryWork.WorkID,
		rollbackCompany, 1, rollbackCompany.Website, recipe)
	rediscovery, _ = rediscovery.BindWebsiteRevision(rollbackCompany, rollbackRevision)
	if err := repository.CreateSourceDiscovery(ctx, rediscovery, rediscoveryWork, placement, now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	storedDiscovery, err := repository.GetSourceDiscovery(ctx, rediscovery.DiscoveryID)
	if err != nil || storedDiscovery.WebsiteRevisionID != rollbackRevision.RevisionID {
		t.Fatalf("rediscovery website lineage=%+v err=%v", storedDiscovery, err)
	}
}

func TestConcurrentCompanyWebsiteChangesCommitOneCompleteRevision(t *testing.T) {
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
	now := time.Date(2026, 9, 12, 7, 0, 0, 0, time.UTC)
	company, _ := model.NewCompany("concurrent-website-company", "Concurrent Website", "https://old.example")
	if err := repository.CreateCompany(ctx, company, now); err != nil {
		t.Fatal(err)
	}
	type candidate struct {
		company      model.Company
		revision     model.CompanyWebsiteRevision
		work         model.Work
		placement    WorkPlacement
		receipt      model.CommandReceipt
		companyEvent model.EventIntent
		reviewEvent  model.EventIntent
	}
	build := func(index int, website string) candidate {
		commandID := "concurrent-website-command-" + jsonNumber(uint64(index))
		updated, updateErr := company.Update(company.Version, model.CompanyUpdate{Website: &website})
		if updateErr != nil {
			t.Fatal(updateErr)
		}
		work, _ := model.NewWork("concurrent-website-work-"+jsonNumber(uint64(index)), "company", company.CompanyID,
			"source_relationship_review", "human")
		work, _ = work.WithCausality("human:concurrent-operator", "concurrent-message-"+jsonNumber(uint64(index)), "")
		revision, revisionErr := model.NewCompanyWebsiteRevision("concurrent-website-revision-"+jsonNumber(uint64(index)),
			company, updated.Company, work.WorkID, work.InitiatorActorID, "concurrent website correction", "", now.Add(time.Minute))
		if revisionErr != nil {
			t.Fatal(revisionErr)
		}
		response, _ := json.Marshal(CompanyWebsiteChangeOutcome{Company: updated.Company, Revision: revision, Work: work})
		receipt, _ := model.NewCommandReceipt(commandID, "recruiting.company.update", "sha256:"+commandID, response)
		companyEvent, _ := model.NewEventIntent(commandID+"-company-event", "company.updated", "company",
			company.CompanyID, updated.Company.Version, now.Add(time.Minute).Format(time.RFC3339Nano), commandID, json.RawMessage(`{}`))
		reviewEvent, _ := model.NewEventIntent(commandID+"-review-event", "company.website_review_required", "work",
			work.WorkID, work.Version, now.Add(time.Minute).Format(time.RFC3339Nano), commandID, json.RawMessage(`{}`))
		return candidate{company: updated.Company, revision: revision, work: work,
			placement: WorkPlacement{BusinessKey: "company-website-review|" + company.CompanyID + "|2", NotBefore: now.Add(time.Minute)},
			receipt:   receipt, companyEvent: companyEvent, reviewEvent: reviewEvent}
	}
	candidates := []candidate{build(1, "https://one.example"), build(2, "https://two.example")}
	start := make(chan struct{})
	errorsByIndex := make([]error, len(candidates))
	var group sync.WaitGroup
	for index := range candidates {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			item := candidates[index]
			_, errorsByIndex[index] = repository.ApplyCompanyWebsiteChangeCommand(ctx, company.Version, item.company,
				item.revision, item.work, item.placement, item.receipt, item.companyEvent, item.reviewEvent, now.Add(time.Minute))
		}(index)
	}
	close(start)
	group.Wait()
	succeeded, conflicted := 0, 0
	for _, applyErr := range errorsByIndex {
		if applyErr == nil {
			succeeded++
			continue
		}
		var conflict *model.VersionConflictError
		if errors.As(applyErr, &conflict) {
			conflicted++
		}
	}
	var revisions, works, receipts, events, heads int
	if err := db.QueryRowContext(ctx, `SELECT
  (SELECT COUNT(*) FROM recruiting_company_website_revisions WHERE company_id = ?),
  (SELECT COUNT(*) FROM recruiting_works WHERE purpose = 'source_relationship_review' AND company_id = ?),
  (SELECT COUNT(*) FROM recruiting_command_receipts WHERE command_id LIKE 'concurrent-website-command-%'),
  (SELECT COUNT(*) FROM recruiting_event_outbox WHERE cause_command_id LIKE 'concurrent-website-command-%'),
  (SELECT COUNT(*) FROM recruiting_company_website_heads WHERE company_id = ?)`,
		company.CompanyID, company.CompanyID, company.CompanyID).Scan(&revisions, &works, &receipts, &events, &heads); err != nil {
		t.Fatal(err)
	}
	storedCompany, err := repository.GetCompany(ctx, company.CompanyID)
	if err != nil || succeeded != 1 || conflicted != 1 || revisions != 1 || works != 1 || receipts != 1 || events != 2 ||
		heads != 1 || storedCompany.Version != 2 || storedCompany.ConfigurationVersion != 2 {
		t.Fatalf("concurrent website convergence errors=%v success/conflict=%d/%d facts=%d/%d/%d/%d/%d Company=%+v err=%v",
			errorsByIndex, succeeded, conflicted, revisions, works, receipts, events, heads, storedCompany, err)
	}
}

func jsonNumber(value uint64) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
