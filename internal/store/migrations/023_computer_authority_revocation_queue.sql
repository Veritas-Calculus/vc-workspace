CREATE TABLE desktop_computer_revocations (
  desktop_vmid bigint PRIMARY KEY REFERENCES managed_desktops(vmid) ON DELETE CASCADE,
  requested_revision bigint NOT NULL DEFAULT 1,
  completed_revision bigint NOT NULL DEFAULT 0,
  updated_at timestamptz NOT NULL DEFAULT now(),
  CHECK (completed_revision >= 0 AND requested_revision >= completed_revision)
);
CREATE INDEX desktop_computer_revocations_pending_idx ON desktop_computer_revocations(updated_at)
WHERE requested_revision > completed_revision;

-- Every authoritative lease transition queues Guest fencing in its own
-- transaction, including disable, assignment removal and inventory retirement.
CREATE FUNCTION queue_desktop_computer_revocation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.state='active' AND (NEW.state<>'active' OR NEW.control_epoch<>OLD.control_epoch) THEN
    INSERT INTO desktop_computer_revocations(desktop_vmid)
    SELECT vmid FROM managed_desktops WHERE vmid::text=OLD.desktop_id
    ON CONFLICT(desktop_vmid) DO UPDATE
    SET requested_revision=desktop_computer_revocations.requested_revision+1,updated_at=now();
  END IF;
  RETURN NEW;
END;
$$;
CREATE TRIGGER desktop_lease_computer_revocation
AFTER UPDATE OF state,control_epoch ON desktop_leases
FOR EACH ROW EXECUTE FUNCTION queue_desktop_computer_revocation();

-- Old closed leases may have returned success without a confirmed Guest
-- tombstone. A one-time reconciliation fences that old authority on upgrade.
INSERT INTO desktop_computer_revocations(desktop_vmid)
SELECT DISTINCT d.vmid FROM managed_desktops d JOIN desktop_leases l ON l.desktop_id=d.vmid::text
WHERE l.state<>'active';
