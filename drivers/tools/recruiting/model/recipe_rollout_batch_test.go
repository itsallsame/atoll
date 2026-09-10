package model

import "testing"

const (
	rolloutInputHash   = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	rolloutPreviewHash = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func activeRolloutBatchRecipe(t *testing.T) Recipe {
	t.Helper()
	recipe, err := NewRecipe("listing-batch-v2", RecipeListing, "jobs.example.com", 2,
		"sha256:batch-content", "sha256:batch-contract", testRecipeExecution("listing-batch-v2"))
	if err != nil {
		t.Fatal(err)
	}
	validating, _ := recipe.BeginValidation(recipe.StateVersion)
	active, _ := validating.Publish(validating.StateVersion)
	return active
}

func previewedRolloutBatch(t *testing.T, batchID string, sourceCount, canarySize, waveSize int) RecipeRolloutBatch {
	t.Helper()
	batch, err := NewRecipeRolloutBatch(batchID, "work-"+batchID, activeRolloutBatchRecipe(t),
		"artifact://rollout/"+batchID, rolloutInputHash, "recipe-rollout-sources.v1", 1, canarySize, waveSize)
	if err != nil {
		t.Fatal(err)
	}
	remaining := sourceCount
	for sequence := 1; remaining > 0; sequence++ {
		chunk := minInt(remaining, 500)
		batch, err = batch.AppendPreviewChunk(batch.Version, sequence, chunk)
		if err != nil {
			t.Fatal(err)
		}
		remaining -= chunk
	}
	batch, err = batch.FinishPreview(batch.Version, rolloutPreviewHash)
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

func TestRecipeRolloutBatchRequiresCanaryBeforeBoundedWaves(t *testing.T) {
	batch := previewedRolloutBatch(t, "batch-1", 12, 2, 4)
	var err error
	running, err := batch.Start(batch.Version, batch.PreviewHash)
	if err != nil || running.Phase != RecipeRolloutCanary || running.ActiveFrom != 1 || running.ActiveThrough != 2 {
		t.Fatalf("canary start=%+v err=%v", running, err)
	}

	waiting, err := running.ObserveWave(running.Version, RecipeRolloutWaveProgress{
		From: 1, Through: 2, AwaitingValidation: 1, Succeeded: 1,
	})
	if err != nil || waiting != running {
		t.Fatalf("incomplete canary advanced batch: before=%+v after=%+v err=%v", running, waiting, err)
	}
	firstWave, err := running.ObserveWave(running.Version, RecipeRolloutWaveProgress{
		From: 1, Through: 2, Succeeded: 2,
	})
	if err != nil || firstWave.Phase != RecipeRolloutWave || firstWave.ActiveFrom != 3 || firstWave.ActiveThrough != 6 ||
		firstWave.SucceededCount != 2 {
		t.Fatalf("first rollout wave=%+v err=%v", firstWave, err)
	}
	secondWave, _ := firstWave.ObserveWave(firstWave.Version, RecipeRolloutWaveProgress{
		From: 3, Through: 6, Succeeded: 4,
	})
	if secondWave.ActiveFrom != 7 || secondWave.ActiveThrough != 10 || secondWave.SucceededCount != 6 {
		t.Fatalf("second rollout wave=%+v", secondWave)
	}
	lastWave, _ := secondWave.ObserveWave(secondWave.Version, RecipeRolloutWaveProgress{
		From: 7, Through: 10, Succeeded: 4,
	})
	if lastWave.ActiveFrom != 11 || lastWave.ActiveThrough != 12 || lastWave.SucceededCount != 10 {
		t.Fatalf("last bounded rollout wave=%+v", lastWave)
	}
	completed, err := lastWave.ObserveWave(lastWave.Version, RecipeRolloutWaveProgress{
		From: 11, Through: 12, Succeeded: 2,
	})
	if err != nil || completed.Status != RecipeRolloutBatchCompleted || completed.SucceededCount != 12 {
		t.Fatalf("completed rollout=%+v err=%v", completed, err)
	}
}

func TestRecipeRolloutBatchFailureStopsExpansionAndNeedsExplicitResume(t *testing.T) {
	batch := previewedRolloutBatch(t, "batch-fail", 1000, 5, 100)
	running, _ := batch.Start(batch.Version, batch.PreviewHash)
	paused, err := running.ObserveWave(running.Version, RecipeRolloutWaveProgress{
		From: 1, Through: 5, Succeeded: 4, Failed: 1,
	})
	if err != nil || paused.Status != RecipeRolloutBatchPaused || paused.ActiveFrom != 1 || paused.ActiveThrough != 5 ||
		paused.FailedCount != 1 || paused.SucceededCount != 0 {
		t.Fatalf("failed canary did not stop expansion: %+v err=%v", paused, err)
	}
	if _, err := paused.ObserveWave(paused.Version, RecipeRolloutWaveProgress{From: 1, Through: 5, Succeeded: 5}); err == nil {
		t.Fatal("paused batch advanced without an explicit resume")
	}
	resumed, err := paused.Resume(paused.Version)
	if err != nil || resumed.Status != RecipeRolloutBatchRunning || resumed.ActiveThrough != 5 {
		t.Fatalf("resumed canary=%+v err=%v", resumed, err)
	}
	next, err := resumed.ObserveWave(resumed.Version, RecipeRolloutWaveProgress{From: 1, Through: 5, Succeeded: 5})
	if err != nil || next.ActiveFrom != 6 || next.ActiveThrough != 105 || next.SucceededCount != 5 {
		t.Fatalf("repaired canary did not open one bounded wave: %+v err=%v", next, err)
	}
}

func TestRecipeRolloutBatchRejectsStaleMalformedAndUnsafeTransitions(t *testing.T) {
	recipe := activeRolloutBatchRecipe(t)
	if _, err := NewRecipeRolloutBatch("batch", "work", recipe, "artifact://input",
		"not-a-hash", "recipe-rollout-sources.v1", 1, 1, 1); err == nil {
		t.Fatal("batch accepted a non-hashed immutable input")
	}
	batch := previewedRolloutBatch(t, "batch", 10, 2, 3)
	if _, err := batch.Start(batch.Version, "sha256:different"); err == nil {
		t.Fatal("batch accepted a different preview")
	}
	running, _ := batch.Start(batch.Version, batch.PreviewHash)
	if _, err := running.ObserveWave(batch.Version, RecipeRolloutWaveProgress{From: 1, Through: 2, Succeeded: 2}); err == nil {
		t.Fatal("batch accepted a stale version")
	}
	if _, err := running.ObserveWave(running.Version, RecipeRolloutWaveProgress{From: 1, Through: 3, Succeeded: 3}); err == nil {
		t.Fatal("batch accepted progress outside the active wave")
	}
	canceled, err := running.Cancel(running.Version)
	if err != nil || canceled.Status != RecipeRolloutBatchCanceled {
		t.Fatalf("cancel=%+v err=%v", canceled, err)
	}
	if _, err := canceled.Resume(canceled.Version); err == nil {
		t.Fatal("canceled batch resumed")
	}
}

func TestRecipeRolloutItemsFreezeVersionsAndBindDeterministicCanaryOrder(t *testing.T) {
	makeItem := func(sourceID string, version uint64) RecipeRolloutBatchItem {
		source, _ := NewRecruitmentSource(sourceID, "company-items", "https://jobs.example.com/"+sourceID, "all", 1)
		source.Version = version
		assignment, _ := NewSourceRecipeAssignment(sourceID, RecipeListing, "listing-v1", 1,
			"sha256:batch-contract", "2026-09-10T00:00:00Z")
		source.ListingAssignment = &assignment
		assessment := verifiedAssessment(source, assignment)
		source.ContractAssessment = &assessment
		item, err := NewRecipeRolloutBatchItem("batch-items", 1, source, assignment)
		if err != nil {
			t.Fatal(err)
		}
		return item
	}
	items := []RecipeRolloutBatchItem{makeItem("source-c", 13), makeItem("source-a", 11), makeItem("source-b", 12)}
	canonicalA, err := CanonicalRecipeRolloutItems("batch-items", items)
	if err != nil {
		t.Fatal(err)
	}
	canonicalB, err := CanonicalRecipeRolloutItems("batch-items", []RecipeRolloutBatchItem{items[1], items[2], items[0]})
	if err != nil {
		t.Fatal(err)
	}
	for index := range canonicalA {
		if canonicalA[index].SourceID != canonicalB[index].SourceID || canonicalA[index].Ordinal != index+1 {
			t.Fatalf("canary order is not deterministic: A=%+v B=%+v", canonicalA, canonicalB)
		}
	}
	batch := previewedRolloutBatch(t, "batch-items", 3, 1, 2)
	hashA, _ := RecipeRolloutPreviewHash(batch, activeRolloutBatchRecipe(t), items)
	hashB, _ := RecipeRolloutPreviewHash(batch, activeRolloutBatchRecipe(t),
		[]RecipeRolloutBatchItem{items[2], items[0], items[1]})
	if hashA == "" || hashA != hashB {
		t.Fatalf("preview hash changed with input order: %q != %q", hashA, hashB)
	}
	changed := append([]RecipeRolloutBatchItem(nil), items...)
	changed[0].ExpectedSourceVersion++
	hashChanged, _ := RecipeRolloutPreviewHash(batch, activeRolloutBatchRecipe(t), changed)
	if hashChanged == hashA {
		t.Fatal("preview hash did not bind frozen Source versions")
	}
	policyChanged := batch
	policyChanged.PolicyVersion++
	hashPolicyChanged, _ := RecipeRolloutPreviewHash(policyChanged, activeRolloutBatchRecipe(t), items)
	if hashPolicyChanged == hashA {
		t.Fatal("preview hash did not bind the policy version")
	}
	duplicate := append(items, items[0])
	if _, err := CanonicalRecipeRolloutItems("batch-items", duplicate); err == nil {
		t.Fatal("preview accepted a duplicate Source")
	}

	planned, err := canonicalA[0].PlanApply(canonicalA[0].Version, "2026-09-10T01:00:00Z")
	// Canonical items intentionally erase persistence state; actual stored items
	// are created again with version 1 after ordering.
	if err == nil || planned.Version != 0 {
		t.Fatalf("canonical hash projection unexpectedly acted as mutable item: %+v err=%v", planned, err)
	}
	stored := items[0]
	planned, err = stored.PlanApply(stored.Version, "2026-09-10T01:00:00Z")
	if err != nil || planned.Status != RecipeRolloutItemApplying {
		t.Fatalf("planned item=%+v err=%v", planned, err)
	}
	applied, err := planned.MarkApplied(planned.Version, stored.ExpectedSourceVersion+1,
		stored.ExpectedAssignmentVersion+1)
	if err != nil || applied.Status != RecipeRolloutItemAwaitingValidation {
		t.Fatalf("applied item=%+v err=%v", applied, err)
	}
	bound, err := applied.BindValidation(applied.Version, "work-validation", "run-validation")
	if err != nil || bound.ValidationRunID != "run-validation" {
		t.Fatalf("bound item=%+v err=%v", bound, err)
	}
	succeeded, err := bound.MarkSucceeded(bound.Version, "work-validation", "2026-09-10T01:01:00Z")
	if err != nil || succeeded.Status != RecipeRolloutItemSucceeded {
		t.Fatalf("succeeded item=%+v err=%v", succeeded, err)
	}
	failed, err := applied.MarkFailed(applied.Version, "quality_rejected")
	if err != nil {
		t.Fatal(err)
	}
	retried, err := failed.Retry(failed.Version)
	if err != nil || retried.Status != RecipeRolloutItemAwaitingValidation ||
		retried.AppliedAssignmentVersion != applied.AppliedAssignmentVersion {
		t.Fatalf("post-application retry=%+v err=%v", retried, err)
	}
	preApplyFailed, err := planned.MarkFailed(planned.Version, "version_conflict")
	if err != nil {
		t.Fatal(err)
	}
	preApplyRetried, err := preApplyFailed.Retry(preApplyFailed.Version)
	if err != nil || preApplyRetried.Status != RecipeRolloutItemPending {
		t.Fatalf("pre-application retry=%+v err=%v", preApplyRetried, err)
	}
}
