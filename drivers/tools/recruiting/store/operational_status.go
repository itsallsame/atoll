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
	ExecutionHealth     ExecutionHealth   `json:"execution_health"`
}

type LatencySummary struct {
	Samples uint64 `json:"samples"`
	P50MS   uint64 `json:"p50_ms"`
	P95MS   uint64 `json:"p95_ms"`
	P99MS   uint64 `json:"p99_ms"`
	MaxMS   uint64 `json:"max_ms"`
}

// ExecutionHealth is a bounded recent-window projection. Counts are exact only
// when their corresponding truncated flag is false; callers must not promote a
// bounded lower bound into an all-history metric.
type ExecutionHealth struct {
	WindowStartAt              string            `json:"window_start_at"`
	SampleLimit                uint64            `json:"sample_limit"`
	AttemptResults             map[string]uint64 `json:"attempt_results"`
	AttemptsScanned            uint64            `json:"attempts_scanned"`
	AttemptsTruncated          bool              `json:"attempts_truncated"`
	TerminalLatency            LatencySummary    `json:"terminal_latency"`
	OfferToAcceptLatency       LatencySummary    `json:"offer_to_accept_observed_latency"`
	AcceptToStartLatency       LatencySummary    `json:"accept_to_start_observed_latency"`
	StartToTerminalLatency     LatencySummary    `json:"start_to_terminal_observed_latency"`
	TerminalToNextOfferLatency LatencySummary    `json:"terminal_to_next_offer_observed_latency"`
	ExpiredAttempts            uint64            `json:"expired_attempts"`
	RecoveredExpiredAttempts   uint64            `json:"recovered_expired_attempts"`
	RecoveryLatency            LatencySummary    `json:"recovery_latency"`
	RejectedArtifactsScanned   uint64            `json:"rejected_artifacts_scanned"`
	RejectedArtifactsTruncated bool              `json:"rejected_artifacts_truncated"`
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

const (
	defaultExecutionHealthWindow = time.Hour
	defaultExecutionHealthLimit  = 1_000
	maxExecutionHealthLimit      = 5_000
)

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
	if status.ExecutionHealth, err = executionHealth(ctx, tx, asOf, defaultExecutionHealthWindow, defaultExecutionHealthLimit); err != nil {
		return OperationalStatus{}, fmt.Errorf("read execution health: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return OperationalStatus{}, fmt.Errorf("commit operational status snapshot: %w", err)
	}
	return status, nil
}

type recentAttempt struct {
	workID             string
	status             string
	executorActorID    string
	createdAt          time.Time
	updatedAt          time.Time
	offeredObservedAt  sql.NullTime
	acceptedObservedAt sql.NullTime
	startedObservedAt  sql.NullTime
	terminalObservedAt sql.NullTime
}

func executionHealth(ctx context.Context, tx *sql.Tx, asOf time.Time, window time.Duration, limit int) (ExecutionHealth, error) {
	if window <= 0 || limit < 1 || limit > maxExecutionHealthLimit {
		return ExecutionHealth{}, fmt.Errorf("execution health requires a positive window and limit in [1,%d]", maxExecutionHealthLimit)
	}
	start := asOf.Add(-window)
	health := ExecutionHealth{
		WindowStartAt: start.Format(time.RFC3339Nano), SampleLimit: uint64(limit),
		AttemptResults: map[string]uint64{},
	}
	rows, err := tx.QueryContext(ctx, `
SELECT work_id, attempt_status, COALESCE(executor_actor_id, ''), created_at, updated_at,
       offered_observed_at, accepted_observed_at, started_observed_at, terminal_observed_at
FROM recruiting_attempts FORCE INDEX (ix_recruiting_attempt_recent)
WHERE updated_at >= ? AND updated_at <= ?
ORDER BY updated_at DESC, attempt_id DESC
LIMIT ?`, start, asOf, limit+1)
	if err != nil {
		return ExecutionHealth{}, err
	}
	attempts := make([]recentAttempt, 0, limit)
	for rows.Next() {
		var item recentAttempt
		if err := rows.Scan(&item.workID, &item.status, &item.executorActorID, &item.createdAt, &item.updatedAt,
			&item.offeredObservedAt, &item.acceptedObservedAt, &item.startedObservedAt, &item.terminalObservedAt); err != nil {
			_ = rows.Close()
			return ExecutionHealth{}, err
		}
		if len(attempts) == limit {
			health.AttemptsTruncated = true
			continue
		}
		attempts = append(attempts, item)
		health.AttemptResults[item.status]++
	}
	if err := rows.Close(); err != nil {
		return ExecutionHealth{}, err
	}
	if err := rows.Err(); err != nil {
		return ExecutionHealth{}, err
	}
	health.AttemptsScanned = uint64(len(attempts))
	terminalLatencies := make([]uint64, 0, len(attempts))
	recoveryLatencies := make([]uint64, 0)
	offerToAcceptLatencies := make([]uint64, 0, len(attempts))
	acceptToStartLatencies := make([]uint64, 0, len(attempts))
	startToTerminalLatencies := make([]uint64, 0, len(attempts))
	terminalToNextOfferLatencies := make([]uint64, 0, len(attempts))
	latestSuccess := map[string]time.Time{}
	for _, item := range attempts {
		if item.offeredObservedAt.Valid && item.acceptedObservedAt.Valid {
			offerToAcceptLatencies = append(offerToAcceptLatencies, elapsedMilliseconds(item.offeredObservedAt.Time, item.acceptedObservedAt.Time))
		}
		if item.acceptedObservedAt.Valid && item.startedObservedAt.Valid {
			acceptToStartLatencies = append(acceptToStartLatencies, elapsedMilliseconds(item.acceptedObservedAt.Time, item.startedObservedAt.Time))
		}
		if item.startedObservedAt.Valid && item.terminalObservedAt.Valid {
			startToTerminalLatencies = append(startToTerminalLatencies, elapsedMilliseconds(item.startedObservedAt.Time, item.terminalObservedAt.Time))
		}
		switch item.status {
		case "succeeded", "failed", "expired", "rejected":
			terminalLatencies = append(terminalLatencies, elapsedMilliseconds(item.createdAt, item.updatedAt))
		}
		if item.status == "succeeded" {
			latestSuccess[item.workID] = item.updatedAt
		}
		if item.status == "expired" {
			health.ExpiredAttempts++
			if recoveredAt, ok := latestSuccess[item.workID]; ok && recoveredAt.After(item.updatedAt) {
				health.RecoveredExpiredAttempts++
				recoveryLatencies = append(recoveryLatencies, elapsedMilliseconds(item.updatedAt, recoveredAt))
			}
		}
	}
	attemptsByExecutor := map[string][]recentAttempt{}
	for _, item := range attempts {
		if item.executorActorID != "" && item.offeredObservedAt.Valid {
			attemptsByExecutor[item.executorActorID] = append(attemptsByExecutor[item.executorActorID], item)
		}
	}
	for _, executorAttempts := range attemptsByExecutor {
		sort.Slice(executorAttempts, func(left, right int) bool {
			return executorAttempts[left].offeredObservedAt.Time.Before(executorAttempts[right].offeredObservedAt.Time)
		})
		for index := 1; index < len(executorAttempts); index++ {
			previous, current := executorAttempts[index-1], executorAttempts[index]
			if previous.terminalObservedAt.Valid && current.offeredObservedAt.Time.After(previous.terminalObservedAt.Time) {
				terminalToNextOfferLatencies = append(terminalToNextOfferLatencies,
					elapsedMilliseconds(previous.terminalObservedAt.Time, current.offeredObservedAt.Time))
			}
		}
	}
	health.TerminalLatency = summarizeLatencies(terminalLatencies)
	health.OfferToAcceptLatency = summarizeLatencies(offerToAcceptLatencies)
	health.AcceptToStartLatency = summarizeLatencies(acceptToStartLatencies)
	health.StartToTerminalLatency = summarizeLatencies(startToTerminalLatencies)
	health.TerminalToNextOfferLatency = summarizeLatencies(terminalToNextOfferLatencies)
	health.RecoveryLatency = summarizeLatencies(recoveryLatencies)
	rows, err = tx.QueryContext(ctx, `
SELECT artifact_id
FROM recruiting_artifacts FORCE INDEX (ix_recruiting_artifact_rejected_recent)
WHERE rejected = 1 AND created_at >= ? AND created_at <= ?
ORDER BY created_at DESC, artifact_id DESC
LIMIT ?`, start, asOf, limit+1)
	if err != nil {
		return ExecutionHealth{}, err
	}
	for rows.Next() {
		var artifactID string
		if err := rows.Scan(&artifactID); err != nil {
			_ = rows.Close()
			return ExecutionHealth{}, err
		}
		if health.RejectedArtifactsScanned == uint64(limit) {
			health.RejectedArtifactsTruncated = true
			continue
		}
		health.RejectedArtifactsScanned++
	}
	if err := rows.Close(); err != nil {
		return ExecutionHealth{}, err
	}
	return health, rows.Err()
}

func elapsedMilliseconds(start, end time.Time) uint64 {
	if !end.After(start) {
		return 0
	}
	return uint64(end.Sub(start).Milliseconds())
}

func summarizeLatencies(values []uint64) LatencySummary {
	if len(values) == 0 {
		return LatencySummary{}
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	pick := func(percentile int) uint64 {
		index := (percentile*len(values) + 99) / 100
		if index < 1 {
			index = 1
		}
		return values[index-1]
	}
	return LatencySummary{Samples: uint64(len(values)), P50MS: pick(50), P95MS: pick(95), P99MS: pick(99), MaxMS: values[len(values)-1]}
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
