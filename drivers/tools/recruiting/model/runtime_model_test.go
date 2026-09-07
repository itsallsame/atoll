package model

import "testing"

func TestCheckpointRequiresCompleteBoundaryProofAndCAS(t *testing.T) {
	initial, err := EstablishCheckpoint(IncrementalCheckpoint{
		SourceID: "source-1", RecipeID: "listing-1", RecipeVersion: 1, ContractHash: "contract-a",
		Strategy: CheckpointActivityTime, FrontierActivityAt: "2026-09-01T00:00:00Z",
		OverlapPages: 1, LastOccurrenceID: "baseline-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	candidate := initial
	candidate.RecipeVersion = 2
	candidate.FrontierActivityAt = "2026-09-02T00:00:00Z"
	candidate.LastOccurrenceID = "daily-1"
	proof := ListingProgress{PreviousFrontierReached: true, OverlapCompleted: true, OrderingContractHeld: true, SameTimeGroupCompleted: true, Candidate: candidate}
	if _, err := initial.Commit(initial.Version-1, proof); err == nil {
		t.Fatal("stale checkpoint CAS was accepted")
	}
	incomplete := proof
	incomplete.SameTimeGroupCompleted = false
	if _, err := initial.Commit(initial.Version, incomplete); err == nil {
		t.Fatal("checkpoint advanced before same-time group completed")
	}
	committed, err := initial.Commit(initial.Version, proof)
	if err != nil || committed.Version != 2 || committed.LastOccurrenceID != "daily-1" {
		t.Fatalf("commit = %+v, %v", committed, err)
	}
	changedContract := proof
	changedContract.Candidate.ContractHash = "contract-b"
	if _, err := initial.Commit(initial.Version, changedContract); err == nil {
		t.Fatal("incompatible contract reused checkpoint")
	}
}

func TestCheckpointRejectsEveryIncompleteBoundaryProof(t *testing.T) {
	checkpoint, _ := EstablishCheckpoint(IncrementalCheckpoint{
		SourceID: "source-1", RecipeID: "listing-1", RecipeVersion: 1, ContractHash: "contract-a",
		Strategy: CheckpointActivityTime, FrontierActivityAt: "2026-09-01T00:00:00Z", OverlapPages: 1, LastOccurrenceID: "baseline-1",
	})
	candidate := checkpoint
	candidate.FrontierActivityAt = "2026-09-02T00:00:00Z"
	candidate.LastOccurrenceID = "daily-1"
	complete := ListingProgress{PreviousFrontierReached: true, OverlapCompleted: true, OrderingContractHeld: true, SameTimeGroupCompleted: true, Candidate: candidate}
	for name, mutate := range map[string]func(*ListingProgress){
		"frontier":  func(p *ListingProgress) { p.PreviousFrontierReached = false },
		"overlap":   func(p *ListingProgress) { p.OverlapCompleted = false },
		"ordering":  func(p *ListingProgress) { p.OrderingContractHeld = false },
		"same_time": func(p *ListingProgress) { p.SameTimeGroupCompleted = false },
	} {
		t.Run(name, func(t *testing.T) {
			proof := complete
			mutate(&proof)
			if _, err := checkpoint.Commit(checkpoint.Version, proof); err == nil {
				t.Fatal("incomplete proof advanced checkpoint")
			}
		})
	}
}

func TestWorkResolutionAndAttemptFencing(t *testing.T) {
	w, err := NewWork("work-1", "source", "source-1", "listing_sync", "timer")
	if err != nil {
		t.Fatal(err)
	}
	w, err = w.Start(w.Version)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := NewAttempt("attempt-1", w)
	if err != nil {
		t.Fatal(err)
	}
	canceled, err := w.Cancel(w.Version)
	if err != nil {
		t.Fatal(err)
	}
	if err := attempt.CanSubmit(canceled); err == nil {
		t.Fatal("attempt survived work cancellation fencing")
	}
	attempt, _ = attempt.Accept()
	attempt, _ = attempt.Start()
	attempt, err = attempt.Reject()
	if err != nil || attempt.Status != AttemptRejected {
		t.Fatalf("attempt rejection = %+v, %v", attempt, err)
	}
	if _, err := canceled.Start(canceled.Version); err == nil {
		t.Fatal("terminal work reopened")
	}

	w2, _ := NewWork("work-2", "job", "job-1", "detail_sync", "event")
	w2, _ = w2.Start(w2.Version)
	w2, _ = w2.WaitHuman(w2.Version, "detail inaccessible")
	if _, err := w2.Complete(w2.Version, ResolutionAcceptedGap, "", ""); err == nil {
		t.Fatal("accepted gap without actor and reason was accepted")
	}
	resolved, err := w2.Complete(w2.Version, ResolutionAcceptedGap, "human:operator", "source does not expose this field")
	if err != nil || resolved.Resolution != ResolutionAcceptedGap {
		t.Fatalf("resolve gap = %+v, %v", resolved, err)
	}
}

func TestAttemptResultChecksExecutorAndEveryRelevantDomainFence(t *testing.T) {
	work, _ := NewWork("work-1", "source", "source-1", "listing_sync", "timer")
	work, _ = work.Start(work.Version)
	attempt, _ := NewAttempt("attempt-1", work)
	attempt, err := attempt.BindExecutor("executor-1", "incarnation-1", "http.fetch")
	if err != nil {
		t.Fatal(err)
	}
	fence := AttemptFence{
		CompanyVersion: 2, SourceVersion: 3, AssignmentVersion: 4, RecipeID: "listing-1", RecipeVersion: 5,
		CheckpointVersion: 6, ProfileID: "profile-1", ProfileVersion: 7,
	}
	attempt, err = attempt.WithFence(fence)
	if err != nil {
		t.Fatal(err)
	}
	attempt, _ = attempt.Accept()
	attempt, _ = attempt.Start()
	if err := attempt.CanAcceptResult(work, fence, "executor-1", "incarnation-1"); err != nil {
		t.Fatalf("matching result rejected: %v", err)
	}
	stale := fence
	stale.SourceVersion++
	if err := attempt.CanAcceptResult(work, stale, "executor-1", "incarnation-1"); err == nil {
		t.Fatal("stale source version was accepted")
	}
	if err := attempt.CanAcceptResult(work, fence, "executor-1", "old-incarnation"); err == nil {
		t.Fatal("stale executor incarnation was accepted")
	}
	mutations := map[string]func(*AttemptFence){
		"company":         func(f *AttemptFence) { f.CompanyVersion++ },
		"source":          func(f *AttemptFence) { f.SourceVersion++ },
		"assignment":      func(f *AttemptFence) { f.AssignmentVersion++ },
		"recipe_id":       func(f *AttemptFence) { f.RecipeID = "listing-2" },
		"recipe_version":  func(f *AttemptFence) { f.RecipeVersion++ },
		"checkpoint":      func(f *AttemptFence) { f.CheckpointVersion++ },
		"refresh":         func(f *AttemptFence) { f.RefreshGeneration++ },
		"profile_id":      func(f *AttemptFence) { f.ProfileID = "profile-2" },
		"profile_version": func(f *AttemptFence) { f.ProfileVersion++ },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := fence
			mutate(&changed)
			if err := attempt.CanAcceptResult(work, changed, "executor-1", "incarnation-1"); err == nil {
				t.Fatal("changed domain fence was accepted")
			}
		})
	}
}

func TestJobRefreshGenerationRejectsLateDetail(t *testing.T) {
	job, err := NewSourceJob("job-1", "source-1", "external-1", "https://jobs.example.com/1?utm_source=x")
	if err != nil {
		t.Fatal(err)
	}
	oldGeneration, oldVersion := job.RefreshGeneration, job.Version
	job, err = job.ForceRefresh(job.Version, "https://jobs.example.com/1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := job.AcceptDetail(job.Version, oldGeneration, "hash-old"); err == nil {
		t.Fatal("late detail generation was accepted")
	}
	if _, err := job.AcceptDetail(oldVersion, job.RefreshGeneration, "hash-new"); err == nil {
		t.Fatal("stale job version was accepted")
	}
	accepted, err := job.AcceptDetail(job.Version, job.RefreshGeneration, "hash-new")
	if err != nil || accepted.Job.Status != JobAvailable || !accepted.ContentChanged {
		t.Fatalf("detail acceptance = %+v, %v", accepted, err)
	}
	duplicate, err := accepted.Job.AcceptDetail(0, accepted.Job.RefreshGeneration, "hash-new")
	if err != nil || duplicate.ContentChanged || duplicate.Job.DetailVersion != accepted.Job.DetailVersion {
		t.Fatalf("equal duplicate detail did not converge: %+v, %v", duplicate, err)
	}
}

func TestOverlapObservationDoesNotBecomeDailyFullDetailRefresh(t *testing.T) {
	observation, err := NewListingObservation(ListingObservation{
		ObservationID: "observation-1", OccurrenceID: "baseline-1", SourceID: "source-1", SourceJobKey: "external-1",
		DetailURL: "https://jobs.example.com/1", ActivityAt: "2026-09-01T00:00:00Z", ListingFingerprint: "listing-a",
		RecipeID: "listing-1", RecipeVersion: 1, ArtifactID: "artifact-1",
	})
	if err != nil {
		t.Fatal(err)
	}
	job, err := NewSourceJobFromObservation("job-1", observation, "2026-09-01T00:01:00Z")
	if err != nil {
		t.Fatal(err)
	}
	duplicate := observation
	duplicate.ObservationID = "observation-2"
	duplicate.OccurrenceID = "daily-1"
	unchanged, refresh, err := job.ObserveListing(job.Version, duplicate)
	if err != nil || refresh || unchanged.RefreshGeneration != job.RefreshGeneration || unchanged.Version != job.Version {
		t.Fatalf("overlap observation caused detail refresh: %+v refresh=%v err=%v", unchanged, refresh, err)
	}
	updated := duplicate
	updated.ObservationID = "observation-3"
	updated.ActivityAt = "2026-09-02T00:00:00Z"
	updated.ListingFingerprint = "listing-b"
	changed, refresh, err := job.ObserveListing(job.Version, updated)
	if err != nil || !refresh || changed.RefreshGeneration != job.RefreshGeneration+1 || changed.Status != JobUpdatePending {
		t.Fatalf("real source update did not trigger detail refresh: %+v refresh=%v err=%v", changed, refresh, err)
	}
	accepted, err := changed.AcceptDetailVersion(changed.Version, changed.RefreshGeneration, "detail-1", "sha256:detail", "artifact-detail", "detail-recipe", 4, "2026-09-02T00:02:00Z")
	if err != nil || accepted.DetailVersion == nil || accepted.DetailVersion.Version != 1 {
		t.Fatalf("detail version was not created: %+v err=%v", accepted, err)
	}
}

func TestOccurrenceSnapshotAndCoverageOutcome(t *testing.T) {
	key1, err := OccurrenceKey("source-1", "2026-09-07", 3)
	if err != nil {
		t.Fatal(err)
	}
	key2, _ := OccurrenceKey("source-1", "2026-09-07", 3)
	if key1 != key2 {
		t.Fatalf("occurrence key is unstable: %q %q", key1, key2)
	}
	o, err := NewSourceOccurrence("occ-1", "daily-1", "source-1", "2026-09-07", 3, 8, 13)
	if err != nil {
		t.Fatal(err)
	}
	o, _ = o.Start(o.Version)
	o, err = o.Finish(o.Version, false, "boundary_missing")
	if err != nil || o.Status != OccurrenceException {
		t.Fatalf("exception occurrence = %+v, %v", o, err)
	}
	if _, err := OccurrenceKey("source-1", "not-a-date", 3); err == nil {
		t.Fatal("invalid schedule date was accepted")
	}
}

func TestPlannedOccurrenceCanBeExplicitlyExcluded(t *testing.T) {
	o, err := NewSourceOccurrence("occ-excluded", "daily-1", "source-1", "2026-09-07", 3, 8, 13)
	if err != nil {
		t.Fatal(err)
	}
	excluded, err := o.Exclude(o.Version, "paused before cutoff")
	if err != nil || excluded.Status != OccurrenceExcluded || excluded.Outcome != "paused before cutoff" || excluded.Version != 2 {
		t.Fatalf("excluded occurrence = %+v, %v", excluded, err)
	}
	if _, err := excluded.Start(excluded.Version); err == nil {
		t.Fatal("excluded occurrence was restarted")
	}
}

func TestDailyRunDoesNotHideAcceptedGaps(t *testing.T) {
	run, err := NewDailyRun("daily-1", "2026-09-07", 2)
	if err != nil {
		t.Fatal(err)
	}
	run, _ = run.Start(run.Version)
	if _, err := run.Close(run.Version, CoverageSummary{ListingSucceeded: 1}); err == nil {
		t.Fatal("daily run closed with an unaccounted source")
	}
	run, err = run.Close(run.Version, CoverageSummary{
		ListingSucceeded: 2, DetailExpected: 2, DetailSucceeded: 1, DetailAcceptedGap: 1,
	})
	if err != nil || run.Status != DailyRunCompletedWithExceptions {
		t.Fatalf("accepted gap was hidden: %+v, %v", run, err)
	}
}

func TestProfileRepairRotatesOnlyOpaqueReference(t *testing.T) {
	p, err := NewBrowserProfile("profile-1", "jobs.example.com", "device-1", "secret://profiles/1/v1")
	if err != nil {
		t.Fatal(err)
	}
	p, _ = p.BeginRepair(p.Version)
	p, err = p.BeginVerification(p.Version, "secret://profiles/1/v2")
	if err != nil {
		t.Fatal(err)
	}
	p, err = p.Verify(p.Version)
	if err != nil || p.AuthStatus != ProfileReady || p.SecretRef != "secret://profiles/1/v2" {
		t.Fatalf("profile recovery = %+v, %v", p, err)
	}
}

func TestRecipeQuarantineRequiresValidationBeforeRepublish(t *testing.T) {
	r, err := NewRecipe("recipe-1", RecipeListing, "moka", 1, "content", "contract")
	if err != nil {
		t.Fatal(err)
	}
	r, _ = r.BeginValidation(r.StateVersion)
	r, _ = r.Publish(r.StateVersion)
	r, err = r.Quarantine(r.StateVersion)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Publish(r.StateVersion); err == nil {
		t.Fatal("quarantined recipe republished without validation")
	}
	r, _ = r.BeginValidation(r.StateVersion)
	r, err = r.Publish(r.StateVersion)
	if err != nil || r.Status != RecipeActive {
		t.Fatalf("validated recovery = %+v, %v", r, err)
	}
}

func FuzzCanonicalHTTPURLIdempotent(f *testing.F) {
	for _, seed := range []string{
		"https://example.com/jobs", "HTTPS://EXAMPLE.COM:443/jobs/?utm_source=x&a=2&a=1", "http://example.com:80/",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		canonical, err := CanonicalHTTPURL(raw)
		if err != nil || canonical == "" {
			return
		}
		again, err := CanonicalHTTPURL(canonical)
		if err != nil || again != canonical {
			t.Fatalf("canonicalization not idempotent: %q -> %q -> %q (%v)", raw, canonical, again, err)
		}
	})
}
