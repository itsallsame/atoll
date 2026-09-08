package model

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestEffectiveFieldPreservesEvidencePriority(t *testing.T) {
	listing := &FieldValue{Value: json.RawMessage(`"listing"`), Source: FieldFromListing, SourceVersion: 1}
	detail := &FieldValue{Value: json.RawMessage(`"detail"`), Source: FieldFromDetail, SourceVersion: 4}
	override, err := NewCuratedOverride("override-1", "job-1", "title", json.RawMessage(`"curated"`), "human:alice", "verified correction")
	if err != nil {
		t.Fatal(err)
	}
	effective, ok := EffectiveField(listing, detail, &override)
	if !ok || effective.Source != FieldFromOverride || string(effective.Value) != `"curated"` {
		t.Fatalf("override did not win: %+v", effective)
	}
	override, err = override.Clear(override.Version, "human:bob", "source corrected")
	if err != nil {
		t.Fatal(err)
	}
	effective, ok = EffectiveField(listing, detail, &override)
	if !ok || effective.Source != FieldFromDetail || string(effective.Value) != `"detail"` {
		t.Fatalf("verified detail did not reappear after clear: %+v", effective)
	}
}

func TestRepairIncidentCoalescesSameFailure(t *testing.T) {
	first, err := NewRepairIncident("repair-1", FailureRecipeVersion, "recipe-1", "selector_missing", "v3", "work-2")
	if err != nil {
		t.Fatal(err)
	}
	key, err := RepairKey(FailureRecipeVersion, "recipe-1", "selector_missing", "v3")
	if err != nil || key != first.RepairKey {
		t.Fatalf("repair key is unstable: %q %v", key, err)
	}
	duplicate, err := first.AddAffectedWork(first.Version, "work-2")
	if err != nil || duplicate.Version != first.Version {
		t.Fatalf("duplicate affected work was not idempotent: %+v %v", duplicate, err)
	}
	second, err := first.AddAffectedWork(first.Version, "work-1")
	if err != nil || second.Version != first.Version+1 || second.AffectedWorkIDs[0] != "work-1" {
		t.Fatalf("affected works not deterministically aggregated: %+v %v", second, err)
	}
}

func TestBudgetPermitHasOneTerminalDisposition(t *testing.T) {
	permit, err := NewBudgetPermit("permit-1", "attempt-1", "jobs.example.com", "profile-1", "browser.recipe", "company-1", 2)
	if err != nil {
		t.Fatal(err)
	}
	released, err := permit.Release(permit.Version)
	if err != nil || released.Status != PermitReleased {
		t.Fatalf("release = %+v %v", released, err)
	}
	if _, err := released.Expire(released.Version); err == nil {
		t.Fatal("released permit was expired a second time")
	}
}

func TestBatchAggregationAndCancellationKeepChildBoundaries(t *testing.T) {
	parent, _ := NewWork("parent-1", "company_set", "selection-1", "company_import", "human")
	openChild, err := NewChildWork(parent, "child-1", "company", "company-1", "company_import_item", "parent")
	if err != nil {
		t.Fatal(err)
	}
	runningChild, _ := NewChildWork(parent, "child-2", "company", "company-2", "company_import_item", "parent")
	runningChild, _ = runningChild.Start(runningChild.Version)
	attempt, _ := NewAttempt("attempt-1", runningChild)
	finishedChild, _ := NewChildWork(parent, "child-3", "company", "company-3", "company_import_item", "parent")
	finishedChild, _ = finishedChild.Start(finishedChild.Version)
	finishedChild, _ = finishedChild.Complete(finishedChild.Version, ResolutionSucceeded, "", "")
	canceled, err := CancelBatchChildren(parent.WorkID, []Work{openChild, runningChild, finishedChild})
	if err != nil {
		t.Fatal(err)
	}
	if canceled[0].Status != WorkCanceled || canceled[1].Status != WorkCanceled || canceled[2].Status != WorkCompleted {
		t.Fatalf("unexpected cancellation result: %+v", canceled)
	}
	if err := attempt.CanSubmit(canceled[1]); err == nil {
		t.Fatal("in-flight child result was not fenced")
	}
	outcome, err := AggregateBatch([]BatchItemResult{
		{ItemKey: "company-1", Status: BatchItemCanceled},
		{ItemKey: "company-2", Status: BatchItemWaitingHuman},
		{ItemKey: "company-3", Status: BatchItemSucceeded},
	})
	if err != nil || outcome.Total != 3 || outcome.Canceled != 1 || outcome.WaitingHuman != 1 || outcome.Succeeded != 1 {
		t.Fatalf("batch outcome = %+v %v", outcome, err)
	}
	if _, err := AggregateBatch([]BatchItemResult{{ItemKey: "same", Status: BatchItemSucceeded}, {ItemKey: "same", Status: BatchItemFailed}}); err == nil {
		t.Fatal("duplicate item result was accepted")
	}
}

func TestCompanyImportBindsPreviewAndLifecycleToImmutableArtifact(t *testing.T) {
	artifactHash := "sha256:" + strings.Repeat("a", 64)
	batch, err := NewCompanyImport("import-1", "parent-1", "artifact://imports/companies.csv", artifactHash, "company-import.v1", 2)
	if err != nil {
		t.Fatal(err)
	}
	items := []CompanyImportItem{
		{ItemKey: "row-2", CompanyID: "company-2", Name: "Two", Website: "HTTPS://TWO.EXAMPLE:443/"},
		{ItemKey: "row-1", CompanyID: "company-1", Name: "One", Website: "https://one.example"},
	}
	previewed, err := batch.RecordPreview(batch.Version, items)
	if err != nil || previewed.Status != CompanyImportPreviewed || previewed.ItemCount != 2 || !strings.HasPrefix(previewed.PreviewHash, "sha256:") {
		t.Fatalf("preview = %+v %v", previewed, err)
	}
	reordered := []CompanyImportItem{items[1], items[0]}
	digest, err := CompanyImportPreviewHash(batch, reordered)
	if err != nil || digest != previewed.PreviewHash {
		t.Fatalf("preview hash is not stable: %q %v", digest, err)
	}
	if _, err := previewed.Confirm(previewed.Version, "sha256:"+strings.Repeat("b", 64)); err == nil {
		t.Fatal("changed preview was confirmed")
	}
	confirmed, err := previewed.Confirm(previewed.Version, previewed.PreviewHash)
	if err != nil || confirmed.Status != CompanyImportConfirmed {
		t.Fatalf("confirm = %+v %v", confirmed, err)
	}
	running, err := confirmed.Start(confirmed.Version)
	if err != nil {
		t.Fatal(err)
	}
	completed, err := running.Complete(running.Version, []BatchItemResult{
		{ItemKey: "row-1", Status: BatchItemSucceeded},
		{ItemKey: "row-2", Status: BatchItemWaitingHuman, Detail: "website conflict"},
	})
	if err != nil || completed.Status != CompanyImportCompleted || completed.Outcome.Succeeded != 1 || completed.Outcome.WaitingHuman != 1 {
		t.Fatalf("completion = %+v %v", completed, err)
	}
}

func TestCompanyImportRejectsInlineOrUnhashedInputAndDuplicatePreviewRows(t *testing.T) {
	validHash := "sha256:" + strings.Repeat("c", 64)
	for _, fixture := range []struct{ ref, hash string }{
		{"[{\"name\":\"inline\"}]", validHash},
		{"artifact://imports/input", "sha256:short"},
	} {
		if _, err := NewCompanyImport("import-1", "work-1", fixture.ref, fixture.hash, "company-import.v1", 1); err == nil {
			t.Fatalf("unsafe input accepted: %+v", fixture)
		}
	}
	batch, _ := NewCompanyImport("import-1", "work-1", "artifact://imports/input", validHash, "company-import.v1", 1)
	_, err := batch.RecordPreview(batch.Version, []CompanyImportItem{
		{ItemKey: "row-1", CompanyID: "company-1", Name: "One"},
		{ItemKey: "row-1", CompanyID: "company-2", Name: "Two"},
	})
	if err == nil {
		t.Fatal("duplicate preview item key was accepted")
	}
	if _, err := batch.AppendPreviewChunk(batch.Version, 1, 1); err == nil {
		t.Fatal("out-of-order preview chunk was accepted")
	}
	if _, err := batch.AppendPreviewChunk(batch.Version, 0, 501); err == nil {
		t.Fatal("oversized preview chunk was accepted")
	}
}

func TestArtifactAndListingObservationRequireStableReferences(t *testing.T) {
	artifact, err := NewArtifactMetadata("artifact-1", ArtifactPage, "sha256:page", "object://bucket/page", "work-1", "attempt-1", "operators", "30d", true)
	if err != nil || artifact.Kind != ArtifactPage {
		t.Fatalf("artifact = %+v %v", artifact, err)
	}
	if _, err := NewArtifactMetadata("artifact-2", "unknown", "hash", "object://x", "work-1", "", "operators", "30d", false); err == nil {
		t.Fatal("unknown artifact kind was accepted")
	}
	observation, err := NewListingObservation(ListingObservation{
		ObservationID: "observation-1", OccurrenceID: "occurrence-1", SourceID: "source-1", SourceJobKey: "source-1|external-1",
		DetailURL: "HTTPS://JOBS.EXAMPLE.COM:443/roles/1?utm_source=test", RecipeID: "listing-1", RecipeVersion: 3, ArtifactID: "artifact-1",
	})
	if err != nil || observation.DetailURL != "https://jobs.example.com/roles/1" {
		t.Fatalf("observation = %+v %v", observation, err)
	}
}
