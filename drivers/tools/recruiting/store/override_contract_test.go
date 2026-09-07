package store

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/wanpengxie/atoll/drivers/tools/recruiting/model"
)

func TestCuratedOverrideRepositoryContract(t *testing.T) {
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
	if err := Migrate(ctx, db); err != nil {
		t.Fatal(err)
	}
	repository, _ := NewRepository(db)
	now := time.Date(2026, 9, 7, 8, 0, 0, 0, time.UTC)

	initial, _ := model.NewCuratedOverride("override-title-1", "job-override-1", "title", json.RawMessage(`"human title"`), "human:alice", "verified from employer")
	if err := repository.ApplyOverrideCAS(ctx, nil, initial, now); err != nil {
		t.Fatal(err)
	}
	head, stored, err := repository.GetOverrideHead(ctx, initial.TargetID, initial.Field)
	if err != nil || !head.Active || string(stored.Value) != `"human title"` {
		t.Fatalf("initial override head=%+v override=%+v err=%v", head, stored, err)
	}

	left, _ := model.NewCuratedOverride("override-title-left", initial.TargetID, initial.Field, json.RawMessage(`"left"`), "human:bob", "new evidence")
	right, _ := model.NewCuratedOverride("override-title-right", initial.TargetID, initial.Field, json.RawMessage(`"right"`), "human:carol", "other evidence")
	results := make(chan error, 2)
	var group sync.WaitGroup
	for _, candidate := range []model.CuratedOverride{left, right} {
		candidate := candidate
		group.Add(1)
		go func() {
			defer group.Done()
			results <- repository.ApplyOverrideCAS(ctx, &head, candidate, now.Add(time.Second))
		}()
	}
	group.Wait()
	close(results)
	var replaced, conflicted int
	for applyErr := range results {
		if applyErr == nil {
			replaced++
		} else if errors.Is(applyErr, ErrOverrideConflict) {
			conflicted++
		} else {
			t.Fatalf("replace override = %v", applyErr)
		}
	}
	if replaced != 1 || conflicted != 1 {
		t.Fatalf("override replacements=%d conflicts=%d", replaced, conflicted)
	}

	head, current, err := repository.GetOverrideHead(ctx, initial.TargetID, initial.Field)
	if err != nil || !current.Active || (string(current.Value) != `"left"` && string(current.Value) != `"right"`) {
		t.Fatalf("replacement head=%+v override=%+v err=%v", head, current, err)
	}
	newerDetail := &model.FieldValue{Value: json.RawMessage(`"later crawl"`), Source: model.FieldFromDetail, SourceVersion: 99}
	effective, ok := model.EffectiveField(nil, newerDetail, &current)
	if !ok || effective.Source != model.FieldFromOverride || string(effective.Value) != string(current.Value) {
		t.Fatalf("later crawl displaced manual override: %+v", effective)
	}

	cleared, _ := current.Clear(current.Version, "human:dora", "employer source corrected")
	if err := repository.ApplyOverrideCAS(ctx, &head, cleared, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	_, inactive, err := repository.GetOverrideHead(ctx, initial.TargetID, initial.Field)
	if err != nil || inactive.Active {
		t.Fatalf("cleared override=%+v err=%v", inactive, err)
	}
	effective, ok = model.EffectiveField(nil, newerDetail, &inactive)
	if !ok || effective.Source != model.FieldFromDetail || string(effective.Value) != `"later crawl"` {
		t.Fatalf("clear did not reveal verified detail: %+v", effective)
	}
	if err := repository.ApplyOverrideCAS(ctx, &head, cleared, now.Add(3*time.Second)); !errors.Is(err, ErrOverrideConflict) {
		t.Fatalf("stale clear = %v", err)
	}

	history, err := repository.ListOverrideHistory(ctx, initial.TargetID, initial.Field, 10)
	if err != nil || len(history) != 3 || history[len(history)-1].Active {
		t.Fatalf("override history=%+v err=%v", history, err)
	}
	var versionRows int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_override_versions WHERE target_id = ? AND field_name = ?`, initial.TargetID, initial.Field).Scan(&versionRows); err != nil || versionRows != 3 {
		t.Fatalf("append-only version count=%d err=%v", versionRows, err)
	}
	var explain string
	if err := db.QueryRowContext(ctx, `EXPLAIN FORMAT=JSON
SELECT override_id, override_version, actor_id, reason_text, active, value_json
FROM recruiting_override_versions
WHERE target_id = ? AND field_name = ?
ORDER BY created_at, override_id, override_version LIMIT 10`, initial.TargetID, initial.Field).Scan(&explain); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(explain, "ix_recruiting_override_target") {
		t.Fatalf("override history did not use intended index: %s", explain)
	}
}
