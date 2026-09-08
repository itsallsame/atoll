package store

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestOperationalWorkListUsesBoundSeekCursorAndSelectors(t *testing.T) {
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
	base := time.Date(2092, 9, 9, 1, 0, 0, 0, time.UTC)
	create := func(id, target, purpose, trigger, initiator string, at time.Time) model.Work {
		work, createErr := model.NewWork(id, "source", target, purpose, trigger)
		if createErr != nil {
			t.Fatal(createErr)
		}
		work, createErr = work.WithCausality(initiator, "message-"+id, "")
		if createErr != nil {
			t.Fatal(createErr)
		}
		createErr = repository.CreateWork(ctx, work, WorkPlacement{BusinessKey: "business-" + id, Priority: 71,
			Capability: "http.fetch", Origin: "work-query.example", ProfileID: "profile-work-query", NotBefore: at.Add(-time.Minute)}, at)
		if createErr != nil {
			t.Fatal(createErr)
		}
		return work
	}
	older := create("work-query-older", "work-query-source", "repair", "manual", "human:query:a", base)
	newer := create("work-query-newer", "work-query-source", "repair", "scheduled", "human:query:b", base.Add(time.Second))
	waiting := create("work-query-waiting", "work-query-other", "diagnostic", "manual", "human:query:a", base.Add(2*time.Second))
	running, _ := waiting.Start(waiting.Version)
	if err := repository.UpdateWorkCAS(ctx, waiting.Version, running, base.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	waitingHuman, _ := running.WaitHuman(running.Version, "recipe_invalid")
	if err := repository.UpdateWorkCAS(ctx, running.Version, waitingHuman, base.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}

	selector := WorkListQuery{TargetType: "source", TargetID: "work-query-source", Limit: 1}
	first, err := repository.ListWorks(ctx, selector)
	if err != nil || len(first.Items) != 1 || first.Items[0].Work.WorkID != newer.WorkID || !first.HasMore || first.NextCursor == "" {
		t.Fatalf("first page = %+v err=%v", first, err)
	}
	if first.Items[0].Placement.Capability != "http.fetch" || first.Items[0].Placement.BusinessKey != "business-"+newer.WorkID {
		t.Fatalf("first placement = %+v", first.Items[0].Placement)
	}
	selector.Cursor = first.NextCursor
	second, err := repository.ListWorks(ctx, selector)
	if err != nil || len(second.Items) != 1 || second.Items[0].Work.WorkID != older.WorkID || second.HasMore {
		t.Fatalf("second page = %+v err=%v", second, err)
	}
	selector.Status = model.WorkOpen
	if _, err := repository.ListWorks(ctx, selector); !errors.Is(err, ErrInvalidCursor) {
		t.Fatalf("cursor crossed selector: %v", err)
	}
	waitingPage, err := repository.ListWorks(ctx, WorkListQuery{Status: model.WorkWaitingHuman, WaitingReason: "recipe_invalid", InitiatorActorID: "human:query:a", Limit: 10})
	if err != nil || len(waitingPage.Items) != 1 || waitingPage.Items[0].Work.WorkID != waiting.WorkID {
		t.Fatalf("waiting-human page = %+v err=%v", waitingPage, err)
	}
	if _, err := repository.ListWorks(ctx, WorkListQuery{WaitingReason: "recipe_invalid", Limit: 10}); err == nil {
		t.Fatal("unscoped waiting reason was accepted")
	}

	var explain string
	if err := db.QueryRowContext(ctx, `EXPLAIN FORMAT=JSON SELECT state_json, updated_at, work_id
FROM recruiting_works FORCE INDEX (ix_recruiting_work_status_updated)
WHERE status = 'waiting_human' ORDER BY updated_at DESC, work_id DESC LIMIT 10`).Scan(&explain); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "ix_recruiting_work_status_updated") {
		t.Fatalf("status index missing: %s", explain)
	}
	if err := db.QueryRowContext(ctx, `EXPLAIN FORMAT=JSON SELECT state_json, updated_at, work_id
FROM recruiting_works FORCE INDEX (ix_recruiting_work_updated)
WHERE TRUE ORDER BY updated_at DESC, work_id DESC LIMIT 10`).Scan(&explain); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "ix_recruiting_work_updated") {
		t.Fatalf("global index missing: %s", explain)
	}
}
