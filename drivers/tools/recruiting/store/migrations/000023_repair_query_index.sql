ALTER TABLE recruiting_repair_incidents
  ADD KEY ix_recruiting_repair_updated (updated_at, incident_id);
