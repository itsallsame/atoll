package store

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestOperationalAndCapacityStatusUseCoherentBoundedFacts(t *testing.T) {
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
	now := time.Date(2094, 6, 7, 8, 0, 0, 0, time.UTC)
	deadline := now.Add(time.Hour)
	work, _ := model.NewWork("status-runnable-work", "source", "status-source", "repair", "manual")
	if err := repository.CreateWork(ctx, work, WorkPlacement{
		BusinessKey: "status|runnable", Priority: 10, Capability: "status.http.fetch", Origin: "https://status.example.test",
		ProfileID: "status-profile", NotBefore: now.Add(-time.Minute), DeadlineAt: &deadline,
	}, now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	expiredDeadline := now.Add(-time.Second)
	expired, _ := model.NewWork("status-deadline-work", "source", "status-source", "repair", "manual")
	if err := repository.CreateWork(ctx, expired, WorkPlacement{
		BusinessKey: "status|deadline", Priority: 1, Capability: "status.deadline", NotBefore: now.Add(-time.Hour), DeadlineAt: &expiredDeadline,
	}, now.Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `
INSERT INTO recruiting_budget_usage(dimension_type, dimension_key, active_count, version, updated_at)
VALUES ('capability', 'status.http.fetch', 3, 1, ?)`, now); err != nil {
		t.Fatal(err)
	}

	status, err := repository.GetOperationalStatus(ctx, now)
	if err != nil || status.AsOf != now.Format(time.RFC3339Nano) || status.WorkCounts[string(model.WorkOpen)] < 2 ||
		status.RunnableWorks < 1 || status.DeadlineMisses < 1 || status.OldestRunnableAt == "" {
		t.Fatalf("operational status = %+v err=%v", status, err)
	}
	capacity, err := repository.GetCapacitySnapshot(ctx, now, 100)
	if err != nil {
		t.Fatal(err)
	}
	if capacity.RunnableScanned < 1 || capacity.RunnableScanned > capacity.RunnableScanLimit || capacity.RunnableScanLimit != 10_000 {
		t.Fatalf("capacity scan bounds = %+v", capacity)
	}
	var capabilityActive, capabilityRunnable, origin, profile bool
	for _, item := range capacity.Dimensions {
		switch {
		case item.DimensionType == "capability" && item.DimensionKey == "status.http.fetch":
			capabilityActive = item.Active == 3
			capabilityRunnable = item.Runnable == 1 && item.OldestRunnableAt != ""
		case item.DimensionType == "origin" && item.DimensionKey == "https://status.example.test":
			origin = item.Runnable == 1
		case item.DimensionType == "profile" && item.DimensionKey == "status-profile":
			profile = item.Runnable == 1
		}
	}
	if !capabilityActive || (capacity.RunnableCountsExact && (!capabilityRunnable || !origin || !profile)) {
		t.Fatalf("capacity dimensions missing: %+v", capacity.Dimensions)
	}
	if _, err := repository.GetCapacitySnapshot(ctx, now, 101); err == nil {
		t.Fatal("unbounded capacity status was accepted")
	}

	var explain string
	if err := db.QueryRowContext(ctx, `EXPLAIN FORMAT=JSON
SELECT work_id FROM recruiting_works
WHERE status IN ('open','running','waiting_retry','waiting_human','paused') AND deadline_at IS NOT NULL AND deadline_at <= ?`, now).Scan(&explain); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "ix_recruiting_work_deadline") {
		t.Fatalf("deadline status query missed its index: %s", explain)
	}
	if err := db.QueryRowContext(ctx, `EXPLAIN FORMAT=JSON
SELECT capability, origin, profile_id, not_before
FROM recruiting_works FORCE INDEX (ix_recruiting_work_runnable)
WHERE status = 'open' AND not_before <= ? AND (deadline_at IS NULL OR deadline_at > ?)
ORDER BY not_before, priority DESC, deadline_at, work_id LIMIT 5001`, now, now).Scan(&explain); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "ix_recruiting_work_runnable") {
		t.Fatalf("capacity scan missed its runnable index: %s", explain)
	}
}
