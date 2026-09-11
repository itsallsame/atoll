package store

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"time"
)

type DeliveryBacklog struct {
	Pending     uint64 `json:"pending"`
	Due         uint64 `json:"due"`
	Exhausted   uint64 `json:"exhausted"`
	OldestDueAt string `json:"oldest_due_at,omitempty"`
}

type OperationalStatus struct {
	AsOf                string            `json:"as_of"`
	WorkCounts          map[string]uint64 `json:"work_counts"`
	RunnableWorks       uint64            `json:"runnable_works"`
	DeadlineMisses      uint64            `json:"deadline_misses"`
	OldestRunnableAt    string            `json:"oldest_runnable_at,omitempty"`
	AttemptCounts       map[string]uint64 `json:"attempt_counts"`
	DailyRunCounts      map[string]uint64 `json:"daily_run_counts"`
	RepairCounts        map[string]uint64 `json:"repair_counts"`
	RepairRecoveryQueue uint64            `json:"repair_recovery_queue"`
	EventOutbox         DeliveryBacklog   `json:"event_outbox"`
	ExecutionDispatches DeliveryBacklog   `json:"execution_dispatches"`
}

type CapacityDimension struct {
	DimensionType    string `json:"dimension_type"`
	DimensionKey     string `json:"dimension_key"`
	Active           uint64 `json:"active"`
	Runnable         uint64 `json:"runnable"`
	OldestRunnableAt string `json:"oldest_runnable_at,omitempty"`
}

type CapacitySnapshot struct {
	AsOf                string              `json:"as_of"`
	Dimensions          []CapacityDimension `json:"dimensions"`
	RunnableScanned     uint64              `json:"runnable_scanned"`
	RunnableScanLimit   uint64              `json:"runnable_scan_limit"`
	RunnableCountsExact bool                `json:"runnable_counts_exact"`
}

const capacityRunnableScanPerStatus = 5_000

func (r *Repository) GetOperationalStatus(ctx context.Context, asOf time.Time) (OperationalStatus, error) {
	if asOf.IsZero() {
		return OperationalStatus{}, fmt.Errorf("operational status time is required")
	}
	asOf = asOf.UTC()
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return OperationalStatus{}, fmt.Errorf("begin operational status snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	status := OperationalStatus{AsOf: asOf.Format(time.RFC3339Nano)}
	if status.WorkCounts, err = groupedCounts(ctx, tx, "SELECT status, COUNT(*) FROM recruiting_works GROUP BY status"); err != nil {
		return OperationalStatus{}, fmt.Errorf("count work status: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*), COALESCE(DATE_FORMAT(MIN(not_before), '%Y-%m-%dT%H:%i:%s.%fZ'), '')
FROM recruiting_works
WHERE status IN ('open', 'waiting_retry') AND not_before <= ? AND (deadline_at IS NULL OR deadline_at > ?)`, asOf, asOf).
		Scan(&status.RunnableWorks, &status.OldestRunnableAt); err != nil {
		return OperationalStatus{}, fmt.Errorf("read runnable work status: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `
SELECT COUNT(*) FROM recruiting_works
WHERE status IN ('open','running','waiting_retry','waiting_human','paused') AND deadline_at IS NOT NULL AND deadline_at <= ?`, asOf).
		Scan(&status.DeadlineMisses); err != nil {
		return OperationalStatus{}, fmt.Errorf("count missed work deadlines: %w", err)
	}
	if status.AttemptCounts, err = groupedCounts(ctx, tx, "SELECT attempt_status, COUNT(*) FROM recruiting_attempts GROUP BY attempt_status"); err != nil {
		return OperationalStatus{}, fmt.Errorf("count attempt status: %w", err)
	}
	if status.DailyRunCounts, err = groupedCounts(ctx, tx, "SELECT status, COUNT(*) FROM recruiting_daily_runs GROUP BY status"); err != nil {
		return OperationalStatus{}, fmt.Errorf("count daily run status: %w", err)
	}
	if status.RepairCounts, err = groupedCounts(ctx, tx, "SELECT repair_status, COUNT(*) FROM recruiting_repair_incidents GROUP BY repair_status"); err != nil {
		return OperationalStatus{}, fmt.Errorf("count repair status: %w", err)
	}
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM recruiting_repair_incidents
WHERE recovery_pending = 1`).Scan(&status.RepairRecoveryQueue); err != nil {
		return OperationalStatus{}, fmt.Errorf("count repair recovery queue: %w", err)
	}
	if status.EventOutbox, err = deliveryBacklog(ctx, tx, "recruiting_event_outbox", asOf); err != nil {
		return OperationalStatus{}, fmt.Errorf("read event outbox status: %w", err)
	}
	if status.ExecutionDispatches, err = deliveryBacklog(ctx, tx, "recruiting_execution_dispatch_outbox", asOf); err != nil {
		return OperationalStatus{}, fmt.Errorf("read execution dispatch status: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return OperationalStatus{}, fmt.Errorf("commit operational status snapshot: %w", err)
	}
	return status, nil
}

func (r *Repository) GetCapacitySnapshot(ctx context.Context, asOf time.Time, limit int) (CapacitySnapshot, error) {
	if asOf.IsZero() || limit < 1 || limit > 100 {
		return CapacitySnapshot{}, fmt.Errorf("capacity status requires time and limit in [1,100]")
	}
	asOf = asOf.UTC()
	tx, err := r.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return CapacitySnapshot{}, fmt.Errorf("begin capacity snapshot: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	dimensions := map[string]CapacityDimension{}
	rows, err := tx.QueryContext(ctx, `
SELECT dimension_type, dimension_key, active_count
FROM recruiting_budget_usage
WHERE active_count > 0
ORDER BY active_count DESC, dimension_type, dimension_key
LIMIT ?`, limit)
	if err != nil {
		return CapacitySnapshot{}, fmt.Errorf("read active budget usage: %w", err)
	}
	for rows.Next() {
		var item CapacityDimension
		if err := rows.Scan(&item.DimensionType, &item.DimensionKey, &item.Active); err != nil {
			_ = rows.Close()
			return CapacitySnapshot{}, fmt.Errorf("scan active budget usage: %w", err)
		}
		dimensions[item.DimensionType+"\x00"+item.DimensionKey] = item
	}
	if err := rows.Close(); err != nil {
		return CapacitySnapshot{}, err
	}
	if err := rows.Err(); err != nil {
		return CapacitySnapshot{}, err
	}
	var runnableScanned uint64
	runnableExact := true
	for _, workStatus := range []string{"open", "waiting_retry"} {
		rows, err = tx.QueryContext(ctx, `
SELECT capability, origin, profile_id, not_before
FROM recruiting_works FORCE INDEX (ix_recruiting_work_runnable)
WHERE status = ? AND not_before <= ? AND (deadline_at IS NULL OR deadline_at > ?)
ORDER BY not_before, priority DESC, deadline_at, work_id
LIMIT ?`, workStatus, asOf, asOf, capacityRunnableScanPerStatus+1)
		if err != nil {
			return CapacitySnapshot{}, fmt.Errorf("read bounded runnable capacity: %w", err)
		}
		seen := 0
		for rows.Next() {
			var capability, origin, profile sql.NullString
			var notBefore time.Time
			if err := rows.Scan(&capability, &origin, &profile, &notBefore); err != nil {
				_ = rows.Close()
				return CapacitySnapshot{}, fmt.Errorf("scan bounded runnable capacity: %w", err)
			}
			seen++
			if seen > capacityRunnableScanPerStatus {
				runnableExact = false
				continue
			}
			runnableScanned++
			for _, dimension := range []struct {
				typeName string
				key      string
			}{
				{typeName: "capability", key: capability.String},
				{typeName: "origin", key: origin.String},
				{typeName: "profile", key: profile.String},
			} {
				if dimension.key == "" {
					continue
				}
				mapKey := dimension.typeName + "\x00" + dimension.key
				item := dimensions[mapKey]
				item.DimensionType, item.DimensionKey = dimension.typeName, dimension.key
				item.Runnable++
				if item.OldestRunnableAt == "" {
					item.OldestRunnableAt = notBefore.UTC().Format(time.RFC3339Nano)
				}
				dimensions[mapKey] = item
			}
		}
		if err := rows.Close(); err != nil {
			return CapacitySnapshot{}, err
		}
		if err := rows.Err(); err != nil {
			return CapacitySnapshot{}, err
		}
	}
	items := make([]CapacityDimension, 0, len(dimensions))
	for _, item := range dimensions {
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].DimensionType != items[j].DimensionType {
			return items[i].DimensionType < items[j].DimensionType
		}
		leftPressure, rightPressure := items[i].Runnable+items[i].Active, items[j].Runnable+items[j].Active
		if leftPressure != rightPressure {
			return leftPressure > rightPressure
		}
		return items[i].DimensionKey < items[j].DimensionKey
	})
	bounded := items[:0]
	countsByType := map[string]int{}
	for _, item := range items {
		if countsByType[item.DimensionType] >= limit {
			continue
		}
		bounded = append(bounded, item)
		countsByType[item.DimensionType]++
	}
	items = bounded
	if err := tx.Commit(); err != nil {
		return CapacitySnapshot{}, fmt.Errorf("commit capacity snapshot: %w", err)
	}
	return CapacitySnapshot{
		AsOf: asOf.Format(time.RFC3339Nano), Dimensions: items, RunnableScanned: runnableScanned,
		RunnableScanLimit: 2 * capacityRunnableScanPerStatus, RunnableCountsExact: runnableExact,
	}, nil
}

func groupedCounts(ctx context.Context, tx *sql.Tx, query string) (map[string]uint64, error) {
	rows, err := tx.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]uint64{}
	for rows.Next() {
		var status string
		var count uint64
		if err := rows.Scan(&status, &count); err != nil {
			return nil, err
		}
		counts[status] = count
	}
	return counts, rows.Err()
}

func deliveryBacklog(ctx context.Context, tx *sql.Tx, table string, asOf time.Time) (DeliveryBacklog, error) {
	if table != "recruiting_event_outbox" && table != "recruiting_execution_dispatch_outbox" {
		return DeliveryBacklog{}, fmt.Errorf("unsupported delivery table")
	}
	query := fmt.Sprintf(`
SELECT
  SUM(delivery_status = 'pending'),
  SUM(delivery_status = 'pending' AND next_attempt_at <= ?),
  SUM(delivery_status = 'exhausted'),
  COALESCE(DATE_FORMAT(MIN(CASE WHEN delivery_status = 'pending' THEN next_attempt_at END), '%%Y-%%m-%%dT%%H:%%i:%%s.%%fZ'), '')
FROM %s`, table)
	var pending, due, exhausted sql.NullInt64
	var oldest string
	if err := tx.QueryRowContext(ctx, query, asOf).Scan(&pending, &due, &exhausted, &oldest); err != nil {
		return DeliveryBacklog{}, err
	}
	return DeliveryBacklog{Pending: uint64(pending.Int64), Due: uint64(due.Int64), Exhausted: uint64(exhausted.Int64), OldestDueAt: oldest}, nil
}
