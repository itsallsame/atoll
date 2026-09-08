ALTER TABLE recruiting_works
  ADD KEY ix_recruiting_work_deadline (status, deadline_at, work_id);
