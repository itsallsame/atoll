package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

type capacityWorkload struct {
	Companies        int
	Sources          int
	DetailsPerSource int
	RetryPercent     int
	Timeout          time.Duration
}

type capacityScheduleShape struct {
	Window  time.Duration
	Buckets int
}

var capacityWorkloads = map[string]capacityWorkload{
	"L0": {Companies: 100, Sources: 200, DetailsPerSource: 0, RetryPercent: 0, Timeout: 2 * time.Minute},
	"L1": {Companies: 1_000, Sources: 2_000, DetailsPerSource: 2, RetryPercent: 0, Timeout: 5 * time.Minute},
	"L2": {Companies: 10_000, Sources: 20_000, DetailsPerSource: 2, RetryPercent: 0, Timeout: 15 * time.Minute},
	"L3": {Companies: 10_000, Sources: 20_000, DetailsPerSource: 20, RetryPercent: 0, Timeout: 30 * time.Minute},
	"L4": {Companies: 10_000, Sources: 20_000, DetailsPerSource: 20, RetryPercent: 10, Timeout: 30 * time.Minute},
}

var capacityScheduleShapes = map[string]capacityScheduleShape{
	"8h":     {Window: 8 * time.Hour, Buckets: 8},
	"24h":    {Window: 24 * time.Hour, Buckets: 24},
	"peak5x": {Window: 96 * time.Minute, Buckets: 8},
}

// TestDailyCapacityWorkload is intentionally opt-in. The committed manifest
// defines the only accepted sizes, so a typo cannot accidentally create an
// unbounded database load. The fixture never accesses a third-party site.
func TestDailyCapacityWorkload(t *testing.T) {
	level := os.Getenv("RECRUITING_CAPACITY_LEVEL")
	if level == "" {
		t.Skip("RECRUITING_CAPACITY_LEVEL is not set")
	}
	workload, ok := capacityWorkloads[level]
	if !ok {
		t.Fatalf("unknown capacity workload %q", level)
	}
	shapeName := os.Getenv("RECRUITING_CAPACITY_SHAPE")
	if shapeName == "" {
		shapeName = "8h"
	}
	shape, ok := capacityScheduleShapes[shapeName]
	if !ok {
		t.Fatalf("unknown capacity schedule shape %q", shapeName)
	}
	dsn := os.Getenv("RECRUITING_MYSQL_TEST_DSN")
	if dsn == "" {
		t.Fatal("capacity workload requires RECRUITING_MYSQL_TEST_DSN")
	}
	db, err := Open(dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), workload.Timeout)
	defer cancel()
	migrateTestDatabase(t, ctx, db)
	repository, _ := NewRepository(db)
	now := time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC)
	started := time.Now()
	seedCapacityRoster(t, ctx, db, repository, workload, now)
	seedDuration := time.Since(started)

	windowStart := now.Add(time.Hour)
	windowEnd := windowStart.Add(shape.Window)
	planStarted := time.Now()
	plan, err := repository.PlanDailyRunAtCutoff(ctx, DailyPlanRequest{DailyRunID: "capacity-" + level + "-" + shapeName,
		ScheduleDate: "2099-01-01", Schedule: model.DailySchedule{PolicyVersion: 1,
			CutoffAt: now.Format(time.RFC3339Nano), WindowStartAt: windowStart.Format(time.RFC3339Nano),
			WindowEndAt: windowEnd.Format(time.RFC3339Nano)}, TriggerID: "capacity-trigger-" + level + "-" + shapeName,
		EventID: "capacity-event-" + level + "-" + shapeName}, now)
	if err != nil || plan.Occurrences != workload.Sources || plan.Run.ExpectedSources != workload.Sources {
		t.Fatalf("capacity daily plan=%+v err=%v", plan, err)
	}
	planDuration := time.Since(planStarted)

	materializeStarted := time.Now()
	queued := 0
	bucketWidth := shape.Window / time.Duration(shape.Buckets)
	for bucket := 0; bucket < shape.Buckets; bucket++ {
		cutoff := windowStart.Add(time.Duration(bucket+1)*bucketWidth - time.Microsecond)
		for {
			result, err := repository.MaterializeDueOccurrenceWorks(ctx, cutoff, 500, "recruiting",
				fmt.Sprintf("capacity-materializer-%s-%s-%02d", level, shapeName, bucket), cutoff)
			if err != nil {
				t.Fatalf("capacity materialization failed at bucket %d and %d/%d: %v", bucket, queued, workload.Sources, err)
			}
			if result.Queued == 0 {
				break
			}
			queued += result.Queued
		}
	}
	if queued != workload.Sources {
		t.Fatalf("capacity materialization stopped at %d/%d", queued, workload.Sources)
	}
	materializeDuration := time.Since(materializeStarted)
	detailStarted := time.Now()
	seedCapacityDetailWorks(t, ctx, db, workload, now)
	detailDuration := time.Since(detailStarted)

	assertCapacityFacts(t, ctx, db, repository, workload, shape, plan.Run.DailyRunID, windowStart, windowEnd,
		windowEnd.Add(-time.Microsecond))
	metrics, _ := json.Marshal(map[string]any{"version": "recruiting.capacity-result.v1", "level": level,
		"shape": shapeName, "window_minutes": int(shape.Window.Minutes()), "materialize_ticks": shape.Buckets,
		"companies": workload.Companies, "sources": workload.Sources, "details_per_source": workload.DetailsPerSource,
		"retry_percent": workload.RetryPercent, "seed_ms": seedDuration.Milliseconds(),
		"plan_ms": planDuration.Milliseconds(), "materialize_ms": materializeDuration.Milliseconds(),
		"detail_seed_ms": detailDuration.Milliseconds(), "total_ms": time.Since(started).Milliseconds()})
	t.Log(string(metrics))
}

func seedCapacityRoster(t *testing.T, ctx context.Context, db *sql.DB, repository *Repository,
	workload capacityWorkload, now time.Time) {
	t.Helper()
	recipe := activeRecipe(t, "capacity-listing", model.RecipeListing, "jobs.capacity.example.test", 1, "capacity-contract")
	if err := repository.CreateRecipe(ctx, recipe, now); err != nil {
		t.Fatal(err)
	}
	for offset := 0; offset < workload.Companies; offset += 500 {
		through := min(offset+500, workload.Companies)
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		statement, err := tx.PrepareContext(ctx, `INSERT INTO recruiting_companies(
  company_id, normalized_website, name, onboarding_status, control_status,
  version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		for index := offset; index < through; index++ {
			company, err := model.NewCompany(fmt.Sprintf("capacity-company-%05d", index), "Capacity Company",
				fmt.Sprintf("https://capacity.example.test/company/%05d", index))
			if err == nil {
				company, err = company.StartDiscovery(company.Version)
			}
			if err == nil {
				company, err = company.StartInitialization(company.Version)
			}
			if err == nil {
				company, err = company.MarkReady(company.Version)
			}
			if err != nil {
				_ = statement.Close()
				_ = tx.Rollback()
				t.Fatal(err)
			}
			state, _ := json.Marshal(company)
			if _, err := statement.ExecContext(ctx, company.CompanyID, company.Website, company.Name,
				company.OnboardingStatus, company.ControlStatus, company.Version, state, now, now); err != nil {
				_ = statement.Close()
				_ = tx.Rollback()
				t.Fatalf("seed capacity Company %d: %v", index, err)
			}
		}
		_ = statement.Close()
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
	for offset := 0; offset < workload.Sources; offset += 500 {
		through := min(offset+500, workload.Sources)
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		sourceStatement, err := tx.PrepareContext(ctx, `INSERT INTO recruiting_sources(
  source_id, company_id, canonical_source_key, origin, readiness_status, control_status, health_status,
  discovery_generation, version, state_json, created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		assignmentStatement, assignmentErr := tx.PrepareContext(ctx, `INSERT INTO recruiting_source_assignments(
  source_id, recipe_kind, recipe_id, recipe_version, contract_hash, effective_at, assignment_version, state_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
		historyStatement, historyErr := tx.PrepareContext(ctx, `INSERT INTO recruiting_source_assignment_versions(
  source_id, recipe_kind, assignment_version, recipe_id, recipe_version, contract_hash, effective_at, state_json, recorded_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
		if err != nil || assignmentErr != nil || historyErr != nil {
			_ = tx.Rollback()
			t.Fatalf("prepare capacity Source statements: %v %v %v", err, assignmentErr, historyErr)
		}
		for index := offset; index < through; index++ {
			sourceID := fmt.Sprintf("capacity-source-%05d", index)
			companyID := fmt.Sprintf("capacity-company-%05d", index%workload.Companies)
			source, err := model.NewRecruitmentSource(sourceID, companyID,
				fmt.Sprintf("https://jobs.capacity.example.test/openings/%05d", index), "all", 1)
			if err == nil {
				source, err = source.BeginValidation(source.Version)
			}
			assignment, assignmentErr := model.NewSourceRecipeAssignment(sourceID, model.RecipeListing,
				recipe.RecipeID, recipe.Version, recipe.ContractHash, now.Format(time.RFC3339Nano))
			if err == nil && assignmentErr == nil {
				source, err = source.PublishValidated(source.Version, assignment, verifiedStoreAssessment(source, assignment, now))
			} else if err == nil {
				err = assignmentErr
			}
			if err != nil {
				_ = tx.Rollback()
				t.Fatalf("build capacity Source %d: %v", index, err)
			}
			sourceState, _ := json.Marshal(source)
			assignmentState, _ := json.Marshal(assignment)
			if _, err := sourceStatement.ExecContext(ctx, source.SourceID, source.CompanyID,
				source.ActiveEndpoint.CanonicalKey, "jobs.capacity.example.test", source.ReadinessStatus,
				source.ControlStatus, source.HealthStatus, source.DiscoveryGeneration, source.Version, sourceState, now, now); err != nil {
				_ = tx.Rollback()
				t.Fatalf("seed capacity Source %d: %v", index, err)
			}
			if _, err := assignmentStatement.ExecContext(ctx, source.SourceID, assignment.Kind, assignment.RecipeID,
				assignment.RecipeVersion, assignment.ContractHash, now, assignment.AssignmentVersion, assignmentState); err != nil {
				_ = tx.Rollback()
				t.Fatalf("seed capacity Assignment %d: %v", index, err)
			}
			if _, err := historyStatement.ExecContext(ctx, source.SourceID, assignment.Kind, assignment.AssignmentVersion,
				assignment.RecipeID, assignment.RecipeVersion, assignment.ContractHash, now, assignmentState, now); err != nil {
				_ = tx.Rollback()
				t.Fatalf("seed capacity Assignment history %d: %v", index, err)
			}
		}
		_ = sourceStatement.Close()
		_ = assignmentStatement.Close()
		_ = historyStatement.Close()
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
}

func seedCapacityDetailWorks(t *testing.T, ctx context.Context, db *sql.DB, workload capacityWorkload, now time.Time) {
	t.Helper()
	total := workload.Sources * workload.DetailsPerSource
	for offset := 0; offset < total; offset += 500 {
		through := min(offset+500, total)
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		for ordinal := offset; ordinal < through; ordinal++ {
			sourceIndex := ordinal / workload.DetailsPerSource
			detailIndex := ordinal % workload.DetailsPerSource
			workID := fmt.Sprintf("capacity-detail-%05d-%02d", sourceIndex, detailIndex)
			work, err := model.NewWork(workID, "source", fmt.Sprintf("capacity-source-%05d", sourceIndex), "detail_sync", "timer")
			if err == nil {
				work, err = work.WithCausality("recruiting", "capacity-detail-materialization", "")
			}
			if err == nil && workload.RetryPercent > 0 && ordinal%100 < workload.RetryPercent {
				work, err = work.Start(work.Version)
				if err == nil {
					work, err = work.ApplyExecutionFailure(work.Version, model.ExecutionFailureDecision{PolicyVersion: 1,
						AttemptCount: 1, FailureClass: "capacity_transient", Route: model.FailureRetry,
						RetryNotBefore: now.Add(time.Hour).Format(time.RFC3339Nano)})
				}
			}
			if err != nil {
				_ = tx.Rollback()
				t.Fatalf("build capacity Detail Work %d: %v", ordinal, err)
			}
			placement := WorkPlacement{BusinessKey: "capacity-detail|" + workID, Priority: 50,
				Capability: "http.fetch", Origin: "https://jobs.capacity.example.test", NotBefore: now}
			if work.Status == model.WorkWaitingRetry {
				placement.NotBefore = now.Add(time.Hour)
			}
			if err := insertCapacityWork(ctx, tx, work, placement, now); err != nil {
				_ = tx.Rollback()
				t.Fatalf("seed capacity Detail Work %d: %v", ordinal, err)
			}
		}
		if err := tx.Commit(); err != nil {
			t.Fatal(err)
		}
	}
}

func insertCapacityWork(ctx context.Context, tx *sql.Tx, work model.Work, placement WorkPlacement, now time.Time) error {
	state, err := json.Marshal(work)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO recruiting_works(
  work_id, parent_work_id, initiator_actor_id, cause_message_id, cause_work_id,
  business_key, target_type, target_id, purpose, trigger_kind, status, resolution,
  priority, capability, origin, profile_id, blocked_by_repair_work_id, not_before,
  deadline_at, acceptance_version, version, state_json, created_at, updated_at
) VALUES (?, NULL, ?, ?, NULL, ?, ?, ?, ?, ?, ?, NULL, ?, ?, ?, NULL, NULL, ?, NULL, ?, ?, ?, ?, ?)`,
		work.WorkID, work.InitiatorActorID, work.CauseMessageID, placement.BusinessKey, work.TargetType,
		work.TargetID, work.Purpose, work.Trigger, work.Status, placement.Priority, placement.Capability,
		placement.Origin, placement.NotBefore, work.AcceptanceVersion, work.Version, state, now, now)
	return err
}

func assertCapacityFacts(t *testing.T, ctx context.Context, db *sql.DB, repository *Repository,
	workload capacityWorkload, shape capacityScheduleShape, dailyRunID string, windowStart, windowEnd, dueAt time.Time) {
	t.Helper()
	var occurrences, distinctSources, queued, listingWorks, detailWorks, distinctBusinessKeys, openStatus, retryStatus int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*), COUNT(DISTINCT source_id),
  SUM(status = 'queued') FROM recruiting_source_occurrences WHERE daily_run_id = ?`, dailyRunID).
		Scan(&occurrences, &distinctSources, &queued); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT
  SUM(purpose = 'listing_sync'), SUM(purpose = 'detail_sync'), COUNT(DISTINCT business_key),
  SUM(status = 'open'), SUM(status = 'waiting_retry')
FROM recruiting_works`).Scan(&listingWorks, &detailWorks, &distinctBusinessKeys, &openStatus, &retryStatus); err != nil {
		t.Fatal(err)
	}
	expectedDetails := workload.Sources * workload.DetailsPerSource
	expectedWorks := workload.Sources + expectedDetails
	expectedRetryWorks := 0
	if workload.RetryPercent > 0 {
		for ordinal := 0; ordinal < expectedDetails; ordinal++ {
			if ordinal%100 < workload.RetryPercent {
				expectedRetryWorks++
			}
		}
	}
	expectedOpenWorks := expectedWorks - expectedRetryWorks
	if occurrences != workload.Sources || distinctSources != workload.Sources || queued != workload.Sources ||
		listingWorks != workload.Sources || detailWorks != expectedDetails || distinctBusinessKeys != expectedWorks ||
		openStatus != expectedOpenWorks || retryStatus != expectedRetryWorks {
		t.Fatalf("capacity facts occurrences=%d sources=%d queued=%d listing=%d detail=%d keys=%d open=%d retry=%d want sources=%d detail=%d works=%d open=%d retry=%d",
			occurrences, distinctSources, queued, listingWorks, detailWorks, distinctBusinessKeys, openStatus, retryStatus,
			workload.Sources, expectedDetails, expectedWorks, expectedOpenWorks, expectedRetryWorks)
	}
	var outsideWindow int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_source_occurrences
WHERE daily_run_id = ? AND (due_at < ? OR due_at >= ?)`, dailyRunID, windowStart, windowEnd).Scan(&outsideWindow); err != nil || outsideWindow != 0 {
		t.Fatalf("capacity due times outside window=%d err=%v", outsideWindow, err)
	}
	rows, err := db.QueryContext(ctx, `SELECT due_at FROM recruiting_source_occurrences
WHERE daily_run_id = ? ORDER BY due_at, occurrence_id`, dailyRunID)
	if err != nil {
		t.Fatal(err)
	}
	bucketWidth := shape.Window / time.Duration(shape.Buckets)
	bucketCounts := make([]int, shape.Buckets)
	for rows.Next() {
		var scheduled time.Time
		if err := rows.Scan(&scheduled); err != nil {
			_ = rows.Close()
			t.Fatal(err)
		}
		bucket := int(scheduled.Sub(windowStart) / bucketWidth)
		if bucket < 0 || bucket >= len(bucketCounts) {
			_ = rows.Close()
			t.Fatalf("capacity due time %s maps outside %d buckets", scheduled, shape.Buckets)
		}
		bucketCounts[bucket]++
	}
	_ = rows.Close()
	maxBucket := 0
	populatedBuckets := 0
	for _, count := range bucketCounts {
		if count > 0 {
			populatedBuckets++
		}
		if count > maxBucket {
			maxBucket = count
		}
	}
	if populatedBuckets != shape.Buckets || maxBucket > (2*workload.Sources+shape.Buckets-1)/shape.Buckets {
		t.Fatalf("capacity due distribution buckets=%d/%d max=%d sources=%d window=%s",
			populatedBuckets, shape.Buckets, maxBucket, workload.Sources, shape.Window)
	}
	snapshot, err := repository.GetCapacitySnapshot(ctx, dueAt, 100)
	if err != nil {
		t.Fatal(err)
	}
	expectedScanned := min(expectedOpenWorks, capacityRunnableScanPerStatus) + min(expectedRetryWorks, capacityRunnableScanPerStatus)
	expectedExact := expectedOpenWorks <= capacityRunnableScanPerStatus && expectedRetryWorks <= capacityRunnableScanPerStatus
	if snapshot.RunnableScanned != uint64(expectedScanned) || snapshot.RunnableCountsExact != expectedExact {
		t.Fatalf("capacity projection=%+v expected_scanned=%d expected_exact=%v total=%d open=%d retry=%d",
			snapshot, expectedScanned, expectedExact, expectedWorks, expectedOpenWorks, expectedRetryWorks)
	}
}
