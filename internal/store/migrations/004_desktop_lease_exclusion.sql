CREATE UNIQUE INDEX IF NOT EXISTS desktop_leases_active_desktop_idx
  ON desktop_leases(desktop_id)
  WHERE state = 'active';
