-- 018 introduced controls without invalidating previously applied snapshots.
-- Reconcile existing desktops once; never rewrite an already-shipped migration.
UPDATE desktop_access_policies
SET desired_revision=desired_revision+1, state='pending', last_error='', updated_at=now();
