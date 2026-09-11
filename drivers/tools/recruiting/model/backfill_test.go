package model

import "testing"

func backfillFixture(t *testing.T, mode BackfillMode) (Backfill, BackfillItem) {
	t.Helper()
	backfill, err := NewBackfill("backfill-1", "work-backfill-1", "human:operator", "source", "source-1", mode,
		"2026-01-01T00:00:00Z", "2026-02-01T00:00:00Z", []string{"title", "description"}, "detail-recipe", 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	job, _ := NewSourceJob("job-1", "source-1", "remote-1", "https://jobs.example.test/1")
	company, _ := NewCompany("company-1", "Company", "https://example.test")
	source, _ := NewRecruitmentSource(job.SourceID, company.CompanyID, "https://jobs.example.test", "all", 1)
	var inputDetail *JobDetailVersion
	if mode == BackfillArtifactRecompute {
		inputDetail = &JobDetailVersion{DetailVersionID: "detail-version-1", JobID: job.JobID, Version: 1,
			ArtifactID: "artifact-original", ObservedAt: "2026-01-05T00:00:00Z"}
	}
	item, err := NewBackfillItem(backfill.BackfillID, "item-1", mode, job, source, company, inputDetail, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return backfill, item
}

func TestBackfillPreviewAndOutcomeStateMachine(t *testing.T) {
	backfill, _ := backfillFixture(t, BackfillArtifactRecompute)
	firstHash, _ := AdvanceBackfillPreviewHash(backfill, "", []BackfillItem{})
	first, err := backfill.AdvancePreview(backfill.Version, 500, "job-0500", firstHash, false)
	if err != nil || first.PreviewedItems != 500 || first.Status != BackfillPreviewing {
		t.Fatalf("first preview=%+v err=%v", first, err)
	}
	finalHash, _ := AdvanceBackfillPreviewHash(first, first.PreviewAccumulator, []BackfillItem{})
	previewed, err := first.AdvancePreview(first.Version, 2, "", finalHash, true)
	if err != nil || previewed.PreviewedItems != 502 || previewed.Status != BackfillPreviewed {
		t.Fatalf("final preview=%+v err=%v", previewed, err)
	}
	if _, err := previewed.Confirm(previewed.Version, "sha256:wrong"); err == nil {
		t.Fatal("wrong preview hash was accepted")
	}
	running, err := previewed.Confirm(previewed.Version, previewed.PreviewHash)
	if err != nil || running.Status != BackfillRunning {
		t.Fatalf("confirm=%+v err=%v", running, err)
	}
	paused, err := running.ReconcileCounts(running.Version, 400, 1, 1)
	if err != nil || paused.Status != BackfillPaused {
		t.Fatalf("pause=%+v err=%v", paused, err)
	}
	if _, err := paused.Resume(paused.Version); err == nil {
		t.Fatal("resume erased unresolved failed item count")
	}
	resolved, err := paused.ReconcileCounts(paused.Version, 400, 2, 0)
	if err != nil || resolved.Status != BackfillRunning || resolved.FailedItems != 0 {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	manuallyPaused, err := resolved.Pause(resolved.Version)
	if err != nil || manuallyPaused.Status != BackfillPaused {
		t.Fatalf("manual pause=%+v err=%v", manuallyPaused, err)
	}
	resumed, err := manuallyPaused.Resume(manuallyPaused.Version)
	if err != nil || resumed.Status != BackfillRunning {
		t.Fatalf("manual resume=%+v err=%v", resumed, err)
	}
	completed, err := resumed.ReconcileCounts(resumed.Version, 500, 2, 0)
	if err != nil || completed.Status != BackfillCompleted {
		t.Fatalf("complete=%+v err=%v", completed, err)
	}
	if _, err := completed.Cancel(completed.Version, "command-too-late"); err == nil {
		t.Fatal("completed backfill was canceled")
	}
}

func TestBackfillOutputKeepsHistoricalClaimsHonest(t *testing.T) {
	recompute, recomputeItem := backfillFixture(t, BackfillArtifactRecompute)
	derived, err := NewBackfillOutput("output-1", recompute, recomputeItem, "sha256:content", "artifact-output", "")
	if err != nil || derived.InputArtifactID != recomputeItem.InputArtifactID || !derived.ClaimsHistoricalSnapshot {
		t.Fatalf("artifact output=%+v err=%v", derived, err)
	}
	if derived.InputDetailVersionID != recomputeItem.InputDetailVersionID || derived.DerivedFromObservedAt != recomputeItem.InputObservedAt {
		t.Fatalf("artifact output did not freeze its input detail: %+v", derived)
	}
	if _, err := NewBackfillOutput("output-bad", recompute, recomputeItem, "sha256:content", "artifact-output",
		"2026-09-11T00:00:00Z"); err == nil {
		t.Fatal("artifact recompute claimed a live refetch time")
	}

	live, liveItem := backfillFixture(t, BackfillLiveRefetch)
	refetched, err := NewBackfillOutput("output-2", live, liveItem, "sha256:content", "artifact-live",
		"2026-09-11T00:00:00Z")
	if err != nil || refetched.InputArtifactID != "" || refetched.ClaimsHistoricalSnapshot || refetched.RefetchedAt == "" {
		t.Fatalf("live output=%+v err=%v", refetched, err)
	}
	forbidden := &JobDetailVersion{DetailVersionID: "detail-bad", JobID: liveItem.JobID, ArtifactID: "artifact-forbidden", ObservedAt: "2026-01-05T00:00:00Z"}
	job, _ := NewSourceJob(liveItem.JobID, liveItem.SourceID, "remote-bad", liveItem.DetailURL)
	company, _ := NewCompany(liveItem.CompanyID, "Company", "https://example.test")
	source, _ := NewRecruitmentSource(job.SourceID, company.CompanyID, "https://jobs.example.test", "all", 1)
	if _, err := NewBackfillItem(live.BackfillID, "bad", BackfillLiveRefetch, job, source, company, forbidden, nil, nil); err == nil {
		t.Fatal("live refetch accepted an input Artifact")
	}
}

func TestBackfillCancellationRequiresEveryNonSuccessfulItemToSettle(t *testing.T) {
	backfill, item := backfillFixture(t, BackfillArtifactRecompute)
	hash, _ := AdvanceBackfillPreviewHash(backfill, "", nil)
	previewed, err := backfill.AdvancePreview(backfill.Version, 2, "", hash, true)
	if err != nil {
		t.Fatal(err)
	}
	canceling, err := previewed.RequestCancel(previewed.Version, "command-cancel")
	if err != nil || canceling.Status != BackfillCanceling {
		t.Fatalf("request cancel=%+v err=%v", canceling, err)
	}
	progress, err := canceling.RecordCancellationProgress(canceling.Version, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := progress.FinishCancel(progress.Version, 1); err == nil {
		t.Fatal("partially settled cancellation completed")
	}
	progress, err = progress.RecordCancellationProgress(progress.Version, 2, 0)
	if err != nil {
		t.Fatal(err)
	}
	canceled, err := progress.FinishCancel(progress.Version, 2)
	if err != nil || canceled.Status != BackfillCanceled || canceled.CanceledItems != 2 {
		t.Fatalf("finish cancel=%+v err=%v", canceled, err)
	}
	canceledItem, err := item.Cancel(item.Version)
	if err != nil || canceledItem.Status != BackfillItemCanceled {
		t.Fatalf("cancel item=%+v err=%v", canceledItem, err)
	}
}

func TestBackfillAllowsMultipleHistoricalVersionsOfOneJob(t *testing.T) {
	backfill, first := backfillFixture(t, BackfillArtifactRecompute)
	job, _ := NewSourceJob(first.JobID, first.SourceID, "remote-1", first.DetailURL)
	company, _ := NewCompany(first.CompanyID, "Company", "https://example.test")
	source, _ := NewRecruitmentSource(job.SourceID, company.CompanyID, "https://jobs.example.test", "all", 1)
	secondDetail := &JobDetailVersion{DetailVersionID: "detail-version-2", JobID: job.JobID, Version: 2,
		ArtifactID: "artifact-second", ObservedAt: "2026-01-20T00:00:00Z"}
	second, err := NewBackfillItem(backfill.BackfillID, "item-2", backfill.Mode, job, source, company, secondDetail, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if first.JobID != second.JobID || first.InputDetailVersionID == second.InputDetailVersionID ||
		first.InputObservedAt == second.InputObservedAt {
		t.Fatalf("historical selections were collapsed: first=%+v second=%+v", first, second)
	}
}

func TestBackfillPreviewHashBindsHistoricalVersionAndOrder(t *testing.T) {
	backfill, first := backfillFixture(t, BackfillArtifactRecompute)
	job, _ := NewSourceJob(first.JobID, first.SourceID, "remote-1", first.DetailURL)
	company, _ := NewCompany(first.CompanyID, "Company", "https://example.test")
	source, _ := NewRecruitmentSource(job.SourceID, company.CompanyID, "https://jobs.example.test", "all", 1)
	input := &JobDetailVersion{DetailVersionID: "detail-version-2", JobID: job.JobID, Version: 2,
		ArtifactID: "artifact-second", ObservedAt: "2026-01-20T00:00:00Z"}
	second, _ := NewBackfillItem(backfill.BackfillID, "item-2", backfill.Mode, job, source, company, input, nil, nil)
	hashA, err := BackfillPreviewHash(backfill, []BackfillItem{second, first})
	hashB, errB := BackfillPreviewHash(backfill, []BackfillItem{first, second})
	if err != nil || errB != nil || hashA == "" || hashA != hashB {
		t.Fatalf("hashA=%q hashB=%q err=%v errB=%v", hashA, hashB, err, errB)
	}
	changed := second
	changed.InputObservedAt = "2026-01-21T00:00:00Z"
	hashChanged, err := BackfillPreviewHash(backfill, []BackfillItem{first, changed})
	if err != nil || hashChanged == hashA {
		t.Fatalf("historical lineage was not bound: hash=%q changed=%q err=%v", hashA, hashChanged, err)
	}
	empty, err := BackfillPreviewHash(backfill, nil)
	if err != nil || empty == "" {
		t.Fatalf("empty preview hash=%q err=%v", empty, err)
	}
}

func TestBackfillItemRequiresCausalRetryAndExplicitGap(t *testing.T) {
	_, item := backfillFixture(t, BackfillLiveRefetch)
	queued, err := item.Queue(item.Version, "work-item-1")
	if err != nil || queued.Status != BackfillItemQueued {
		t.Fatalf("queue=%+v err=%v", queued, err)
	}
	failed, err := queued.Fail(queued.Version, "parse_error")
	if err != nil || failed.Status != BackfillItemFailed {
		t.Fatalf("fail=%+v err=%v", failed, err)
	}
	if failed.RetryAllowed(BackfillArtifactRecompute) || !failed.RetryAllowed(BackfillLiveRefetch) {
		t.Fatal("deterministic historical parse failure had incorrect retry eligibility")
	}
	if _, err := failed.Retry(failed.Version, failed.WorkID); err == nil {
		t.Fatal("retry reused the failed Work")
	}
	retried, err := failed.Retry(failed.Version, "work-item-2")
	if err != nil || retried.Status != BackfillItemQueued || retried.FailureClass != "" {
		t.Fatalf("retry=%+v err=%v", retried, err)
	}
	succeeded, err := retried.Succeed(retried.Version, "output-1")
	if err != nil || succeeded.Status != BackfillItemSucceeded {
		t.Fatalf("succeed=%+v err=%v", succeeded, err)
	}
	gap, err := failed.AcceptGap(failed.Version)
	if err != nil || gap.Status != BackfillItemAcceptedGap || gap.FailureClass != "parse_error" {
		t.Fatalf("gap=%+v err=%v", gap, err)
	}
}

func TestBackfillItemCanRecordFrozenFenceFailureBeforeQueue(t *testing.T) {
	_, item := backfillFixture(t, BackfillLiveRefetch)
	failed, err := item.RejectBeforeQueue(item.Version, "contract_violated")
	if err != nil || failed.Status != BackfillItemFailed || failed.WorkID != "" || failed.FailureClass != "contract_violated" {
		t.Fatalf("failed=%+v err=%v", failed, err)
	}
	if _, err := failed.Queue(failed.Version, "work-too-late"); err == nil {
		t.Fatal("fence-rejected item was queued")
	}
	if _, err := failed.Retry(failed.Version, "work-impossible-retry"); err == nil {
		t.Fatal("prequeue fence failure reused an obsolete immutable preview")
	}
}

func TestBackfillCanonicalizesFieldsAndNeverAdvancesCheckpoint(t *testing.T) {
	backfill, _ := backfillFixture(t, BackfillArtifactRecompute)
	if len(backfill.Fields) != 2 || backfill.Fields[0] != "description" || backfill.Fields[1] != "title" {
		t.Fatalf("fields not canonical: %v", backfill.Fields)
	}
	for _, mode := range []BackfillMode{BackfillArtifactRecompute, BackfillLiveRefetch} {
		if mode.Validate() != nil || mode.MayAdvanceCheckpoint() {
			t.Fatalf("unsafe mode semantics: %s", mode)
		}
	}
}
