-- VM-scoped epochs survive lease deletion and inventory re-import. Do not tie
-- this ledger to a lease or cascade it away when a managed desktop is removed.
CREATE TABLE desktop_computer_epochs (
  desktop_vmid bigint PRIMARY KEY CHECK (desktop_vmid > 0),
  control_epoch bigint NOT NULL CHECK (control_epoch > 0),
  updated_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO desktop_computer_epochs(desktop_vmid,control_epoch)
SELECT d.vmid,GREATEST(COALESCE(MAX(l.control_epoch),0),1)
FROM managed_desktops d LEFT JOIN desktop_leases l ON l.desktop_id=d.vmid::text GROUP BY d.vmid;

CREATE FUNCTION assign_desktop_computer_epoch() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE target bigint;
BEGIN
  IF TG_OP='UPDATE' THEN
    IF NEW.desktop_id<>OLD.desktop_id OR NEW.agent_id<>OLD.agent_id THEN
      RAISE EXCEPTION 'lease subject and desktop are immutable';
    END IF;
    IF NEW.state=OLD.state AND NEW.control_epoch=OLD.control_epoch THEN RETURN NEW; END IF;
    IF OLD.state<>'active' AND NEW.state='active' THEN
      RAISE EXCEPTION 'closed lease cannot reactivate';
    END IF;
  END IF;
  SELECT vmid INTO STRICT target FROM managed_desktops WHERE vmid::text=NEW.desktop_id;
  INSERT INTO desktop_computer_epochs(desktop_vmid,control_epoch) VALUES(target,1)
  ON CONFLICT(desktop_vmid) DO UPDATE SET control_epoch=desktop_computer_epochs.control_epoch+1,updated_at=now()
  RETURNING control_epoch INTO NEW.control_epoch;
  RETURN NEW;
END;
$$;
CREATE TRIGGER desktop_lease_assign_computer_epoch
BEFORE INSERT OR UPDATE OF state,control_epoch,desktop_id,agent_id ON desktop_leases
FOR EACH ROW EXECUTE FUNCTION assign_desktop_computer_epoch();

-- The queue fences its own closing epoch, never a newer active lease which
-- may have been issued while this old cleanup was waiting for the Guest.
ALTER TABLE desktop_computer_revocations ADD COLUMN control_epoch bigint NOT NULL DEFAULT 1 CHECK (control_epoch>0);
UPDATE desktop_computer_revocations r SET control_epoch=e.control_epoch
FROM desktop_computer_epochs e WHERE e.desktop_vmid=r.desktop_vmid;
CREATE OR REPLACE FUNCTION queue_desktop_computer_revocation() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF OLD.state='active' AND (NEW.state<>'active' OR NEW.control_epoch<>OLD.control_epoch) THEN
    INSERT INTO desktop_computer_revocations(desktop_vmid,control_epoch)
    SELECT vmid,NEW.control_epoch FROM managed_desktops WHERE vmid::text=OLD.desktop_id
    ON CONFLICT(desktop_vmid) DO UPDATE
    SET requested_revision=desktop_computer_revocations.requested_revision+1,
      control_epoch=GREATEST(desktop_computer_revocations.control_epoch,excluded.control_epoch),updated_at=now();
  END IF;
  RETURN NEW;
END;
$$;

-- Old per-lease epochs are not globally ordered. Invalidate active grants on
-- upgrade and queue both authority and OS-session cleanup via existing triggers.
-- Clients must acquire a fresh lease; no silent reuse of a possibly stale grant.
UPDATE desktop_leases SET state='revoked',updated_at=now() WHERE state='active';
