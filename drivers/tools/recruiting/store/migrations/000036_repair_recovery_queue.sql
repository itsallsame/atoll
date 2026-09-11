ALTER TABLE recruiting_repair_incidents
  ADD COLUMN recovery_pending TINYINT(1) NOT NULL DEFAULT 0 AFTER recovered_work_count,
  ADD KEY ix_recruiting_repair_recovery_queue (recovery_pending, updated_at, incident_id);

UPDATE recruiting_repair_incidents i
SET i.recovery_pending = 1
WHERE i.repair_status = 'resolved'
  AND EXISTS (
    SELECT 1
    FROM recruiting_repair_affected_works a
    JOIN recruiting_works w ON w.work_id = a.work_id
    WHERE a.incident_id = i.incident_id
      AND w.status = 'waiting_human'
      AND w.blocked_by_repair_work_id = i.repair_work_id
  );
